package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
)

type fakeRun struct {
	calls []mcpcall.Call
	resp  string
}

func (f *fakeRun) Run(_ context.Context, c mcpcall.Call, out any) error {
	f.calls = append(f.calls, c)
	return json.Unmarshal([]byte(f.resp), out)
}

func TestSlackMCPPostsThroughTheUsersSlackMCPWithTheSentinelaPrefix(t *testing.T) {
	f := &fakeRun{resp: `{"ok":true}`}
	s := &SlackMCP{Run: f, Server: "claude.ai Slack"}
	if err := s.Post(context.Background(), "C0TESTE0001", "nova issue EX-1"); err != nil {
		t.Fatal(err)
	}
	c := f.calls[0]
	if c.Server != "claude.ai Slack" || c.Agent != "slack_mcp" || !strings.Contains(c.Prompt, "mcp__claude_ai_Slack__slack_send_message") ||
		!strings.Contains(c.Prompt, `"channel_id":"C0TESTE0001"`) || !strings.Contains(c.Prompt, `"message":"*Sentinela* · nova issue EX-1"`) {
		t.Errorf("chamada = %+v", c)
	}
	if err := s.Post(context.Background(), "C0TESTE0001", "*Sentinela · rodada 3*"); err != nil || strings.Contains(f.calls[1].Prompt, "*Sentinela* · *Sentinela") {
		t.Errorf("prefixo duplicado: %v %s", err, f.calls[1].Prompt)
	}
	f.resp = `{"ok":false,"error":"channel_not_found"}`
	if err := s.Post(context.Background(), "C0X", "x"); !errors.Is(err, mcpcall.ErrMCP) || !strings.Contains(err.Error(), "channel_not_found") {
		t.Errorf("erro: %v", err)
	}
}
