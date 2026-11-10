package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// Invocation é uma chamada headless do Claude Code com o plugin carregado.
type Invocation struct {
	Prompt     string // ex.: "/sentinela-factory:sentinela-fix <JOB>"; numa retomada, a mensagem
	Resume     string // id da sessão a retomar (--resume); vazio começa uma sessão nova
	Dir        string // worktree do job (diretório atual do Claude Code)
	PluginDir  string
	JobDir     string // liberado com --add-dir
	Model      string // modelo da sessão principal (orquestrador)
	MaxBudget  float64
	Disallowed []string
	Timeout    time.Duration
	// IdleTimeout encerra a sessão que passa esse tempo sem nenhuma linha nova no stream (ADR
	// 0034); zero desliga.
	IdleTimeout time.Duration
	// CompletionTimeout encerra, com aviso, o processo que não sai depois do evento result; o
	// resultado vale e os commits ficam no worktree. Zero desliga.
	CompletionTimeout time.Duration
	StreamFile        string // onde gravar o stream-json bruto (auditoria)
}

// ModelUsage é o consumo por modelo que o Claude Code informa no resultado.
type ModelUsage struct {
	InputTokens              int64   `json:"inputTokens"`
	OutputTokens             int64   `json:"outputTokens"`
	CacheReadInputTokens     int64   `json:"cacheReadInputTokens"`
	CacheCreationInputTokens int64   `json:"cacheCreationInputTokens"`
	CostUSD                  float64 `json:"costUSD"`
}

// RunResult é o fim da sessão headless.
type RunResult struct {
	SessionID string
	Subtype   string // success | error_max_turns | error_during_execution | error_max_budget_usd...
	IsError   bool
	Result    string
	CostUSD   float64
	NumTurns  int
	Duration  time.Duration
	Models    map[string]ModelUsage
	// Warning: o processo não saiu depois do resultado e foi encerrado (completion_timeout).
	Warning string
}

// Runner invoca o Claude Code. Nos testes, um binário falso faz o papel do plugin.
type Runner interface {
	Run(ctx context.Context, inv Invocation) (RunResult, error)
}

// ClaudeRunner chama o binário `claude` em modo -p com saída stream-json.
type ClaudeRunner struct {
	Binary string
	// Home é o HOME próprio do job (M6); vazio usa o HOME do worker.
	Home JobHome
	// Container roda a sessão num contêiner (execution.runtime: container, ADR 0053); nil roda no
	// host, como processo do worker.
	Container *ContainerJob
}

// Args monta a linha de comando. Flags conferidas em `claude --help` (2.1.296): -p,
// --output-format stream-json (exige --verbose), --plugin-dir, --add-dir, --model,
// --permission-mode, --allowedTools, --disallowedTools, --max-budget-usd, --resume <id>.
func (inv Invocation) Args() []string {
	args := []string{"-p", inv.Prompt}
	if inv.Resume != "" {
		args = append(args, "--resume", inv.Resume)
	}
	args = append(args,
		"--output-format", "stream-json", "--verbose",
		"--plugin-dir", inv.PluginDir,
		"--add-dir", inv.JobDir,
		"--permission-mode", "acceptEdits",
		// O builder e os reviewers rodam os comandos de teste do repo; o que não pode (push, gh,
		// rede) vai em --disallowedTools, que tem precedência.
		"--allowedTools", "Bash", "Read", "Edit", "Write", "Glob", "Grep", "Agent", "SendMessage", "Skill", "TodoWrite",
	)
	if len(inv.Disallowed) > 0 {
		args = append(args, "--disallowedTools")
		args = append(args, inv.Disallowed...)
	}
	if inv.Model != "" {
		args = append(args, "--model", inv.Model)
	}
	if inv.MaxBudget > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(inv.MaxBudget, 'f', 2, 64))
	}
	return args
}

func (r ClaudeRunner) Run(ctx context.Context, inv Invocation) (RunResult, error) {
	if inv.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, inv.Timeout)
		defer cancel()
	}
	bin := r.Binary
	if bin == "" {
		bin = "claude"
	}
	// Só a lista fechada de jobEnv (C2 da revisão 2026-10-11): sem os segredos do Sentinela e sem
	// ANTHROPIC_* (com a chave no ambiente, o Claude Code usa crédito da API em vez da assinatura;
	// aconteceu no primeiro job real, EX-12).
	env := jobEnv(os.Environ())
	if r.Home.Dir != "" {
		prepared, err := r.Home.prepare(ctx, os.Environ(), inv.Dir)
		if err != nil {
			return RunResult{}, err
		}
		env = prepared
		defer func() { _ = r.Home.finish() }()
	}
	env = withToolEnv(env, os.Environ())
	args := inv.Args()
	if r.Container != nil {
		// O processo do worker é o cliente do engine; o Claude Code roda na imagem, com o ambiente
		// fechado montado em --env, e a sessão termina com o contêiner, o proxy e a rede removidos.
		cargs, clientEnv, job, err := r.Container.start(ctx, inv, env, r.Home)
		if err != nil {
			return RunResult{}, fmt.Errorf("runtime container: %w", err)
		}
		defer job.cleanup()
		inner := r.Container.Binary
		if inner == "" {
			inner = "claude"
		}
		bin = r.Container.Engine
		args = append(cargs, append([]string{inner}, args...)...)
		env = append(llm.ClosedEnv(os.Environ()), clientEnv...)
		for _, k := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "CONTAINER_HOST"} {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // binário da config; argumentos montados aqui
	cmd.Dir = inv.Dir
	cmd.Stdin = nil // sem stdin: o Claude Code não espera entrada
	cmd.Env = env
	// Grupo próprio: timeout, idle e completion matam também os filhos, e o Wait não fica preso a
	// um filho que herdou o stdout (ADR 0034).
	ownGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 10 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return RunResult{}, err
	}
	sink := io.Discard
	if inv.StreamFile != "" {
		f, err := os.Create(inv.StreamFile) //nolint:gosec // arquivo do diretório do job
		if err != nil {
			return RunResult{}, err
		}
		defer func() { _ = f.Close() }()
		sink = f
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return RunResult{}, fmt.Errorf("iniciando %s: %w", bin, err)
	}
	// Matar o grupo pode não fechar o stdout (um neto fora do grupo o herdou): fechar a ponta de
	// leitura destrava a leitura do stream.
	abort := func() { _ = killGroup(cmd); _ = stdout.Close() }
	wd := newWatchdog(inv.IdleTimeout, inv.CompletionTimeout, abort)
	defer wd.stop()
	readDone := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			abort()
		case <-readDone:
		}
	}()
	res, perr := parseStream(io.TeeReader(stdout, sink), streamHooks{
		onInit: func(source string) error {
			// Segunda trava: se mesmo assim a sessão começou com chave de API, mata antes de chamar modelo.
			if source != "" && source != "none" {
				_ = killGroup(cmd)
				return fmt.Errorf("%w: a sessão usaria %s em vez da assinatura", ErrAPIKey, source)
			}
			return nil
		},
		onLine:   wd.activity,
		onResult: wd.finished,
	})
	close(readDone)
	if perr != nil && res.Subtype != "" {
		perr = nil // o resultado chegou; o resto do stream não importa
	}
	if perr == nil {
		// Leu até o fim do stdout: espera o processo sair, dentro do completion_timeout.
		wd.finished()
	}
	werr := cmd.Wait()
	wd.stop()
	res.Duration = time.Since(start)
	if ctx.Err() != nil {
		return res, fmt.Errorf("claude: %w (timeout %s)", ctx.Err(), inv.Timeout)
	}
	switch wd.reason() {
	case killIdle:
		return res, fmt.Errorf("%w: nenhuma saída do agente por %s", ErrIdleTimeout, inv.IdleTimeout)
	case killCompletion:
		res.Warning = fmt.Sprintf("o Claude Code sinalizou o fim, mas o processo não saiu em %s; foi encerrado (completion_timeout), e os commits ficaram no worktree", inv.CompletionTimeout)
	}
	if perr != nil {
		return res, perr
	}
	if werr != nil && res.Subtype == "" {
		return res, fmt.Errorf("claude: %w: %s", werr, truncate(strings.TrimSpace(stderr.String()), 400))
	}
	return res, nil
}

// ErrIdleTimeout é a sessão encerrada por ficar sem saída por execution.idle_timeout.
var ErrIdleTimeout = errors.New("claude: sem saída do agente (idle_timeout)")

// Motivos do watchdog para encerrar o processo.
const (
	killNone = iota
	killIdle
	killCompletion
)

// watchdog encerra a sessão parada (sem linha nova por idle) ou o processo que não sai depois do
// resultado (completion). Zero desliga cada um.
type watchdog struct {
	mu         sync.Mutex
	idle, done time.Duration
	last       time.Time
	finishedAt time.Time
	why        int
	kill       func()
	quit       chan struct{}
	once       sync.Once
}

func newWatchdog(idle, completion time.Duration, kill func()) *watchdog {
	w := &watchdog{idle: idle, done: completion, last: time.Now(), kill: kill, quit: make(chan struct{})}
	if idle <= 0 && completion <= 0 {
		return w
	}
	tick := time.Second
	for _, d := range []time.Duration{idle, completion} {
		if d > 0 && d/10 < tick {
			tick = max(d/10, 5*time.Millisecond)
		}
	}
	go w.loop(tick)
	return w
}

func (w *watchdog) loop(tick time.Duration) {
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-w.quit:
			return
		case now := <-t.C:
			w.mu.Lock()
			switch {
			case w.why != killNone:
			case !w.finishedAt.IsZero():
				if w.done > 0 && now.Sub(w.finishedAt) > w.done {
					w.why = killCompletion
				}
			case w.idle > 0 && now.Sub(w.last) > w.idle:
				w.why = killIdle
			}
			fire := w.why != killNone
			w.mu.Unlock()
			if fire {
				w.kill()
				return
			}
		}
	}
}

func (w *watchdog) activity() {
	w.mu.Lock()
	w.last = time.Now()
	w.mu.Unlock()
}

func (w *watchdog) finished() {
	w.mu.Lock()
	if w.finishedAt.IsZero() {
		w.finishedAt = time.Now()
	}
	w.mu.Unlock()
}

func (w *watchdog) reason() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.why
}

func (w *watchdog) stop() { w.once.Do(func() { close(w.quit) }) }

// ErrAPIKey indica uma sessão que usaria crédito da API em vez da assinatura.
var ErrAPIKey = errors.New("claude: chave de API no ambiente")

// ParseStream lê o stream-json: o evento system/init traz a sessão; o evento result traz custo,
// turnos e uso por modelo.
func ParseStream(r io.Reader) (RunResult, error) { return parseStream(r, streamHooks{}) }

// streamHooks: onInit recebe o apiKeySource do evento init (um erro dele encerra a leitura);
// onLine, cada linha lida; onResult, o evento result.
type streamHooks struct {
	onInit   func(source string) error
	onLine   func()
	onResult func()
}

func parseStream(r io.Reader, h streamHooks) (RunResult, error) {
	var res RunResult
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		if h.onLine != nil {
			h.onLine()
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev struct {
			Type         string                `json:"type"`
			Subtype      string                `json:"subtype"`
			SessionID    string                `json:"session_id"`
			IsError      bool                  `json:"is_error"`
			Result       string                `json:"result"`
			TotalCostUSD float64               `json:"total_cost_usd"`
			NumTurns     int                   `json:"num_turns"`
			ModelUsage   map[string]ModelUsage `json:"modelUsage"`
			APIKeySource string                `json:"apiKeySource"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.Type == "system" && ev.Subtype == "init" && h.onInit != nil {
			if err := h.onInit(ev.APIKeySource); err != nil {
				return res, err
			}
		}
		if ev.SessionID != "" && res.SessionID == "" {
			res.SessionID = ev.SessionID
		}
		if ev.Type == "result" {
			res.Subtype, res.IsError, res.Result = ev.Subtype, ev.IsError, ev.Result
			res.CostUSD, res.NumTurns, res.Models = ev.TotalCostUSD, ev.NumTurns, ev.ModelUsage
			if h.onResult != nil {
				h.onResult()
			}
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("lendo stream do claude: %w", err)
	}
	if res.Subtype == "" {
		return res, fmt.Errorf("claude terminou sem evento result")
	}
	return res, nil
}
