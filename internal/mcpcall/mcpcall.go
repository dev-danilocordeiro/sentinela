// Package mcpcall chama um servidor MCP configurado no Claude Code do usuário por `claude -p`,
// com só as ferramentas desse servidor permitidas, e devolve a resposta estruturada (ADR 0039).
// É o meio de transporte dos adaptadores linear_mcp e slack_mcp: o Sentinela não guarda chave
// nenhuma, e cada chamada gasta a cota da assinatura e vai para llm_calls.
//
// Flags conferidas em `claude --help` e em chamadas reais (2026-10-11): os conectores do
// claude.ai entram sob demanda pela ToolSearch, então ela é a única ferramenta embutida liberada
// (--tools ToolSearch); --allowedTools mcp__<servidor> dá permissão só às ferramentas daquele
// servidor; as outras são negadas no modo não interativo.
package mcpcall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// Call é uma chamada: o prompt diz qual ferramenta usar e com que argumentos; a resposta segue o
// schema.
type Call struct {
	Agent string // linear_mcp | slack_mcp (agente em llm_calls)
	// Op é a operação (poll, get, detail, create, comment...): vai para llm_calls.custom_id e
	// permite contar ciclos de leitura e chamadas por ciclo no sentinela status.
	Op     string
	Server string // nome do servidor MCP no Claude Code, ex.: "claude.ai Linear"
	Prompt string
	Schema map[string]any
	// Steps são os passos que o Prompt descreve, para conferência (testes); o CLI usa só o Prompt.
	Steps []Step
}

// Runner roda uma chamada e decodifica a resposta estruturada em out.
type Runner interface {
	Run(ctx context.Context, c Call, out any) error
}

// CLI é o Runner real.
type CLI struct {
	Binary   string // padrão claude
	Model    string
	WorkDir  string // fora de qualquer projeto (sem CLAUDE.md acima)
	Timeout  time.Duration
	Recorder llm.Recorder // nil não grava
	Environ  []string     // padrão os.Environ(), filtrado por llm.ClosedEnv (sem ANTHROPIC_* nem segredos)
	// RetryWaits são as esperas antes de cada nova tentativa quando a sessão começa sem as
	// ferramentas do conector (ToolsMissing); nil usa DefaultRetryWaits.
	RetryWaits []time.Duration
}

// DefaultRetryWaits: os conectores do claude.ai às vezes terminam de carregar depois que a sessão
// começa, e a chamada seguinte, segundos depois, já os encontra (visto em 2026-10-17).
var DefaultRetryWaits = []time.Duration{10 * time.Second, 30 * time.Second}

// ErrToolsMissing: a sessão não teve as ferramentas do servidor em nenhuma das tentativas.
var ErrToolsMissing = errors.New("as ferramentas do conector não carregaram na sessão do claude -p")

var toolsMissingRe = regexp.MustCompile(`(?i)(n[ãa]o (est[áa]|est[ãa]o|estava|estavam) (dispon[íi]ve|carregad)|indispon[íi]ve|n[ãa]o (foi|foram) carregad|not available|unavailable|not loaded)`)

// ToolsMissing reconhece a resposta de uma sessão que começou sem as ferramentas do servidor: a
// mensagem cita uma ferramenta dele (mcp__<servidor>__...) e diz que ela não está disponível ou
// carregada. Um erro devolvido pela própria ferramenta não cita o prefixo e não conta.
func ToolsMissing(msg, server string) bool {
	return strings.Contains(msg, ToolPrefix(server)+"__") && toolsMissingRe.MatchString(msg)
}

var nonTool = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// ToolPrefix é o prefixo das ferramentas do servidor no Claude Code: "claude.ai Linear" vira
// "mcp__claude_ai_Linear".
func ToolPrefix(server string) string { return "mcp__" + nonTool.ReplaceAllString(server, "_") }

// Args monta a linha de comando do claude para a chamada.
func (c *CLI) Args(call Call) ([]string, error) {
	schema, err := json.Marshal(call.Schema)
	if err != nil {
		return nil, err
	}
	return []string{"-p", "--output-format", "json", "--model", c.Model,
		"--tools", "ToolSearch", "--allowedTools", ToolPrefix(call.Server),
		"--no-session-persistence", "--json-schema", string(schema)}, nil
}

type result struct {
	IsError          bool            `json:"is_error"`
	Subtype          string          `json:"subtype"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	SessionID        string          `json:"session_id"`
	StopReason       string          `json:"stop_reason"`
	PermissionDenial []struct {
		ToolName string `json:"tool_name"`
	} `json:"permission_denials"`
	Usage struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// ErrMCP: a chamada não chegou a uma resposta utilizável (servidor fora, ferramenta negada,
// erro do modelo). A mensagem diz o que aconteceu.
var ErrMCP = errors.New("chamada ao MCP falhou")

// Run roda a chamada. Se a sessão começar sem as ferramentas do servidor, espera e tenta de novo
// (RetryWaits) antes de devolver ErrToolsMissing; cada tentativa vai para llm_calls.
func (c *CLI) Run(ctx context.Context, call Call, out any) error {
	waits := c.RetryWaits
	if waits == nil {
		waits = DefaultRetryWaits
	}
	for attempt := 0; ; attempt++ {
		raw, missing, err := c.runOnce(ctx, call)
		if err != nil {
			return err
		}
		if missing == "" {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("%w: resposta fora do formato: %w", ErrMCP, err)
			}
			return nil
		}
		if attempt >= len(waits) {
			return fmt.Errorf("%w: %w (%d tentativas): %s", ErrMCP, ErrToolsMissing, attempt+1, truncate(missing, 300))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(waits[attempt]):
		}
	}
}

// runOnce roda uma tentativa e devolve a resposta estruturada crua; missing é a mensagem quando a
// sessão não teve as ferramentas do servidor.
func (c *CLI) runOnce(ctx context.Context, call Call) (raw json.RawMessage, missing string, err error) {
	args, err := c.Args(call)
	if err != nil {
		return nil, "", err
	}
	bin := c.Binary
	if bin == "" {
		bin = "claude"
	}
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	env := c.Environ
	if env == nil {
		env = os.Environ()
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...) //nolint:gosec // binário da config
	cmd.Dir = c.WorkDir
	cmd.Env = llm.ClosedEnv(env) // mesma lista fechada do job (D-17)
	cmd.Stdin = strings.NewReader(call.Prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	runErr := cmd.Run()
	rec := llm.CallRecord{Provider: "claude_code", Agent: call.Agent, Model: c.Model, PromptVersion: "mcp", Mode: "cli", CustomID: call.Op, Latency: time.Since(start), Status: "succeeded"}
	var r result
	decodeErr := json.Unmarshal(stdout.Bytes(), &r)
	rec.MessageID, rec.StopReason = r.SessionID, r.StopReason
	rec.Usage = llm.Usage{InputTokens: r.Usage.InputTokens, OutputTokens: r.Usage.OutputTokens, CacheReadTokens: r.Usage.CacheReadInputTokens, CacheWrite5mTokens: r.Usage.CacheCreationInputTokens}
	var fail error
	switch {
	case decodeErr != nil && runErr != nil:
		fail = fmt.Errorf("%w: claude -p: %w: %s", ErrMCP, runErr, truncate(strings.TrimSpace(stderr.String()), 300))
	case decodeErr != nil:
		fail = fmt.Errorf("%w: saída do claude -p não é JSON: %w", ErrMCP, decodeErr)
	case r.IsError:
		fail = fmt.Errorf("%w (%s): %s", ErrMCP, r.Subtype, truncate(r.Result, 300))
	case len(r.StructuredOutput) == 0 || string(r.StructuredOutput) == "null":
		fail = fmt.Errorf("%w: sem resposta estruturada: %s", ErrMCP, truncate(r.Result, 300))
	}
	if fail == nil {
		var peek struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(r.StructuredOutput, &peek); err != nil {
			fail = fmt.Errorf("%w: resposta fora do formato: %w", ErrMCP, err)
		} else if ToolsMissing(peek.Error, call.Server) {
			missing = peek.Error
		}
	} else if ToolsMissing(r.Result, call.Server) {
		missing, fail = r.Result, nil
	}
	if fail != nil && len(r.PermissionDenial) > 0 {
		var tools []string
		for _, d := range r.PermissionDenial {
			tools = append(tools, d.ToolName)
		}
		fail = fmt.Errorf("%w (ferramentas negadas: %s)", fail, strings.Join(tools, ", "))
	}
	switch {
	case fail != nil:
		rec.Status, rec.Error = "errored", fail.Error()
	case missing != "":
		rec.Status, rec.Error = "errored", ErrToolsMissing.Error()+": "+truncate(missing, 300)
	}
	if c.Recorder != nil {
		if err := c.Recorder.RecordCall(context.WithoutCancel(ctx), rec); err != nil {
			return nil, "", errors.Join(fail, err)
		}
	}
	return r.StructuredOutput, missing, fail
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Prompt monta o pedido de uma chamada: a ferramenta, os argumentos exatos em JSON e o que
// devolver. Argumentos exatos tiram do modelo a decisão do que mandar: ele só executa.
func Prompt(task string, steps ...Step) string {
	var b strings.Builder
	b.WriteString("Você é um executor de chamadas de ferramenta para o Sentinela. Faça exatamente os passos abaixo, sem chamar outras ferramentas nem mudar argumentos, e responda só com o JSON do schema.\n\n")
	b.WriteString("Tarefa: " + task + "\n")
	for i, s := range steps {
		args, _ := json.Marshal(s.Args)
		fmt.Fprintf(&b, "\n%d. Ferramenta `%s` com os argumentos: %s", i+1, s.Tool, args)
		if s.Note != "" {
			b.WriteString("\n   " + s.Note)
		}
	}
	b.WriteString("\n\nSe uma ferramenta falhar, responda com o campo error preenchido com a mensagem dela.\n")
	return b.String()
}

// Step é um passo do prompt.
type Step struct {
	Tool string
	Args any
	Note string
}
