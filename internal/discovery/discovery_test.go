package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jackc/pgx/v5/pgtype"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

const testConfig = `
defaults:
  linear: { team: EX, triage_state: Backlog, labels: [sentinela] }
  notify: { slack: C0DEFAULT01 }
budget: { per_run_usd: 2, per_pr_usd: 1 }
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
  refusal: { action: skip }
  use_batch: false
pricing:
  models:
    claude-haiku-5-5: { input: 0.10, output: 0.50, cache_read: 0.01 }
    claude-sonnet-5-5: { input: 2.00, output: 10.00, cache_read: 0.20 }
    claude-opus-5-5: { input: 4.00, output: 20.00, cache_read: 0.20 }
discovery: { scanners: [], min_confidence: 0.6, similarity_threshold: 0.92 }
llm: { provider: anthropic_api }
teams:
  core: { github: ["@acme/core"], linear: { team: EX, project: core }, notify: { slack: C0CORETEAM1, mention: S0COREGRP1 } }
repos:
  - { name: acme/svc, team: core }
`

// fakeClaude responde por agente, olhando o prompt de sistema e o pacote na entrada.
type fakeClaude struct {
	calls atomic.Int64
	// heavy faz cada chamada reportar muitos tokens, para estourar o orçamento no meio da rodada.
	heavy atomic.Bool
	// discard faz o Judge descartar.
	discard atomic.Bool
	// overlap faz o Analyst propor um achado que repete parte do primeiro: 1 = o Judge reduz o
	// escopo ao que sobra; 2 = o achado novo só tem o critério já coberto.
	overlap atomic.Int32
	// notesInput guarda a última entrada do intérprete de notas.
	notesInput atomic.Value
	// judgeScript, se não vazio, dá as respostas do Judge em ordem (uma por chamada).
	scriptMu    sync.Mutex
	judgeScript []string
	judgeCalls  int
}

func (f *fakeClaude) scripted() (string, bool) {
	f.scriptMu.Lock()
	defer f.scriptMu.Unlock()
	if len(f.judgeScript) == 0 {
		return "", false
	}
	f.judgeCalls++
	out := f.judgeScript[0]
	f.judgeScript = f.judgeScript[1:]
	return out, true
}

// verdict monta uma resposta do Judge.
func verdict(decision string, conf float64, premise string) string {
	return fmt.Sprintf(`{"decision":%q,"confidence":%v,"reason":"voto","merge_with":"","product_decision":{"depends":%v,"decision":%q},"related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]}}`, decision, conf, premise != "", premise)
}

// broader é o achado que se sobrepõe em parte a "Trata erro ignorado ao salvar".
const broader = `{"findings":[{"category":"bug","severity":"high","confidence":0.8,"title":"Trata erros ignorados de Save e Delete","context":"Os erros de Save e de Delete são descartados.","problem_key":"erros ignorados save delete","evidence":[{"file":"internal/store/store.go","line":5,"snippet":"_ = db.Delete(x)"}],"proposed_fix":"Devolver os erros.","acceptance_criteria":[%s],"scope_paths":["internal/store"],"signal_refs":[]}]}`

// judgeOverlap responde como o Judge diante de um similar com sobreposição detectada.
func judgeOverlap(user string, mode int32) (string, bool) {
	var in struct {
		Similar []struct {
			Fingerprint string `json:"fingerprint"`
			Overlap     struct {
				Criteria []string `json:"criteria"`
			} `json:"overlap"`
		} `json:"similar"`
	}
	_ = json.Unmarshal([]byte(user), &in)
	for _, s := range in.Similar {
		if len(s.Overlap.Criteria) == 0 {
			continue
		}
		narrowed := `{"title":"Trata erro ignorado ao apagar","context":"O erro de Delete é descartado; o de Save já tem issue.","proposed_fix":"Devolver o erro de Delete.","acceptance_criteria":["Delete com falha devolve erro ao chamador"]}`
		if mode == 2 {
			narrowed = `{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]}`
		}
		return `{"decision":"publish","confidence":0.8,"reason":"Parte já coberta.","merge_with":"","product_decision":{"depends":false,"decision":""},"related_to":["` + s.Fingerprint + `"],"narrowed":` + narrowed + `}`, true
	}
	return "", false
}

func (f *fakeClaude) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	var req struct {
		Model  string `json:"model"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
		Messages []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	system, user := req.System[0].Text, req.Messages[0].Content[0].Text
	var out string
	switch {
	case strings.Contains(system, "Package Triager"):
		if strings.HasPrefix(user, `{"repo":"acme/svc","package":"internal/store"`) {
			out = `{"category":"bug","deepen":true,"reason":"store.go:4 ignora o erro de Save.","summary":"Grava registros."}`
		} else {
			out = `{"category":"none","deepen":false,"reason":"wiring simples.","summary":"Ponto de entrada."}`
		}
	case strings.Contains(system, "Package Analyst") && f.overlap.Load() == 1:
		out = fmt.Sprintf(broader, `"Save com falha devolve erro ao chamador","Delete com falha devolve erro ao chamador"`)
	case strings.Contains(system, "Package Analyst") && f.overlap.Load() == 2:
		out = fmt.Sprintf(broader, `"Save com falha devolve erro"`)
	case strings.Contains(system, "Package Analyst"):
		out = `{"findings":[{"category":"bug","severity":"high","confidence":0.85,"title":"Trata erro ignorado ao salvar","context":"O erro de Save é descartado e o dado some.","problem_key":"erro ignorado ao salvar","evidence":[{"file":"internal/store/store.go","line":4,"snippet":"_ = db.Save(x)"},{"file":"internal/store/inexistente.go","line":1,"snippet":"x"}],"proposed_fix":"Devolver o erro de Save.","acceptance_criteria":["Save com falha devolve erro"],"scope_paths":["internal/store"],"signal_refs":[]},{"category":"bug","severity":"low","confidence":0.4,"title":"Achado sem evidência real","context":"c","problem_key":"fantasma","evidence":[{"file":"internal/store/store.go","line":999,"snippet":""}],"proposed_fix":"f","acceptance_criteria":[],"scope_paths":[],"signal_refs":[]}]}`
	case strings.Contains(system, "Review Note Interpreter"):
		f.notesInput.Store(user)
		// Os ids das notas dependem da ordem dos reviews: acha cada uma pelo texto.
		id := func(text string) string {
			m := regexp.MustCompile(`"id":"(note:\d+)","lens":"[a-z]+","round":\d+,"text":"[^"]*` + text).FindStringSubmatch(user)
			if m == nil {
				return "note:0"
			}
			return m[1]
		}
		out = `{"findings":[{"category":"bug","severity":"medium","confidence":0.7,"title":"Trata erro ignorado ao apagar","context":"Nota do reviewer: o erro de Delete também é descartado.","problem_key":"erro ignorado ao apagar","evidence":[{"file":"internal/store/store.go","line":5,"snippet":"_ = db.Delete(x)"}],"proposed_fix":"Devolver o erro de Delete.","acceptance_criteria":["Delete com falha devolve erro"],"scope_paths":["internal/store"],"signal_refs":["` + id("o erro de Delete") + `"]},` +
			`{"category":"bug","severity":"low","confidence":0.5,"title":"Problema inventado","context":"c","problem_key":"inventado","evidence":[{"file":"internal/store/store.go","line":999,"snippet":"nada"}],"proposed_fix":"f","acceptance_criteria":["x"],"scope_paths":["internal/store"],"signal_refs":["` + id("algo que não existe") + `"]}],"ignored_notes":[]}`
	case strings.Contains(system, "System Architect"):
		out = `{"findings":[]}`
	case strings.Contains(system, "Judge") && func() bool { s, ok := f.scripted(); out = s; return ok }():
	case strings.Contains(system, "Judge") && f.overlap.Load() > 0:
		var ok bool
		if out, ok = judgeOverlap(user, f.overlap.Load()); !ok {
			out = `{"decision":"publish","confidence":0.8,"reason":"Sem sobreposição.","merge_with":"","product_decision":{"depends":false,"decision":""},"related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]}}`
		}
	case strings.Contains(system, "Judge") && f.discard.Load():
		out = `{"decision":"discard","confidence":0.7,"reason":"Especulativo.","merge_with":"","product_decision":{"depends":false,"decision":""},"related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]}}`
	case strings.Contains(system, "Judge"):
		out = `{"decision":"publish","confidence":0.8,"reason":"Evidência clara.","merge_with":"","product_decision":{"depends":false,"decision":""},"related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]}}`
	default:
		http.Error(w, "agente desconhecido", http.StatusBadRequest)
		return
	}
	usage := map[string]int{"input_tokens": 2000, "output_tokens": 300}
	if f.heavy.Load() {
		usage = map[string]int{"input_tokens": 50_000, "output_tokens": 20_000}
	}
	body, _ := json.Marshal(map[string]any{
		"id": "msg_fake", "type": "message", "role": "assistant", "model": req.Model,
		"content":     []map[string]string{{"type": "text", "text": out}},
		"stop_reason": "end_turn",
		"usage":       usage,
	})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(body)
}

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

type env struct {
	runner    *Runner
	fake      *fakeClaude
	st        *store.Store
	spans     *tracetest.SpanRecorder
	src, bare string
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

	remote := t.TempDir()
	src := filepath.Join(remote, "src")
	for rel, content := range map[string]string{
		"go.mod":                  "module acme.com/svc\n\ngo 1.23\n",
		"cmd/main.go":             "package main\n\nimport \"acme.com/svc/internal/store\"\n\nfunc main() { store.Run() }\n",
		"internal/store/store.go": "package store\n\nfunc Run() {\n\t_ = db.Save(x)\n\t_ = db.Delete(x)\n}\n",
	} {
		p := filepath.Join(src, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o750)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	gitCmd(t, src, "init", "-q", "-b", "main")
	gitCmd(t, src, "add", ".")
	gitCmd(t, src, "commit", "-qm", "init")
	bare := filepath.Join(remote, "acme", "svc.git")
	gitCmd(t, remote, "clone", "-q", "--bare", src, bare)

	cfg, err := config.Parse([]byte(testConfig))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeClaude{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	rec := tracetest.NewSpanRecorder()
	tracer := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer(telemetry.TracerName)
	prices := llm.NewPriceTable(cfg.Pricing)
	r := &Runner{
		Cfg: cfg, Store: st, Prices: prices, Tracer: tracer, Embedder: HashEmbedder{},
		Git:      &repo.Git{Root: t.TempDir(), BaseURL: "file://" + remote},
		Scanners: &scan.Runner{},
		NewLLM: func(runID int64, budget llm.NanoUSD) (llm.Caller, error) {
			return llm.NewClient(llm.Options{
				API:    anthropic.NewClient(option.WithAPIKey("k"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0)),
				Prices: prices, Recorder: store.NewLLMRecorder(st), Budget: llm.NewBudget(budget), RunID: runID, Tracer: tracer, Sync: true,
			})
		},
	}
	return &env{runner: r, fake: fake, st: st, spans: rec, src: src, bare: bare}
}

func (e *env) findingCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := e.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM findings").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestShadowRunStoresJudgedFindingsAndRepeatsWithoutNewOnes(t *testing.T) {
	e := setup(t)
	ctx := context.Background()

	first, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "succeeded" || first.Stats.Mode != "full" || first.Stats.Published != 1 {
		t.Fatalf("primeira rodada = %+v", first)
	}
	if first.Stats.Deepened != 1 || first.Stats.PackagesAnalyzed != 2 {
		t.Errorf("triagem: %+v", first.Stats)
	}
	if e.findingCount(t) != 1 {
		t.Fatalf("achados gravados = %d, quer 1", e.findingCount(t))
	}
	var evidence []byte
	var state string
	if err := e.st.Pool.QueryRow(ctx, "SELECT evidence, state FROM findings").Scan(&evidence, &state); err != nil {
		t.Fatal(err)
	}
	if state != "open" || strings.Contains(string(evidence), "inexistente.go") || !strings.Contains(string(evidence), "/blob/") {
		t.Errorf("evidência inválida deveria ser descartada e permalink preenchido: %s (%s)", evidence, state)
	}
	items, err := e.st.Q.ListRunItems(ctx, first.RunID)
	if err != nil {
		t.Fatal(err)
	}
	var skippedGhost bool
	for _, it := range items {
		if it.Agent == "analyst" && it.Status == "skipped" && strings.HasPrefix(it.Reason.String, "evidência inválida: internal/store/store.go:999") {
			skippedGhost = true
		}
	}
	if !skippedGhost {
		t.Error("o achado com linha inexistente deveria ser pulado com motivo \"evidência inválida\"")
	}
	if first.Stats.InvalidEvidence != 1 || first.Stats.EvidenceDropped != 1 {
		t.Errorf("evidência: inválidos %d, removidas %d; quer 1 e 1", first.Stats.InvalidEvidence, first.Stats.EvidenceDropped)
	}
	calls, err := e.st.Q.ListLLMCallsByRun(ctx, pgtype.Int8{Int64: first.RunID, Valid: true})
	if err != nil || len(calls) != 5 { // 2 triagens + 1 análise + 1 arquitetura + 1 juiz
		t.Fatalf("llm_calls = %d (%v)", len(calls), err)
	}
	run, _ := e.st.Q.GetRun(ctx, first.RunID)
	if cost, _ := store.NumericToNanoUSD(run.CostUsd); llm.NanoUSD(cost) != first.Cost || cost == 0 {
		t.Errorf("custo da rodada no banco %d, no orçamento %d", cost, first.Cost)
	}

	// Segunda rodada sem commits novos: nada a analisar, nenhuma chamada, nenhum achado novo.
	before := e.fake.calls.Load()
	second, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.NoChanges || second.Status != "no_changes" || e.fake.calls.Load() != before || e.findingCount(t) != 1 {
		t.Errorf("segunda rodada = %+v, chamadas extras = %d, achados = %d", second, e.fake.calls.Load()-before, e.findingCount(t))
	}

	// Rodada completa forçada: o mesmo problema volta do modelo e é barrado pelo fingerprint.
	third, err := e.runner.Run(ctx, Options{Repo: "acme/svc", Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if third.Stats.Published != 0 || third.Stats.Duplicates != 1 || e.findingCount(t) != 1 {
		t.Errorf("rodada completa repetida = %+v, achados = %d", third.Stats, e.findingCount(t))
	}

	// Commit novo só em cmd/: rodada incremental analisa um pacote.
	if err := os.WriteFile(filepath.Join(e.src, "cmd", "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitCmd(t, e.src, "commit", "-qam", "muda cmd")
	gitCmd(t, e.src, "push", "-q", e.bare, "main")
	fourth, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil {
		t.Fatal(err)
	}
	if fourth.Stats.Mode != "incremental" || fourth.Stats.PackagesAnalyzed != 1 || fourth.Stats.BaseSHA == "" {
		t.Errorf("rodada incremental = %+v", fourth.Stats)
	}

	// Spans: rodada, repo, agente e chamada com custo.
	names := map[string]int{}
	var withCost bool
	for _, s := range e.spans.Ended() {
		names[s.Name()]++
		for _, a := range s.Attributes() {
			if a.Key == telemetry.AttrCostUSD && a.Value.AsFloat64() > 0 {
				withCost = true
			}
		}
	}
	for _, n := range []string{"sentinela.run", "repo acme/svc", "agent.triager", "agent.analyst", "agent.judge", "llm.call"} {
		if names[n] == 0 {
			t.Errorf("span %q ausente (spans: %v)", n, names)
		}
	}
	if !withCost {
		t.Error("spans de chamada deveriam ter custo como atributo")
	}
}

func TestRunAbortsWhenBudgetIsExceededAndRecordsStatus(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	// Orçamento abaixo da estimativa esperada: barrado antes de qualquer chamada.
	_, err := e.runner.Run(ctx, Options{Repo: "acme/svc", BudgetUSD: 0.000001})
	if !errors.Is(err, ErrEstimateOverBudget) {
		t.Fatalf("esperava ErrEstimateOverBudget, veio %v", err)
	}
	if e.fake.calls.Load() != 0 {
		t.Error("nenhuma chamada deveria ser feita")
	}

	// Orçamento acima do esperado (~$0,18), mas o custo real estoura: cada chamada reporta
	// 50K/20K tokens e a análise no Sonnet sozinha custa ~$0,30. A rodada aborta no meio.
	e.fake.heavy.Store(true)
	res, err := e.runner.Run(ctx, Options{Repo: "acme/svc", BudgetUSD: 0.20})
	if !errors.Is(err, llm.ErrBudgetExceeded) {
		t.Fatalf("esperava ErrBudgetExceeded, veio %v (res %+v)", err, res)
	}
	run, err := e.st.Q.GetRun(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "aborted_budget" || !run.FinishedAt.Valid {
		t.Errorf("rodada = %s (finished %v)", run.Status, run.FinishedAt.Valid)
	}
}

func TestEstimateOnlyDoesNotCallModelsNorCreateRun(t *testing.T) {
	e := setup(t)
	res, err := e.runner.Run(context.Background(), Options{Repo: "acme/svc", EstimateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.RunID != 0 || e.fake.calls.Load() != 0 || len(res.Stats.Estimate.Agents) != 4 || res.Stats.Estimate.Expected <= 0 ||
		res.Stats.Estimate.Worst < res.Stats.Estimate.Expected {
		t.Errorf("estimativa = %+v", res)
	}
}

// Com provider sem custo por token, a rodada é limitada por chamadas: a estimativa barra
// antes de começar e, se o limite estourar no meio, a rodada termina como aborted_budget.
func TestCallLimitGatesAndAbortsRunsWithReplayProvider(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	dir := t.TempDir()
	for agent, text := range map[string]string{
		"triager":   `{"category":"bug","deepen":true,"reason":"r","summary":"s"}`,
		"analyst":   `{"findings":[]}`,
		"architect": `{"findings":[]}`,
	} {
		_ = os.MkdirAll(filepath.Join(dir, agent), 0o750)
		if err := os.WriteFile(filepath.Join(dir, agent, "default.json"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.runner.Cfg.LLM.Provider = config.ProviderReplay
	e.runner.Cfg.LLM.ClaudeCode.MaxCallsPerRun = 3
	if _, err := e.runner.Run(ctx, Options{Repo: "acme/svc"}); !errors.Is(err, ErrEstimateOverBudget) {
		t.Fatalf("estimativa acima do limite de chamadas deveria barrar, veio %v", err)
	}

	e.runner.Cfg.LLM.ClaudeCode.MaxCallsPerRun = 50
	e.runner.NewLLM = func(runID int64, _ llm.NanoUSD) (llm.Caller, error) {
		return &llm.Replay{Dir: dir, Recorder: store.NewLLMRecorder(e.st), B: llm.NewCallBudget(3), RunID: runID}, nil
	}
	res, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if !errors.Is(err, llm.ErrCallLimitExceeded) {
		t.Fatalf("esperava ErrCallLimitExceeded, veio %v", err)
	}
	run, _ := e.st.Q.GetRun(ctx, res.RunID)
	if run.Status != "aborted_budget" || res.Calls != 3 {
		t.Errorf("rodada = %s, chamadas = %d", run.Status, res.Calls)
	}
	var provider string
	if err := e.st.Pool.QueryRow(ctx, "SELECT DISTINCT provider FROM llm_calls WHERE run_id = $1", res.RunID).Scan(&provider); err != nil || provider != "replay" {
		t.Errorf("provider em llm_calls = %q (%v)", provider, err)
	}
}

// Override por time entra na estimativa: Opus na análise do time core encarece o Analyst e
// aparece na coluna de modelo.
func TestEstimateUsesTheModelsOfEachPackageOwnerTeam(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	base, err := e.runner.Run(ctx, Options{Repo: "acme/svc", EstimateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	e.runner.Cfg.Teams["core"].Models = config.ModelRoles{Analysis: "claude-opus-5-5"}
	over, err := e.runner.Run(ctx, Options{Repo: "acme/svc", EstimateOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	agent := func(r Result, name string) AgentEstimate {
		for _, a := range r.Stats.Estimate.Agents {
			if a.Agent == name {
				return a
			}
		}
		t.Fatalf("sem estimativa do %s", name)
		return AgentEstimate{}
	}
	b, o := agent(base, "analyst"), agent(over, "analyst")
	if o.Model != "claude-opus-5-5" || o.Expected <= b.Expected || o.Worst <= b.Worst {
		t.Errorf("analyst sem o override do time: antes %+v, depois %+v", b, o)
	}
	if j := agent(over, "judge"); j.Model != "claude-sonnet-5-5" || j.Expected != agent(base, "judge").Expected {
		t.Errorf("o judge não foi trocado e não deveria mudar: %+v", j)
	}
}

// limitedCaller faz a análise parar no limite de uso da assinatura, como o provider claude_code
// devolve quando o CLI diz "You've hit your weekly limit".
type limitedCaller struct{ llm.Caller }

func (c limitedCaller) Call(ctx context.Context, req llm.Request) (llm.Response, error) {
	if req.Agent == "analyst" {
		return llm.Response{}, fmt.Errorf("%w: You've hit your weekly limit · resets Oct 16, 7am (America/Sao_Paulo)", llm.ErrUsageLimit)
	}
	return c.Caller.Call(ctx, req)
}

func (c limitedCaller) Batch(ctx context.Context, items []llm.BatchItem) (map[string]llm.BatchResult, error) {
	for _, it := range items {
		if it.Request.Agent == "analyst" {
			return nil, fmt.Errorf("%w: You've hit your weekly limit · resets Oct 16, 7am (America/Sao_Paulo)", llm.ErrUsageLimit)
		}
	}
	return c.Caller.Batch(ctx, items)
}

// Cota da assinatura no meio da rodada (bug de 2026-10-14): a rodada termina como aborted_budget,
// sem avançar o head_sha, e a rodada seguinte analisa o mesmo código e grava os achados.
func TestUsageLimitAbortsTheRunWithoutLosingFindings(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	orig := e.runner.NewLLM
	e.runner.NewLLM = func(runID int64, budget llm.NanoUSD) (llm.Caller, error) {
		c, err := orig(runID, budget)
		return limitedCaller{c}, err
	}
	res, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if !errors.Is(err, llm.ErrUsageLimit) {
		t.Fatalf("esperava ErrUsageLimit, veio %v", err)
	}
	run, err := e.st.Q.GetRun(ctx, res.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "aborted_budget" || e.findingCount(t) != 0 {
		t.Fatalf("rodada = %s, achados = %d", run.Status, e.findingCount(t))
	}
	e.runner.NewLLM = orig
	again, err := e.runner.Run(ctx, Options{Repo: "acme/svc"})
	if err != nil {
		t.Fatal(err)
	}
	if again.Stats.Mode != "full" || again.Status != "succeeded" || e.findingCount(t) != 1 {
		t.Errorf("rodada seguinte = %s (%s), achados = %d; quer a análise completa de novo, com o achado", again.Status, again.Stats.Mode, e.findingCount(t))
	}
}
