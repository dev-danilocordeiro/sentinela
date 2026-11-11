package llm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

// ErrInvalidOutput indica saída do modelo fora do JSON Schema mesmo após a nova tentativa.
var ErrInvalidOutput = errors.New("saída do modelo fora do schema")

// ErrUsageLimit é o limite de uso da assinatura (ex.: "You've hit your session limit ·
// resets 3:40am"). Embrulha ErrBudgetExceeded: a rodada para de despachar e termina como
// aborted_budget, em vez de pular cada item e perder os achados em silêncio.
var ErrUsageLimit = fmt.Errorf("%w: limite de uso da assinatura do Claude Code", ErrBudgetExceeded)

// UsageLimited reconhece a mensagem de limite de uso do CLI (também usada pelo executor).
func UsageLimited(msg string) bool { return usageLimited(msg) }

// hitLimitRe matches any "You've hit your <kind> limit" from the CLI, including kinds not listed
// below ("5-hour limit" in Claude Code 2.1.322).
var hitLimitRe = regexp.MustCompile(`(?i)hit your [a-z0-9 -]{0,30}limit`)

// usageLimited reconhece a mensagem de limite de uso do CLI: o de sessão e o semanal da
// assinatura e o rate limit da API (429, rate_limit_error).
func usageLimited(msg string) bool {
	if hitLimitRe.MatchString(msg) {
		return true
	}
	m := strings.ToLower(msg)
	for _, k := range []string{"session limit", "usage limit", "weekly limit", "hit your limit", "limit reached", "rate limit", "rate_limit"} {
		if strings.Contains(m, k) {
			return true
		}
	}
	return false
}

// ClaudeCodeOptions configura o provider claude_code.
type ClaudeCodeOptions struct {
	Binary      string        // padrão "claude"
	Concurrency int           // chamadas simultâneas em Batch; padrão 2
	Timeout     time.Duration // por chamada; padrão 10 min
	WorkDir     string        // diretório vazio onde o CLI roda (evita CLAUDE.md do projeto); padrão os.TempDir()
	RecordDir   string        // se definido, grava respostas válidas para o provider replay
	Recorder    Recorder
	// Prices calcula o custo equivalente quando o resultado do claude -p não traz total_cost_usd
	// (D-13 da revisão de prontidão); nil deixa o custo zero.
	Prices *PriceTable
	Budget *Budget
	RunID  int64
	Tracer trace.Tracer
	Now    func() time.Time
	// Environ é o ambiente base do subprocesso (padrão os.Environ()), filtrado por ClosedEnv.
	Environ []string
}

// ClaudeCode chama o Claude Code CLI em modo não interativo (`claude -p`) pela assinatura
// do usuário. Flags conferidas em `claude --help` (2.1.296) e numa chamada real:
// --output-format json devolve um objeto com structured_output, usage, is_error,
// stop_reason e safety_stops; --json-schema pede saída estruturada (ADR 0013).
type ClaudeCode struct {
	o       ClaudeCodeOptions
	mu      sync.Mutex
	schemas map[string]*jsonschema.Schema
}

var _ Caller = (*ClaudeCode)(nil)

func NewClaudeCode(o ClaudeCodeOptions) (*ClaudeCode, error) {
	if o.Recorder == nil || o.Budget == nil {
		return nil, errors.New("claude_code: Recorder e Budget são obrigatórios")
	}
	if o.Binary == "" {
		o.Binary = "claude"
	}
	if o.Concurrency < 1 {
		o.Concurrency = 2
	}
	if o.Timeout <= 0 {
		o.Timeout = 10 * time.Minute
	}
	if o.WorkDir == "" {
		o.WorkDir = os.TempDir()
	}
	if found := InstructionsAbove(o.WorkDir, true); len(found) > 0 {
		return nil, fmt.Errorf("%w: o diretório de trabalho %s carregaria %s", ErrForeignInstructions, o.WorkDir, strings.Join(found, ", "))
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Tracer == nil {
		o.Tracer = noop.NewTracerProvider().Tracer(telemetry.TracerName)
	}
	if o.Environ == nil {
		o.Environ = os.Environ()
	}
	return &ClaudeCode{o: o, schemas: map[string]*jsonschema.Schema{}}, nil
}

func (c *ClaudeCode) Provider() string { return "claude_code" }
func (c *ClaudeCode) Budget() *Budget  { return c.o.Budget }

// Call faz a chamada e valida a saída contra o schema; se falhar, tenta mais uma vez
// com o erro de validação no prompt. Cada tentativa conta no limite e vai para llm_calls.
func (c *ClaudeCode) Call(ctx context.Context, req Request) (Response, error) {
	if err := validate(req); err != nil {
		return Response{}, err
	}
	var schema *jsonschema.Schema
	var schemaJSON []byte
	if req.Schema != nil {
		var err error
		if schemaJSON, err = json.Marshal(req.Schema); err != nil {
			return Response{}, err
		}
		if schema, err = c.compile(schemaJSON); err != nil {
			return Response{}, err
		}
	}
	user := req.User
	var resp Response
	for attempt := 1; attempt <= 2; attempt++ {
		if err := c.o.Budget.ReserveCall(); err != nil {
			return resp, err
		}
		var err error
		resp, err = c.once(ctx, req, user, schemaJSON, attempt, schema)
		if err == nil {
			c.record(req, resp)
			return resp, nil
		}
		var verr *validationError
		if !errors.As(err, &verr) {
			return resp, err
		}
		if attempt == 2 {
			return resp, fmt.Errorf("%w: %s", ErrInvalidOutput, verr.msg)
		}
		user = req.User + "\n\nSua resposta anterior não passou na validação do JSON Schema:\n" + verr.msg +
			"\nResponda de novo, só com o JSON válido segundo o schema."
	}
	return resp, nil
}

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }

// cliResult é o objeto de `claude -p --output-format json`.
type cliResult struct {
	Type             string          `json:"type"`
	Subtype          string          `json:"subtype"`
	IsError          bool            `json:"is_error"`
	Result           string          `json:"result"`
	StructuredOutput json.RawMessage `json:"structured_output"`
	StopReason       string          `json:"stop_reason"`
	SafetyStops      json.RawMessage `json:"safety_stops"`
	SessionID        string          `json:"session_id"`
	Usage            struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		CacheCreation            struct {
			Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
			Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	} `json:"usage"`
	ModelUsage map[string]json.RawMessage `json:"modelUsage"`
	// TotalCostUSD é o custo equivalente em preço de lista que o Claude Code informa.
	TotalCostUSD float64 `json:"total_cost_usd"`
}

func (c *ClaudeCode) once(ctx context.Context, req Request, user string, schemaJSON []byte, attempt int, schema *jsonschema.Schema) (Response, error) {
	ctx, span := c.o.Tracer.Start(ctx, "llm.call", trace.WithAttributes(
		telemetry.AttrAgent.String(req.Agent), telemetry.AttrModel.String(req.Model),
		telemetry.AttrPromptVersion.String(req.PromptVersion), telemetry.AttrMode.String("cli"),
	))
	defer span.End()

	args := []string{"-p", "--output-format", "json", "--model", req.Model,
		"--tools", "", "--strict-mcp-config", "--no-session-persistence"}
	if req.System != "" {
		args = append(args, "--system-prompt", req.System)
	}
	if req.Effort != "" {
		args = append(args, "--effort", req.Effort)
	}
	if schemaJSON != nil {
		args = append(args, "--json-schema", string(schemaJSON))
	}
	cctx, cancel := context.WithTimeout(ctx, c.o.Timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, c.o.Binary, args...) //nolint:gosec // binário vem da config do operador
	cmd.Dir = c.o.WorkDir
	// Lista fechada, como no job (D-17): sem ANTHROPIC_* (a sessão usaria crédito da API em vez
	// da assinatura) e sem os segredos do Sentinela.
	cmd.Env = ClosedEnv(c.o.Environ)
	cmd.Stdin = strings.NewReader(user) // o prompt vai por stdin: argv tem limite de 128 KB por argumento
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := c.o.Now()
	runErr := cmd.Run()
	latency := c.o.Now().Sub(start)

	rec := CallRecord{
		RunID: c.o.RunID, Provider: c.Provider(), Agent: req.Agent, Model: req.Model,
		PromptVersion: req.PromptVersion, Mode: "cli", Latency: latency, Status: "succeeded",
	}
	if attempt > 1 {
		rec.CustomID = fmt.Sprintf("tentativa-%d", attempt)
	}
	fail := func(err error) (Response, error) {
		rec.Status, rec.Error = "errored", err.Error()
		span.SetStatus(codes.Error, rec.Error)
		if rerr := c.o.Recorder.RecordCall(ctx, rec); rerr != nil {
			return Response{}, errors.Join(err, rerr)
		}
		return Response{}, err
	}

	if dir := os.Getenv("SENTINELA_CLAUDE_DEBUG_DIR"); dir != "" {
		// Diagnóstico: guarda a saída bruta do CLI de cada chamada.
		_ = os.MkdirAll(dir, 0o750) //nolint:gosec // diretório escolhido pelo operador para diagnóstico
		name := filepath.Join(dir, fmt.Sprintf("%s-%d-%d.json", filepath.Base(req.Agent), start.UnixNano(), attempt))
		_ = os.WriteFile(name, stdout.Bytes(), 0o600) //nolint:gosec // idem
	}
	var out cliResult
	if jerr := json.Unmarshal(stdout.Bytes(), &out); jerr != nil {
		msg := strings.TrimSpace(stderr.String())
		if runErr != nil {
			return fail(fmt.Errorf("claude -p: %w: %s", runErr, truncate(msg, 300)))
		}
		return fail(fmt.Errorf("claude -p: saída não é JSON: %w", jerr))
	}
	rec.MessageID = out.SessionID
	rec.StopReason = out.StopReason
	rec.Usage = Usage{
		InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens,
		CacheReadTokens:    out.Usage.CacheReadInputTokens,
		CacheWrite5mTokens: out.Usage.CacheCreation.Ephemeral5m, CacheWrite1hTokens: out.Usage.CacheCreation.Ephemeral1h,
	}
	if detailed := rec.Usage.CacheWrite5mTokens + rec.Usage.CacheWrite1hTokens; detailed < out.Usage.CacheCreationInputTokens {
		rec.Usage.CacheWrite5mTokens += out.Usage.CacheCreationInputTokens - detailed
	}
	if models := keys(out.ModelUsage); len(models) > 0 {
		rec.Model = models[0]
	}
	// Custo equivalente: o que o Claude Code informa; sem ele, a tabela de preços, marcado como
	// estimado no custom_id (D-13).
	switch {
	case out.TotalCostUSD > 0:
		rec.Cost = USD(out.TotalCostUSD)
	case c.o.Prices != nil:
		if cost, err := c.o.Prices.Cost(rec.Model, rec.Usage, false); err == nil {
			rec.Cost = cost
			rec.CustomID = strings.TrimPrefix(rec.CustomID+" custo-estimado", " ")
		}
	}
	text := out.Result
	if len(out.StructuredOutput) > 0 && string(out.StructuredOutput) != "null" {
		text = string(out.StructuredOutput)
	}
	resp := Response{MessageID: out.SessionID, Model: rec.Model, Text: text, StopReason: out.StopReason, Usage: rec.Usage, Latency: latency}

	refused := out.StopReason == "refusal" || safetyStopped(out.SafetyStops)
	switch {
	case refused:
		resp.StopReason, rec.StopReason = "refusal", "refusal"
		rec.Error = "recusa do modelo"
	case out.IsError || runErr != nil:
		msg := out.Result
		if msg == "" {
			msg = out.Subtype
		}
		if usageLimited(msg) {
			return fail(fmt.Errorf("%w: %s", ErrUsageLimit, truncate(msg, 300)))
		}
		return fail(fmt.Errorf("claude -p (%s): %s", out.Subtype, truncate(msg, 300)))
	case schema != nil:
		if verr := validateJSON(schema, text); verr != nil {
			rec.Error = "saída fora do schema: " + verr.Error()
		}
	}
	annotate(span, rec)
	if err := c.o.Recorder.RecordCall(ctx, rec); err != nil {
		return resp, fmt.Errorf("gravando llm_call: %w", err)
	}
	if refused {
		return resp, fmt.Errorf("%w: stop_reason=refusal", ErrBadStop)
	}
	if strings.HasPrefix(rec.Error, "saída fora do schema: ") {
		return resp, &validationError{msg: strings.TrimPrefix(rec.Error, "saída fora do schema: ")}
	}
	return resp, nil
}

// Batch roda os itens com concorrência limitada. Ao estourar o limite de chamadas,
// para de despachar e devolve o que já terminou junto com o erro.
func (c *ClaudeCode) Batch(ctx context.Context, items []BatchItem) (map[string]BatchResult, error) {
	seen := map[string]bool{}
	for _, it := range items {
		if it.CustomID == "" || seen[it.CustomID] {
			return nil, fmt.Errorf("custom_id vazio ou repetido: %q", it.CustomID)
		}
		seen[it.CustomID] = true
	}
	ctx, span := c.o.Tracer.Start(ctx, "llm.batch", trace.WithAttributes(
		telemetry.AttrItems.Int(len(items)), telemetry.AttrMode.String("cli"),
	))
	defer span.End()

	results := make(map[string]BatchResult, len(items))
	var mu sync.Mutex
	var stopErr error
	sem := make(chan struct{}, c.o.Concurrency)
	var wg sync.WaitGroup
	for _, it := range items {
		mu.Lock()
		stop := stopErr != nil
		mu.Unlock()
		if stop || ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(it BatchItem) {
			defer wg.Done()
			defer func() { <-sem }()
			resp, err := c.Call(ctx, it.Request)
			mu.Lock()
			defer mu.Unlock()
			if errors.Is(err, ErrBudgetExceeded) {
				if stopErr == nil {
					stopErr = err
				}
				return
			}
			results[it.CustomID] = BatchResult{Response: resp, Err: err}
		}(it)
	}
	wg.Wait()
	if stopErr == nil && ctx.Err() != nil {
		stopErr = ctx.Err()
	}
	return results, stopErr
}

func (c *ClaudeCode) compile(schemaJSON []byte) (*jsonschema.Schema, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := string(schemaJSON)
	if s, ok := c.schemas[key]; ok {
		return s, nil
	}
	s, err := CompileSchema(schemaJSON)
	if err != nil {
		return nil, err
	}
	c.schemas[key] = s
	return s, nil
}

// CompileSchema compila um JSON Schema para validação local.
func CompileSchema(schemaJSON []byte) (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaJSON))
	if err != nil {
		return nil, err
	}
	comp := jsonschema.NewCompiler()
	sum := sha256.Sum256(schemaJSON)
	url := "sentinela://llm/" + hex.EncodeToString(sum[:8]) + ".json"
	if err := comp.AddResource(url, doc); err != nil {
		return nil, err
	}
	return comp.Compile(url)
}

func validateJSON(s *jsonschema.Schema, text string) error {
	doc, err := jsonschema.UnmarshalJSON(strings.NewReader(text))
	if err != nil {
		return fmt.Errorf("JSON inválido: %w", err)
	}
	return s.Validate(doc)
}

// record grava a resposta válida em RecordDir para reuso pelo provider replay.
func (c *ClaudeCode) record(req Request, resp Response) {
	if c.o.RecordDir == "" {
		return
	}
	dir := filepath.Join(c.o.RecordDir, req.Agent)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, ReplayKey(req)+".json"), []byte(resp.Text), 0o600)
}

// ReplayKey identifica uma requisição de forma estável (modelo, prompt e entrada).
func ReplayKey(req Request) string {
	sum := sha256.Sum256([]byte(req.PromptVersion + "\x00" + req.System + "\x00" + req.User))
	return hex.EncodeToString(sum[:])[:20]
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

// safetyStopped interpreta safety_stops, que na saída real do CLI (2.1.296) é uma
// contagem (0 quando nada foi barrado); aceita também lista, por segurança.
func safetyStopped(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err == nil {
		return n > 0
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		return len(list) > 0
	}
	return false
}

// ErrForeignInstructions indica um diretório de trabalho abaixo de um CLAUDE.md: o Claude Code
// carregaria essas instruções (de outro projeto) no contexto de cada chamada.
var ErrForeignInstructions = errors.New("claude: instruções de outro projeto no caminho")

// InstructionsAbove lista CLAUDE.md e CLAUDE.local.md nos diretórios acima de dir (e no próprio
// dir, se withSelf). Até a Fase 4 o provider rodava em .sentinela/claude-cwd, dentro deste
// projeto, e cada chamada da descoberta recebia o CLAUDE.md do Sentinela e o dos diretórios acima do projeto.
func InstructionsAbove(dir string, withSelf bool) []string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	d := filepath.Clean(abs)
	if !withSelf {
		d = filepath.Dir(d)
	}
	var found []string
	for {
		for _, name := range []string{"CLAUDE.md", "CLAUDE.local.md"} {
			if _, err := os.Stat(filepath.Join(d, name)); err == nil {
				found = append(found, filepath.Join(d, name))
			}
		}
		if d == filepath.Dir(d) {
			return found
		}
		d = filepath.Dir(d)
	}
}
