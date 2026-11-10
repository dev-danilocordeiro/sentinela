package executor

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/github"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

const execConfig = `
defaults:
  linear: { team: EX, triage_state: Backlog, labels: [sentinela] }
  policy: { auto_approve: false, max_lines: 50, max_files: 3 }
labels: { trigger: sentinela-fix, done: sentinela-done, failed: sentinela-failed, finding: sentinela }
budget: { per_run_usd: 2, per_pr_usd: 3 }
models:
  triage: claude-haiku-5-5
  analysis: claude-sonnet-5-5
  architect: claude-sonnet-5-5
  judge: claude-sonnet-5-5
  curator: claude-haiku-5-5
  spec_writer: claude-sonnet-5-5
  executor: claude-opus-5-5
  reviewer_security: claude-opus-5-5
  reviewer_code: claude-sonnet-5-5
  reviewer_reliability: claude-sonnet-5-5
  reviewer_domain: claude-sonnet-5-5
  approver: claude-opus-5-5
pricing:
  models:
    claude-haiku-5-5: { input: 0.10, output: 0.50 }
    claude-sonnet-5-5: { input: 2.00, output: 10.00 }
    claude-opus-5-5: { input: 4.00, output: 20.00 }
blocked_paths: ["**/financeiro/**"]
github_apps: { bot: sentinela-bot-exemplo, reviewer: sentinela-reviewer-exemplo, reviewer_approves: false }
teams:
  core: { github: ["@acme/core"], linear: { team: EX, project: core }, policy: { auto_approve: false } }
repos:
  - { name: acme/svc, team: core }
`

type env struct {
	w      *Worker
	st     *store.Store
	lin    *lineartest.Server
	gh     *fakeGitHub
	runner *fakeRunner
	remote string // diretório com acme/svc.git
	cfg    *config.Config
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// setupOpts troca o repo acme/svc e o achado da EX-1 do setup padrão (módulo Go em cmd/api).
type setupOpts struct {
	files    map[string]string // conteúdo do repo; nil: o módulo Go padrão
	module   string
	evidence string // JSON da evidência
	scope    []string
}

// setup sobe banco migrado, um remoto git com o repo acme/svc, Linear e GitHub falsos, uma
// issue EX-1 publicada para um achado e o worker com o plugin real (renderizado no job).
func setup(t *testing.T, withAgentsMD bool) *env {
	t.Helper()
	files := map[string]string{"go.mod": "module acme.com/svc\n\ngo 1.23\n", "cmd/api/main.go": "package main\n\nfunc main() {}\n"}
	if withAgentsMD {
		files["AGENTS.md"] = "# Comandos\n\n- teste: `go test ./...`\n"
	}
	return setupWith(t, setupOpts{files: files})
}

func setupWith(t *testing.T, o setupOpts) *env {
	t.Helper()
	if o.module == "" {
		o.module, o.evidence, o.scope = "cmd/api", `[{"file":"cmd/api/main.go","line":3,"snippet":"func main() {}"}]`, []string{"cmd/api"}
	}
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := store.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = m.DownTo(context.Background(), 0); _ = m.Close() })
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)

	remote := t.TempDir()
	src := filepath.Join(remote, "src")
	for rel, content := range o.files {
		p := filepath.Join(src, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		_ = os.WriteFile(p, []byte(content), 0o600)
	}
	gitIn(t, src, "init", "-q", "-b", "main")
	gitIn(t, src, "add", ".")
	gitIn(t, src, "commit", "-qm", "init")
	bare := filepath.Join(remote, "acme", "svc.git")
	gitIn(t, remote, "clone", "-q", "--bare", src, bare)

	cfg, err := config.Parse([]byte(execConfig))
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	cfg.Execution.Worktrees, cfg.Execution.Jobs = filepath.Join(work, "worktrees"), filepath.Join(work, "jobs")
	cfg.Execution.Plugin = filepath.Join("..", "..", "plugins", "sentinela-factory")

	lin := lineartest.New()
	linSrv := httptest.NewServer(lin)
	t.Cleanup(linSrv.Close)
	lc := linear.NewClient(linSrv.URL, "k")
	issue, err := lc.CreateIssue(ctx, linear.IssueInput{TeamID: "team-ex", Title: "Configura timeouts no servidor HTTP", Description: "Corpo da issue.\n\n`sentinela-fingerprint: aaaaaaaaaaaa`"})
	if err != nil {
		t.Fatal(err)
	}
	routing, _ := json.Marshal(repo.Routing{Team: "core", LinearTeam: "EX", LinearProject: "core", Rule: "repo"})
	fid, err := st.Q.InsertFinding(ctx, db.InsertFindingParams{
		Fingerprint: "aaaaaaaaaaaa", Source: "code_analysis", Repo: "acme/svc", Module: o.module, Owners: []string{"core"}, Routing: routing,
		Category: "bug", Severity: "medium", Confidence: 0.8, Title: "Configura timeouts no servidor HTTP", Context: "O servidor não tem timeouts.",
		Evidence: []byte(o.evidence), Signals: []byte("[]"),
		ProposedFix: "Usar http.Server com ReadHeaderTimeout.", AcceptanceCriteria: []string{"Servidor com ReadHeaderTimeout", "Teste cobre o timeout"},
		ScopePaths: o.scope, RelatedModules: []string{}, Embedding: pgvector.NewVector(make([]float32, 1024)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Q.InsertLinearIssue(ctx, db.InsertLinearIssueParams{
		FindingID: fid, Role: "primary", Team: "core", Module: o.module, LinearTeam: "EX", LinearID: issue.ID,
		Identifier: issue.Identifier, Url: issue.URL, StateName: "Backlog", StateType: "backlog",
	}); err != nil {
		t.Fatal(err)
	}

	gh, ghSrv := newFakeGitHub(t)
	runner := &fakeRunner{rounds: []map[string]string{{}}, decision: "APPROVE", home: filepath.Join(work, "claude")}
	w := &Worker{
		Cfg: cfg, Store: st, Linear: lc, Runner: runner, ClaudeHome: runner.home,
		Git: &repo.Git{Root: filepath.Join(work, "repos"), BaseURL: "file://" + remote},
		Bot: fakeApp{token: "tok-bot", base: ghSrv.URL}, Reviewer: fakeApp{token: "tok-reviewer", base: ghSrv.URL},
	}
	return &env{w: w, st: st, lin: lin, gh: gh, runner: runner, remote: bare, cfg: cfg}
}

func (e *env) runOnce(t *testing.T) db.Execution {
	t.Helper()
	ran, err := e.w.RunNext(context.Background())
	if err != nil || !ran {
		t.Fatalf("RunNext = %v, %v", ran, err)
	}
	rows, err := e.st.Q.ListExecutions(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	return rows[0]
}

// Uma pessoa aplica a label; o worker enfileira, roda o plugin no worktree, faz push só da branch
// do job, abre o PR em draft e a revisora só comenta. Nada é aprovado nem mergeado.
func TestLabelTriggersJobThatOpensDraftPRAndTheReviewerOnlyComments(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 0 {
		t.Fatalf("sem label não enfileira: %v %v", q, err)
	}
	e.lin.AddLabel("EX-1", "sentinela-fix") // decisão humana
	q, err := e.w.Enqueue(ctx)
	if err != nil || len(q) != 1 || q[0].IssueIdentifier != "EX-1" {
		t.Fatalf("enfileirado = %+v (%v)", q, err)
	}
	if again, _ := e.w.Enqueue(ctx); len(again) != 0 {
		t.Fatal("a mesma label não pode enfileirar de novo")
	}
	job := e.runOnce(t)

	if job.Status != "succeeded" || job.Decision.String != "approve" || job.Rounds != 1 || job.Gate.String != "human" {
		t.Fatalf("job = %s/%s rodadas %d gate %s erro %s\ncomentários: %+v\nreqs: %v", job.Status, job.Decision.String, job.Rounds, job.Gate.String, job.Error.String, e.lin.CommentsOf("EX-1"), e.gh.requests)
	}
	inv := e.runner.invocations()[0]
	if inv.Prompt != "/sentinela-factory:sentinela-fix "+job.JobDir.String || inv.Dir != job.Worktree.String || inv.Model != "claude-sonnet-5-5" {
		t.Errorf("invocação = %+v", inv)
	}
	builder, _ := os.ReadFile(filepath.Join(job.JobDir.String, "plugin", "agents", "builder.md"))
	security, _ := os.ReadFile(filepath.Join(job.JobDir.String, "plugin", "agents", "reviewer-security.md"))
	if !bytes.Contains(builder, []byte("\nmodel: claude-opus-5-5\n")) || !bytes.Contains(security, []byte("\nmodel: claude-opus-5-5\n")) {
		t.Error("o plugin do job deveria usar os modelos do sentinela.yaml")
	}
	if spec, _ := os.ReadFile(filepath.Join(job.JobDir.String, "spec.md")); !bytes.HasPrefix(spec, []byte("SPEC: READY\n")) || !bytes.Contains(spec, []byte("Teste cobre o timeout")) {
		t.Errorf("spec do achado (spec-writer pulado):\n%s", spec)
	}
	for _, d := range []string{"--disallowedTools", "Bash(git push:*)", "--plugin-dir", "--add-dir"} {
		if !strings.Contains(strings.Join(inv.Args(), " "), d) {
			t.Errorf("linha de comando sem %s: %v", d, inv.Args())
		}
	}

	prs, reviews := e.gh.snapshot()
	if len(prs) != 1 || !prs[0].Draft || prs[0].Head.Ref != "sentinela/EX-1" || prs[0].Base != "main" ||
		!strings.HasPrefix(prs[0].Body, "Fixes EX-1\n") || !strings.Contains(prs[0].Body, "Decisão do approver") {
		t.Fatalf("PR = %+v", prs)
	}
	if len(reviews) != 1 || reviews[0].Event != "COMMENT" || !strings.Contains(reviews[0].Body, "não aprova o PR") || !strings.Contains(reviews[0].Body, "auto_approve desligado") {
		t.Fatalf("reviews = %+v", reviews)
	}
	if heads := gitIn(t, e.remote, "for-each-ref", "--format=%(refname:short)", "refs/heads"); heads != "main\nsentinela/EX-1" {
		t.Errorf("branches no remoto = %q", heads)
	}
	if mainLog := gitIn(t, e.remote, "log", "--oneline", "main"); strings.Count(mainLog, "\n") != 0 {
		t.Errorf("a branch padrão não pode mudar: %s", mainLog)
	}
	var rv, calls int
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM reviews WHERE execution_id = $1", job.ID).Scan(&rv)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM llm_calls WHERE custom_id = $1", "execution:1").Scan(&calls)
	if rv != 4 || calls != 2 {
		t.Errorf("reviews gravadas %d (quer 4), llm_calls %d (quer 2, uma por modelo)", rv, calls)
	}
	if f, _ := e.st.Q.GetFinding(ctx, job.FindingID); f.State != "executing" {
		t.Errorf("achado = %s, quer executing", f.State)
	}
	issue := e.lin.Snapshot()[0]
	if !slicesContains(issue.LabelsAddedBySentinela, "lbl-done") || slicesContains(issue.LabelsAddedBySentinela, "lbl-fix") {
		t.Errorf("labels aplicadas pelo Sentinela = %v (só a de resultado; nunca a de gatilho)", issue.LabelsAddedBySentinela)
	}
	if c := e.lin.CommentsOf("EX-1"); len(c) != 1 || !strings.Contains(c[0].Body, "PR em draft: https://github.com/acme/svc/pull/1") {
		t.Errorf("comentário = %+v", c)
	}
}

// Com política que aprovaria e CI verde, a revisora só publica APPROVE se a trava da config for
// liberada; com reviewer_approves false, continua COMMENT.
func TestReviewerApprovesOnlyWhenTheConfigUnlocksIt(t *testing.T) {
	for _, unlocked := range []bool{false, true} {
		e := setup(t, true)
		ctx := context.Background()
		e.cfg.Teams["core"].Policy.AutoApprove = ptr(true)
		e.cfg.GitHubApps.ReviewerApproves = unlocked
		e.lin.AddLabel("EX-1", "sentinela-fix")
		if _, err := e.w.Enqueue(ctx); err != nil {
			t.Fatal(err)
		}
		// CI verde em qualquer commit: o fake devolve sucesso para o head do PR.
		e.gh.mu.Lock()
		e.gh.checks = map[string][]github.CheckRun{}
		e.gh.mu.Unlock()
		job := e.runOnce(t)
		head := job.HeadSha.String
		e.gh.mu.Lock()
		e.gh.checks[head] = []github.CheckRun{{ID: 1, Name: "ci", Status: "completed", Conclusion: "success"}}
		e.gh.prs[0].Head.SHA = head
		e.gh.mu.Unlock()
		if _, err := e.w.FollowUp(ctx); err != nil {
			t.Fatal(err)
		}
		_, reviews := e.gh.snapshot()
		last := reviews[len(reviews)-1]
		want := map[bool]string{false: "COMMENT", true: "APPROVE"}[unlocked]
		if last.Event != want {
			t.Errorf("reviewer_approves=%v: evento %s, quer %s\n%s", unlocked, last.Event, want, last.Body)
		}
		prs, _ := e.gh.snapshot()
		if !prs[0].Draft || prs[0].Merged {
			t.Error("o PR continua em draft e sem merge")
		}
	}
}

// Rodadas esgotadas viram ESCALATE sem approver; um humano aprova com job approve (a revisora
// só comenta) e pede rework com nota: o builder volta na mesma branch e o PR é atualizado.
func TestExhaustedRoundsEscalateThenHumanApprovesAndAsksForRework(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.runner.rounds = []map[string]string{{"code": "CHANGES"}, {"reliability": "CHANGES"}, {"code": "CHANGES"}}
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	if job.Decision.String != "escalate" || job.Rounds != 3 || !strings.Contains(job.DecisionReason.String, "rodadas esgotadas") {
		t.Fatalf("job = %s rodadas %d: %s / %s", job.Decision.String, job.Rounds, job.DecisionReason.String, job.Error.String)
	}
	var blocks int
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM reviews WHERE execution_id = $1 AND verdict = 'CHANGES'", job.ID).Scan(&blocks)
	if blocks != 3 {
		t.Errorf("bloqueios gravados = %d, quer 3", blocks)
	}
	if _, err := os.Stat(filepath.Join(job.JobDir.String, "decision.md")); err == nil {
		t.Error("rodadas esgotadas não chamam o approver")
	}

	if _, _, err := e.w.Approve(ctx, job.ID, ""); err == nil {
		t.Error("approve sem quem aprova deveria falhar")
	}
	_, g, err := e.w.Approve(ctx, job.ID, "Fulano")
	if err != nil {
		t.Fatal(err)
	}
	_, reviews := e.gh.snapshot()
	if last := reviews[len(reviews)-1]; last.Event != "COMMENT" || !strings.Contains(last.Body, "Aprovado por humano no Sentinela: Fulano") || g.Decision != "human" {
		t.Errorf("review depois do approve = %+v (gate %+v)", last, g)
	}
	if _, _, err := e.w.Approve(ctx, job.ID, "Fulano"); err == nil {
		t.Error("o mesmo ESCALATE não pode ser aprovado duas vezes")
	}

	e.runner.rounds, e.runner.decision = []map[string]string{{}}, "APPROVE"
	if _, err := e.w.Rework(ctx, job.ID, ""); err == nil {
		t.Error("rework sem nota deveria falhar")
	}
	rw, err := e.w.Rework(ctx, job.ID, "use http.Server{ReadHeaderTimeout: 5 * time.Second}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.w.Rework(ctx, job.ID, "outra"); err == nil {
		t.Error("não pode haver dois jobs ativos para o mesmo achado")
	}
	done := e.runOnce(t)
	if done.ID != rw.ID || done.Kind != "rework" || done.Decision.String != "approve" || done.Branch.String != "sentinela/EX-1" {
		t.Fatalf("rework = %+v / %s", done, done.Error.String)
	}
	var spec JobSpec
	data, _ := os.ReadFile(filepath.Join(done.JobDir.String, "job.json"))
	_ = json.Unmarshal(data, &spec)
	if spec.ReworkNote == "" || spec.Previous == nil {
		t.Errorf("job.json do rework sem nota ou anterior: %+v", spec)
	}
	if _, err := os.Stat(filepath.Join(done.JobDir.String, "previous", "round-3", "review-code.md")); err != nil {
		t.Error("o rework deveria levar os reviews do job anterior")
	}
	prs, _ := e.gh.snapshot()
	if len(prs) != 1 || !strings.Contains(prs[0].Body, "Nota de rework") {
		t.Errorf("o rework atualiza o mesmo PR: %+v", prs)
	}
	if log := gitIn(t, e.remote, "log", "--oneline", "sentinela/EX-1"); strings.Count(log, "\n") != 4 {
		t.Errorf("branch do job deveria ter init + 3 rodadas + rework:\n%s", log)
	}
}

// Contrato quebrado (primeira linha fora do esperado) e repo sem AGENTS.md: o job falha com o
// motivo, sem PR, e a issue recebe a label de falha.
func TestBrokenContractAndMissingAgentsMDFailTheJob(t *testing.T) {
	t.Run("contrato", func(t *testing.T) {
		e := setup(t, true)
		e.runner.breakAt = "decision.md"
		e.lin.AddLabel("EX-1", "sentinela-fix")
		_, _ = e.w.Enqueue(context.Background())
		job := e.runOnce(t)
		if job.Status != "failed" || !strings.Contains(job.Error.String, `decision.md começa com "LGTM"`) {
			t.Fatalf("job = %s: %s", job.Status, job.Error.String)
		}
		if prs, _ := e.gh.snapshot(); len(prs) != 0 {
			t.Error("contrato quebrado não abre PR")
		}
		if !slicesContains(e.lin.Snapshot()[0].LabelsAddedBySentinela, "lbl-failed") {
			t.Error("faltou a label de falha")
		}
		// Sem PR não há rework; um humano manda refazer com job retry, e o job novo abre o PR.
		if _, err := e.w.Rework(context.Background(), job.ID, "nota"); err == nil || !strings.Contains(err.Error(), "job retry") {
			t.Errorf("rework sem PR deveria apontar o retry: %v", err)
		}
		if _, err := e.w.Retry(context.Background(), job.ID); err != nil {
			t.Fatal(err)
		}
		e.runner.breakAt = ""
		again := e.runOnce(t)
		if again.Status != "succeeded" || again.Decision.String != "approve" || !again.PrUrl.Valid || again.PreviousID.Int64 != job.ID {
			t.Fatalf("retry = %s/%s pr %v: %s", again.Status, again.Decision.String, again.PrUrl, again.Error.String)
		}
		if _, err := e.w.Retry(context.Background(), again.ID); err == nil {
			t.Error("job que deu certo não pode ser refeito com retry")
		}
	})
	t.Run("sem AGENTS.md", func(t *testing.T) {
		e := setup(t, false)
		e.lin.AddLabel("EX-1", "sentinela-fix")
		_, _ = e.w.Enqueue(context.Background())
		job := e.runOnce(t)
		if job.Status != "failed" || !strings.Contains(job.Error.String, "não tem AGENTS.md nem CLAUDE.md") || len(e.runner.invocations()) != 0 {
			t.Fatalf("job = %s: %s (invocações %d)", job.Status, job.Error.String, len(e.runner.invocations()))
		}
		if c := e.lin.CommentsOf("EX-1"); len(c) != 1 || !strings.Contains(c[0].Body, "AGENTS.md") {
			t.Errorf("a issue deveria dizer o que falta: %+v", c)
		}
	})
}

// CI vermelho dispara o PR Shepherd, que corrige e o worker faz push; depois de 3 ciclos o
// Sentinela para e avisa. Merge e revert viram desfechos do achado.
func TestShepherdFixesRedCIUpToThreeCyclesAndOutcomesAreRecorded(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.AddLabel("EX-1", "sentinela-fix")
	_, _ = e.w.Enqueue(ctx)
	job := e.runOnce(t)
	red := func() {
		e.gh.mu.Lock()
		defer e.gh.mu.Unlock()
		head := gitIn(t, e.remote, "rev-parse", "sentinela/EX-1")
		e.gh.prs[0].Head.SHA = head
		e.gh.checks[head] = []github.CheckRun{{ID: 9, Name: "go test", Status: "completed", Conclusion: "failure"}}
	}
	for cycle := 1; cycle <= 3; cycle++ {
		red()
		res, err := e.w.FollowUp(ctx)
		if err != nil || len(res.Warnings) > 0 {
			t.Fatalf("ciclo %d: %v %v", cycle, err, res.Warnings)
		}
		got, _ := e.st.Q.GetExecution(ctx, job.ID)
		if int(got.ShepherdCycles) != cycle {
			t.Fatalf("ciclos = %d, quer %d (%v)", got.ShepherdCycles, cycle, res.Events)
		}
	}
	red()
	before := len(e.runner.invocations())
	res, _ := e.w.FollowUp(ctx)
	if len(e.runner.invocations()) != before || !strings.Contains(strings.Join(res.Events, " "), "shepherd esgotado") {
		t.Fatalf("depois de 3 ciclos o shepherd para: %v", res.Events)
	}
	if log := gitIn(t, e.remote, "log", "--oneline", "sentinela/EX-1"); strings.Count(log, " shepherd") != 3 {
		t.Errorf("três commits do shepherd na branch:\n%s", log)
	}

	// Um humano faz o merge (fora do Sentinela); depois alguém reverte na main.
	e.gh.mu.Lock()
	e.gh.prs[0].State, e.gh.prs[0].Merged = "closed", true
	now := time.Now()
	e.gh.prs[0].MergedAt, e.gh.prs[0].MergeCommitSHA = &now, "abc1234def5678"
	e.gh.mu.Unlock()
	if _, err := e.w.FollowUp(ctx); err != nil {
		t.Fatal(err)
	}
	if f, _ := e.st.Q.GetFinding(ctx, job.FindingID); f.State != "fixed" {
		t.Errorf("achado depois do merge = %s", f.State)
	}
	// O próprio worker move a issue para Done (ADR 0028), sem depender da integração do Linear
	// com o GitHub. O polling vê a issue concluída com o achado já fixed e grava o estado, sem um
	// segundo desfecho. O merge foi decisão humana.
	if got := issueState(e, "EX-1"); got != "Done" {
		t.Errorf("issue depois do merge = %q, quer Done", got)
	}
	var decidedBy string
	_ = e.st.Pool.QueryRow(ctx, "SELECT decided_by FROM outcomes WHERE finding_id = $1 AND kind = 'merged'", job.FindingID).Scan(&decidedBy)
	if decidedBy != "human" {
		t.Errorf("desfecho merged com decided_by = %q, quer human", decidedBy)
	}
	if _, err := (&linear.Syncer{Store: e.st, Linear: e.w.Linear}).Poll(ctx); err != nil {
		t.Fatal(err)
	}
	var stateType string
	var outcomes int
	_ = e.st.Pool.QueryRow(ctx, "SELECT state_type FROM linear_issues WHERE identifier = 'EX-1'").Scan(&stateType)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE finding_id = $1", job.FindingID).Scan(&outcomes)
	if stateType != "completed" || outcomes != 1 {
		t.Errorf("issue no banco = %q (quer completed), desfechos = %d (quer só o merged)", stateType, outcomes)
	}
	e.gh.mu.Lock()
	e.gh.commits = []github.Commit{{SHA: "fedcba9876543"}}
	e.gh.commits[0].Commit.Message = "Revert \"EX-1: ...\"\n\nThis reverts commit abc1234def5678."
	e.gh.mu.Unlock()
	if _, err := e.w.FollowUp(ctx); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	rows, _ := e.st.Pool.Query(ctx, "SELECT kind FROM outcomes WHERE execution_id = $1 ORDER BY id", job.ID)
	for rows.Next() {
		var k string
		_ = rows.Scan(&k)
		kinds = append(kinds, k)
	}
	rows.Close()
	if strings.Join(kinds, ",") != "merged,reverted" {
		t.Errorf("desfechos = %v", kinds)
	}
	if f, _ := e.st.Q.GetFinding(ctx, job.FindingID); f.State != "reverted" {
		t.Errorf("achado depois do revert = %s", f.State)
	}
	reverts, _ := e.st.Q.ModuleReverts(ctx, db.ModuleRevertsParams{Repo: "acme/svc", Module: "cmd/api"})
	if reverts != 1 {
		t.Errorf("histórico de revert do módulo = %d (o gate usa)", reverts)
	}
}

// Review humana pedindo mudança também aciona o shepherd; o webhook do GitHub valida a
// assinatura, deduplica a entrega e dispara o acompanhamento.
func TestHumanChangesRequestAndGitHubWebhook(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.AddLabel("EX-1", "sentinela-fix")
	_, _ = e.w.Enqueue(ctx)
	job := e.runOnce(t)
	e.gh.mu.Lock()
	e.gh.prs[0].Head.SHA = job.HeadSha.String
	e.gh.human[1] = []github.Review{{ID: 77, State: "CHANGES_REQUESTED", Body: "Use uma constante para o timeout."}}
	e.gh.human[1][0].User.Login, e.gh.human[1][0].User.Type = "fulano", "User"
	e.gh.mu.Unlock()

	calls := 0
	hook := &GitHubWebhook{Secret: "s3cr3t", Store: e.st, OnEvent: func(ctx context.Context, _ string) error {
		calls++
		_, err := e.w.FollowUp(ctx)
		return err
	}}
	body := []byte(`{"action":"submitted"}`)
	send := func(sig, delivery string) int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
		req.Header.Set("X-Hub-Signature-256", sig)
		req.Header.Set("X-GitHub-Event", "pull_request_review")
		req.Header.Set("X-GitHub-Delivery", delivery)
		rec := httptest.NewRecorder()
		hook.ServeHTTP(rec, req)
		return rec.Code
	}
	mac := hmac.New(sha256.New, []byte("s3cr3t"))
	mac.Write(body)
	good := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if code := send("sha256=00", "d1"); code != http.StatusUnauthorized {
		t.Errorf("assinatura inválida = %d", code)
	}
	if code := send(good, "d1"); code != http.StatusOK || calls != 1 {
		t.Fatalf("entrega válida = %d, chamadas %d", code, calls)
	}
	if code := send(good, "d1"); code != http.StatusOK || calls != 1 {
		t.Errorf("entrega repetida deveria ser ignorada: %d, chamadas %d", code, calls)
	}
	got, _ := e.st.Q.GetExecution(ctx, job.ID)
	if got.ShepherdCycles != 1 {
		t.Fatalf("a review humana deveria acionar 1 ciclo do shepherd, veio %d", got.ShepherdCycles)
	}
	last := e.runner.invocations()[len(e.runner.invocations())-1]
	event, _ := os.ReadFile(filepath.Join(strings.TrimPrefix(last.Prompt, "/sentinela-factory:sentinela-shepherd "), "event.md"))
	if !bytes.Contains(event, []byte("Use uma constante para o timeout.")) || last.Model != "claude-sonnet-5-5" {
		t.Errorf("evento do shepherd = %s (modelo %s)", event, last.Model)
	}
}

func slicesContains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func ptr[T any](v T) *T { return &v }

// Instruções da equipe (ADR 0026), como na EX-14: comentários humanos até o job começar entram na
// spec, no job.json, no corpo do PR e no registro do job; os do Sentinela, de integração e de
// automação ignorada ficam de fora. No job seguinte do achado, só os feitos depois do anterior.
func TestTeamCommentsBecomeInstructionsInSpecPRAndRecord(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.Execution.IgnoreCommentPrefixes = []string{"🤖 Claude"}
	before := time.Now().Add(-time.Hour)
	e.lin.CommentAt("EX-1", "Fulano", "🤖 Claude começou a trabalhar nesta issue", before)
	e.lin.CommentAt("EX-1", "Fulano", "**Sentinela:** comentário do próprio Sentinela", before.Add(time.Minute))
	e.lin.CommentAt("EX-1", "", "comentário de integração", before.Add(2*time.Minute))
	e.lin.AddLabel("EX-1", "sentinela-fix")
	e.lin.CommentAt("EX-1", "Fulano", "Use 5s de ReadHeaderTimeout, sem outros timeouts.", before.Add(3*time.Minute))
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	if job.Decision.String != "approve" {
		t.Fatalf("job = %s: %s", job.Decision.String, job.Error.String)
	}
	spec, _ := os.ReadFile(filepath.Join(job.JobDir.String, "spec.md"))
	issue, _ := os.ReadFile(filepath.Join(job.JobDir.String, "issue.md"))
	for name, doc := range map[string][]byte{"spec.md": spec, "issue.md": issue} {
		if !bytes.Contains(doc, []byte("## Instruções da equipe")) || !bytes.Contains(doc, []byte("> Use 5s de ReadHeaderTimeout, sem outros timeouts.")) {
			t.Errorf("%s sem a instrução:\n%s", name, doc)
		}
		for _, out := range []string{"Claude começou", "próprio Sentinela", "integração"} {
			if bytes.Contains(doc, []byte(out)) {
				t.Errorf("%s com comentário que não é instrução (%q)", name, out)
			}
		}
	}
	var js JobSpec
	data, _ := os.ReadFile(filepath.Join(job.JobDir.String, "job.json"))
	_ = json.Unmarshal(data, &js)
	if len(js.Instructions) != 1 || !js.InstructionWindow.From.IsZero() {
		t.Errorf("job.json: %+v / %+v", js.Instructions, js.InstructionWindow)
	}
	var arts map[string]Artifact
	_ = json.Unmarshal(job.Artifacts, &arts)
	if a := arts[instructionsKey]; a.Verdict != "INSTRUCTIONS: 1" || len(a.Items) != 1 || !strings.Contains(a.Items[0], "Use 5s de ReadHeaderTimeout") {
		t.Errorf("registro do job = %+v", a)
	}
	prs, _ := e.gh.snapshot()
	if len(prs) != 1 || !strings.Contains(prs[0].Body, "### Instruções da equipe") {
		t.Fatalf("PR sem as instruções: %+v", prs)
	}

	// Rework: entra só o comentário feito depois de o primeiro job começar.
	e.lin.CommentAt("EX-1", "Fulano", "Cubra também o ReadTimeout no teste.", time.Now())
	if _, err := e.w.Rework(ctx, job.ID, "nota"); err != nil {
		t.Fatal(err)
	}
	done := e.runOnce(t)
	_ = json.Unmarshal(done.Artifacts, &arts)
	if a := arts[instructionsKey]; a.Verdict != "INSTRUCTIONS: 1" || !strings.Contains(a.Items[0], "ReadTimeout") || !strings.Contains(a.Summary, "quando o job anterior começou") {
		t.Errorf("registro do rework = %+v", a)
	}
}

// Com PR aberto, a issue vai para o review_state do time (ADR 0027): registro de fato. Sem
// review_state, nada se move; issue concluída ou cancelada nunca é mexida.
func TestOpenPRMovesIssueToReviewState(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	stateOf := func() string {
		for _, is := range e.lin.Snapshot() {
			if is.Identifier == "EX-1" {
				for _, s := range e.lin.States["team-ex"] {
					if s.ID == is.StateID {
						return s.Name
					}
				}
			}
		}
		return ""
	}
	e.cfg.Defaults.Linear.ReviewState = "In Review"
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	if !job.PrNumber.Valid || stateOf() != "In Review" {
		t.Fatalf("PR %v, estado %q; quer In Review", job.PrNumber, stateOf())
	}

	// Concluída por uma pessoa: um novo PR (rework) não a tira de Done.
	e.lin.Move("EX-1", "Done")
	f, _ := e.w.finding(ctx, job.FindingID)
	e.w.moveToReview(ctx, job, f)
	if stateOf() != "Done" {
		t.Errorf("issue concluída foi movida para %q", stateOf())
	}
	e.cfg.Defaults.Linear.ReviewState = ""
	e.lin.Move("EX-1", "Backlog")
	e.w.moveToReview(ctx, job, f)
	if stateOf() != "Backlog" {
		t.Errorf("sem review_state a issue foi movida para %q", stateOf())
	}
}

// issueState devolve o nome do estado atual de uma issue no Linear falso.
func issueState(e *env, identifier string) string {
	for _, is := range e.lin.Snapshot() {
		if is.Identifier == identifier {
			for _, st := range e.lin.States["team-ex"] {
				if st.ID == is.StateID {
					return st.Name
				}
			}
		}
	}
	return ""
}

// Com o PR mergeado, a issue vai para o done_state do time (ADR 0028), padrão Done. Issue
// cancelada ou já concluída por uma pessoa não é mexida.
func TestMergedPRMovesIssueToDoneState(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	if !job.PrNumber.Valid {
		t.Fatal("sem PR")
	}
	if e.w.Cfg.DoneStateFor(nil) != "Done" {
		t.Fatalf("done_state padrão = %q", e.w.Cfg.DoneStateFor(nil))
	}

	// Cancelada por uma pessoa: o merge não a reabre como concluída.
	e.lin.Move("EX-1", "Canceled")
	e.w.moveToDone(ctx, job)
	if got := issueState(e, "EX-1"); got != "Canceled" {
		t.Errorf("issue cancelada foi movida para %q", got)
	}

	// done_state configurado vale no lugar do padrão.
	e.cfg.Defaults.Linear.DoneState = "Todo"
	e.lin.Move("EX-1", "In Review")
	e.w.moveToDone(ctx, job)
	if got := issueState(e, "EX-1"); got != "Todo" {
		t.Errorf("com done_state Todo a issue foi para %q", got)
	}

	// Estado que não existe no time: só avisa, não move.
	e.cfg.Defaults.Linear.DoneState = "Shipped"
	e.lin.Move("EX-1", "In Review")
	e.w.moveToDone(ctx, job)
	if got := issueState(e, "EX-1"); got != "In Review" {
		t.Errorf("com estado inexistente a issue foi para %q", got)
	}
}

// Job que falhou ou foi bloqueado sem PR: o worker tira a label de gatilho e, quando uma pessoa
// a aplica de novo, enfileira uma nova tentativa, como `job retry` (ADR 0029). Job com PR não
// reenfileira.
func TestReappliedTriggerLabelRetriesAFailedJob(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.runner.breakAt = "decision.md"
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	failed := e.runOnce(t)
	if failed.Status != "failed" {
		t.Fatalf("job = %s", failed.Status)
	}
	hasTrigger := func() bool { return slicesContains(e.lin.Snapshot()[0].LabelIDs, e.lin.Labels["sentinela-fix"]) }
	if hasTrigger() {
		t.Fatal("o worker deveria ter tirado a label de gatilho ao terminar sem PR")
	}
	enqueue := func() []db.Execution {
		t.Helper()
		out, err := e.w.Enqueue(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := enqueue(); len(got) != 0 {
		t.Fatalf("sem a label, nada entra na fila: %+v", got)
	}

	e.lin.AddLabel("EX-1", "sentinela-fix")
	got := enqueue()
	if len(got) != 1 || got[0].PreviousID.Int64 != failed.ID || got[0].Kind != "fix" {
		t.Fatalf("label aplicada de novo por uma pessoa: %+v", got)
	}
	if again := enqueue(); len(again) != 0 {
		t.Fatalf("com o job na fila, nada mais entra: %+v", again)
	}
	var told, retried bool
	for _, c := range e.lin.Snapshot()[0].Comments {
		told = told || strings.Contains(c.Body, "aplique `sentinela-fix` outra vez")
		retried = retried || strings.Contains(c.Body, fmt.Sprintf("nova tentativa da execução #%d", failed.ID))
	}
	if !told || !retried {
		t.Errorf("comentários na issue: explica como refazer %v, nova tentativa %v", told, retried)
	}

	// A nova tentativa abre PR: a label fica, e nada reenfileira (o caminho é o PR).
	e.runner.breakAt = ""
	ok := e.runOnce(t)
	if !ok.PrNumber.Valid || !hasTrigger() {
		t.Fatalf("nova tentativa: PR %v, label %v (%s)", ok.PrNumber, hasTrigger(), ok.Error.String)
	}
	if got := enqueue(); len(got) != 0 {
		t.Fatalf("job com PR reenfileirou: %+v", got)
	}
}

// Execução que terminou sem PR antes desta regra (a label ficou na issue): o Enqueue tira a
// label e registra; a label antiga não vira pedido novo.
func TestEnqueueConsumesTheTriggerLeftByAnOlderJob(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.runner.breakAt = "decision.md"
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	failed := e.runOnce(t)
	// Simula o estado antigo: label na issue e nada registrado na execução.
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.st.Pool.Exec(ctx, "UPDATE executions SET artifacts = artifacts - 'trigger-consumed' WHERE id = $1", failed.ID); err != nil {
		t.Fatal(err)
	}
	if got, err := e.w.Enqueue(ctx); err != nil || len(got) != 0 {
		t.Fatalf("label antiga virou pedido: %+v %v", got, err)
	}
	if slicesContains(e.lin.Snapshot()[0].LabelIDs, e.lin.Labels["sentinela-fix"]) {
		t.Error("o Enqueue deveria ter tirado a label antiga")
	}
	if c := e.lin.CommentsOf("EX-1"); !strings.Contains(c[len(c)-1].Body, "tirei a `sentinela-fix`") {
		t.Errorf("o Enqueue deveria avisar que tirou a label: %+v", c[len(c)-1])
	}
	// M7 (revisão 2026-10-11): o status conta as labels de gatilho que o worker tirou.
	if tc, err := e.st.Q.TriggerConsumedCounts(ctx); err != nil || tc.Removed != 1 || tc.Removed30d != 1 || tc.Consumed != 1 {
		t.Errorf("gatilhos consumidos = %+v, %v (quer 1 tirado, 1 em 30 dias, 1 registrado)", tc, err)
	}
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if got, _ := e.w.Enqueue(ctx); len(got) != 1 {
		t.Fatalf("label aplicada de novo: %+v", got)
	}
}

// Job que parou no timeout: o retry retoma a mesma sessão (--resume), no mesmo worktree e
// diretório do job, com o erro na mensagem; os JSONL da sessão ficam em session_log (ADR 0030).
func TestRetryResumesTheSessionAfterTimeout(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.runner.timeoutNext = true
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	failed := e.runOnce(t)
	if failed.Status != "failed" || failureOf(failed) != FailTimeout {
		t.Fatalf("job = %s, falha %q: %s", failed.Status, failureOf(failed), failed.Error.String)
	}
	var log SessionLog
	if err := json.Unmarshal(failed.SessionLog, &log); err != nil || !strings.HasSuffix(log.Main, failed.SessionID.String+".jsonl") || len(log.Subagents) != 1 {
		t.Fatalf("session_log = %s (%v)", failed.SessionLog, err)
	}

	if _, err := e.w.Retry(ctx, failed.ID); err != nil {
		t.Fatal(err)
	}
	again := e.runOnce(t)
	calls := e.runner.invocations()
	inv := calls[len(calls)-1]
	if inv.Resume != failed.SessionID.String || inv.Dir != failed.Worktree.String || inv.JobDir != failed.JobDir.String {
		t.Fatalf("retomada: resume %q dir %q job %q; quer %q %q %q", inv.Resume, inv.Dir, inv.JobDir, failed.SessionID.String, failed.Worktree.String, failed.JobDir.String)
	}
	if !strings.Contains(inv.Prompt, fmt.Sprintf("execução #%d", failed.ID)) || !strings.Contains(inv.Prompt, "deadline exceeded") {
		t.Errorf("mensagem da retomada sem o erro: %q", inv.Prompt)
	}
	if !slicesContains(inv.Args(), "--resume") {
		t.Errorf("args sem --resume: %v", inv.Args())
	}
	if again.Status != "succeeded" || !again.PrUrl.Valid || !strings.Contains(again.ResumeNote.String, "retomou a sessão "+failed.SessionID.String) {
		t.Fatalf("retry = %s pr %v nota %q: %s", again.Status, again.PrUrl, again.ResumeNote.String, again.Error.String)
	}
}

// Sem a sessão no disco (ou com falha não retomável), o retry recomeça do zero e grava o motivo.
func TestRetryStartsOverWhenTheSessionIsGone(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.runner.timeoutNext = true
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	failed := e.runOnce(t)
	if err := os.RemoveAll(filepath.Join(e.runner.home, "projects")); err != nil {
		t.Fatal(err)
	}
	// O que a tentativa que falhou deixou no worktree: um commit e uma mudança não commitada.
	commit(failed.Worktree.String, "cmd/api/parcial.go", "package main\n", "EX-1: trabalho parcial")
	_ = os.WriteFile(filepath.Join(failed.Worktree.String, "rascunho.txt"), []byte("wip"), 0o600)
	if _, err := e.w.Retry(ctx, failed.ID); err != nil {
		t.Fatal(err)
	}
	again := e.runOnce(t)
	// Recomeçar do zero não apaga a tentativa anterior: o worktree dela foi guardado (ADR 0034).
	old, _ := e.st.Q.GetExecution(ctx, failed.ID)
	if want := failed.Worktree.String + ".exec-" + strconv.FormatInt(failed.ID, 10); old.Worktree.String != want || again.Worktree.String != failed.Worktree.String {
		t.Fatalf("worktree guardado em %q (quer %q); o novo em %q", old.Worktree.String, want, again.Worktree.String)
	}
	if log := gitIn(t, old.Worktree.String, "log", "--oneline", "-1"); !strings.Contains(log, "trabalho parcial") {
		t.Errorf("commit da tentativa anterior sumiu: %s", log)
	}
	if _, err := os.Stat(filepath.Join(old.Worktree.String, "rascunho.txt")); err != nil {
		t.Errorf("mudança não commitada sumiu: %v", err)
	}
	calls := e.runner.invocations()
	if inv := calls[len(calls)-1]; inv.Resume != "" || !strings.HasPrefix(inv.Prompt, "/sentinela-factory:sentinela-fix ") {
		t.Fatalf("sem sessão deveria recomeçar: resume %q prompt %q", inv.Resume, inv.Prompt)
	}
	if again.Status != "succeeded" || !strings.Contains(again.ResumeNote.String, "recomeçou do zero") || !strings.Contains(again.ResumeNote.String, "não existe mais") {
		t.Fatalf("retry = %s nota %q", again.Status, again.ResumeNote.String)
	}
}

// execution.max_resumes: depois do limite, a tentativa falha sem rodar, com o motivo, e o retry
// seguinte recomeça do zero (ADR 0030).
func TestResumeLimitFailsWithoutRunningAndNextRetryStartsOver(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.Execution.MaxResumes = 1
	e.runner.timeoutNext = true
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	first := e.runOnce(t)
	retry := func(prev db.Execution) db.Execution {
		t.Helper()
		if _, err := e.w.Retry(ctx, prev.ID); err != nil {
			t.Fatal(err)
		}
		return e.runOnce(t)
	}
	e.runner.timeoutNext = true
	resumed := retry(first)
	if resumed.Status != "failed" || !strings.HasPrefix(resumed.ResumeNote.String, "retomou a sessão") {
		t.Fatalf("primeira retomada = %s, nota %q", resumed.Status, resumed.ResumeNote.String)
	}
	calls := len(e.runner.invocations())
	blocked := retry(resumed)
	if blocked.Status != "failed" || failureOf(blocked) != FailResumeLimit || !strings.Contains(blocked.ResumeNote.String, "execution.max_resumes = 1") {
		t.Fatalf("no limite = %s, falha %q, nota %q", blocked.Status, failureOf(blocked), blocked.ResumeNote.String)
	}
	if len(e.runner.invocations()) != calls {
		t.Error("no limite a tentativa não deveria chamar o Claude Code")
	}
	fresh := retry(blocked)
	inv := e.runner.invocations()[len(e.runner.invocations())-1]
	if fresh.Status != "succeeded" || inv.Resume != "" || !strings.Contains(fresh.ResumeNote.String, "não é retomável") {
		t.Fatalf("retry depois do limite = %s, resume %q, nota %q", fresh.Status, inv.Resume, fresh.ResumeNote.String)
	}
}

// Worktree mexido depois da falha (commit novo ou mudança não commitada): recomeça do zero.
func TestResumeStartsOverWhenTheWorktreeChanged(t *testing.T) {
	for _, tc := range []struct{ name, want string }{
		{"commit", "parou em"},
		{"sujo", "mudanças que não são da execução"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t, true)
			ctx := context.Background()
			e.runner.timeoutNext = true
			e.lin.AddLabel("EX-1", "sentinela-fix")
			if _, err := e.w.Enqueue(ctx); err != nil {
				t.Fatal(err)
			}
			failed := e.runOnce(t)
			if tc.name == "commit" {
				commit(failed.Worktree.String, "outro.txt", "de fora", "commit de fora")
			} else {
				write(failed.Worktree.String, "cmd/api/main.go", "package main\n// editado à mão\n")
			}
			if _, err := e.w.Retry(ctx, failed.ID); err != nil {
				t.Fatal(err)
			}
			again := e.runOnce(t)
			inv := e.runner.invocations()[len(e.runner.invocations())-1]
			if inv.Resume != "" || !strings.Contains(again.ResumeNote.String, tc.want) {
				t.Fatalf("resume %q, nota %q; quer recomeço com %q", inv.Resume, again.ResumeNote.String, tc.want)
			}
		})
	}
}
