package notify

import (
	"context"
	"fmt"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/mcpcall"
)

// SlackMCP posta pelo MCP do Slack configurado no Claude Code do usuário (adapters.notifier
// slack_mcp, ADR 0039). A mensagem sai como a pessoa dona do MCP, não como o bot; o texto começa
// com "Sentinela" para identificar a origem. Cada aviso é uma chamada `claude -p`.
type SlackMCP struct {
	Run    mcpcall.Runner
	Server string // ex.: "claude.ai Slack"
}

var _ Notifier = (*SlackMCP)(nil)

// Post envia text para o canal (ID C... ou #nome).
func (s *SlackMCP) Post(ctx context.Context, channel, text string) error {
	if !strings.HasPrefix(strings.TrimLeft(text, "*_ "), "Sentinela") {
		text = "*Sentinela* · " + text
	}
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	tool := mcpcall.ToolPrefix(s.Server) + "__slack_send_message"
	schema := map[string]any{"type": "object", "required": []string{"ok"}, "properties": map[string]any{
		"ok": map[string]any{"type": "boolean"}, "error": map[string]any{"type": "string"},
	}}
	err := s.Run.Run(ctx, mcpcall.Call{Agent: "slack_mcp", Server: s.Server, Schema: schema,
		Prompt: mcpcall.Prompt("postar uma mensagem no Slack",
			mcpcall.Step{Tool: tool, Args: map[string]any{"channel_id": channel, "message": text},
				Note: "Se o canal vier como #nome, use o ID dele (procure com a busca de canais do mesmo servidor). Passe a mensagem exatamente como está. ok=true se postou."})}, &out)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out.Error) != "" {
		return fmt.Errorf("%w: %s", mcpcall.ErrMCP, out.Error)
	}
	if !out.OK {
		return fmt.Errorf("%w: slack_send_message não confirmou", mcpcall.ErrMCP)
	}
	return nil
}
