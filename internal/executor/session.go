package executor

import (
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

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Tipos de falha de um job (ADR 0030). Os retomáveis voltam à mesma sessão no retry.
const (
	FailUsageLimit = "usage_limit"  // limite de uso da assinatura
	FailTimeout    = "timeout"      // execution.timeout estourou
	FailIdle       = "idle_timeout" // execution.idle_timeout: sessão sem saída (ADR 0034)
	FailContract   = "contract"     // arquivo do contrato faltando ou com primeira linha inválida
	FailOther      = "other"
	// FailResumeLimit: a cadeia de retomadas chegou a execution.max_resumes; não retoma de novo.
	FailResumeLimit = "resume_limit"
)

// ErrResumeLimit é a tentativa que não roda porque a cadeia já retomou o máximo de vezes.
var ErrResumeLimit = errors.New("limite de retomadas da sessão")

// Chaves em executions.artifacts: o tipo de falha e o estado do worktree quando o job falhou.
const (
	failureKey  = "failure"
	worktreeKey = "worktree"
)

// worktreeState é o commit atual do worktree e um resumo (sha256) das mudanças não commitadas:
// git status (com arquivos novos) e o diff contra HEAD.
func worktreeState(ctx context.Context, dir string) (head, digest string, err error) {
	git := func(args ...string) (string, error) {
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output() //nolint:gosec // worktree do job
		return string(out), err
	}
	if head, err = git("rev-parse", "HEAD"); err != nil {
		return "", "", err
	}
	status, err := git("status", "--porcelain=v1", "-uall")
	if err != nil {
		return "", "", err
	}
	diff, err := git("diff", "HEAD", "--binary")
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(status + "\x00" + diff))
	return strings.TrimSpace(head), hex.EncodeToString(sum[:]), nil
}

// worktreeChanged compara o worktree com o estado gravado quando a execução falhou. Devolve o
// motivo para não retomar, ou "" se está igual.
func worktreeChanged(ctx context.Context, prev db.Execution) string {
	var all map[string]Artifact
	_ = json.Unmarshal(prev.Artifacts, &all)
	rec, ok := all[worktreeKey]
	if !ok {
		return fmt.Sprintf("a execução #%d não registrou o estado do worktree", prev.ID)
	}
	head, digest, err := worktreeState(ctx, prev.Worktree.String)
	if err != nil {
		return fmt.Sprintf("não deu para ler o worktree da execução #%d: %v", prev.ID, err)
	}
	if want := strings.TrimPrefix(rec.Verdict, "HEAD: "); head != want {
		return fmt.Sprintf("o worktree está no commit %s e a execução #%d parou em %s", short(head), prev.ID, short(want))
	}
	if digest != rec.Summary {
		return fmt.Sprintf("o worktree tem mudanças que não são da execução #%d", prev.ID)
	}
	return ""
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// Resumable diz se uma falha desse tipo retoma a sessão no retry.
func Resumable(kind string) bool {
	return kind == FailUsageLimit || kind == FailTimeout || kind == FailIdle || kind == FailContract
}

// classifyFailure classifica a falha de um job pelo erro e pelo resultado da sessão.
func classifyFailure(err error, run RunResult) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrIdleTimeout):
		return FailIdle
	case errors.Is(err, context.DeadlineExceeded):
		return FailTimeout
	case errors.Is(err, ErrContract):
		return FailContract
	case llm.UsageLimited(run.Result):
		return FailUsageLimit
	}
	return ClassifyFailureText(err.Error())
}

// ClassifyFailureText classifica pela mensagem gravada em executions.error. Vale para execuções
// anteriores ao registro do tipo de falha em artifacts.
func ClassifyFailureText(msg string) string {
	switch {
	case msg == "":
		return ""
	case llm.UsageLimited(msg):
		return FailUsageLimit
	case strings.Contains(msg, ErrIdleTimeout.Error()):
		return FailIdle
	case strings.Contains(msg, "context deadline exceeded"):
		return FailTimeout
	case strings.Contains(msg, ErrContract.Error()):
		return FailContract
	}
	return FailOther
}

// failureOf devolve o tipo de falha de uma execução: o gravado em artifacts ou, nas antigas, o
// deduzido da mensagem de erro.
func failureOf(e db.Execution) string {
	var all map[string]Artifact
	if json.Unmarshal(e.Artifacts, &all) == nil {
		if a, ok := all[failureKey]; ok {
			return strings.TrimPrefix(a.Verdict, "FAILURE: ")
		}
	}
	return ClassifyFailureText(e.Error.String)
}

// SessionLog são os arquivos JSONL de uma sessão do Claude Code: o da sessão principal e os dos
// subagentes (spec-writer, builder, reviewers, approver).
type SessionLog struct {
	Main      string   `json:"main"`
	Subagents []string `json:"subagents,omitempty"`
}

// ClaudeHome é o diretório de dados do Claude Code: CLAUDE_CONFIG_DIR ou ~/.claude.
func ClaudeHome() string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude")
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// ProjectDirName é o nome da pasta de sessões de um diretório de trabalho em
// <ClaudeHome>/projects: cada caractere fora de [a-zA-Z0-9] vira "-". Conferido no Claude Code
// 2.1.296: /srv/u/.cache/x/EX-15 -> -srv-u--cache-x-EX-15.
func ProjectDirName(cwd string) string { return nonAlnum.ReplaceAllString(cwd, "-") }

// FindSession localiza os arquivos da sessão: <projects>/<cwd>/<id>.jsonl e
// <projects>/<cwd>/<id>/subagents/agent-*.jsonl. ok=false se o arquivo principal não existe.
func FindSession(home, cwd, sessionID string) (SessionLog, bool) {
	if home == "" || cwd == "" || sessionID == "" {
		return SessionLog{}, false
	}
	dir := filepath.Join(home, "projects", ProjectDirName(cwd))
	main := filepath.Join(dir, sessionID+".jsonl")
	if st, err := os.Stat(main); err != nil || !st.Mode().IsRegular() {
		return SessionLog{}, false
	}
	log := SessionLog{Main: main}
	subs, _ := filepath.Glob(filepath.Join(dir, sessionID, "subagents", "agent-*.jsonl"))
	sort.Strings(subs)
	log.Subagents = subs
	return log, true
}

// resumePlan é a retomada da sessão de uma execução anterior.
type resumePlan struct {
	SessionID string
	Message   string
}

// resumeMessage é o que a sessão retomada recebe: o erro, curto, e o que fazer.
func resumeMessage(prev db.Execution, kind, jobDir string, instructions []Instruction) string {
	var b strings.Builder
	fmt.Fprintf(&b, "A execução #%d deste job parou antes de terminar (%s): %s\n\n", prev.ID, kind, truncate(prev.Error.String, 400))
	switch kind {
	case FailContract:
		b.WriteString("Corrija o contrato: cada arquivo do job precisa existir e começar com a primeira linha válida. ")
	case FailUsageLimit, FailTimeout:
		b.WriteString("A sessão foi interrompida no meio. ")
	case FailIdle:
		b.WriteString("A sessão ficou parada, sem saída, e foi interrompida; se um comando travou, não o repita do mesmo jeito. ")
	}
	fmt.Fprintf(&b, "Continue o sentinela-fix deste job (%s) de onde parou: confira state.json e os arquivos já escritos, não refaça o que está pronto e termine a linha normalmente, gravando state.json.\n", jobDir)
	if len(instructions) > 0 {
		b.WriteString("\nDesde então, a equipe comentou na issue (estas instruções têm prioridade sobre a correção proposta):\n")
		for _, in := range instructions {
			fmt.Fprintf(&b, "- %s: %s\n", in.Author, truncate(strings.TrimSpace(in.Body), 600))
		}
	}
	return b.String()
}
