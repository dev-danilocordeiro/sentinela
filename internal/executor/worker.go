package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/gate"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// AppAuth é uma GitHub App autenticada (token de instalação renovado sob demanda).
type AppAuth interface {
	Token(ctx context.Context) (string, error)
	APIBase() string
}

// Worker consome a fila de execuções e acompanha os PRs abertos.
type Worker struct {
	Cfg      *config.Config
	Store    *store.Store
	Git      *repo.Git // clone (leitura)
	Linear   linear.Tracker
	Bot      AppAuth // sentinela-bot: push e PR em draft
	Reviewer AppAuth // sentinela-reviewer: review COMMENT (APPROVE só com a trava liberada)
	Runner   Runner
	Notify   *notify.Router
	Log      *slog.Logger
	// ClaudeHome é o diretório de dados do Claude Code, onde ficam as sessões (vazio: ClaudeHome()).
	ClaudeHome string
	// trigger guarda os membros dos times lidos no ciclo do gatilho (fix_trigger, ADR 0042).
	trigger fixTrigger
	// AfterPR roda depois de um job que abriu ou atualizou PR: as notas fora do escopo dos
	// reviewers viram achados candidatos (ADR 0019). Erro vira aviso, não falha o job.
	AfterPR func(ctx context.Context, e db.Execution) error
	// Now é o relógio da pausa pela cota (nos testes); nil usa time.Now.
	Now func() time.Time
	// quota é a pausa pela cota da assinatura conhecida por este processo.
	quota quotaState
}

// comment comenta na issue do tracker. Sem tracker (adapters.tracker local, ADR 0038) ou sem
// issue, não faz nada: o relatório local lê o estado da execução no Postgres.
func (w *Worker) comment(ctx context.Context, issueID, body string) error {
	if w.Linear == nil || issueID == "" {
		return nil
	}
	return w.Linear.Comment(ctx, issueID, body)
}

func (w *Worker) log() *slog.Logger {
	if w.Log != nil {
		return w.Log
	}
	return slog.Default()
}

func (w *Worker) client(ctx context.Context, a AppAuth) (*github.Client, error) {
	if a == nil {
		return nil, errors.New("GitHub App não configurada (GITHUB_BOT_* / GITHUB_REVIEWER_*)")
	}
	tok, err := a.Token(ctx)
	if err != nil {
		return nil, err
	}
	return github.NewClient(a.APIBase(), tok), nil
}

// RunNext pega uma execução da fila e a processa. Devolve false quando nenhum job pode começar:
// fila vazia ou só jobs esperando por conflito de arquivos (ADR 0031).
func (w *Worker) RunNext(ctx context.Context) (bool, error) {
	// Cota da assinatura esgotada: nenhum job começa até o reset.
	if until, err := w.QuotaPausedUntil(ctx); err != nil || !until.IsZero() {
		return false, err
	}
	e, ok, err := w.claim(ctx)
	if err != nil || !ok {
		return false, err
	}
	w.Process(ctx, e)
	return true, nil
}

// outcome acumula o que aconteceu num job para gravar no fim.
type outcome struct {
	result   Result
	run      RunResult
	decision string
	reason   string
	failErr  error
	failKind string // usage_limit | timeout | contract | other (ADR 0030)
	pr       *github.PullRequest
	gate     *gate.Result
	head     string
	warnings []string
}

// Process roda um job do começo ao fim. Erros não sobem: viram decision=failed no job, comentário
// na issue e aviso no Slack.
func (w *Worker) Process(ctx context.Context, e db.Execution) {
	o := &outcome{}
	f, err := w.finding(ctx, e.FindingID)
	if err != nil {
		o.failErr = err
		w.finish(ctx, e, signals.Finding{}, nil, o)
		return
	}
	ws, err := w.prepare(ctx, &e, f)
	if err != nil {
		o.failErr = err
		if errors.Is(err, ErrResumeLimit) {
			o.failKind = FailResumeLimit
		}
		w.finish(ctx, e, f, ws, o)
		return
	}
	w.execute(ctx, e, f, ws, o)
	// Limite de uso da assinatura não é falha do job: volta para a fila até o reset.
	if o.failKind == FailUsageLimit && w.requeueForQuota(ctx, e, ws, o) {
		return
	}
	w.finish(ctx, e, f, ws, o)
}

// workspace é o lugar do job no disco.
type workspace struct {
	checkout *repo.Checkout
	rc       *config.Repo
	worktree string
	jobDir   string
	branch   string
	base     string
	spec     JobSpec
	// instructionsRead diz que a issue foi lida e as instruções da equipe, escolhidas.
	instructionsRead bool
	// resume é a sessão da execução anterior a retomar (ADR 0030); nil começa do zero.
	resume *resumePlan
}

func (w *Worker) claudeHome() string {
	if w.ClaudeHome != "" {
		return w.ClaudeHome
	}
	return ClaudeHome()
}

// ResumeDecision é o que uma nova tentativa faz com a sessão da execução anterior (ADR 0030).
type ResumeDecision struct {
	Resume bool   // retoma a sessão
	Kind   string // tipo da falha da anterior
	Note   string // vai para resume_note; "" quando a anterior não falhou
	// LimitReached: a cadeia já retomou execution.max_resumes vezes. A tentativa falha sem rodar
	// e só um novo retry (que recomeça do zero) segue.
	LimitReached bool
}

// PlanResume decide se uma nova tentativa retoma a sessão da execução anterior (ADR 0030): só
// quando ela falhou por limite de uso, timeout ou contrato, a cadeia não passou de
// execution.max_resumes retomadas, a sessão e o diretório do job ainda existem e o worktree está
// como a execução o deixou (mesmo commit, nenhuma mudança de fora).
func (w *Worker) PlanResume(ctx context.Context, prev db.Execution) ResumeDecision {
	if prev.Status != "failed" {
		return ResumeDecision{}
	}
	d := ResumeDecision{Kind: failureOf(prev)}
	fresh := func(why string) ResumeDecision {
		d.Note = "recomeçou do zero: " + why
		return d
	}
	if !Resumable(d.Kind) {
		return fresh(fmt.Sprintf("a falha da execução #%d (%s) não é retomável", prev.ID, d.Kind))
	}
	if n, limit := w.resumesBefore(ctx, prev), w.Cfg.Execution.MaxResumes; limit < 0 || n >= limit {
		d.LimitReached = true
		d.Note = fmt.Sprintf("não retomou: a execução #%d já é a %dª retomada seguida (execution.max_resumes = %d); um novo retry recomeça do zero", prev.ID, n, max(limit, 0))
		if limit < 0 {
			d.Note = "não retomou: retomada desligada (execution.max_resumes negativo); um novo retry recomeça do zero"
		}
		return d
	}
	switch {
	case !prev.SessionID.Valid || prev.SessionID.String == "":
		return fresh(fmt.Sprintf("a execução #%d não gravou a sessão", prev.ID))
	case !isDir(prev.Worktree.String):
		return fresh(fmt.Sprintf("o worktree da execução #%d (%s) não existe mais", prev.ID, prev.Worktree.String))
	case !isDir(prev.JobDir.String):
		return fresh(fmt.Sprintf("o diretório do job da execução #%d (%s) não existe mais", prev.ID, prev.JobDir.String))
	}
	if _, found := FindSession(w.claudeHome(), prev.Worktree.String, prev.SessionID.String); !found {
		return fresh(fmt.Sprintf("a sessão %s da execução #%d não existe mais em %s", prev.SessionID.String, prev.ID,
			filepath.Join(w.claudeHome(), "projects", ProjectDirName(prev.Worktree.String))))
	}
	if why := worktreeChanged(ctx, prev); why != "" {
		return fresh(why)
	}
	d.Resume = true
	d.Note = fmt.Sprintf("retomou a sessão %s da execução #%d (%s)", prev.SessionID.String, prev.ID, d.Kind)
	return d
}

// resumesBefore conta as retomadas seguidas que levaram até a execução (ela inclusive).
func (w *Worker) resumesBefore(ctx context.Context, e db.Execution) int {
	n := 0
	for strings.HasPrefix(e.ResumeNote.String, "retomou a sessão") && e.PreviousID.Valid && n < 100 {
		n++
		prev, err := w.Store.Q.GetExecution(ctx, e.PreviousID.Int64)
		if err != nil {
			break
		}
		e = prev
	}
	return n
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return p != "" && err == nil && st.IsDir()
}

func abs(p string) string {
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}

func (w *Worker) prepare(ctx context.Context, e *db.Execution, f signals.Finding) (*workspace, error) {
	rc, ok := w.Cfg.Repo(e.Repo)
	if !ok {
		return nil, fmt.Errorf("repo %s não está no sentinela.yaml", e.Repo)
	}
	co, err := w.Git.Sync(ctx, rc.Name, rc.DefaultBranch)
	if err != nil {
		return nil, err
	}
	ws := &workspace{
		checkout: co, rc: rc, branch: repo.BranchPrefix + e.IssueIdentifier,
		worktree: abs(filepath.Join(w.Cfg.Execution.Worktrees, filepath.FromSlash(rc.Name), e.IssueIdentifier)),
		jobDir:   abs(filepath.Join(w.Cfg.Execution.Jobs, strconv.FormatInt(e.ID, 10))),
	}
	// Nova tentativa de uma execução que falhou: retoma a sessão dela, se der (ADR 0030).
	var prev db.Execution
	var plan ResumeDecision
	quota, pausedAt, quotaPaused := quotaMarker(*e)
	if quotaPaused {
		// Devolvido pela cota: continua a própria sessão; a mensagem da retomada leva o limite.
		plan, prev = w.planQuotaContinue(ctx, *e), *e
		prev.Error, prev.FinishedAt = text(quota.Summary), pgtype.Timestamptz{Time: pausedAt, Valid: true}
		if !plan.Resume && isDir(ws.jobDir) {
			// Recomeça do zero: os arquivos da tentativa interrompida saem do caminho do Collect.
			if err := os.Rename(ws.jobDir, ws.jobDir+".cota-"+pausedAt.UTC().Format("20060102T150405")); err != nil {
				return ws, err
			}
		}
	} else if e.Kind == "fix" && e.PreviousID.Valid {
		if prev, err = w.Store.Q.GetExecution(ctx, e.PreviousID.Int64); err == nil {
			plan = w.PlanResume(ctx, prev)
		}
	}
	if plan.Note != "" {
		if err := w.Store.Q.SetExecutionResumeNote(ctx, db.SetExecutionResumeNoteParams{ID: e.ID, ResumeNote: text(plan.Note)}); err != nil {
			return ws, err
		}
	}
	if plan.LimitReached {
		return ws, fmt.Errorf("%w: %s", ErrResumeLimit, plan.Note)
	}
	resuming, resumeKind := plan.Resume, plan.Kind
	from := "origin/" + rc.DefaultBranch
	if resuming {
		// Mesmo worktree, branch e diretório do job: a sessão retomada conhece esses caminhos.
		ws.worktree, ws.jobDir, ws.branch = prev.Worktree.String, prev.JobDir.String, prev.Branch.String
	} else if e.Kind == "rework" {
		if err := w.Git.FetchBranch(ctx, co, ws.branch); err != nil {
			return ws, fmt.Errorf("rework: branch %s não existe no GitHub: %w", ws.branch, err)
		}
		from = "origin/" + ws.branch
	} else if _, err := os.Stat(ws.worktree); err == nil {
		// Worktree de uma tentativa anterior do mesmo achado: fica guardado com os commits e as
		// mudanças dela, e esta recomeça da branch padrão (ADR 0034).
		if err := w.preserveWorktree(ctx, co, e, ws.worktree); err != nil {
			return ws, err
		}
	}
	if !resuming {
		if err := w.Git.Worktree(ctx, co, ws.worktree, ws.branch, from); err != nil {
			return ws, err
		}
	}
	if ws.base, err = w.Git.Run(ctx, ws.worktree, "merge-base", "origin/"+rc.DefaultBranch, "HEAD"); err != nil {
		return ws, err
	}
	if err := w.Store.Q.SetExecutionWorkspace(ctx, db.SetExecutionWorkspaceParams{
		ID: e.ID, Worktree: text(ws.worktree), JobDir: text(ws.jobDir), Branch: text(ws.branch),
	}); err != nil {
		return ws, err
	}
	if foreign := ForeignInstructions(ws.worktree); len(foreign) > 0 {
		return ws, fmt.Errorf("o worktree %s fica abaixo de %s: o Claude Code carregaria essas instruções nos agentes; mova execution.worktrees para fora de qualquer projeto", ws.worktree, strings.Join(foreign, ", "))
	}
	doc, docs, err := CommandsDoc(ws.worktree, rc.Name, f.CommandsScope())
	if err != nil {
		return ws, err
	}
	detail, err := w.issueDetail(ctx, e, f)
	if err != nil {
		return ws, fmt.Errorf("lendo a issue %s no Linear: %w", e.IssueIdentifier, err)
	}
	ws.spec = JobSpec{
		ExecutionID: e.ID, Kind: e.Kind, Issue: e.IssueIdentifier, IssueURL: detail.URL, Title: detail.Title,
		Repo: rc.Name, DefaultBranch: rc.DefaultBranch, Branch: ws.branch, BaseSHA: ws.base, Worktree: ws.worktree,
		CommandsDoc: doc, CommandsDocs: docs, Workspaces: WorkspacesFor(ws.worktree, f.CommandsScope()), Category: f.Category, Severity: f.Severity, ScopePaths: f.ScopePaths,
		BlockedPaths: w.Cfg.BlockedPaths, Routing: f.Routing, Policy: w.policy(f.Routing.Team), ReworkNote: e.ReworkNote.String,
	}
	// Instruções da equipe (ADR 0026): comentários humanos desde o job anterior do achado até agora.
	// Numa retomada, a sessão já leu a spec da anterior: só os comentários desde o fim dela.
	win := InstructionWindow{Until: time.Now()}
	if resuming {
		win.From = prev.FinishedAt.Time
	} else if start, err := w.Store.Q.PreviousExecutionStart(ctx, db.PreviousExecutionStartParams{FindingID: e.FindingID, ID: e.ID}); err != nil {
		return ws, err
	} else if start.Valid {
		win.From = start.Time
	}
	comments, ignoredAuthors, err := w.instructionAuthors(ctx, detail.Comments.Nodes, e, f.Routing.Team, f.Routing.LinearTeam)
	if err != nil {
		return ws, fmt.Errorf("conferindo os autores dos comentários (fix_trigger): %w", err)
	}
	ws.spec.Instructions = TeamInstructions(comments, win, w.Cfg.Execution.IgnoreCommentPrefixes)
	ws.spec.IgnoredAuthors = ignoredAuthors
	ws.spec.InstructionWindow = win
	ws.instructionsRead = true
	if resuming {
		// job.json, spec e plugin continuam os da execução anterior, que a sessão já conhece.
		ws.resume = &resumePlan{SessionID: prev.SessionID.String, Message: resumeMessage(prev, resumeKind, ws.jobDir, ws.spec.Instructions)}
		return ws, nil
	}
	if e.PreviousID.Valid {
		prev, err := w.Store.Q.GetExecution(ctx, e.PreviousID.Int64)
		if err == nil {
			ws.spec.Previous = map[string]any{"execution_id": prev.ID, "decision": prev.Decision.String, "reason": prev.DecisionReason.String, "dir": "previous/"}
			if prev.JobDir.Valid {
				_ = copyTree(prev.JobDir.String, filepath.Join(ws.jobDir, "previous"))
			}
		}
	}
	if err := WriteJob(ws.jobDir, ws.spec, f, detail.Title, detail.Description); err != nil {
		return ws, err
	}
	if err := RenderPlugin(w.Cfg.Execution.Plugin, filepath.Join(ws.jobDir, "plugin"), w.Cfg.ModelsFor(rc.Name, f.Routing.Team)); err != nil {
		return ws, fmt.Errorf("preparando o plugin: %w", err)
	}
	return ws, nil
}

// issueDetail lê a issue no Linear. Sem issue (tracker local, ADR 0038), monta título e descrição
// do próprio achado, sem comentários: as instruções da equipe vêm só pelo Linear.
func (w *Worker) issueDetail(ctx context.Context, e *db.Execution, f signals.Finding) (linear.IssueDetail, error) {
	if w.Linear != nil && e.LinearIssueID != "" {
		return w.Linear.Detail(ctx, e.LinearIssueID)
	}
	var d linear.IssueDetail
	d.ID, d.Identifier, d.Title = "", e.IssueIdentifier, f.Title
	d.Description = linear.LocalDescription(f)
	return d, nil
}

// preserveWorktree tira do caminho o worktree de uma tentativa anterior sem apagar nada: solta a
// branch (HEAD destacado no mesmo commit, mudanças não commitadas preservadas), move o diretório
// para <worktree>.exec-<id da execução dona> e grava o caminho novo nela (ADR 0034).
func (w *Worker) preserveWorktree(ctx context.Context, co *repo.Checkout, e *db.Execution, wt string) error {
	owner, ownerErr := w.Store.Q.LastExecutionAtWorktree(ctx, db.LastExecutionAtWorktreeParams{Worktree: text(wt), ID: e.ID})
	hasOwner := ownerErr == nil
	suffix := time.Now().UTC().Format("20060102T150405")
	if hasOwner {
		suffix = "exec-" + strconv.FormatInt(owner.ID, 10)
	}
	dest := wt + "." + suffix
	if _, err := os.Stat(dest); err == nil {
		dest += "-" + time.Now().UTC().Format("150405")
	}
	if _, err := w.Git.Run(ctx, wt, "checkout", "--detach"); err != nil {
		return fmt.Errorf("guardando o worktree anterior %s: %w", wt, err)
	}
	if _, err := w.Git.Run(ctx, co.Dir, "worktree", "move", wt, dest); err != nil {
		return fmt.Errorf("guardando o worktree anterior %s: %w", wt, err)
	}
	if hasOwner {
		if err := w.Store.Q.SetExecutionWorktree(ctx, db.SetExecutionWorktreeParams{ID: owner.ID, Worktree: text(dest)}); err != nil {
			return err
		}
	}
	w.log().Info("worktree anterior guardado", "execução", e.ID, "de", wt, "para", dest)
	return nil
}

func (w *Worker) policy(team string) config.Policy {
	if t := w.Cfg.Teams[team]; t != nil {
		return t.Policy.Merge(w.Cfg.Defaults.Policy)
	}
	return w.Cfg.Defaults.Policy
}

func (w *Worker) execute(ctx context.Context, e db.Execution, f signals.Finding, ws *workspace, o *outcome) {
	inv := Invocation{
		Prompt: "/sentinela-factory:sentinela-fix " + ws.jobDir, Dir: ws.worktree,
		PluginDir: filepath.Join(ws.jobDir, "plugin"), JobDir: ws.jobDir, Model: w.Cfg.ModelsFor(e.Repo, f.Routing.Team).Orchestrator,
		MaxBudget: w.Cfg.Execution.MaxBudgetUSD, Disallowed: w.Cfg.Execution.DisallowedTools,
		Timeout: w.Cfg.Execution.Timeout, StreamFile: filepath.Join(ws.jobDir, "stream.jsonl"),
	}
	inv.IdleTimeout, inv.CompletionTimeout = w.Cfg.Execution.Watchdog()
	if w.Cfg.Execution.Runtime == config.RuntimeContainer && w.Cfg.Execution.JobDocker == config.JobDockerNone {
		// Sem Docker no job: avisa quando os comandos do repo pedem Docker (ADR 0053).
		if msg := DockerWarning(ws.worktree, ws.spec.CommandsDocs...); msg != "" {
			o.warnings = append(o.warnings, msg)
			w.log().Warn("job sem Docker", "execução", e.ID, "aviso", msg)
		}
	}
	if ws.resume != nil {
		// O diretório do job é o da anterior: o stream desta execução vai num arquivo próprio.
		inv.Prompt, inv.Resume = ws.resume.Message, ws.resume.SessionID
		inv.StreamFile = filepath.Join(ws.jobDir, fmt.Sprintf("stream-%d.jsonl", e.ID))
	}
	run, err := w.Runner.Run(ctx, inv)
	o.run = run
	w.recordSession(ctx, e, ws, run)
	if run.Warning != "" {
		o.warnings = append(o.warnings, run.Warning)
		w.log().Warn("completion_timeout", "execução", e.ID, "aviso", run.Warning)
	}
	if err != nil {
		o.failErr, o.failKind = err, classifyFailure(err, run)
		return
	}
	if run.IsError {
		o.failErr = fmt.Errorf("claude terminou com %s: %s", run.Subtype, truncate(run.Result, 300))
		o.failKind = classifyFailure(o.failErr, run)
		return
	}
	res, err := Collect(ws.jobDir)
	o.result = res
	if err != nil {
		o.failErr, o.failKind = err, classifyFailure(err, run)
		return
	}
	o.decision, o.reason = res.Decision, res.Reason
	if res.Decision != "approve" && res.Decision != "escalate" {
		return
	}
	// O job terminou com código: push na branch do job e PR em draft.
	commits, err := w.Git.Run(ctx, ws.worktree, "rev-list", "--count", ws.base+"..HEAD")
	if err != nil {
		o.failErr = err
		return
	}
	if commits == "0" {
		o.failErr = fmt.Errorf("a linha terminou com %s mas o builder não fez nenhum commit", res.Decision)
		return
	}
	if o.head, err = w.Git.Run(ctx, ws.worktree, "rev-parse", "HEAD"); err != nil {
		o.failErr = err
		return
	}
	pr, err := w.publishPR(ctx, e, f, ws, o)
	if err != nil {
		o.failErr = fmt.Errorf("push ou PR: %w", err)
		return
	}
	pr.Head.SHA = o.head // o commit que acabamos de empurrar
	o.pr = pr
	g, err := w.gateAndReview(ctx, e, f, ws, pr, res.Approver, o.reason, "")
	if err != nil {
		o.warnings = append(o.warnings, "gate/review: "+err.Error())
	}
	o.gate = g
}

// recordSession grava o id da sessão e onde o Claude Code guardou os JSONL dela (principal e
// subagentes), para retomar no retry e auditar (ADR 0030).
func (w *Worker) recordSession(ctx context.Context, e db.Execution, ws *workspace, run RunResult) {
	if run.SessionID == "" {
		return
	}
	_ = w.Store.Q.SetExecutionSession(ctx, db.SetExecutionSessionParams{ID: e.ID, SessionID: text(run.SessionID)})
	log, ok := FindSession(w.claudeHome(), ws.worktree, run.SessionID)
	if !ok {
		w.log().Warn("sessão do Claude Code não encontrada no disco", "execução", e.ID, "sessão", run.SessionID)
		return
	}
	if err := w.Store.Q.SetExecutionSessionLog(ctx, db.SetExecutionSessionLogParams{ID: e.ID, SessionLog: mustJSON(log)}); err != nil {
		w.log().Warn("gravando session_log", "execução", e.ID, "erro", err)
	}
}

// publishPR faz push com o token da app bot e abre (ou atualiza) o PR em draft.
func (w *Worker) publishPR(ctx context.Context, e db.Execution, f signals.Finding, ws *workspace, o *outcome) (*github.PullRequest, error) {
	tok, err := w.Bot.Token(ctx)
	if err != nil {
		return nil, err
	}
	pushGit := &repo.Git{Root: w.Git.Root, Token: tok, BaseURL: w.Git.BaseURL}
	if err := pushGit.Push(ctx, ws.worktree, ws.branch, ws.rc.DefaultBranch); err != nil {
		return nil, err
	}
	gh := github.NewClient(w.Bot.APIBase(), tok)
	body := PRBody(e, ws.spec, o.result, o.run)
	owner, _, _ := strings.Cut(ws.rc.Name, "/")
	existing, err := gh.FindOpenPR(ctx, ws.rc.Name, owner, ws.branch)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		if err := gh.UpdatePRBody(ctx, ws.rc.Name, existing.Number, body); err != nil {
			return nil, err
		}
		existing.Head.SHA = o.head
		return existing, nil
	}
	pr, err := gh.CreatePR(ctx, ws.rc.Name, github.NewPR{
		Title: fmt.Sprintf("%s: %s", e.IssueIdentifier, f.Title), Head: ws.branch, Base: ws.rc.DefaultBranch, Body: body, Draft: true,
	})
	if err != nil {
		return nil, err
	}
	return &pr, nil
}

// gateAndReview avalia o Policy Gate e publica a review da revisora: COMMENT com a decisão do
// approver e do gate; APPROVE só se o gate aprova e github_apps.reviewer_approves está ligado.
func (w *Worker) gateAndReview(ctx context.Context, e db.Execution, f signals.Finding, ws *workspace, pr *github.PullRequest, approver, reason, humanBy string) (*gate.Result, error) {
	files, lines, err := w.diffStats(ctx, ws.worktree, ws.base)
	if err != nil {
		return nil, err
	}
	bot, err := w.client(ctx, w.Bot)
	if err != nil {
		return nil, err
	}
	ci, err := CIState(ctx, bot, ws.rc.Name, pr.Head.SHA)
	if err != nil {
		return nil, err
	}
	reverts, err := w.Store.Q.ModuleReverts(ctx, db.ModuleRevertsParams{Repo: f.Repo, Module: f.Module})
	if err != nil {
		return nil, err
	}
	in := gate.Input{
		Team: f.Routing.Team, Policy: w.policy(f.Routing.Team), Category: f.Category, ApproverDecision: approver,
		ApproverReason: reason, HumanApprovedBy: humanBy, ChangedFiles: files, ChangedLines: lines,
		ScopePaths: f.ScopePaths, BlockedPaths: w.Cfg.BlockedPaths, CI: ci, ModuleReverts: int(reverts),
		ReviewerApproves: w.Cfg.GitHubApps.ReviewerApproves,
	}
	res := gate.Evaluate(in)
	reasons, _ := json.Marshal(res.Reasons)
	if err := w.Store.Q.SetExecutionGate(ctx, db.SetExecutionGateParams{ID: e.ID, Gate: text(res.Decision), GateReasons: reasons}); err != nil {
		return &res, err
	}
	rev, err := w.client(ctx, w.Reviewer)
	if err != nil {
		return &res, err
	}
	event := github.ReviewEventComment
	// Trava dupla: o gate e a config. Com gh_cli a review sai pela conta da pessoa: só comenta.
	if res.Event == "APPROVE" && w.Cfg.GitHubApps.ReviewerApproves && w.Cfg.Adapters.GitHost != config.GitHostGHCLI {
		event = github.ReviewEventApprove
	}
	_, err = rev.PostReview(ctx, ws.rc.Name, pr.Number, event, gate.Comment(in, res))
	return &res, err
}

// diffStats lista os arquivos alterados e soma as linhas (adicionadas + removidas) desde a base.
func (w *Worker) diffStats(ctx context.Context, dir, base string) ([]string, int, error) {
	out, err := w.Git.Run(ctx, dir, "diff", "--numstat", base+"...HEAD")
	if err != nil {
		return nil, 0, err
	}
	var files []string
	lines := 0
	for _, l := range strings.Split(out, "\n") {
		parts := strings.Fields(l)
		if len(parts) < 3 {
			continue
		}
		add, _ := strconv.Atoi(parts[0])
		del, _ := strconv.Atoi(parts[1])
		lines += add + del
		files = append(files, parts[len(parts)-1])
	}
	return files, lines, nil
}

// CIState resume os checks do commit: success, failure, pending ou none.
func CIState(ctx context.Context, gh *github.Client, repoName, sha string) (string, error) {
	runs, err := gh.CheckRuns(ctx, repoName, sha)
	if err != nil {
		return "", err
	}
	return ciFrom(runs), nil
}

func ciFrom(runs []github.CheckRun) string {
	if len(runs) == 0 {
		return gate.CINone
	}
	state := gate.CISuccess
	for _, r := range runs {
		if r.Status != "completed" {
			state = gate.CIPending
			continue
		}
		switch r.Conclusion {
		case "failure", "timed_out", "cancelled", "action_required", "startup_failure":
			return gate.CIFailure
		}
	}
	return state
}

// finish grava o job, as reviews e o custo, e avisa na issue e no canal do time.
func (w *Worker) finish(ctx context.Context, e db.Execution, f signals.Finding, ws *workspace, o *outcome) {
	ctx = context.WithoutCancel(ctx)
	status, decision, reason := "succeeded", o.decision, o.reason
	if o.failErr != nil {
		status, decision, reason = "failed", "failed", o.failErr.Error()
	}
	if decision == "" || decision == "failed" {
		// A skill também pode declarar failed (contrato quebrado dentro da sessão).
		status, decision = "failed", "failed"
	}
	all := map[string]Artifact{}
	for k, v := range o.result.Artifacts {
		all[k] = v
	}
	if ws != nil && ws.instructionsRead {
		all[instructionsKey] = instructionsArtifact(ws.spec.Instructions, ws.spec.InstructionWindow)
	}
	if status == "failed" {
		kind := o.failKind
		if kind == "" {
			kind = ClassifyFailureText(reason)
		}
		all[failureKey] = Artifact{Verdict: "FAILURE: " + kind, Summary: truncate(reason, 400)}
		// Como a execução deixou o worktree: a retomada confere que ninguém mexeu nele.
		if ws != nil && isDir(ws.worktree) {
			if head, digest, err := worktreeState(ctx, ws.worktree); err == nil {
				all[worktreeKey] = Artifact{File: ws.worktree, Verdict: "HEAD: " + head, Summary: digest}
			}
		}
	}
	artifacts, _ := json.Marshal(all)
	if artifacts == nil || string(artifacts) == "null" {
		artifacts = []byte("{}")
	}
	for _, r := range o.result.Reviews {
		items, _ := json.Marshal(append([]string{}, r.Items...))
		if err := w.Store.Q.InsertReview(ctx, db.InsertReviewParams{ExecutionID: e.ID, Round: int32(r.Round), Lens: r.Lens, Verdict: r.Verdict, Findings: items}); err != nil { //nolint:gosec // rodada <= 3
			w.log().Error("gravando review", "execução", e.ID, "erro", err)
		}
	}
	if err := w.Store.Q.FinishExecution(ctx, db.FinishExecutionParams{
		ID: e.ID, Status: status, Decision: text(decision), DecisionReason: text(reason), Rounds: int32(o.result.State.Rounds), //nolint:gosec // até 3
		Error: textIf(status == "failed", reason), CostUsd: store.NanoUSDToNumeric(int64(o.run.CostUSD * 1e9)), NumTurns: int32(o.run.NumTurns), //nolint:gosec // contadores pequenos
		Artifacts: artifacts, HeadSha: text(o.head), Model: text(w.Cfg.ModelsFor(e.Repo, f.Routing.Team).Executor),
	}); err != nil {
		w.log().Error("gravando execução", "execução", e.ID, "erro", err)
	}
	if o.pr != nil {
		if err := w.Store.Q.SetExecutionPR(ctx, db.SetExecutionPRParams{ID: e.ID, PrNumber: pgtype.Int4{Int32: int32(o.pr.Number), Valid: true}, PrUrl: text(o.pr.HTMLURL), HeadSha: text(o.head)}); err != nil { //nolint:gosec // número de PR
			w.log().Error("gravando PR", "execução", e.ID, "erro", err)
		}
		_ = w.setFindingState(ctx, e.FindingID, "executing")
	}
	w.recordCalls(ctx, e, o.run, "sentinela-fix")
	w.announce(ctx, e, f, ws, o, decision, reason)
	if o.pr != nil && w.AfterPR != nil {
		if err := w.AfterPR(ctx, e); err != nil {
			w.log().Warn("notas de review", "execução", e.ID, "erro", err)
		}
	}
}

// recordCalls grava o consumo da sessão em llm_calls, uma linha por modelo.
func (w *Worker) recordCalls(ctx context.Context, e db.Execution, run RunResult, agent string) {
	for model, u := range run.Models {
		if _, err := w.Store.Q.InsertLLMCall(ctx, db.InsertLLMCallParams{
			Agent: agent, Model: model, PromptVersion: "plugin", Mode: "sync", CustomID: text("execution:" + strconv.FormatInt(e.ID, 10)),
			MessageID: text(run.SessionID), Status: "succeeded", StopReason: text(run.Subtype),
			InputTokens: u.InputTokens, OutputTokens: u.OutputTokens, CacheWrite5mTokens: u.CacheCreationInputTokens,
			CacheReadTokens: u.CacheReadInputTokens, CostUsd: store.NanoUSDToNumeric(int64(u.CostUSD * 1e9)),
			LatencyMs: run.Duration.Milliseconds(), Provider: config.ProviderClaudeCode,
		}); err != nil {
			w.log().Error("gravando llm_call", "execução", e.ID, "erro", err)
		}
	}
}

// announce comenta na issue, aplica a label de resultado (nunca a de gatilho) e avisa o time.
func (w *Worker) announce(ctx context.Context, e db.Execution, f signals.Finding, ws *workspace, o *outcome, decision, reason string) {
	var b strings.Builder
	fmt.Fprintf(&b, "**Sentinela · execução #%d** (%s): **%s**", e.ID, e.Kind, decision)
	if reason != "" {
		fmt.Fprintf(&b, ": %s", reason)
	}
	b.WriteString("\n\n")
	if o.pr != nil {
		fmt.Fprintf(&b, "PR em draft: %s\n", o.pr.HTMLURL)
	}
	if o.gate != nil {
		fmt.Fprintf(&b, "Policy Gate: %s", o.gate.Decision)
		if len(o.gate.Reasons) > 0 {
			fmt.Fprintf(&b, " (%s)", strings.Join(o.gate.Reasons, "; "))
		}
		b.WriteString(". A revisora só comentou; aprovação e merge são de um humano.\n")
	}
	if o.result.State.Rounds > 0 {
		fmt.Fprintf(&b, "Rodadas: %d · turnos: %d · custo equivalente: $%.2f\n", o.result.State.Rounds, o.run.NumTurns, o.run.CostUSD)
	}
	switch {
	case (decision == "blocked" || decision == "failed") && o.pr == nil:
		fmt.Fprintf(&b, "\nPróximo passo (humano): para tentar de novo, comente o que muda e aplique `%s` outra vez (o Sentinela tira a label agora); no terminal, `sentinela job retry %d`.\n", w.Cfg.LabelsFor(f.Routing.Team).Trigger, e.ID)
	case decision == "escalate" || decision == "blocked" || decision == "failed":
		fmt.Fprintf(&b, "\nPróximo passo (humano): `sentinela job approve %d` aprova um ESCALATE; `sentinela job rework %d \"<nota>\"` devolve ao builder.\n", e.ID, e.ID)
	}
	for _, wmsg := range o.warnings {
		fmt.Fprintf(&b, "\nAviso: %s\n", wmsg)
	}
	labels := w.Cfg.LabelsFor(f.Routing.Team)
	if e.LinearIssueID != "" && w.Linear != nil {
		if err := w.Linear.Comment(ctx, e.LinearIssueID, b.String()); err != nil {
			w.log().Warn("comentando na issue", "issue", e.IssueIdentifier, "erro", err)
		}
		label := labels.Done
		if o.pr == nil {
			label = labels.Failed
		}
		w.addLabel(ctx, e, label)
		if o.pr != nil {
			w.moveToReview(ctx, e, f)
		} else if li, err := w.Store.Q.PrimaryIssueForFinding(ctx, e.FindingID); err == nil {
			// O próximo ciclo do Enqueue refaz isto se falhar agora.
			if err := w.consumeTrigger(ctx, e.ID, e.LinearIssueID, e.IssueIdentifier, li.LinearTeam, labels.Trigger, true, false); err != nil {
				w.log().Warn("tirando a label de gatilho", "issue", e.IssueIdentifier, "erro", err)
			}
		}
	}
	if w.Notify != nil {
		text := fmt.Sprintf("Sentinela · %s: execução #%d terminou com *%s*", e.IssueIdentifier, e.ID, decision)
		if o.pr != nil {
			text += " · PR em draft " + notify.Link(o.pr.HTMLURL, "#"+strconv.Itoa(o.pr.Number))
		}
		if reason != "" {
			text += " · " + notify.Escape(truncate(reason, 200))
		}
		for _, wmsg := range w.Notify.Team(ctx, f.Routing.Team, text) {
			w.log().Warn("slack", "aviso", wmsg)
		}
	}
}

// moveToReview move a issue para o estado de revisão do time (review_state) quando há PR (ADR
// 0027). É o registro de um fato, não uma decisão humana: concluir, cancelar ou marcar duplicata
// continuam com as pessoas, e uma issue nesses estados não é mexida.
func (w *Worker) moveToReview(ctx context.Context, e db.Execution, f signals.Finding) {
	w.moveIssue(ctx, e, w.Cfg.ReviewStateFor(w.Cfg.Teams[f.Routing.Team]), "revisão")
}

// moveToDone move a issue para o estado de concluído do time (done_state, padrão Done) quando o
// PR foi mergeado (ADR 0028). Também é registro de fato: o merge foi decisão humana. Não depende
// da integração do Linear com o GitHub, que perde a transição quando o PR sai de draft e é
// mergeado em seguida.
func (w *Worker) moveToDone(ctx context.Context, e db.Execution) {
	f, err := w.finding(ctx, e.FindingID)
	if err != nil {
		w.log().Warn("lendo o achado", "achado", e.FindingID, "erro", err)
		return
	}
	w.moveIssue(ctx, e, w.Cfg.DoneStateFor(w.Cfg.Teams[f.Routing.Team]), "concluído")
}

// freshReader é o tracker que guarda o estado lido e sabe ler de novo na hora (MCPTracker).
type freshReader interface {
	IssuesFresh(ctx context.Context, ids []string) ([]linear.Issue, error)
}

// moveIssue move a issue principal do achado para state no time dela. Issue concluída, cancelada,
// duplicada ou já em state não é mexida; state vazio não move.
func (w *Worker) moveIssue(ctx context.Context, e db.Execution, state, what string) {
	if state == "" || e.LinearIssueID == "" || w.Linear == nil {
		return
	}
	li, err := w.Store.Q.PrimaryIssueForFinding(ctx, e.FindingID)
	if err != nil {
		return
	}
	refs, err := w.Linear.TeamRefs(ctx, li.LinearTeam)
	if err != nil {
		w.log().Warn("time do Linear", "erro", err)
		return
	}
	id, ok := refs.States[state]
	if !ok {
		w.log().Warn("estado de "+what+" não existe no time", "estado", state, "time", li.LinearTeam)
		return
	}
	read := w.Linear.Issues
	if f, ok := w.Linear.(freshReader); ok {
		// Tracker com estado guardado (linear_mcp): lê agora, para não mover uma issue que uma
		// pessoa acabou de cancelar ou concluir.
		read = f.IssuesFresh
	}
	issues, err := read(ctx, []string{e.LinearIssueID})
	if err != nil || len(issues) != 1 {
		w.log().Warn("lendo a issue", "issue", e.IssueIdentifier, "erro", err)
		return
	}
	switch cur := issues[0].State; {
	case cur.Name == state, cur.Type == "completed", cur.Type == "canceled", cur.Type == "duplicate":
		return
	}
	if err := w.Linear.SetState(ctx, e.LinearIssueID, id); err != nil {
		w.log().Warn("movendo a issue para "+what, "issue", e.IssueIdentifier, "erro", err)
	}
}

// isTriggerLabel diz se a label é de gatilho em algum time: o Sentinela nunca a aplica.
func (w *Worker) isTriggerLabel(name string) bool {
	if name == w.Cfg.Labels.Trigger {
		return true
	}
	for _, t := range w.Cfg.Teams {
		if t != nil && t.Labels.Trigger == name {
			return true
		}
	}
	return false
}

func (w *Worker) addLabel(ctx context.Context, e db.Execution, name string) {
	if name == "" || config.ForeignLabel(name) || w.isTriggerLabel(name) || w.Linear == nil {
		return
	}
	li, err := w.Store.Q.PrimaryIssueForFinding(ctx, e.FindingID)
	if err != nil {
		return
	}
	refs, err := w.Linear.TeamRefs(ctx, li.LinearTeam)
	if err != nil {
		w.log().Warn("time do Linear", "erro", err)
		return
	}
	ids, err := w.Linear.LabelIDs(ctx, refs.ID, []string{name})
	if err != nil || len(ids) == 0 {
		w.log().Warn("label do Linear", "label", name, "erro", err)
		return
	}
	if err := w.Linear.AddLabel(ctx, e.LinearIssueID, ids[0]); err != nil {
		w.log().Warn("aplicando label", "label", name, "erro", err)
	}
}

// finding remonta o Finding gravado.
// models resolve os modelos do job pelo repo e pelo time dono do achado.
func (w *Worker) models(ctx context.Context, e db.Execution) config.ModelRoles {
	team := ""
	if f, err := w.finding(ctx, e.FindingID); err == nil {
		team = f.Routing.Team
	}
	return w.Cfg.ModelsFor(e.Repo, team)
}

func (w *Worker) finding(ctx context.Context, id int64) (signals.Finding, error) {
	r, err := w.Store.Q.GetFinding(ctx, id)
	if err != nil {
		return signals.Finding{}, fmt.Errorf("achado %d: %w", id, err)
	}
	f := signals.Finding{
		Source: r.Source, Repo: r.Repo, Module: r.Module, Owners: r.Owners, Category: r.Category, Severity: r.Severity,
		Confidence: float64(r.Confidence), Title: r.Title, Context: r.Context, ProposedFix: r.ProposedFix,
		AcceptanceCriteria: r.AcceptanceCriteria, ScopePaths: r.ScopePaths, RelatedModules: r.RelatedModules, Fingerprint: r.Fingerprint,
	}
	if err := json.Unmarshal(r.Routing, &f.Routing); err != nil {
		return f, err
	}
	if err := json.Unmarshal(r.Evidence, &f.Evidence); err != nil {
		return f, err
	}
	_ = json.Unmarshal(r.Signals, &f.Signals)
	return f, nil
}

func text(s string) pgtype.Text { return pgtype.Text{String: s, Valid: s != ""} }

func textIf(ok bool, s string) pgtype.Text {
	if !ok {
		return pgtype.Text{}
	}
	return text(s)
}

// copyTree copia os artefatos de um job anterior (rework), sem o plugin e o stream.
func copyTree(src, dst string) error {
	return filepath.Walk(src, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "plugin" || strings.HasPrefix(rel, "plugin"+string(filepath.Separator)) || strings.HasPrefix(rel, "stream") || rel == "previous" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o750)
		}
		data, err := os.ReadFile(p) //nolint:gosec // diretório de job do próprio Sentinela
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600) //nolint:gosec // destino é o diretório do job, montado pelo worker
	})
}
