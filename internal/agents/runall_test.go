package agents

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

type nopRecorder struct{ n int }

func (r *nopRecorder) RecordCall(context.Context, llm.CallRecord) error { r.n++; return nil }

// Servidor que recusa tudo em claude-sonnet-5-5 e o item "teimoso" em qualquer modelo.
func refusingClient(t *testing.T) (*llm.Client, *nopRecorder) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		text := req.Messages[0].Content[0].Text
		stop, content := "end_turn", `[{"type":"text","text":"{\"ok\":true}"}]`
		if req.Model == "claude-sonnet-5-5" || strings.Contains(text, "teimoso") {
			stop, content = "refusal", `[]`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"msg_x","type":"message","role":"assistant","model":"` + req.Model +
			`","content":` + content + `,"stop_reason":"` + stop + `","usage":{"input_tokens":10,"output_tokens":1}}`))
	}))
	t.Cleanup(srv.Close)
	cfg, err := config.Load("../../testdata/sentinela.yaml")
	if err != nil {
		t.Fatal(err)
	}
	rec := &nopRecorder{}
	c, err := llm.NewClient(llm.Options{
		API:    anthropic.NewClient(option.WithAPIKey("k"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0)),
		Prices: llm.NewPriceTable(cfg.Pricing), Recorder: rec, Budget: llm.NewBudget(llm.USD(1)), Sync: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, rec
}

func items() []Item {
	req := func(user string) llm.Request {
		return llm.Request{Agent: "analyst", PromptVersion: "analyst@1", Model: "claude-sonnet-5-5", User: user, MaxTokens: 100}
	}
	return []Item{{Key: "pkg/a", Request: req("normal")}, {Key: "pkg/b", Request: req("teimoso")}}
}

func TestRefusalFallsBackToConfiguredModelWithoutFailingTheRun(t *testing.T) {
	c, rec := refusingClient(t)
	out, err := RunAll(context.Background(), c, noop.NewTracerProvider().Tracer("t"), "analyst", items(),
		config.RefusalPolicy{Action: config.RefusalFallbackModel, FallbackModel: "claude-opus-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a := out["pkg/a"]; a.Skipped || a.Response.Model != "claude-opus-5-5" {
		t.Errorf("pkg/a deveria ser resolvido pelo fallback: %+v", a)
	}
	b := out["pkg/b"]
	if !b.Skipped || !strings.Contains(b.Reason, "recusa do modelo claude-sonnet-5-5 e do fallback claude-opus-5-5") {
		t.Errorf("pkg/b deveria ser pulado com motivo: %+v", b)
	}
	if rec.n != 4 {
		t.Errorf("todas as tentativas devem ir para llm_calls: %d", rec.n)
	}
}

func TestRefusalSkipPolicyRecordsReason(t *testing.T) {
	c, _ := refusingClient(t)
	out, err := RunAll(context.Background(), c, noop.NewTracerProvider().Tracer("t"), "analyst", items(),
		config.RefusalPolicy{Action: config.RefusalSkip})
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"pkg/a", "pkg/b"} {
		if !out[k].Skipped || !strings.Contains(out[k].Reason, "recusa do modelo claude-sonnet-5-5") {
			t.Errorf("%s: %+v", k, out[k])
		}
	}
}

// O fallback de um item (o do time dono, via config.ModelsFor) vence o da política global.
func TestItemFallbackFromTheTeamWinsOverTheGlobalPolicy(t *testing.T) {
	c, _ := refusingClient(t)
	its := items()[:1]
	its[0].Fallback = "claude-haiku-5-5"
	out, err := RunAll(context.Background(), c, noop.NewTracerProvider().Tracer("t"), "analyst", its,
		config.RefusalPolicy{Action: config.RefusalFallbackModel, FallbackModel: "claude-opus-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	if a := out["pkg/a"]; a.Skipped || a.Response.Model != "claude-haiku-5-5" {
		t.Errorf("pkg/a deveria usar o fallback do item: %+v", a)
	}
}
