package mcpcall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

type recs struct{ got []llm.CallRecord }

func (r *recs) RecordCall(_ context.Context, rec llm.CallRecord) error {
	r.got = append(r.got, rec)
	return nil
}

func fakeClaude(t *testing.T, stdout string) (bin, argsFile, stdinFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile, stdinFile = filepath.Join(dir, "args"), filepath.Join(dir, "stdin")
	out := filepath.Join(dir, "out.json")
	if err := os.WriteFile(out, []byte(stdout), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "claude")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\" >> " + argsFile + "; done\ncat > " + stdinFile + "\nenv | grep -cE '^(ANTHROPIC_|GITHUB_TOKEN|SLACK_BOT_TOKEN|DATABASE_URL)' >> " + argsFile + "\ncat " + out + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // script de teste
		t.Fatal(err)
	}
	return bin, argsFile, stdinFile
}

func TestToolPrefix(t *testing.T) {
	for in, want := range map[string]string{"claude.ai Linear": "mcp__claude_ai_Linear", "linear": "mcp__linear", "claude.ai Slack": "mcp__claude_ai_Slack"} {
		if got := ToolPrefix(in); got != want {
			t.Errorf("%q -> %q, quer %q", in, got, want)
		}
	}
}

// Só a ToolSearch como ferramenta embutida, só o servidor pedido com permissão, saída
// estruturada, sem ANTHROPIC_* no ambiente e a chamada em llm_calls.
func TestCLIAllowsOnlyTheServerAndDecodesTheStructuredOutput(t *testing.T) {
	bin, argsFile, stdinFile := fakeClaude(t, `{"type":"result","is_error":false,"structured_output":{"ok":true},"session_id":"s1","usage":{"input_tokens":10,"output_tokens":3}}`)
	r := &recs{}
	c := &CLI{Binary: bin, Model: "claude-haiku-5-5", WorkDir: t.TempDir(), Recorder: r, Environ: []string{"PATH=" + os.Getenv("PATH"), "ANTHROPIC_API_KEY=x", "GITHUB_TOKEN=ghp_x", "SLACK_BOT_TOKEN=xoxb-x", "DATABASE_URL=postgres://x"}}
	var out struct {
		OK bool `json:"ok"`
	}
	err := c.Run(context.Background(), Call{Agent: "linear_mcp", Server: "claude.ai Linear", Prompt: Prompt("tarefa", Step{Tool: "mcp__claude_ai_Linear__list_teams", Args: map[string]any{}}),
		Schema: map[string]any{"type": "object"}}, &out)
	if err != nil || !out.OK {
		t.Fatalf("run: %v %+v", err, out)
	}
	args, _ := os.ReadFile(argsFile)
	a := string(args)
	for _, want := range []string{"-p\n", "--tools\nToolSearch\n", "--allowedTools\nmcp__claude_ai_Linear\n", "--model\nclaude-haiku-5-5\n", "--json-schema\n", "--no-session-persistence\n"} {
		if !strings.Contains(a, want) {
			t.Errorf("args sem %q:\n%s", want, a)
		}
	}
	if !strings.HasSuffix(strings.TrimSpace(a), "\n0") {
		t.Errorf("ANTHROPIC_* ou segredo do Sentinela chegou ao claude (D-17):\n%s", a)
	}
	in, _ := os.ReadFile(stdinFile)
	if !strings.Contains(string(in), "`mcp__claude_ai_Linear__list_teams` com os argumentos: {}") {
		t.Errorf("prompt = %s", in)
	}
	if len(r.got) != 1 || r.got[0].Agent != "linear_mcp" || r.got[0].Status != "succeeded" || r.got[0].Usage.InputTokens != 10 || r.got[0].MessageID != "s1" {
		t.Errorf("llm_calls = %+v", r.got)
	}
}

func TestCLIFailuresAreErrMCPAndRecorded(t *testing.T) {
	for name, stdout := range map[string]string{
		"erro do modelo": `{"is_error":true,"subtype":"error_during_execution","result":"MCP server not connected"}`,
		"sem estrutura":  `{"is_error":false,"result":"não consegui"}`,
		"negada":         `{"is_error":false,"result":"sem permissão","permission_denials":[{"tool_name":"mcp__claude_ai_Slack__slack_send_message"}]}`,
		"saída quebrada": `não é json`,
	} {
		bin, _, _ := fakeClaude(t, stdout)
		r := &recs{}
		c := &CLI{Binary: bin, Model: "m", WorkDir: t.TempDir(), Recorder: r}
		var out map[string]any
		err := c.Run(context.Background(), Call{Agent: "slack_mcp", Server: "claude.ai Slack", Prompt: "x", Schema: map[string]any{}}, &out)
		if !errors.Is(err, ErrMCP) || len(r.got) != 1 || r.got[0].Status != "errored" {
			t.Errorf("%s: %v, %+v", name, err, r.got)
		}
		if name == "negada" && !strings.Contains(err.Error(), "ferramentas negadas: mcp__claude_ai_Slack__slack_send_message") {
			t.Errorf("negada: %v", err)
		}
	}
}

// fakeClaudeSeq responde, a cada chamada, a próxima saída da lista (a última se repete).
func fakeClaudeSeq(t *testing.T, outs ...string) (bin, countFile string) {
	t.Helper()
	dir := t.TempDir()
	countFile = filepath.Join(dir, "count")
	var script strings.Builder
	script.WriteString("#!/bin/sh\ncat > /dev/null\necho x >> " + countFile + "\nn=$(wc -l < " + countFile + ")\n")
	for i, o := range outs {
		f := filepath.Join(dir, "out"+string(rune('0'+i)))
		if err := os.WriteFile(f, []byte(o), 0o600); err != nil {
			t.Fatal(err)
		}
		if i < len(outs)-1 {
			script.WriteString("if [ \"$n\" -eq " + string(rune('1'+i)) + " ]; then cat " + f + "; exit 0; fi\n")
		} else {
			script.WriteString("cat " + f + "\n")
		}
	}
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script.String()), 0o700); err != nil { //nolint:gosec // script de teste
		t.Fatal(err)
	}
	return bin, countFile
}

func calls(t *testing.T, countFile string) int {
	t.Helper()
	b, _ := os.ReadFile(countFile) //nolint:gosec // arquivo do teste
	return strings.Count(string(b), "\n")
}

// A sessão às vezes começa sem as ferramentas do conector carregadas: o modelo responde no
// campo error que a ferramenta não está disponível. Isso não é falha do servidor: espera e tenta
// de novo antes de devolver o erro.
func TestCLIRetriesWhenTheConnectorToolsAreMissing(t *testing.T) {
	missing := `{"is_error":false,"structured_output":{"ok":false,"error":"A ferramenta mcp__claude_ai_Linear__save_issue não está disponível nesta sessão."}}`
	ok := `{"is_error":false,"structured_output":{"ok":true,"error":""}}`
	t.Run("na segunda tentativa", func(t *testing.T) {
		bin, count := fakeClaudeSeq(t, missing, ok)
		r := &recs{}
		c := &CLI{Binary: bin, Model: "m", WorkDir: t.TempDir(), Recorder: r, RetryWaits: []time.Duration{time.Millisecond, time.Millisecond}}
		var out struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if err := c.Run(context.Background(), Call{Agent: "linear_mcp", Server: "claude.ai Linear", Prompt: "x", Schema: map[string]any{}}, &out); err != nil {
			t.Fatal(err)
		}
		if !out.OK || out.Error != "" || calls(t, count) != 2 || len(r.got) != 2 {
			t.Errorf("out = %+v, chamadas = %d, registros = %d", out, calls(t, count), len(r.got))
		}
	})
	t.Run("esgota as tentativas", func(t *testing.T) {
		bin, count := fakeClaudeSeq(t, missing)
		c := &CLI{Binary: bin, Model: "m", WorkDir: t.TempDir(), RetryWaits: []time.Duration{time.Millisecond, time.Millisecond}}
		var out struct {
			Error string `json:"error"`
		}
		err := c.Run(context.Background(), Call{Agent: "linear_mcp", Server: "claude.ai Linear", Prompt: "x", Schema: map[string]any{}}, &out)
		if !errors.Is(err, ErrToolsMissing) || calls(t, count) != 3 {
			t.Errorf("err = %v, chamadas = %d", err, calls(t, count))
		}
	})
	t.Run("erro da ferramenta não repete", func(t *testing.T) {
		bin, count := fakeClaudeSeq(t, `{"is_error":false,"structured_output":{"error":"Entity not found: Issue"}}`)
		c := &CLI{Binary: bin, Model: "m", WorkDir: t.TempDir(), RetryWaits: []time.Duration{time.Millisecond}}
		var out struct {
			Error string `json:"error"`
		}
		if err := c.Run(context.Background(), Call{Agent: "linear_mcp", Server: "claude.ai Linear", Prompt: "x", Schema: map[string]any{}}, &out); err != nil || out.Error == "" || calls(t, count) != 1 {
			t.Errorf("err = %v, out = %+v, chamadas = %d", err, out, calls(t, count))
		}
	})
}

func TestToolsMissingRecognizesTheMessagesSeen(t *testing.T) {
	for _, msg := range []string{
		"Não foi possível executar a tarefa: as ferramentas do Linear (mcp__claude_ai_Linear__list_teams, list_issue_statuses) não estão disponíveis nesta sessão.",
		"Ferramenta mcp__claude_ai_Linear__list_issues indisponível nesta sessão: ela não está carregada entre as ferramentas disponíveis",
		"A ferramenta mcp__claude_ai_Linear__save_issue não está disponível nesta sessão. Não há schema carregado para ela",
		"A ferramenta mcp__claude_ai_Linear__list_issues não está disponível nesta sessão. Só a ferramenta StructuredOutput foi carregada",
		"Tool mcp__claude_ai_Linear__save_issue is not available in this session",
	} {
		if !ToolsMissing(msg, "claude.ai Linear") {
			t.Errorf("não reconheceu: %s", msg)
		}
	}
	for _, msg := range []string{"Entity not found: Issue", "A issue não está disponível para este time", "mcp__claude_ai_Slack__x não está disponível"} {
		if ToolsMissing(msg, "claude.ai Linear") {
			t.Errorf("reconheceu demais: %s", msg)
		}
	}
}
