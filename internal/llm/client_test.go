package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// fakeAPI responde com as respostas gravadas em testdata/; nenhum teste chama a API real.
type fakeAPI struct {
	t           *testing.T
	mu          sync.Mutex
	messageFile string
	requests    []string // método + caminho
	bodies      [][]byte
	batchPolls  int
}

func (f *fakeAPI) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.bodies = append(f.bodies, body)
	f.mu.Unlock()

	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
		f.serve(w, f.messageFile)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/messages/batches":
		f.serve(w, "batch_created.json")
	case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/batches/msgbatch_01Golden":
		f.mu.Lock()
		f.batchPolls++
		polls := f.batchPolls
		f.mu.Unlock()
		if polls < 2 {
			f.serve(w, "batch_created.json")
			return
		}
		f.serve(w, "batch_ended.json")
	case r.Method == http.MethodGet && r.URL.Path == "/v1/messages/batches/msgbatch_01Golden/results":
		f.serve(w, "batch_results.jsonl")
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeAPI) serve(w http.ResponseWriter, name string) {
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		f.t.Errorf("golden %s: %v", name, err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(data)
}

func (f *fakeAPI) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

type memRecorder struct {
	mu   sync.Mutex
	recs []CallRecord
}

func (m *memRecorder) RecordCall(_ context.Context, rec CallRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recs = append(m.recs, rec)
	return nil
}

func realPricing(t *testing.T) *PriceTable {
	t.Helper()
	cfg, err := config.Load(filepath.Join("..", "..", "testdata", "sentinela.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return NewPriceTable(cfg.Pricing)
}

func newTestClient(t *testing.T, messageFile string, budget NanoUSD) (*Client, *fakeAPI, *memRecorder) {
	t.Helper()
	fake := &fakeAPI{t: t, messageFile: messageFile}
	srv := httptest.NewServer(http.HandlerFunc(fake.handler))
	t.Cleanup(srv.Close)
	api := anthropic.NewClient(
		option.WithAPIKey("test-key"),
		option.WithBaseURL(srv.URL),
		option.WithMaxRetries(0),
	)
	rec := &memRecorder{}
	c, err := NewClient(Options{
		API: api, Prices: realPricing(t), Recorder: rec, Budget: NewBudget(budget),
		RunID: 42, PollInterval: time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, fake, rec
}

var triageSchema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"category": map[string]any{"type": "string", "enum": []string{"bug", "performance", "improvement", "security"}},
		"deepen":   map[string]any{"type": "boolean"},
		"reason":   map[string]any{"type": "string"},
	},
	"required":             []string{"category", "deepen", "reason"},
	"additionalProperties": false,
}

type triageResult struct {
	Category string `json:"category"`
	Deepen   bool   `json:"deepen"`
	Reason   string `json:"reason"`
}

func triageRequest() Request {
	return Request{
		Agent: "package_triager", PromptVersion: "v1", Model: "claude-haiku-5-5",
		System: "contexto comum do repo", User: "pacote internal/handler", MaxTokens: 1024,
		Schema: triageSchema,
	}
}

func TestSimpleCallReturnsStructuredOutputAndRecordsCost(t *testing.T) {
	c, fake, rec := newTestClient(t, "message_triage.json", USD(5))

	resp, err := c.Call(context.Background(), triageRequest())
	if err != nil {
		t.Fatal(err)
	}
	var out triageResult
	if err := resp.DecodeJSON(&out); err != nil {
		t.Fatal(err)
	}
	if out.Category != "bug" || !out.Deepen {
		t.Errorf("saída = %+v", out)
	}

	// 1200 entrada × $0.10 + 80 saída × $0.50 + 3000 escrita de cache × $0.10 × 1.25, por MTok.
	wantCost := USD((1200*0.10 + 80*0.50 + 3000*0.10*1.25) / 1e6)
	if resp.Cost != wantCost {
		t.Errorf("custo = %s, quer %s", resp.Cost, wantCost)
	}
	if len(rec.recs) != 1 {
		t.Fatalf("esperava 1 llm_call gravada, veio %d", len(rec.recs))
	}
	r := rec.recs[0]
	if r.Agent != "package_triager" || r.PromptVersion != "v1" || r.Mode != "sync" || r.RunID != 42 ||
		r.Status != "succeeded" || r.Cost != wantCost || r.MessageID != "msg_01TriageGolden" || r.Usage.CacheWrite5mTokens != 3000 {
		t.Errorf("registro = %+v", r)
	}
	if c.Budget().Spent() != wantCost {
		t.Errorf("orçamento debitou %s, quer %s", c.Budget().Spent(), wantCost)
	}

	// O pedido usa structured outputs e põe o contexto comum no cache.
	var sent struct {
		System []struct {
			CacheControl map[string]any `json:"cache_control"`
		} `json:"system"`
		OutputConfig struct {
			Format struct {
				Type   string         `json:"type"`
				Schema map[string]any `json:"schema"`
			} `json:"format"`
		} `json:"output_config"`
	}
	if err := json.Unmarshal(fake.bodies[0], &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.System) != 1 || sent.System[0].CacheControl["type"] != "ephemeral" {
		t.Errorf("system sem cache_control: %s", fake.bodies[0])
	}
	if sent.OutputConfig.Format.Type != "json_schema" || sent.OutputConfig.Format.Schema == nil {
		t.Errorf("output_config.format ausente: %s", fake.bodies[0])
	}
}

func TestCallAbortsRunWhenBudgetIsExceeded(t *testing.T) {
	c, fake, rec := newTestClient(t, "message_triage.json", USD(0.0001))

	_, err := c.Call(context.Background(), triageRequest())
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("esperava ErrBudgetExceeded, veio %v", err)
	}
	if len(rec.recs) != 1 {
		t.Fatal("a chamada que estourou o orçamento ainda precisa ser gravada")
	}

	_, err = c.Call(context.Background(), triageRequest())
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("segunda chamada deveria ser recusada, veio %v", err)
	}
	if fake.count() != 1 {
		t.Errorf("a segunda chamada não deveria chegar na API; requisições = %d", fake.count())
	}
}

func TestRefusalIsRecordedAndReturnedAsBadStop(t *testing.T) {
	c, _, rec := newTestClient(t, "message_refusal.json", USD(5))
	req := triageRequest()
	req.Model = "claude-sonnet-5-5"

	_, err := c.Call(context.Background(), req)
	if !errors.Is(err, ErrBadStop) {
		t.Fatalf("esperava ErrBadStop, veio %v", err)
	}
	if len(rec.recs) != 1 || rec.recs[0].StopReason != "refusal" || rec.recs[0].Cost == 0 {
		t.Errorf("recusa deveria ser gravada com custo: %+v", rec.recs)
	}
}

func TestBatchReturnsResultsByCustomIDWithBatchDiscount(t *testing.T) {
	c, fake, rec := newTestClient(t, "message_triage.json", USD(5))
	items := []BatchItem{
		{CustomID: "pkg-handler", Request: triageRequest()},
		{CustomID: "pkg-store", Request: triageRequest()},
	}

	results, err := c.Batch(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	ok := results["pkg-handler"]
	if ok.Err != nil {
		t.Fatal(ok.Err)
	}
	var out triageResult
	if err := ok.DecodeJSON(&out); err != nil || out.Category != "performance" {
		t.Errorf("saída = %+v, err = %v", out, err)
	}
	wantCost := USD((2000*0.10 + 100*0.50) / 1e6 * 0.5)
	if ok.Cost != wantCost {
		t.Errorf("custo em batch = %s, quer %s", ok.Cost, wantCost)
	}
	if results["pkg-store"].Err == nil {
		t.Error("item com erro deveria trazer Err")
	}
	if len(rec.recs) != 2 {
		t.Fatalf("esperava 2 llm_calls gravadas, veio %d", len(rec.recs))
	}
	for _, r := range rec.recs {
		if r.Mode != "batch" || r.BatchID != "msgbatch_01Golden" || r.CustomID == "" {
			t.Errorf("registro de batch incompleto: %+v", r)
		}
	}
	if fake.batchPolls < 2 {
		t.Errorf("o cliente deveria consultar o batch até ended; consultas = %d", fake.batchPolls)
	}
}

func TestBatchIsRefusedBeforeSubmissionWhenWorstCaseDoesNotFit(t *testing.T) {
	c, fake, _ := newTestClient(t, "message_triage.json", USD(0.001))
	req := triageRequest()
	req.MaxTokens = 64000 // 64000 × $0.50/MTok × 0.5 = $0.016, acima de $0.001

	_, err := c.Batch(context.Background(), []BatchItem{{CustomID: "a", Request: req}})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("esperava ErrBudgetExceeded, veio %v", err)
	}
	if fake.count() != 0 {
		t.Errorf("nada deveria ser enviado; requisições = %d", fake.count())
	}
}

func TestBatchRejectsDuplicateCustomIDs(t *testing.T) {
	c, _, _ := newTestClient(t, "message_triage.json", USD(5))
	_, err := c.Batch(context.Background(), []BatchItem{
		{CustomID: "a", Request: triageRequest()},
		{CustomID: "a", Request: triageRequest()},
	})
	if err == nil {
		t.Fatal("custom_id repetido deveria falhar")
	}
}

func TestBatchEmitsOneSpanPerItemWithCostAttributes(t *testing.T) {
	c, _, _ := newTestClient(t, "message_triage.json", USD(5))
	rec := tracetest.NewSpanRecorder()
	c.o.Tracer = sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)).Tracer("t")
	if _, err := c.Batch(context.Background(), []BatchItem{
		{CustomID: "pkg-handler", Request: triageRequest()},
		{CustomID: "pkg-store", Request: triageRequest()},
	}); err != nil {
		t.Fatal(err)
	}
	var batch, calls int
	for _, s := range rec.Ended() {
		switch s.Name() {
		case "llm.batch":
			batch++
		case "llm.call":
			calls++
			var hasCost, hasCustom bool
			for _, a := range s.Attributes() {
				hasCost = hasCost || a.Key == telemetry.AttrCostUSD
				hasCustom = hasCustom || (a.Key == telemetry.AttrCustomID && a.Value.AsString() != "")
			}
			if !hasCost || !hasCustom {
				t.Errorf("span de item sem custo ou custom_id: %v", s.Attributes())
			}
		}
	}
	if batch != 1 || calls != 2 {
		t.Errorf("spans: batch=%d calls=%d", batch, calls)
	}
}
