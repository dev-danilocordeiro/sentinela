package executor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testdata/claude-stream-probe.jsonl é o stream-json real de `claude -p` (2.1.296) com um plugin
// de teste: skill do plugin, subagente com model próprio e retomada do mesmo subagente.
func TestParseStreamReadsSessionCostAndModelsFromARealStream(t *testing.T) {
	f, err := os.Open("testdata/claude-stream-probe.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	res, err := ParseStream(f)
	if err != nil {
		t.Fatal(err)
	}
	if res.SessionID == "" || res.Subtype != "success" || res.IsError || res.NumTurns != 5 || res.CostUSD <= 0 {
		t.Errorf("resultado = %+v", res)
	}
	if u, ok := res.Models["claude-haiku-5-5"]; !ok || u.OutputTokens == 0 {
		t.Errorf("uso por modelo = %+v", res.Models)
	}
	if _, err := ParseStream(strings.NewReader(`{"type":"system","subtype":"init","session_id":"x"}`)); err == nil {
		t.Error("stream sem evento result deveria falhar")
	}
}

// fakeClaude escreve um binário que imita o stream do Claude Code: o apiKeySource do init diz se
// ANTHROPIC_API_KEY chegou ao processo, como no CLI real.
func fakeClaude(t *testing.T, alwaysKey bool) string {
	t.Helper()
	src := `src=none; [ -n "$ANTHROPIC_API_KEY" ] && src=ANTHROPIC_API_KEY`
	if alwaysKey {
		src = `src=ANTHROPIC_API_KEY`
	}
	bin := filepath.Join(t.TempDir(), "claude")
	script := "#!/bin/sh\n" + src + "\n" +
		`echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s1\",\"apiKeySource\":\"$src\"}"` + "\n" +
		`sleep 0.2; echo "{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"num_turns\":1,\"total_cost_usd\":0.01}"` + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
	return bin
}

// O primeiro job real (EX-12) rodou com a ANTHROPIC_API_KEY do .env e consumiu crédito da API.
// O runner tira ANTHROPIC_* do ambiente e, se a sessão ainda assim começar com chave, mata o processo.
func TestClaudeRunnerNeverUsesTheAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-teste")
	t.Setenv("ANTHROPIC_BASE_URL", "http://exemplo")
	res, err := ClaudeRunner{Binary: fakeClaude(t, false)}.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute})
	if err != nil || res.Subtype != "success" {
		t.Fatalf("com a chave removida a sessão usa a assinatura: %+v %v", res, err)
	}
	_, err = ClaudeRunner{Binary: fakeClaude(t, true)}.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute})
	if !errors.Is(err, ErrAPIKey) {
		t.Fatalf("sessão com chave de API deveria ser morta: %v", err)
	}
}

// scriptClaude escreve um binário falso com o corpo dado (sh).
func scriptClaude(t *testing.T, body string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
	return bin
}

const (
	initLine   = `echo '{"type":"system","subtype":"init","session_id":"s1","apiKeySource":"none"}'`
	resultLine = `echo '{"type":"result","subtype":"success","is_error":false,"num_turns":1,"total_cost_usd":0.01}'`
)

// Sessão sem saída por idle_timeout: o processo é encerrado e a falha é idle_timeout (retomável).
func TestIdleTimeoutKillsASilentSession(t *testing.T) {
	bin := scriptClaude(t, initLine+"\nsleep 30\n"+resultLine)
	start := time.Now()
	_, err := ClaudeRunner{Binary: bin}.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute, IdleTimeout: 300 * time.Millisecond})
	if !errors.Is(err, ErrIdleTimeout) || time.Since(start) > 10*time.Second {
		t.Fatalf("erro = %v depois de %s", err, time.Since(start))
	}
	if k := classifyFailure(err, RunResult{}); k != FailIdle || !Resumable(k) {
		t.Errorf("tipo = %s", k)
	}
	if k := ClassifyFailureText(err.Error()); k != FailIdle {
		t.Errorf("tipo pela mensagem = %s", k)
	}
}

// Saída regular, mesmo devagar, não dispara o idle.
func TestIdleTimeoutResetsOnEveryLine(t *testing.T) {
	bin := scriptClaude(t, initLine+"\nfor i in 1 2 3 4 5; do sleep 0.15; echo '{}'; done\n"+resultLine)
	res, err := ClaudeRunner{Binary: bin}.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute, IdleTimeout: 400 * time.Millisecond})
	if err != nil || res.Subtype != "success" || res.Warning != "" {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

// O agente sinalizou o fim, mas o processo não sai: completion_timeout encerra com aviso e o
// resultado vale. Também com um filho, no grupo ou fora dele (setsid), segurando o stdout.
func TestCompletionTimeoutEndsAProcessThatDoesNotExit(t *testing.T) {
	cases := map[string]string{
		"processo parado":           initLine + "\n" + resultLine + "\nsleep 30",
		"filho segura o stdout":     initLine + "\n" + resultLine + "\nsleep 30 &\nexit 0",
		"neto fora do grupo segura": initLine + "\n" + resultLine + "\nsetsid sleep 30 &\nexit 0",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(body, "setsid") {
				if _, err := exec.LookPath("setsid"); err != nil {
					t.Skip("setsid indisponível")
				}
			}
			start := time.Now()
			res, err := ClaudeRunner{Binary: scriptClaude(t, body)}.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute, IdleTimeout: time.Minute, CompletionTimeout: 300 * time.Millisecond})
			if err != nil || res.Subtype != "success" {
				t.Fatalf("res = %+v, %v", res, err)
			}
			if !strings.Contains(res.Warning, "completion_timeout") || time.Since(start) > 15*time.Second {
				t.Errorf("aviso %q depois de %s", res.Warning, time.Since(start))
			}
		})
	}
}
