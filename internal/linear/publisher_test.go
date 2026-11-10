package linear_test

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
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/pgvector/pgvector-go"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
	"github.com/dev-danilocordeiro/sentinela/internal/notify"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

const cfgYAML = `
defaults:
  linear: { team: EX, triage_state: Backlog, labels: [sentinela] }
  notify: { slack: C0DEFAULT01 }
budget: { per_run_usd: 1, per_pr_usd: 1 }
models:
  triage: m
  analysis: m
  architect: m
  judge: m
  curator: m
  spec_writer: m
  executor: m
  reviewer_security: m
  reviewer_code: m
  reviewer_reliability: m
  reviewer_domain: m
  approver: m
pricing: { models: { m: { input: 1, output: 1 } } }
publish: { enabled: true, max_issues_per_team_per_run: 3 }
teams:
  platform:
    github: ["@acme/platform"]
    linear: { team: EX, project: plataforma }
    assignee: round_robin
    members: [ana@acme.com, joao@acme.com]
  payments:
    github: ["@acme/payments"]
    linear: { team: PAY, project: payments, triage_state: Backlog }
    assignee: lead
    lead: lead@acme.com
    notify: { slack: C0PAYMENTS1 }
repos:
  - name: acme/mono
    modules:
      - { path: "services/billing/**", team: payments }
      - { path: "libs/**", team: platform }
    fallback_team: platform
  - name: acme/svc
    team: platform
`

type env struct {
	cfg   *config.Config
	st    *store.Store
	fake  *lineartest.Server
	pub   *linear.Publisher
	slack *posts
	res   linear.Resolve
	cl    *linear.Client
}

type posts struct {
	mu sync.Mutex
	by map[string][]string
}

func (p *posts) Post(_ context.Context, channel, text string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.by == nil {
		p.by = map[string][]string{}
	}
	p.by[channel] = append(p.by[channel], text)
	return nil
}

func setup(t *testing.T) *env {
	t.Helper()
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := store.NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DownTo(context.Background(), 0)
		_ = m.Close()
	})
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
	cfg, err := config.Parse([]byte(cfgYAML))
	if err != nil {
		t.Fatal(err)
	}
	fake := lineartest.New()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	cl := linear.NewClient(srv.URL, "k")
	sl := &posts{}
	resolver := repo.NewResolver(cfg, repo.StaticCodeowners{})
	return &env{
		cfg: cfg, st: st, fake: fake, slack: sl, cl: cl,
		pub: &linear.Publisher{Cfg: cfg, Store: st, Linear: cl, Notify: &notify.Router{Notifier: sl, Cfg: cfg}},
		res: func(p string) (repo.Resolution, error) { return resolver.Resolve("acme/mono", p) },
	}
}

// addFinding grava um achado aprovado como o Judge gravaria.
func (e *env) addFinding(t *testing.T, fp, file, severity string, related ...string) int64 {
	t.Helper()
	return e.addFindingIn(t, "acme/mono", fp, file, severity, related...)
}

func (e *env) addFindingIn(t *testing.T, repoName, fp, file, severity string, related ...string) int64 {
	t.Helper()
	resolver := repo.NewResolver(e.cfg, repo.StaticCodeowners{})
	r, err := resolver.Resolve(repoName, file)
	if err != nil {
		t.Fatal(err)
	}
	routing, _ := json.Marshal(r.Routing)
	ev, _ := json.Marshal([]map[string]any{{"file": file, "line": 3, "permalink": "https://github.com/acme/mono/blob/abc/" + file + "#L3", "snippet": "x := y"}})
	id, err := e.st.Q.InsertFinding(context.Background(), db.InsertFindingParams{
		Fingerprint: fp, Source: "code_analysis", Repo: repoName, Module: file[:strings.LastIndex(file, "/")],
		Owners: []string{r.Team}, Routing: routing, Category: "bug", Severity: severity, Confidence: 0.8,
		Title: "Achado " + fp, Context: "contexto", Evidence: ev, Signals: []byte("[]"), ProposedFix: "corrigir",
		AcceptanceCriteria: []string{"teste cobre"}, ScopePaths: []string{"libs"}, RelatedModules: append([]string{}, related...),
		Embedding:       pgvector.NewVector(make([]float32, 1024)),
		JudgeConfidence: pgtype.Float4{Float32: 0.75, Valid: true}, JudgeReason: pgtype.Text{String: "evidência clara", Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestPublishRespectsPerTeamLimitAndResumesInTheNextRun(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for i, sev := range []string{"low", "high", "medium", "critical"} {
		e.addFinding(t, fmt.Sprintf("fp%010d", i), fmt.Sprintf("libs/p%d/a.go", i), sev)
	}
	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 3 || res.Deferred["platform"] != 1 {
		t.Fatalf("publicadas %d, adiadas %v", len(res.Issues), res.Deferred)
	}
	if res.Issues[0].Severity != "critical" || res.Issues[2].Severity != "medium" {
		t.Errorf("ordem por severidade: %+v", res.Issues)
	}
	issues := e.fake.Snapshot()
	// round_robin: ana, joao, ana.
	if issues[0].AssigneeID != "user-ana" || issues[1].AssigneeID != "user-joao" || issues[2].AssigneeID != "user-ana" {
		t.Errorf("rodízio: %s %s %s", issues[0].AssigneeID, issues[1].AssigneeID, issues[2].AssigneeID)
	}
	if issues[0].ProjectID != "proj-plat" || issues[0].Priority != 1 || issues[0].LabelIDs[0] != "lbl-sentinela" {
		t.Errorf("issue = %+v", issues[0])
	}
	// platform não tem canal: cada issue vai para o canal padrão.
	if len(e.slack.by["C0DEFAULT01"]) != 3 {
		t.Errorf("avisos = %v", e.slack.by)
	}
	next, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil || len(next.Issues) != 1 || next.Issues[0].Severity != "low" || next.Deferred != nil {
		t.Errorf("rodada seguinte = %+v (%v)", next, err)
	}
}

// M8 (revisão 2026-10-11): no scan --all, o limite por time vale para a rodada inteira, somando
// os repos, e a ordem é por severidade entre eles. Antes, cada repo tinha o seu limite: o time
// recebia até 3 issues por repo, e os achados low do primeiro repo saíam antes do critical do
// segundo.
func TestRoundSharesThePerTeamLimitAcrossReposBySeverity(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.addFindingIn(t, "acme/mono", "fpmono00001", "libs/a/a.go", "low")
	e.addFindingIn(t, "acme/mono", "fpmono00002", "libs/b/a.go", "low")
	e.addFindingIn(t, "acme/mono", "fpmono00003", "libs/c/a.go", "medium")
	e.addFindingIn(t, "acme/svc", "fpsvc000001", "cmd/a.go", "critical")
	e.addFindingIn(t, "acme/svc", "fpsvc000002", "cmd/b.go", "high")
	e.addFindingIn(t, "acme/svc", "fpsvc000003", "cmd/c.go", "low")
	resolver := repo.NewResolver(e.cfg, repo.StaticCodeowners{})
	resolveIn := func(name string) linear.Resolve {
		return func(p string) (repo.Resolution, error) { return resolver.Resolve(name, p) }
	}
	round := &linear.Round{}
	for _, name := range []string{"acme/mono", "acme/svc"} {
		got, err := round.Publish(ctx, name, 0, resolveIn(name))
		if err != nil || !got.Postponed || len(got.Issues) != 0 {
			t.Fatalf("registro de %s = %+v (%v)", name, got, err)
		}
	}
	if len(e.fake.Snapshot()) != 0 {
		t.Fatal("o registro do repo publicou antes do fim da rodada")
	}
	res, err := round.Flush(ctx, e.pub)
	if err != nil {
		t.Fatal(err)
	}
	var sevs []string
	for _, p := range res.Issues {
		sevs = append(sevs, p.Severity)
	}
	if strings.Join(sevs, ",") != "critical,high,medium" {
		t.Errorf("publicadas = %v, quer critical,high,medium (3 do time na rodada inteira)", sevs)
	}
	if res.Deferred["platform"] != 3 || res.DeferredSeverity["platform"]["low"] != 3 {
		t.Errorf("adiadas = %v %v, quer platform 3 (low 3)", res.Deferred, res.DeferredSeverity)
	}
	next, err := (&linear.Round{}).Flush(ctx, e.pub)
	if err != nil || len(next.Issues) != 0 {
		t.Errorf("rodada sem repos = %+v (%v)", next, err)
	}
	round2 := &linear.Round{}
	_, _ = round2.Publish(ctx, "acme/mono", 0, resolveIn("acme/mono"))
	_, _ = round2.Publish(ctx, "acme/svc", 0, resolveIn("acme/svc"))
	res2, err := round2.Flush(ctx, e.pub)
	if err != nil || len(res2.Issues) != 3 || res2.Deferred != nil {
		t.Errorf("rodada seguinte = %+v (%v), quer as 3 low", res2, err)
	}
}

func TestCrossModuleFindingOpensLinkedIssueInTheOtherTeamsBoard(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	id := e.addFinding(t, "crossmodule1", "libs/money/round.go", "high", "services/billing", "libs/other")
	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || len(res.Issues[0].Related) != 1 || res.Issues[0].Related[0].Team != "payments" {
		t.Fatalf("publicação = %+v", res)
	}
	issues := e.fake.Snapshot()
	primary, rel := issues[0], issues[1]
	if primary.TeamID != "team-ex" || rel.TeamID != "team-pay" || rel.ProjectID != "proj-pay" || rel.AssigneeID != "user-lead" {
		t.Errorf("principal %+v\nrelacionada %+v", primary, rel)
	}
	if len(primary.Related) != 1 || primary.Related[0] != rel.ID || !strings.Contains(rel.Description, primary.Identifier) {
		t.Errorf("relação não criada: %+v", primary.Related)
	}
	rows, err := e.st.Q.ListLinearIssuesByFindings(ctx, []int64{id})
	if err != nil || len(rows) != 2 || rows[0].Role != "primary" || rows[1].Role != "related" || rows[1].Team != "payments" {
		t.Errorf("linear_issues = %+v (%v)", rows, err)
	}
	if len(e.slack.by["C0PAYMENTS1"]) != 1 || len(e.slack.by["C0DEFAULT01"]) != 1 {
		t.Errorf("avisos por time = %v", e.slack.by)
	}
}

// Se o processo cair depois de criar a issue e antes de gravar no Postgres, a próxima
// publicação reencontra a issue pelo fingerprint no corpo em vez de duplicar.
func TestPublishIsIdempotentWhenTheIssueAlreadyExistsInLinear(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.addFinding(t, "idempotent01", "libs/x/a.go", "medium")
	if _, err := e.pub.Publish(ctx, "acme/mono", 0, e.res); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.Pool.Exec(ctx, "DELETE FROM linear_issues"); err != nil {
		t.Fatal(err)
	}
	res, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Issues) != 1 || !res.Issues[0].Reused || e.fake.Count("SentinelaCreateIssue") != 1 || len(e.slack.by["C0DEFAULT01"]) != 1 {
		t.Errorf("republicação = %+v, criações = %d, avisos = %v", res, e.fake.Count("SentinelaCreateIssue"), e.slack.by)
	}
}

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestWebhookValidatesSignatureDeduplicatesAndRejectsCanceledIssue(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.addFinding(t, "webhook00001", "libs/w/a.go", "high")
	pub, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil {
		t.Fatal(err)
	}
	ident := pub.Issues[0].Identifier
	e.fake.Cancel(ident, "Ana", "Não se aplica: o módulo é só de exemplo.")
	issueID := e.fake.Snapshot()[0].ID

	now := time.Now()
	h := &linear.WebhookHandler{Secret: "s3cr3t", Syncer: &linear.Syncer{Store: e.st, Linear: e.cl}, Now: func() time.Time { return now }}
	body := []byte(fmt.Sprintf(`{"action":"update","type":"Issue","data":{"id":%q},"webhookTimestamp":%d}`, issueID, now.UnixMilli()))
	send := func(sig, delivery string, b []byte) int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/linear", bytes.NewReader(b))
		req.Header.Set("Linear-Signature", sig)
		req.Header.Set("Linear-Delivery", delivery)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	if code := send(sign("outro", body), "d1", body); code != http.StatusUnauthorized {
		t.Errorf("assinatura errada = %d", code)
	}
	old := []byte(fmt.Sprintf(`{"action":"update","type":"Issue","data":{"id":%q},"webhookTimestamp":%d}`, issueID, now.Add(-2*time.Minute).UnixMilli()))
	if code := send(sign("s3cr3t", old), "d0", old); code != http.StatusUnauthorized {
		t.Errorf("timestamp antigo = %d", code)
	}
	if code := send(sign("s3cr3t", body), "d1", body); code != http.StatusOK {
		t.Fatalf("evento válido = %d", code)
	}
	if code := send(sign("s3cr3t", body), "d1", body); code != http.StatusOK {
		t.Errorf("reentrega = %d", code)
	}
	var state string
	var outcomes, sups, events int
	_ = e.st.Pool.QueryRow(ctx, "SELECT state FROM findings").Scan(&state)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes").Scan(&outcomes)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM suppressions WHERE reason LIKE 'Não se aplica%'").Scan(&sups)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM webhook_events WHERE event_id = 'd1' AND processed_at IS NOT NULL").Scan(&events)
	if state != "rejected" || outcomes != 1 || sups != 1 || events != 1 {
		t.Errorf("estado %s, desfechos %d, supressões %d, eventos %d", state, outcomes, sups, events)
	}
}

// Duplicate no Linear é merge humano para a issue original, não recusa: sem supressão, outcome
// duplicate_of (decidido e executado por humano) e o achado acompanha o estado do original, inclusive depois.
func TestDuplicateIsAHumanMergeIntoTheOriginalNotARejection(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	orig := e.addFinding(t, "orig00000001", "libs/a/a.go", "high")
	dup := e.addFinding(t, "dupl00000001", "libs/b/b.go", "medium")
	pub, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
	if err != nil || len(pub.Issues) != 2 {
		t.Fatalf("publicação = %+v (%v)", pub, err)
	}
	ident := map[string]string{}
	for _, is := range pub.Issues {
		ident[is.Fingerprint] = is.Identifier
	}
	e.fake.MarkDuplicate(ident["dupl00000001"], ident["orig00000001"], "Ana")
	syncer := &linear.Syncer{Store: e.st, Linear: e.cl}
	res, err := syncer.Poll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Changes) != 1 || res.Changes[0].Outcome != "duplicate_of" || res.Changes[0].DuplicateOf != ident["orig00000001"] || res.Changes[0].SuppressionID != 0 {
		t.Fatalf("sync = %+v", res.Changes)
	}
	var state string
	var mergedInto int64
	var sups, kinds int
	var decidedBy, executedBy, actor string
	_ = e.st.Pool.QueryRow(ctx, "SELECT state, merged_into FROM findings WHERE id = $1", dup).Scan(&state, &mergedInto)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM suppressions").Scan(&sups)
	_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM outcomes WHERE kind = 'rejected'").Scan(&kinds)
	_ = e.st.Pool.QueryRow(ctx, "SELECT decided_by, executed_by, actor FROM outcomes WHERE kind = 'duplicate_of' AND finding_id = $1", dup).Scan(&decidedBy, &executedBy, &actor)
	if state != "open" || mergedInto != orig || sups != 0 || kinds != 0 || decidedBy != "human" || executedBy != "human" || actor != "Ana" {
		t.Errorf("duplicata: estado %s, merged_into %d (quer %d), supressões %d, recusas %d, %s/%s/%s", state, mergedInto, orig, sups, kinds, decidedBy, executedBy, actor)
	}
	// A duplicata não volta a ser acompanhada nem exemplo próprio; o original segue o fluxo.
	e.fake.Move(ident["orig00000001"], "Done")
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	_ = e.st.Pool.QueryRow(ctx, "SELECT state FROM findings WHERE id = $1", dup).Scan(&state)
	if state != "fixed" {
		t.Errorf("a duplicata deveria acompanhar o original (fixed), está %s", state)
	}
	ex, err := e.st.Q.JudgeExamples(ctx, db.JudgeExamplesParams{Repo: "acme/mono", Limit: 10})
	if err != nil || len(ex) != 1 {
		t.Errorf("exemplos = %+v (só o original conta) %v", ex, err)
	}
}

// Achado com premissa de produto sai como pergunta ao time (label sentinela-pergunta, título em
// forma de pergunta, evidência no corpo). "Não é intencional" vira o achado normal na mesma
// issue; "é intencional" vira supressão com o motivo, sem o Sentinela fechar a issue.
func TestPremiseFindingIsPublishedAsAQuestionAndTheAnswerDecides(t *testing.T) {
	for _, answer := range []string{"sentinela-nao-intencional", "sentinela-intencional"} {
		t.Run(answer, func(t *testing.T) {
			e := setup(t)
			ctx := context.Background()
			e.cfg.Labels.Question, e.cfg.Labels.Intentional, e.cfg.Labels.NotIntentional = "sentinela-pergunta", "sentinela-intencional", "sentinela-nao-intencional"
			id := e.addFinding(t, "premissa0001", "libs/api/analytics.go", "medium")
			if _, err := e.st.Pool.Exec(ctx, "UPDATE findings SET premise = $2 WHERE id = $1", id,
				"O endpoint de analytics deve ser público? Mesmo sendo, IP e user-agent devem aparecer?"); err != nil {
				t.Fatal(err)
			}
			pub, err := e.pub.Publish(ctx, "acme/mono", 0, e.res)
			if err != nil || len(pub.Issues) != 1 || !pub.Issues[0].Question {
				t.Fatalf("publicação = %+v (%v)", pub, err)
			}
			is := e.fake.Snapshot()[0]
			if is.Title != "O endpoint de analytics deve ser público?" || !strings.Contains(is.Description, "## Pergunta ao time") ||
				!strings.Contains(is.Description, "## Evidência") || !slices.Contains(is.LabelIDs, "lbl-pergunta") || !strings.Contains(is.Description, "sentinela-fingerprint: premissa0001") {
				t.Fatalf("issue de pergunta = %q\n%s\nlabels %v", is.Title, is.Description, is.LabelIDs)
			}

			e.fake.AddLabel(is.Identifier, answer) // resposta humana
			if answer == "sentinela-intencional" {
				e.fake.Comment(is.Identifier, "Ana", "É público de propósito: o link é anônimo.")
			}
			syncer := &linear.Syncer{Store: e.st, Linear: e.cl, Cfg: e.cfg}
			res, err := syncer.Poll(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var state, ans string
			var sups int
			_ = e.st.Pool.QueryRow(ctx, "SELECT state, premise_answer FROM findings WHERE id = $1", id).Scan(&state, &ans)
			_ = e.st.Pool.QueryRow(ctx, "SELECT count(*) FROM suppressions WHERE reason LIKE 'É intencional%'").Scan(&sups)
			after := e.fake.Snapshot()[0]
			switch answer {
			case "sentinela-nao-intencional":
				if ans != "not_intentional" || state != "open" || sups != 0 || after.Title == is.Title || slices.Contains(after.LabelIDs, "lbl-pergunta") ||
					!strings.Contains(after.Description, "## Critérios de aceite") {
					t.Errorf("não intencional: %s/%s supressões %d, título %q, labels %v", ans, state, sups, after.Title, after.LabelIDs)
				}
				// Mesmo registro dos três modos: outcome accepted, decided_by human, com quem aplicou a label.
				var kind, decided, actor string
				_ = e.st.Pool.QueryRow(ctx, "SELECT kind, decided_by, COALESCE(actor, '') FROM outcomes WHERE finding_id = $1", id).Scan(&kind, &decided, &actor)
				if kind != "accepted" || decided != "human" || actor != "Lead" {
					t.Errorf("outcome da resposta = %s/%s por %q (quer accepted/human por Lead)", kind, decided, actor)
				}
			default:
				var reason string
				_ = e.st.Pool.QueryRow(ctx, "SELECT reason FROM suppressions").Scan(&reason)
				if ans != "intentional" || state != "rejected" || sups != 1 || reason != "É intencional: É público de propósito: o link é anônimo." || after.StateID == "st-canceled" {
					t.Errorf("intencional: %s/%s supressões %d motivo %q estado %s (%+v)", ans, state, sups, reason, after.StateID, res.Changes)
				}
			}
			// Os comentários do próprio Sentinela não viram motivo de decisão humana.
			for _, c := range e.fake.CommentsOf(is.Identifier) {
				if strings.HasPrefix(c.Body, "**Sentinela") && !linear.IsSentinelaComment(c.Body) {
					t.Error("comentário do Sentinela sem o prefixo")
				}
			}
		})
	}
}
