package linear_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

func newMCP(fake *lineartest.FakeMCP, now *time.Time) *linear.MCPTracker {
	fake.Clock = func() time.Time { return *now }
	return &linear.MCPTracker{Run: fake, Server: "claude.ai Linear", PollInterval: 15 * time.Minute, Teams: []string{"EX", "PAY"},
		FindingLabel: "sentinela", Now: func() time.Time { return *now }}
}

// Pelo MCP, o mesmo fluxo do app: publica a issue (idempotente pelo marcador), comenta com o
// prefixo e lê de volta a decisão humana, que vira rejected com supressão e o motivo humano (o
// comentário do Sentinela, da mesma conta, é separado pelo prefixo).
func TestMCPTrackerPublishesAndReadsTheHumanDecisionBack(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now()
	fake := &lineartest.FakeMCP{Labels: []string{"sentinela", "sentinela-fix"}}
	tr := newMCP(fake, &now)
	e.pub.Linear = tr
	id := e.addFinding(t, "aaaaaaaaaaaa", "libs/http/server.go", "high")

	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil || len(res.Issues) != 1 {
		t.Fatalf("publicação: %+v, %v", res, err)
	}
	is := fake.Issues[0]
	if is.State != "Backlog" || is.Project != "plataforma" || !strings.Contains(is.Desc, "sentinela-fingerprint: aaaaaaaaaaaa") || strings.Join(is.Labels, ",") != "sentinela" {
		t.Errorf("issue criada = %+v", is)
	}
	// Publicação retomada: o marcador acha a issue, sem duplicar.
	if _, err := e.st.Pool.Exec(ctx, "DELETE FROM linear_issues"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pub.Publish(ctx, "acme/mono", 0, e.res); err != nil || len(fake.Issues) != 1 {
		t.Fatalf("republicação duplicou: %d issues, %v", len(fake.Issues), err)
	}
	if fake.Calls["list_issue_statuses"] != 1 {
		t.Errorf("TeamRefs foi ao MCP %d vezes (quer 1 por processo)", fake.Calls["list_issue_statuses"])
	}

	syncer := &linear.Syncer{Store: e.st, Linear: tr}
	if r, err := syncer.Poll(ctx); err != nil || len(r.Changes) != 0 {
		t.Fatalf("primeiro ciclo: %+v, %v", r, err)
	}
	// O Sentinela comenta como a mesma pessoa: o prefixo entra se faltar.
	if err := tr.Comment(ctx, is.UUID, "PR em draft aberto."); err != nil {
		t.Fatal(err)
	}
	// Uma pessoa cancela com o motivo; depois o Sentinela ainda comenta.
	is.Comments = append([]map[string]any{{"body": "Não é bug: o proxy já limita o tempo.", "createdAt": now.UTC().Format(time.RFC3339), "author": "Dona do MCP"}}, is.Comments...)
	is.State, is.StateType, is.Updated = "Canceled", "canceled", now.Add(time.Minute)

	if r, err := syncer.Poll(ctx); err != nil || len(r.Changes) != 0 {
		t.Fatalf("antes de vencer o intervalo, o sync usa o estado guardado (sem chamada): %+v, %v", r, err)
	}
	now = now.Add(16 * time.Minute)
	r, err := syncer.Poll(ctx)
	if err != nil || len(r.Changes) != 1 || r.Changes[0].Outcome != "rejected" {
		t.Fatalf("sync = %+v, %v", r, err)
	}
	if _, ok := fake.LastListArgs["updatedAt"]; !ok {
		t.Errorf("a segunda listagem deveria pedir só as atualizadas: %v", fake.LastListArgs)
	}
	if r.Changes[0].Reason != "Não é bug: o proxy já limita o tempo." {
		t.Errorf("motivo = %q (o comentário do Sentinela não pode ser o motivo)", r.Changes[0].Reason)
	}
	f, _ := e.st.Q.GetFinding(ctx, id)
	sups, _ := e.st.Q.ActiveSuppressions(ctx, "acme/mono")
	if f.State != "rejected" || len(sups) != 1 {
		t.Errorf("achado %s, supressões %d", f.State, len(sups))
	}
	det, err := tr.Detail(ctx, is.UUID)
	if err != nil {
		t.Fatal(err)
	}
	var own, human int
	for _, cm := range det.Comments.Nodes {
		if cm.BySentinela && strings.HasPrefix(cm.Body, "**Sentinela:** PR em draft") {
			own++
		}
		if cm.Human() {
			human++
		}
	}
	if own != 1 || human != 1 {
		t.Errorf("comentários: %d do Sentinela, %d humanos: %+v", own, human, det.Comments.Nodes)
	}
}

// Um ciclo é uma chamada (listagem por time), com 3 ou 300 issues abertas; issues fora da
// listagem saem numa leitura só; IssuesFresh lê na hora.
func TestMCPTrackerOneCallPerCycleWhateverTheNumberOfIssues(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fake := &lineartest.FakeMCP{Labels: []string{"sentinela"}}
	var ids []string
	for i := range 300 {
		id := fmt.Sprintf("u%d", i)
		ids = append(ids, id)
		fake.Issues = append(fake.Issues, &lineartest.FakeMCPIssue{UUID: id, Identifier: fmt.Sprintf("EX-%d", i), State: "Todo", StateType: "unstarted", Labels: []string{"sentinela"}, Updated: now.Add(-time.Hour)})
	}
	fake.Issues = append(fake.Issues, &lineartest.FakeMCPIssue{UUID: "sem-label", Identifier: "EX-999", State: "Todo", StateType: "unstarted", Updated: now})
	tr := newMCP(fake, &now)
	if _, err := tr.LabelIDs(ctx, "EX", []string{"sentinela", "nao-existe"}); err == nil || !strings.Contains(err.Error(), "nao-existe") {
		t.Errorf("label inexistente: %v", err)
	}
	for range 3 {
		got, err := tr.Issues(ctx, ids)
		if err != nil || len(got) != 300 {
			t.Fatalf("issues = %d, %v", len(got), err)
		}
	}
	if fake.Calls["op:poll"] != 1 || fake.Calls["get_issue"] != 0 {
		t.Errorf("chamadas no intervalo: %v", fake.Calls)
	}
	if _, ok := fake.LastListArgs["updatedAt"]; ok {
		t.Error("a primeira listagem traz todas, sem updatedAt")
	}
	// Uma issue fora da listagem (sem a label): uma leitura com get_issue.
	if got, _ := tr.Issues(ctx, []string{"u1", "sem-label"}); len(got) != 2 || fake.Calls["op:get"] != 1 {
		t.Errorf("fora da listagem: %d issues, %v", len(got), fake.Calls)
	}
	// Vencido o intervalo, outra listagem, só das atualizadas.
	now = now.Add(15 * time.Minute)
	fake.Issues[7].State, fake.Issues[7].StateType, fake.Issues[7].Updated = "Canceled", "canceled", now
	got, _ := tr.Issues(ctx, []string{"u7"})
	if fake.Calls["op:poll"] != 2 || got[0].State.Type != "canceled" {
		t.Errorf("segunda listagem: %v, %+v", fake.Calls, got)
	}
	since, _ := time.Parse(time.RFC3339, fake.LastListArgs["updatedAt"].(string))
	if d := now.Add(-15 * time.Minute).Sub(since); d < time.Minute || d > 3*time.Minute {
		t.Errorf("updatedAt = %v (quer a listagem anterior menos a margem)", since)
	}
	if _, err := tr.IssuesFresh(ctx, []string{"u7"}); err != nil || fake.Calls["op:get_fresh"] != 1 {
		t.Errorf("leitura na hora: %v, %v", err, fake.Calls)
	}
}

// As mudanças de estado do próprio Sentinela, que no MCP saem com a conta da pessoa, não viram
// decisão: In Review é estado started, e Done só vem depois do desfecho do merge gravado (o achado
// já está fixed). Done ou Canceled feitos pela pessoa num achado pendente viram decisão.
func TestMCPSentinelaStateChangesAreNotHumanDecisions(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	now := time.Now()
	fake := &lineartest.FakeMCP{Labels: []string{"sentinela"}}
	tr := newMCP(fake, &now)
	e.pub.Linear = tr
	merged := e.addFinding(t, "bbbbbbbbbbbb", "libs/http/a.go", "high")
	doneByMe := e.addFinding(t, "cccccccccccc", "libs/http/b.go", "high")
	canceledByMe := e.addFinding(t, "dddddddddddd", "libs/http/c.go", "high")
	if res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res); err != nil || len(res.Issues) != 3 {
		t.Fatalf("publicação: %+v, %v", res, err)
	}
	issueOf := map[int64]*lineartest.FakeMCPIssue{}
	for _, is := range fake.Issues {
		for fid, fp := range map[int64]string{merged: "bbbbbbbbbbbb", doneByMe: "cccccccccccc", canceledByMe: "dddddddddddd"} {
			if strings.Contains(is.Desc, fp) {
				issueOf[fid] = is
			}
		}
	}
	syncer := &linear.Syncer{Store: e.st, Linear: tr}
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	// O worker abre o PR (In Review, pelo Sentinela) e depois vê o merge: grava o desfecho e fecha
	// o achado (closePR), e só então move para Done (moveToDone).
	if _, err := e.st.Pool.Exec(ctx, "UPDATE findings SET state = 'executing' WHERE id = $1", merged); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := tr.SetState(ctx, issueOf[merged].UUID, "In Review"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(16 * time.Minute)
	if r, err := syncer.Poll(ctx); err != nil || hasOutcome(r) {
		t.Fatalf("In Review do Sentinela virou decisão: %+v, %v", r, err)
	}
	if _, err := e.st.Pool.Exec(ctx, "UPDATE findings SET state = 'fixed' WHERE id = $1", merged); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(ctx, "INSERT INTO outcomes (finding_id, kind, reason, actor) VALUES ($1, 'merged', 'PR mergeado', 'github')", merged); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := tr.SetState(ctx, issueOf[merged].UUID, "Done"); err != nil {
		t.Fatal(err)
	}
	// A pessoa conclui uma e cancela outra, as duas ainda pendentes.
	for fid, st := range map[int64]string{doneByMe: "Done", canceledByMe: "Canceled"} {
		is := issueOf[fid]
		is.State, is.StateType, is.Updated = st, lineartest.StateTypes[st], now
	}
	now = now.Add(16 * time.Minute)
	r, err := syncer.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]string{}
	for _, ch := range r.Changes {
		got[ch.FindingID] = ch.Outcome
	}
	if got[merged] != "" || got[doneByMe] != "accepted" || got[canceledByMe] != "rejected" {
		t.Errorf("desfechos do sync = %v (quer: Done do Sentinela nenhum, Done da pessoa accepted, Canceled rejected)", got)
	}
	var n int
	if err := e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE finding_id = $1", merged).Scan(&n); err != nil || n != 1 {
		t.Errorf("o achado mergeado tem %d desfechos (quer só o merged): %v", n, err)
	}
}

func hasOutcome(r linear.SyncResult) bool {
	for _, ch := range r.Changes {
		if ch.Outcome != "" {
			return true
		}
	}
	return false
}

// O modelo das chamadas é o mais barato de pricing.models quando adapters.mcp.model fica vazio.
func TestMCPModelDefaultsToTheCheapest(t *testing.T) {
	y := strings.Replace(cfgYAML, "pricing: { models: { m: { input: 1, output: 1 } } }",
		"pricing: { models: { m: { input: 1, output: 1 }, barato: { input: 0.1, output: 0.5 } } }", 1) + "\nadapters: { tracker: linear_mcp }\n"
	cfg, err := config.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	if m := cfg.Adapters.MCP; m.Model != "barato" || m.PollInterval != 15*time.Minute || m.LinearServer != "claude.ai Linear" {
		t.Errorf("mcp = %+v", m)
	}
	if _, err := config.Parse([]byte(y + "trigger: { mode: webhook }\n")); err == nil || !strings.Contains(err.Error(), "polling") {
		t.Errorf("linear_mcp com webhook: %v", err)
	}
}

// Achado gravado sem projeto: a publicação usa defaults.linear.project como último recurso.
func TestPublishUsesTheDefaultsProjectForFindingsStoredWithoutOne(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id := e.addFinding(t, "eeeeeeeeeeee", "libs/http/server.go", "high")
	if _, err := e.st.Pool.Exec(ctx, `UPDATE findings SET routing = jsonb_set(routing, '{linear_project}', '""') WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	e.cfg.Defaults.Linear.Project = "core"
	if res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res); err != nil || len(res.Issues) != 1 {
		t.Fatalf("publicação: %+v, %v", res, err)
	}
	if got := e.fake.Snapshot()[0].ProjectID; got != "proj-core" {
		t.Errorf("projeto = %q, quer o de defaults (proj-core)", got)
	}
}

// Membros do time pelo MCP: uma chamada por intervalo; quem aplicou a label não é exposto pelo MCP.
func TestMCPTeamMembersOnceAndTriggerActorUnsupported(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	fake := &lineartest.FakeMCP{Members: map[string][]map[string]any{"PAG": {{"id": "u1", "name": "Ana", "email": "ana@acme.com"}}}}
	tr := newMCP(fake, &now)
	for range 3 {
		m, err := tr.TeamMembers(ctx, "PAG")
		if err != nil || len(m) != 1 || m[0].Email != "ana@acme.com" {
			t.Fatalf("membros = %+v, %v", m, err)
		}
	}
	if fake.Calls["op:members"] != 1 {
		t.Errorf("membros lidos %d vezes no intervalo (quer 1)", fake.Calls["op:members"])
	}
	if _, _, err := tr.TriggerActor(ctx, "u", "sentinela-fix"); !errors.Is(err, linear.ErrUnsupported) {
		t.Errorf("TriggerActor pelo MCP: %v", err)
	}
}

// Achado entre módulos de times diferentes, no cenário acme: o dono é pagamentos (a evidência
// principal), a issue ganha "Também afeta" com identidade e o aviso menciona o grupo dela, sem
// issue duplicada (ADR 0044).
func TestCrossTeamFindingAlsoAffectsWithoutDuplicateIssue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	acme := filepath.Join("..", "..", "examples", "monorepo-acme")
	cfg, err := config.Load(filepath.Join(acme, "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(filepath.Join(acme, "repos", "plataforma", ".github", "CODEOWNERS"))
	if err != nil {
		t.Fatal(err)
	}
	co, err := repo.ParseCodeowners(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	resolver := repo.NewResolver(cfg, repo.StaticCodeowners{"acme/plataforma": co})
	resolve := func(p string) (repo.Resolution, error) { return resolver.Resolve("acme/plataforma", p) }
	owner, _ := resolve("modules/cobranca/fatura.go")
	routing, _ := json.Marshal(owner.Routing)
	ev, _ := json.Marshal([]map[string]any{
		{"file": "modules/cobranca/fatura.go", "line": 10, "snippet": "x"},
		{"file": "modules/contas/login.go", "line": 12, "snippet": "y"},
		{"file": "modules/cobranca/juros.go", "line": 3, "snippet": "z"},
	})
	if _, err := e.st.Q.InsertFinding(ctx, db.InsertFindingParams{
		Fingerprint: "acme00000001", Source: "code_analysis", Repo: "acme/plataforma", Module: "modules/cobranca",
		Owners: []string{"pagamentos"}, Routing: routing, Category: "bug", Severity: "high", Confidence: 0.8,
		Title: "Valida a sessão antes de cobrar", Context: "c", Evidence: ev, Signals: []byte("[]"), ProposedFix: "f",
		AcceptanceCriteria: []string{"ok"}, ScopePaths: []string{"modules/cobranca"}, RelatedModules: []string{},
		Embedding: pgvector.NewVector(make([]float32, 1024)),
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	fake := &lineartest.FakeMCP{Labels: []string{"sentinela"}}
	slack := &posts{}
	pub := &linear.Publisher{Cfg: cfg, Store: e.st, Linear: newMCP(fake, &now), Notify: &notify.Router{Notifier: slack, Cfg: cfg}}
	res, err := pub.Publish(ctx, "acme/plataforma", 0, resolve)
	if err != nil || len(res.Issues) != 1 {
		t.Fatalf("publicação: %+v, %v", res, err)
	}
	if len(fake.Issues) != 1 {
		t.Fatalf("issues criadas = %d (quer 1: sem issue duplicada para o time secundário)", len(fake.Issues))
	}
	desc := fake.Issues[0].Desc
	if !strings.Contains(desc, "## Também afeta") || !strings.Contains(desc, "- **identidade**, módulo `/modules/contas/`: `modules/contas/login.go:12`") || strings.Contains(desc, "**pagamentos**, módulo") {
		t.Errorf("seção Também afeta:\n%s", desc)
	}
	msgs := strings.Join(slack.by["C0ACMEPAG01"], "\n")
	if !strings.Contains(msgs, "também afeta: <!subteam^S0ACMEIDN01> (identidade)") || len(slack.by["C0ACMEIDN01"]) != 0 {
		t.Errorf("avisos: pagamentos %q, identidade %v", msgs, slack.by["C0ACMEIDN01"])
	}
	if p := res.Issues[0]; len(p.AlsoAffects) != 1 || p.AlsoAffects[0].Team != "identidade" || p.Team != "pagamentos" {
		t.Errorf("publicada = %+v", p)
	}
}

// Modo MCP: sentinela-nao-intencional numa pergunta grava o mesmo outcome dos outros modos
// (accepted, decided_by human); sem o histórico de labels no MCP, sem o autor.
func TestMCPNotIntentionalAnswerRecordsAcceptedOutcome(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.cfg.Labels.Question, e.cfg.Labels.Intentional, e.cfg.Labels.NotIntentional = "sentinela-pergunta", "sentinela-intencional", "sentinela-nao-intencional"
	now := time.Now()
	fake := &lineartest.FakeMCP{Labels: []string{"sentinela", "sentinela-pergunta", "sentinela-intencional", "sentinela-nao-intencional"}}
	tr := newMCP(fake, &now)
	e.pub.Linear = tr
	id := e.addFinding(t, "premissamcp1", "libs/api/analytics.go", "medium")
	if _, err := e.st.Pool.Exec(ctx, "UPDATE findings SET premise = 'O endpoint deve ser público?' WHERE id = $1", id); err != nil {
		t.Fatal(err)
	}
	if res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res); err != nil || len(res.Issues) != 1 || !res.Issues[0].Question {
		t.Fatalf("publicação: %+v, %v", res, err)
	}
	syncer := &linear.Syncer{Store: e.st, Linear: tr, Cfg: e.cfg}
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	is := fake.Issues[0]
	now = now.Add(time.Minute)
	is.Labels, is.Updated = append(is.Labels, "sentinela-nao-intencional"), now
	now = now.Add(16 * time.Minute)
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	var kind, decided, actor, ans string
	_ = e.st.Pool.QueryRow(ctx, "SELECT kind, decided_by, COALESCE(actor, '') FROM outcomes WHERE finding_id = $1", id).Scan(&kind, &decided, &actor)
	_ = e.st.Pool.QueryRow(ctx, "SELECT COALESCE(premise_answer, '') FROM findings WHERE id = $1", id).Scan(&ans)
	if kind != "accepted" || decided != "human" || actor != "" || ans != "not_intentional" {
		t.Errorf("outcome %s/%s por %q, resposta %q", kind, decided, actor, ans)
	}
}
