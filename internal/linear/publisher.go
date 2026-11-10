package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Resolve decide o dono de um caminho do repo (o resolvedor de ownership da rodada).
type Resolve func(path string) (repo.Resolution, error)

// Publisher cria no Linear as issues dos achados aprovados pelo Judge. É código
// determinístico: board, projeto, estado, labels e responsável vêm do roteamento gravado no
// achado e do sentinela.yaml.
type Publisher struct {
	Cfg    *config.Config
	Store  *store.Store
	Linear Tracker
	// Notify avisa cada issue criada no canal do time dono (teams[].notify.slack, ou o canal
	// padrão); nil desliga. Falhas viram avisos no resultado.
	Notify *notify.Router
	// CommandsDoc diz qual AGENTS.md ou CLAUDE.md o job usaria para o escopo do achado (o mais
	// próximo dele ou o da raiz; doc vazio: nenhum) e se isso é conhecido (o clone do scan existe).
	// Sem o arquivo, a issue ganha uma linha avisando que a correção só roda depois que ele existir.
	// nil: não confere.
	CommandsDoc func(repoName string, scope []string) (doc string, known bool)
	// Now é o relógio (testes); nil usa time.Now.
	Now func() time.Time

	teams    map[string]*TeamRefs
	projects map[string]string
	users    map[string]string
	install  *Install
}

// RelatedIssue é uma issue criada no board de outro time afetado por um achado entre módulos.
type RelatedIssue struct {
	Team       string `json:"team"`
	Module     string `json:"module"`
	Identifier string `json:"identifier"`
	URL        string `json:"url"`
}

// PublishedIssue é a issue principal de um achado.
type PublishedIssue struct {
	FindingID   int64          `json:"finding_id"`
	Fingerprint string         `json:"fingerprint"`
	Title       string         `json:"title"`
	Category    string         `json:"category"`
	Severity    string         `json:"severity"`
	Team        string         `json:"team"`
	Identifier  string         `json:"identifier"`
	URL         string         `json:"url"`
	Reused      bool           `json:"reused,omitempty"` // já existia no Linear (publicação retomada)
	Related     []RelatedIssue `json:"related,omitempty"`
	// Overlaps são issues já existentes que cobrem parte deste achado (o Judge reduziu o
	// escopo); ficam linkadas como relacionadas.
	Overlaps []string `json:"overlaps,omitempty"`
	// Question: o achado depende de premissa de produto e saiu como pergunta ao time.
	Question bool `json:"question,omitempty"`
	// AlsoAffects são os outros times que as evidências tocam (seção "Também afeta", ADR 0044).
	AlsoAffects []repo.Secondary `json:"also_affects,omitempty"`
}

// overlapIssue é um achado aberto que cobre parte do achado publicado.
type overlapIssue struct {
	Fingerprint, Title string
	Issue              *db.LinearIssue // nil se ainda não tem issue
}

// PublishResult resume uma publicação.
type PublishResult struct {
	Issues   []PublishedIssue `json:"issues"`
	Deferred map[string]int   `json:"deferred,omitempty"` // time -> achados adiados pelo limite
	// DeferredSeverity é a mesma contagem aberta por severidade (time -> severidade -> achados).
	DeferredSeverity map[string]map[string]int `json:"deferred_severity,omitempty"`
	Warnings         []string                  `json:"warnings,omitempty"`
	// Postponed: a publicação deste repo ficou para o fim do scan --all, com o limite por time
	// somado entre os repos (Round).
	Postponed bool `json:"postponed,omitempty"`
	// Failed conta as issues que falharam nesta rodada: ficam pendentes e a próxima tenta de novo.
	Failed int `json:"failed,omitempty"`
	// PausedUntil: a publicação parou no limite de uso da assinatura e retoma depois deste
	// horário (o serve retoma sozinho; ADR 0055).
	PausedUntil *time.Time `json:"paused_until,omitempty"`
}

// ErrPublishFailed: parte das issues não foi publicada; o resto saiu e as que falharam ficam
// pendentes para a próxima rodada.
var ErrPublishFailed = errors.New("publicação no Linear incompleta")

// ErrUsagePaused: a publicação parou no limite de uso da assinatura; o que falta fica pendente.
var ErrUsagePaused = errors.New("publicação pausada pelo limite de uso da assinatura")

func (p *Publisher) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

const defaultTeam = "(padrão)"

// pendingItem é um achado pendente com a rodada e o resolvedor de ownership do repo dele.
type pendingItem struct {
	row     db.PendingFindingsRow
	runID   int64
	resolve Resolve
}

// Publish cria as issues pendentes do repo, no máximo publish.max_issues_per_team_per_run
// por time (principais e relacionadas). O excedente fica pendente para a próxima rodada.
func (p *Publisher) Publish(ctx context.Context, repoName string, runID int64, resolve Resolve) (PublishResult, error) {
	pending, err := p.Store.Q.PendingFindings(ctx, repoName)
	if err != nil {
		return PublishResult{}, err
	}
	items := make([]pendingItem, len(pending))
	for i, row := range pending {
		items[i] = pendingItem{row: row, runID: runID, resolve: resolve}
	}
	return p.publishPending(ctx, items)
}

// publishPending publica os itens na ordem dada, com um contador por time só para todos eles.
func (p *Publisher) publishPending(ctx context.Context, pending []pendingItem) (PublishResult, error) {
	res := PublishResult{Deferred: map[string]int{}, DeferredSeverity: map[string]map[string]int{}}
	if len(pending) > 0 {
		in, err := LoadInstall(ctx, p.Store.Q)
		if err != nil {
			return res, err
		}
		p.install = &in
	}
	used := map[string]int{}
	limit := p.Cfg.Publish.MaxIssuesPerTeamPerRun
	for _, it := range pending {
		row, resolve := it.row, it.resolve
		f, err := findingFrom(row)
		if err != nil {
			return res, err
		}
		team := teamKey(f.Routing.Team)
		related := p.relatedTargets(f, team, resolve, &res)
		need := map[string]int{team: 1}
		for _, r := range related {
			need[teamKey(r.Team)]++
		}
		over := false
		for t, n := range need {
			if used[t]+n > limit {
				over = true
			}
		}
		if over {
			res.Deferred[team]++
			if res.DeferredSeverity[team] == nil {
				res.DeferredSeverity[team] = map[string]int{}
			}
			res.DeferredSeverity[team][f.Severity]++
			continue
		}
		pub, err := p.publishOne(ctx, it.runID, row, f, related, alsoAffects(f, related, resolve))
		if err != nil {
			switch {
			case errors.Is(err, ErrNotFound):
				res.Warnings = append(res.Warnings, fmt.Sprintf("achado %d (%s) não publicado: %v", row.ID, f.Fingerprint, err))
			case ctx.Err() != nil:
				return res, err
			case llm.UsageLimited(err.Error()):
				// As próximas falhariam do mesmo jeito: para e guarda quando a cota volta.
				until, ok := llm.UsageResetAt(err.Error(), p.now())
				if !ok {
					until = p.now().Add(time.Hour)
				}
				res.PausedUntil = &until
				return res, fmt.Errorf("%w até %s: %w", ErrUsagePaused, until.Format("02/01 15:04 MST"), err)
			default:
				// Uma issue que falha não segura as outras (ADR 0055); a próxima rodada tenta de novo.
				res.Failed++
				res.Warnings = append(res.Warnings, fmt.Sprintf("achado %d (%s) não publicado; fica pendente para a próxima rodada: %v", row.ID, f.Fingerprint, err))
			}
			continue
		}
		for t, n := range need {
			used[t] += n
		}
		res.Issues = append(res.Issues, pub)
		p.notifyTeams(ctx, f, pub, &res)
	}
	if len(res.Deferred) == 0 {
		res.Deferred, res.DeferredSeverity = nil, nil
	}
	if res.Failed > 0 {
		return res, fmt.Errorf("%w: %d issue(s) falharam e ficam pendentes", ErrPublishFailed, res.Failed)
	}
	return res, nil
}

// Round junta a publicação dos repos de um scan --all (M8 da revisão 2026-10-11): cada repo só
// registra a rodada e o resolvedor, e Flush publica no fim os pendentes de todos, por severidade,
// com publish.max_issues_per_team_per_run contado por time na rodada inteira, somando os repos.
// Assim, um low do primeiro repo não toma a vaga de um critical do último.
type Round struct {
	repos []roundRepo
}

type roundRepo struct {
	name    string
	runID   int64
	resolve Resolve
}

// Publish registra o repo para o Flush; nada vai ao Linear agora.
func (r *Round) Publish(_ context.Context, repoName string, runID int64, resolve Resolve) (PublishResult, error) {
	for i := range r.repos {
		if r.repos[i].name == repoName {
			r.repos[i] = roundRepo{repoName, runID, resolve}
			return PublishResult{Postponed: true}, nil
		}
	}
	r.repos = append(r.repos, roundRepo{repoName, runID, resolve})
	return PublishResult{Postponed: true}, nil
}

// Repos são os repos registrados, na ordem do scan.
// RunIDs são as rodadas dos repos registrados, para gravar nelas uma pausa da publicação.
func (r *Round) RunIDs() []int64 {
	ids := make([]int64, 0, len(r.repos))
	for _, rr := range r.repos {
		ids = append(ids, rr.runID)
	}
	return ids
}

func (r *Round) Repos() []string {
	out := make([]string, len(r.repos))
	for i, rr := range r.repos {
		out[i] = rr.name
	}
	return out
}

// Flush publica os pendentes dos repos registrados com o publisher dado. A ordem é a do
// PendingFindings, agora entre os repos: severidade, confiança do Judge, id.
func (r *Round) Flush(ctx context.Context, p *Publisher) (PublishResult, error) {
	var items []pendingItem
	for _, rr := range r.repos {
		pending, err := p.Store.Q.PendingFindings(ctx, rr.name)
		if err != nil {
			return PublishResult{}, err
		}
		for _, row := range pending {
			items = append(items, pendingItem{row: row, runID: rr.runID, resolve: rr.resolve})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i].row, items[j].row
		if sa, sb := severityRank(a.Severity), severityRank(b.Severity); sa != sb {
			return sa < sb
		}
		if a.JudgeConfidence.Valid != b.JudgeConfidence.Valid {
			return a.JudgeConfidence.Valid
		}
		if a.JudgeConfidence.Float32 != b.JudgeConfidence.Float32 {
			return a.JudgeConfidence.Float32 > b.JudgeConfidence.Float32
		}
		return a.ID < b.ID
	})
	return p.publishPending(ctx, items)
}

// severityRank é a ordem de publicação do PendingFindings.
func severityRank(s string) int {
	switch s {
	case "critical":
		return 0
	case "high":
		return 1
	case "medium":
		return 2
	}
	return 3
}

func teamKey(t string) string {
	if t == "" {
		return defaultTeam
	}
	return t
}

// relatedTarget é um módulo afetado de outro time.
type relatedTarget struct {
	Team    string
	Module  string
	Routing repo.Resolution
}

// relatedTargets resolve o dono de cada módulo relacionado. Módulos do mesmo time do achado
// não geram issue extra: já estão no board certo e aparecem no corpo da principal.
func (p *Publisher) relatedTargets(f signals.Finding, team string, resolve Resolve, res *PublishResult) []relatedTarget {
	var out []relatedTarget
	seen := map[string]bool{team: true}
	for _, m := range f.RelatedModules {
		r, err := resolve(strings.TrimSuffix(m, "/") + "/")
		if err != nil {
			res.Warnings = append(res.Warnings, fmt.Sprintf("módulo relacionado %s de %s: %v", m, f.Fingerprint, err))
			continue
		}
		if r.Ignored || seen[teamKey(r.Team)] {
			continue
		}
		seen[teamKey(r.Team)] = true
		out = append(out, relatedTarget{Team: r.Team, Module: m, Routing: r})
	}
	return out
}

func (p *Publisher) publishOne(ctx context.Context, runID int64, row db.PendingFindingsRow, f signals.Finding, related []relatedTarget, also []repo.Secondary) (PublishedIssue, error) {
	findingID := row.ID
	verdict := judgeInfo{Confidence: float64(row.JudgeConfidence.Float32), Valid: row.JudgeConfidence.Valid, Reason: row.JudgeReason.String}
	pub := PublishedIssue{FindingID: findingID, Fingerprint: f.Fingerprint, Title: f.Title, Category: f.Category, Severity: f.Severity, Team: f.Routing.Team, AlsoAffects: also}
	team := p.Cfg.Teams[f.Routing.Team]
	overlaps, err := p.overlaps(ctx, row)
	if err != nil {
		return pub, err
	}
	// Módulo > categoria do time > o gravado (time > repo > defaults) > defaults (ADR 0040).
	project := p.Cfg.PublishProject(f.Routing.Team, f.Category, f.Routing.LinearProject)
	t := target{
		linearTeam: f.Routing.LinearTeam, project: project, team: team, teamName: f.Routing.Team,
		assignee: f.Routing.Assignee, marker: primaryMarker(f.Fingerprint),
		priority: priority(f.Severity),
	}
	t.title, t.body = f.Title, issueBody(f, installMarker(t.marker, p.install.ID), verdict, related, overlaps, pub.AlsoAffects...)
	if row.Premise != "" && row.PremiseAnswer == "" && p.Cfg.Labels.Question != "" {
		// Depende de premissa de produto: vira pergunta ao time, não issue normal (ADR 0020).
		t.title, t.body = QuestionTitle(row.Premise), questionBody(f, installMarker(t.marker, p.install.ID), verdict, row.Premise, p.Cfg.Labels)
		t.extraLabels = []string{p.Cfg.Labels.Question}
		pub.Question = true
	}
	if p.CommandsDoc != nil {
		if doc, known := p.CommandsDoc(f.Repo, f.CommandsScope()); known && doc == "" {
			t.body = withNote(t.body, noCommandsDocNote(f.Repo, p.Cfg.LabelsFor(f.Routing.Team).Trigger))
		}
	}
	issue, reused, err := p.ensureIssue(ctx, t)
	if err != nil {
		return pub, err
	}
	pub.Identifier, pub.URL, pub.Reused = issue.Identifier, issue.URL, reused
	for _, o := range overlaps {
		if o.Issue == nil {
			continue
		}
		if !reused {
			if err := p.Linear.RelateIssues(ctx, issue.ID, o.Issue.LinearID); err != nil {
				return pub, err
			}
		}
		pub.Overlaps = append(pub.Overlaps, o.Issue.Identifier)
	}
	if err := p.record(ctx, findingID, runID, "primary", f.Routing.Team, f.Module, f.Routing.LinearTeam, issue); err != nil {
		return pub, err
	}
	if err := p.Store.Q.SetFindingLinearIssue(ctx, db.SetFindingLinearIssueParams{ID: findingID, LinearIssueID: pgtype.Text{String: issue.ID, Valid: true}}); err != nil {
		return pub, err
	}
	for _, r := range related {
		rt := p.Cfg.Teams[r.Team]
		relMarker := relatedMarker(f.Fingerprint, teamKey(r.Team))
		rel, relReused, err := p.ensureIssue(ctx, target{
			linearTeam: r.Routing.LinearTeam, project: p.Cfg.PublishProject(r.Team, f.Category, r.Routing.LinearProject), team: rt, teamName: r.Team,
			assignee: r.Routing.Assignee, marker: relMarker,
			title: "Relacionada: " + f.Title, body: relatedBody(f, installMarker(relMarker, p.install.ID), issue, r), priority: priority(f.Severity),
		})
		if err != nil {
			return pub, err
		}
		if !relReused {
			if err := p.Linear.RelateIssues(ctx, issue.ID, rel.ID); err != nil {
				return pub, err
			}
		}
		if err := p.record(ctx, findingID, runID, "related", r.Team, r.Module, r.Routing.LinearTeam, rel); err != nil {
			return pub, err
		}
		pub.Related = append(pub.Related, RelatedIssue{Team: r.Team, Module: r.Module, Identifier: rel.Identifier, URL: rel.URL})
	}
	return pub, nil
}

// overlaps lê da decisão do Judge (run_items) os achados abertos que cobrem parte deste e as
// issues principais deles.
func (p *Publisher) overlaps(ctx context.Context, row db.PendingFindingsRow) ([]overlapIssue, error) {
	if !row.RunID.Valid {
		return nil, nil
	}
	raw, err := p.Store.Q.JudgeRelatedTo(ctx, db.JudgeRelatedToParams{RunID: row.RunID.Int64, ItemKey: row.Fingerprint})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var fps []string
	if err := json.Unmarshal(raw, &fps); err != nil {
		return nil, fmt.Errorf("achado %d: related_to inválido: %w", row.ID, err)
	}
	var out []overlapIssue
	for _, fp := range fps {
		other, err := p.Store.Q.FindingByFingerprint(ctx, fp)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		o := overlapIssue{Fingerprint: fp, Title: other.Title}
		issues, err := p.Store.Q.ListLinearIssuesByFindings(ctx, []int64{other.ID})
		if err != nil {
			return nil, err
		}
		for i := range issues {
			if issues[i].Role == "primary" {
				o.Issue = &issues[i]
				break
			}
		}
		out = append(out, o)
	}
	return out, nil
}

type target struct {
	linearTeam, project, teamName, assignee, marker, title, body string
	team                                                         *config.Team
	priority                                                     int
	extraLabels                                                  []string
}

// ensureIssue devolve a issue com o marcador, criando-a se ainda não existe no Linear. Só
// reaproveita issue desta instalação, aberta (nem arquivada nem na lixeira), no time e no projeto
// que o roteamento dá agora (pickMarked); as descartadas viram aviso no log.
func (p *Publisher) ensureIssue(ctx context.Context, t target) (Issue, bool, error) {
	refs, err := p.teamRefs(ctx, t.linearTeam)
	if err != nil {
		return Issue{}, false, err
	}
	projectID := ""
	if t.project != "" {
		if projectID, err = p.projectID(ctx, refs.ID, t.project); err != nil {
			return Issue{}, false, err
		}
	}
	cands, err := p.Linear.FindIssuesByMarker(ctx, refs.ID, t.marker)
	if err != nil {
		return Issue{}, false, err
	}
	found, skipped := pickMarked(cands, MarkerQuery{Base: t.marker, TeamID: refs.ID, ProjectID: projectID}, *p.install)
	for _, s := range skipped {
		slog.Warn("publicação: issue com o marcador não reaproveitada", "marker", t.marker, "issue", s)
	}
	if found != nil {
		return *found, true, nil
	}
	in := IssueInput{TeamID: refs.ID, Title: t.title, Description: t.body, Priority: t.priority, ProjectID: projectID}
	if state := p.Cfg.TriageStateFor(t.team); state != "" {
		id, ok := refs.States[state]
		if !ok {
			return Issue{}, false, fmt.Errorf("%w: estado %q no time %s", ErrNotFound, state, refs.Key)
		}
		in.StateID = id
	}
	var labels []string
	for _, l := range append(p.Cfg.IssueLabels(t.team), t.extraLabels...) {
		if !config.ForeignLabel(l) { // defesa extra: config validate já rejeita claude-*
			labels = append(labels, l)
		}
	}
	if in.LabelIDs, err = p.Linear.LabelIDs(ctx, refs.ID, labels); err != nil {
		return Issue{}, false, err
	}
	if in.AssigneeID, err = p.assignee(ctx, t); err != nil {
		return Issue{}, false, err
	}
	issue, err := p.Linear.CreateIssue(ctx, in)
	return issue, false, err
}

// assignee aplica teams[].assignee: none, lead (e-mail do lead) ou round_robin (members
// com e-mail, em rodízio pelo número de issues já criadas para o time).
func (p *Publisher) assignee(ctx context.Context, t target) (string, error) {
	email := ""
	switch {
	case t.team == nil:
	case t.team.Assignee == config.AssigneeLead:
		email = t.team.Lead
	case t.team.Assignee == config.AssigneeRoundRobin:
		var members []string
		for _, m := range t.team.Members {
			if strings.Contains(m, "@") {
				members = append(members, m)
			}
		}
		if len(members) > 0 {
			n, err := p.Store.Q.CountTeamIssues(ctx, t.teamName)
			if err != nil {
				return "", err
			}
			email = members[int(n)%len(members)]
		}
	}
	if email == "" {
		return "", nil
	}
	if p.users == nil {
		p.users = map[string]string{}
	}
	if id, ok := p.users[email]; ok {
		return id, nil
	}
	id, err := p.Linear.UserIDByEmail(ctx, email)
	if err != nil {
		return "", err
	}
	p.users[email] = id
	return id, nil
}

func (p *Publisher) teamRefs(ctx context.Context, key string) (*TeamRefs, error) {
	if key == "" {
		key = p.Cfg.Defaults.Linear.Team
	}
	if p.teams == nil {
		p.teams = map[string]*TeamRefs{}
	}
	if r, ok := p.teams[key]; ok {
		return r, nil
	}
	r, err := p.Linear.TeamRefs(ctx, key)
	if err != nil {
		return nil, err
	}
	p.teams[key] = r
	return r, nil
}

func (p *Publisher) projectID(ctx context.Context, teamID, name string) (string, error) {
	if p.projects == nil {
		p.projects = map[string]string{}
	}
	k := teamID + "/" + name
	if id, ok := p.projects[k]; ok {
		return id, nil
	}
	id, err := p.Linear.ProjectID(ctx, teamID, name)
	if err != nil {
		return "", err
	}
	p.projects[k] = id
	return id, nil
}

func (p *Publisher) record(ctx context.Context, findingID, runID int64, role, team, module, linearTeam string, issue Issue) error {
	if linearTeam == "" {
		linearTeam = p.Cfg.Defaults.Linear.Team
	}
	_, err := p.Store.Q.InsertLinearIssue(ctx, db.InsertLinearIssueParams{
		FindingID: findingID, RunID: pgtype.Int8{Int64: runID, Valid: runID != 0}, Role: role,
		Team: teamKey(team), Module: module, LinearTeam: linearTeam, LinearID: issue.ID,
		Identifier: issue.Identifier, Url: issue.URL, StateName: issue.State.Name, StateType: issue.State.Type,
	})
	return err
}

// notifyTeams avisa a issue principal no canal do time dono e cada relacionada no canal do
// time dela. Issues reencontradas (publicação retomada) não são avisadas de novo.
func (p *Publisher) notifyTeams(ctx context.Context, f signals.Finding, pub PublishedIssue, res *PublishResult) {
	if p.Notify == nil || pub.Reused {
		return
	}
	text := fmt.Sprintf("Nova issue do Sentinela: %s %s (%s, %s) em `%s`", notify.Link(pub.URL, pub.Identifier),
		notify.Escape(pub.Title), pub.Category, pub.Severity, notify.Escape(f.Repo))
	if len(pub.AlsoAffects) > 0 {
		// Os times secundários são mencionados no aviso do dono, sem issue própria (ADR 0044).
		var who []string
		for _, s := range pub.AlsoAffects {
			name := notify.Escape(s.Team)
			if t := p.Cfg.Teams[s.Team]; t != nil && t.Notify.Mention != "" {
				name = notify.Mention(t.Notify.Mention) + " (" + name + ")"
			}
			who = append(who, name)
		}
		text += " · também afeta: " + strings.Join(who, ", ")
	}
	res.Warnings = append(res.Warnings, p.Notify.Team(ctx, f.Routing.Team, text)...)
	for _, r := range pub.Related {
		text := fmt.Sprintf("Issue relacionada do Sentinela: %s afeta `%s` (principal %s): %s", notify.Link(r.URL, r.Identifier),
			notify.Escape(r.Module), notify.Link(pub.URL, pub.Identifier), notify.Escape(pub.Title))
		res.Warnings = append(res.Warnings, p.Notify.Team(ctx, r.Team, text)...)
	}
}

func priority(severity string) int {
	switch severity {
	case "critical":
		return 1
	case "high":
		return 2
	case "medium":
		return 3
	default:
		return 4
	}
}

// findingFrom remonta o Finding (e a confiança do juiz) a partir da linha do banco.
func findingFrom(r db.PendingFindingsRow) (signals.Finding, error) {
	f := signals.Finding{
		Source: signals.SourceCodeAnalysis, Repo: r.Repo, Module: r.Module, Owners: r.Owners,
		Category: r.Category, Severity: r.Severity, Confidence: float64(r.Confidence), Title: r.Title,
		Context: r.Context, ProposedFix: r.ProposedFix, AcceptanceCriteria: r.AcceptanceCriteria,
		ScopePaths: r.ScopePaths, RelatedModules: r.RelatedModules, Fingerprint: r.Fingerprint,
	}
	for name, raw := range map[string][]byte{"routing": r.Routing, "evidence": r.Evidence, "signals": r.Signals} {
		var dst any
		switch name {
		case "routing":
			dst = &f.Routing
		case "evidence":
			dst = &f.Evidence
		default:
			dst = &f.Signals
		}
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, dst); err != nil {
				return f, fmt.Errorf("achado %d: %s inválido: %w", r.ID, name, err)
			}
		}
	}
	return f, nil
}

// findingFromDB remonta o Finding de uma linha de findings.
func findingFromDB(r db.Finding) (signals.Finding, error) {
	return findingFrom(db.PendingFindingsRow{
		ID: r.ID, RunID: r.RunID, Fingerprint: r.Fingerprint, Repo: r.Repo, Module: r.Module, Owners: r.Owners, Routing: r.Routing,
		Category: r.Category, Severity: r.Severity, Confidence: r.Confidence, Title: r.Title, Context: r.Context, Evidence: r.Evidence,
		Signals: r.Signals, ProposedFix: r.ProposedFix, AcceptanceCriteria: r.AcceptanceCriteria, ScopePaths: r.ScopePaths,
		RelatedModules: r.RelatedModules, JudgeConfidence: r.JudgeConfidence, JudgeReason: r.JudgeReason,
	})
}

// FindingFromDB remonta o Finding gravado (para o tracker local e a CLI).
func FindingFromDB(r db.Finding) (signals.Finding, error) { return findingFromDB(r) }

// alsoAffects são os times que as evidências do achado tocam além do dono (ADR 0044). Times que já
// ganham issue relacionada (módulos citados pelo Architect) ficam de fora: eles têm a issue deles.
func alsoAffects(f signals.Finding, related []relatedTarget, resolve Resolve) []repo.Secondary {
	if resolve == nil {
		return nil
	}
	var files, refs []string
	for _, e := range f.Evidence {
		files = append(files, e.File)
		ref := e.File
		if e.Line > 0 {
			ref = fmt.Sprintf("%s:%d", e.File, e.Line)
		}
		refs = append(refs, ref)
	}
	var out []repo.Secondary
	for _, s := range repo.SecondaryTeams(resolve, f.Routing.Team, files, refs) {
		skip := false
		for _, r := range related {
			if r.Team == s.Team {
				skip = true
			}
		}
		if !skip {
			out = append(out, s)
		}
	}
	return out
}
