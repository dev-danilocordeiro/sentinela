package llm

import (
	"context"
	"os"
	"testing"
)

// Teste manual contra o Claude Code CLI de verdade (usa a assinatura, não a API).
// Rode com SENTINELA_REAL_CLAUDE=1 go test ./internal/llm -run RealClaude -v
func TestRealClaudeCodeCall(t *testing.T) {
	if os.Getenv("SENTINELA_REAL_CLAUDE") != "1" {
		t.Skip("defina SENTINELA_REAL_CLAUDE=1 para chamar o claude de verdade")
	}
	rec := &memRec{}
	cc, err := NewClaudeCode(ClaudeCodeOptions{Recorder: rec, Budget: NewCallBudget(3), WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cc.Call(context.Background(), Request{
		Agent: "probe", PromptVersion: "probe@1", Model: "claude-haiku-5-5", System: "Responda no schema.",
		User: "O valor de ok é verdadeiro.", MaxTokens: 100, Schema: okSchema, Effort: "low",
	})
	t.Logf("resp=%+v err=%v", resp, err)
	for _, r := range rec.recs {
		t.Logf("rec=%+v", r)
	}
	if err != nil || resp.StopReason == "refusal" {
		t.Fatal("chamada real falhou")
	}
}
