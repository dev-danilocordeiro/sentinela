package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude é um script no lugar do `claude`: responde conforme a entrada (stdin) e
// grava argumentos e ambiente para o teste conferir. Saída no formato real de
// `claude -p --output-format json` (campos usados pelo provider).
const fakeClaude = `#!/bin/sh
dir=$(dirname "$0")
input=$(cat)
printf '%s\n' "$@" > "$dir/args"
env > "$dir/env"
ok='{"type":"result","subtype":"success","is_error":false,"result":"","stop_reason":"tool_use","safety_stops":0,"session_id":"s-1","structured_output":{"ok":true},"usage":{"input_tokens":12,"output_tokens":34,"cache_creation_input_tokens":100,"cache_read_input_tokens":5,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":100}},"modelUsage":{"claude-haiku-5-5":{}}}'
bad='{"type":"result","subtype":"success","is_error":false,"result":"","stop_reason":"tool_use","session_id":"s-2","structured_output":{"ok":"talvez"},"usage":{"input_tokens":1,"output_tokens":1},"modelUsage":{"claude-haiku-5-5":{}}}'
case "$input" in
  *teimoso*) echo "$bad" ;;
  *uma-vez*) case "$input" in *"não passou na validação"*) echo "$ok" ;; *) echo "$bad" ;; esac ;;
  *recusa*) echo '{"type":"result","subtype":"success","is_error":false,"result":"","stop_reason":"refusal","safety_stops":1,"session_id":"s-3","usage":{"input_tokens":1,"output_tokens":0},"modelUsage":{}}' ;;
  *limite*) echo '{"type":"result","subtype":"success","is_error":true,"result":"You'"'"'ve hit your session limit · resets 3:40am (America/Sao_Paulo)","stop_reason":"stop_sequence","session_id":"s-5","usage":{}}'; exit 1 ;;
  *quebra*) echo '{"type":"result","subtype":"error_during_execution","is_error":true,"result":"falha interna do CLI","session_id":"s-4","usage":{}}'; exit 1 ;;
  *) echo "$ok" ;;
esac
`

type memRec struct{ recs []CallRecord }

func (m *memRec) RecordCall(_ context.Context, r CallRecord) error {
	m.recs = append(m.recs, r)
	return nil
}

var okSchema = map[string]any{
	"type":                 "object",
	"properties":           map[string]any{"ok": map[string]any{"type": "boolean"}},
	"required":             []string{"ok"},
	"additionalProperties": false,
}

func newFakeCC(t *testing.T, maxCalls int) (*ClaudeCode, *memRec, string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(fakeClaude), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
	rec := &memRec{}
	cc, err := NewClaudeCode(ClaudeCodeOptions{
		Binary: bin, Concurrency: 2, WorkDir: dir, Recorder: rec, Budget: NewCallBudget(maxCalls), RunID: 9,
		Environ: []string{"PATH=" + os.Getenv("PATH"), "ANTHROPIC_API_KEY=sk-ant-segredo", "ANTHROPIC_WORKSPACE_ID=w", "HOME=" + dir,
			"GITHUB_TOKEN=ghp_segredo", "SLACK_BOT_TOKEN=xoxb-segredo", "LINEAR_API_KEY=lin_api_segredo", "DATABASE_URL=postgres://u:senha@h/db", "LANG=pt_BR.UTF-8"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cc, rec, dir
}

func ccRequest(user string) Request {
	return Request{Agent: "triager", PromptVersion: "triager@1", Model: "claude-haiku-5-5", System: "sistema", User: user, MaxTokens: 100, Schema: okSchema, Effort: "low"}
}

func TestClaudeCodeReturnsStructuredOutputUsingTheSubscription(t *testing.T) {
	cc, rec, dir := newFakeCC(t, 10)
	resp, err := cc.Call(context.Background(), ccRequest("pacote normal"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"ok":true}` || resp.Model != "claude-haiku-5-5" {
		t.Errorf("resp = %+v", resp)
	}
	if len(rec.recs) != 1 {
		t.Fatalf("llm_calls = %d", len(rec.recs))
	}
	r := rec.recs[0]
	if r.Provider != "claude_code" || r.Mode != "cli" || r.Cost != 0 || r.RunID != 9 ||
		r.Usage.InputTokens != 12 || r.Usage.OutputTokens != 34 || r.Usage.CacheWrite1hTokens != 100 || r.Usage.CacheReadTokens != 5 {
		t.Errorf("registro = %+v", r)
	}
	env, _ := os.ReadFile(filepath.Join(dir, "env")) //nolint:gosec // arquivo do teste
	if strings.Contains(string(env), "ANTHROPIC_") {
		t.Errorf("o CLI não pode receber variáveis ANTHROPIC_* (cobraria a API):\n%s", env)
	}
	args, _ := os.ReadFile(filepath.Join(dir, "args")) //nolint:gosec // arquivo do teste
	for _, want := range []string{"-p", "--output-format\njson", "--model\nclaude-haiku-5-5", "--json-schema", "--system-prompt\nsistema", "--effort\nlow", "--tools\n\n", "--no-session-persistence"} {
		if !strings.Contains(string(args), want) {
			t.Errorf("argumentos sem %q:\n%s", want, args)
		}
	}
	if cc.Budget().Calls() != 1 {
		t.Errorf("chamadas = %d", cc.Budget().Calls())
	}
}

func TestClaudeCodeRetriesOnceWithTheValidationError(t *testing.T) {
	cc, rec, _ := newFakeCC(t, 10)
	resp, err := cc.Call(context.Background(), ccRequest("falha uma-vez"))
	if err != nil {
		t.Fatal(err)
	}
	if resp.Text != `{"ok":true}` || len(rec.recs) != 2 || !strings.Contains(rec.recs[0].Error, "fora do schema") || rec.recs[1].Error != "" {
		t.Errorf("resp=%+v registros=%+v", resp, rec.recs)
	}

	_, err = cc.Call(context.Background(), ccRequest("teimoso"))
	if !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("esperava ErrInvalidOutput, veio %v", err)
	}
	if cc.Budget().Calls() != 4 {
		t.Errorf("cada tentativa conta no limite: %d", cc.Budget().Calls())
	}
}

func TestClaudeCodeMapsRefusalAndCLIErrors(t *testing.T) {
	cc, rec, _ := newFakeCC(t, 10)
	resp, err := cc.Call(context.Background(), ccRequest("recusa"))
	if !errors.Is(err, ErrBadStop) || resp.StopReason != "refusal" {
		t.Fatalf("recusa: resp=%+v err=%v", resp, err)
	}
	if _, err := cc.Call(context.Background(), ccRequest("quebra")); err == nil || errors.Is(err, ErrUsageLimit) || !strings.Contains(err.Error(), "falha interna do CLI") {
		t.Fatalf("erro do CLI: %v", err)
	}
	if last := rec.recs[len(rec.recs)-1]; last.Status != "errored" {
		t.Errorf("erro deveria ser gravado: %+v", last)
	}
}

func TestClaudeCodeBatchStopsAtTheCallLimit(t *testing.T) {
	cc, _, _ := newFakeCC(t, 3)
	var items []BatchItem
	for _, id := range []string{"a", "b", "c", "d", "e"} {
		items = append(items, BatchItem{CustomID: id, Request: ccRequest("pacote " + id)})
	}
	results, err := cc.Batch(context.Background(), items)
	if !errors.Is(err, ErrCallLimitExceeded) || !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("esperava estouro do limite de chamadas, veio %v", err)
	}
	if len(results) > 3 || cc.Budget().Calls() != 3 {
		t.Errorf("resultados=%d chamadas=%d", len(results), cc.Budget().Calls())
	}
}

// Mensagem real do CLI quando a assinatura bate no limite de sessão (rodada 6 do app-java-angular):
// a rodada tem de parar, não pular cada item.
func TestClaudeCodeUsageLimitStopsTheBatch(t *testing.T) {
	cc, _, _ := newFakeCC(t, 50)
	var items []BatchItem
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		items = append(items, BatchItem{CustomID: id, Request: ccRequest("limite " + id)})
	}
	results, err := cc.Batch(context.Background(), items)
	if !errors.Is(err, ErrUsageLimit) || !errors.Is(err, ErrBudgetExceeded) || !strings.Contains(err.Error(), "resets 3:40am") {
		t.Fatalf("esperava ErrUsageLimit, veio %v", err)
	}
	if len(results) != 0 || cc.Budget().Calls() > 3 {
		t.Errorf("resultados=%d chamadas=%d: deveria parar de despachar", len(results), cc.Budget().Calls())
	}
}

func TestReplayServesRecordedResponsesWithoutCallingModels(t *testing.T) {
	dir := t.TempDir()
	cc, _, _ := newFakeCC(t, 10)
	cc.o.RecordDir = dir
	req := ccRequest("pacote gravado")
	if _, err := cc.Call(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	rec := &memRec{}
	rp := &Replay{Dir: dir, Recorder: rec, B: NewCallBudget(10)}
	resp, err := rp.Call(context.Background(), req)
	if err != nil || resp.Text != `{"ok":true}` || rec.recs[0].Provider != "replay" {
		t.Fatalf("replay: %+v %v %+v", resp, err, rec.recs)
	}
	if _, err := rp.Call(context.Background(), ccRequest("nunca gravado")); !errors.Is(err, ErrNoRecording) {
		t.Errorf("esperava ErrNoRecording, veio %v", err)
	}
}

func TestSafetyStopsIsReadAsCountOrList(t *testing.T) {
	for raw, want := range map[string]bool{"": false, "0": false, "2": true, "[]": false, `[{"category":"cyber"}]`: true, "null": false} {
		if got := safetyStopped([]byte(raw)); got != want {
			t.Errorf("safety_stops=%q: %v, quer %v", raw, got, want)
		}
	}
}

// Até a Fase 4 o provider rodava em .sentinela/claude-cwd, dentro do projeto, e cada chamada da
// descoberta recebia o CLAUDE.md do Sentinela. Diretório abaixo de um CLAUDE.md é recusado.
func TestClaudeCodeRefusesAWorkDirBelowAnotherProjectsInstructions(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "projeto", ".sentinela", "claude-cwd")
	if err := os.MkdirAll(cwd, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaudeCode(ClaudeCodeOptions{WorkDir: cwd, Recorder: &memRec{}, Budget: NewCallBudget(1)}); err != nil {
		t.Fatalf("sem CLAUDE.md acima: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "projeto", "CLAUDE.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewClaudeCode(ClaudeCodeOptions{WorkDir: cwd, Recorder: &memRec{}, Budget: NewCallBudget(1)}); !errors.Is(err, ErrForeignInstructions) {
		t.Fatalf("esperava ErrForeignInstructions, veio %v", err)
	}
}

// D-17 da revisão de prontidão: o claude -p da descoberta recebe a mesma lista fechada de ambiente
// do job, não o ambiente do Sentinela menos ANTHROPIC_*: os tokens do GitHub, do Slack e do Linear
// e o DATABASE_URL não chegam ao processo.
func TestClaudeCodeGetsOnlyTheClosedEnvironment(t *testing.T) {
	cc, _, dir := newFakeCC(t, 10)
	if _, err := cc.Call(context.Background(), ccRequest("pacote normal")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "env"))
	if err != nil {
		t.Fatal(err)
	}
	env := string(raw)
	for _, secret := range []string{"ANTHROPIC_", "GITHUB_TOKEN", "SLACK_BOT_TOKEN", "LINEAR_API_KEY", "DATABASE_URL"} {
		if strings.Contains(env, secret) {
			t.Errorf("%s chegou ao claude -p da descoberta", secret)
		}
	}
	for _, need := range []string{"PATH=", "HOME=", "LANG="} {
		if !strings.Contains(env, need) {
			t.Errorf("%s não chegou (precisa para rodar e para o login)", need)
		}
	}
	for _, kv := range strings.Split(strings.TrimSpace(env), "\n") {
		k, _, _ := strings.Cut(kv, "=")
		if !EnvAllowed(k) && k != "PWD" && k != "SHLVL" && k != "_" && k != "OLDPWD" {
			t.Errorf("variável fora da lista chegou ao claude -p: %s", k)
		}
	}
}
