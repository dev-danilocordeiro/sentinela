package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/github"
)

// fakeGitHub é a API REST do GitHub para um repo: PRs, reviews, checks e commits.
type fakeGitHub struct {
	mu       sync.Mutex
	prs      []*fakePR
	reviews  []postedReview          // reviews publicadas pela revisora
	human    map[int][]github.Review // reviews de humanos, por PR
	checks   map[string][]github.CheckRun
	commits  []github.Commit // branch padrão, para detectar revert
	closedBy map[int]string  // quem fechou cada PR (evento closed da issue)
	requests []string
}

type fakePR struct {
	github.PullRequest
	Title, Base, Body string
}

type postedReview struct {
	PR          int
	Event, Body string
}

func newFakeGitHub(t *testing.T) (*fakeGitHub, *httptest.Server) {
	t.Helper()
	f := &fakeGitHub{human: map[int][]github.Review{}, checks: map[string][]github.CheckRun{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

var (
	rePulls   = regexp.MustCompile(`^/repos/([^/]+/[^/]+)/pulls$`)
	rePull    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/(\d+)$`)
	reReviews = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/(\d+)/reviews$`)
	reComms   = regexp.MustCompile(`^/repos/[^/]+/[^/]+/pulls/(\d+)/comments$`)
	reChecks  = regexp.MustCompile(`^/repos/[^/]+/[^/]+/commits/([0-9a-f]+)/check-runs$`)
	reAnns    = regexp.MustCompile(`^/repos/[^/]+/[^/]+/check-runs/\d+/annotations$`)
	reCommits = regexp.MustCompile(`^/repos/[^/]+/[^/]+/commits$`)
	reEvents  = regexp.MustCompile(`^/repos/[^/]+/[^/]+/issues/(\d+)/events$`)
)

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer tok-bot" && r.Header.Get("Authorization") != "Bearer tok-reviewer" {
		http.Error(w, "sem token de instalação", http.StatusUnauthorized)
		return
	}
	enc := json.NewEncoder(w)
	p := r.URL.Path
	switch {
	case rePulls.MatchString(p) && r.Method == http.MethodPost:
		var in github.NewPR
		_ = json.NewDecoder(r.Body).Decode(&in)
		pr := &fakePR{Title: in.Title, Base: in.Base, Body: in.Body}
		pr.Number, pr.State, pr.Draft = len(f.prs)+1, "open", in.Draft
		pr.HTMLURL = fmt.Sprintf("https://github.com/acme/svc/pull/%d", pr.Number)
		pr.Head.Ref = in.Head
		f.prs = append(f.prs, pr)
		w.WriteHeader(http.StatusCreated)
		_ = enc.Encode(pr.PullRequest)
	case rePulls.MatchString(p) && r.Method == http.MethodGet:
		head := r.URL.Query().Get("head")
		out := []github.PullRequest{}
		for _, pr := range f.prs {
			if pr.State == "open" && strings.HasSuffix(head, ":"+pr.Head.Ref) {
				out = append(out, pr.PullRequest)
			}
		}
		_ = enc.Encode(out)
	case rePull.MatchString(p):
		pr := f.pr(rePull.FindStringSubmatch(p)[1])
		if pr == nil {
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPatch {
			var in map[string]string
			_ = json.NewDecoder(r.Body).Decode(&in)
			pr.Body = in["body"]
		}
		_ = enc.Encode(pr.PullRequest)
	case reReviews.MatchString(p) && r.Method == http.MethodPost:
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		n, _ := strconv.Atoi(reReviews.FindStringSubmatch(p)[1])
		f.reviews = append(f.reviews, postedReview{PR: n, Event: in["event"], Body: in["body"]})
		_ = enc.Encode(github.Review{ID: int64(len(f.reviews)), State: "COMMENTED"})
	case reReviews.MatchString(p):
		n, _ := strconv.Atoi(reReviews.FindStringSubmatch(p)[1])
		_ = enc.Encode(append([]github.Review{}, f.human[n]...))
	case reComms.MatchString(p), reAnns.MatchString(p):
		_ = enc.Encode([]any{})
	case reChecks.MatchString(p):
		_ = enc.Encode(map[string]any{"check_runs": append([]github.CheckRun{}, f.checks[reChecks.FindStringSubmatch(p)[1]]...)})
	case reCommits.MatchString(p):
		_ = enc.Encode(f.commits)
	case reEvents.MatchString(p):
		n, _ := strconv.Atoi(reEvents.FindStringSubmatch(p)[1])
		events := []any{map[string]any{"event": "labeled", "actor": map[string]any{"login": "outra"}}}
		if who := f.closedBy[n]; who != "" {
			events = append(events, map[string]any{"event": "closed", "actor": map[string]any{"login": who}})
		}
		_ = enc.Encode(events)
	default:
		http.Error(w, "fake: rota não suportada "+r.Method+" "+p, http.StatusNotImplemented)
	}
}

func (f *fakeGitHub) pr(n string) *fakePR {
	for _, pr := range f.prs {
		if strconv.Itoa(pr.Number) == n {
			return pr
		}
	}
	return nil
}

func (f *fakeGitHub) snapshot() ([]fakePR, []postedReview) {
	f.mu.Lock()
	defer f.mu.Unlock()
	prs := make([]fakePR, 0, len(f.prs))
	for _, p := range f.prs {
		prs = append(prs, *p)
	}
	return prs, append([]postedReview{}, f.reviews...)
}

// fakeApp é uma GitHub App com token fixo.
type fakeApp struct {
	token, base string
}

func (a fakeApp) Token(context.Context) (string, error) { return a.token, nil }
func (a fakeApp) APIBase() string                       { return a.base }

// fakeRunner faz o papel do Claude Code com o plugin: escreve os artefatos de acordo com um
// roteiro e faz commits no worktree. Grava cada invocação.
type fakeRunner struct {
	mu    sync.Mutex
	calls []Invocation
	// roteiro do job: vereditos dos reviews por rodada (lente -> PASS/CHANGES), decisão final
	rounds   []map[string]string
	decision string // APPROVE | ESCALATE | "" (rodadas esgotadas)
	breakAt  string // arquivo a escrever fora do contrato
	shepherd string // STATUS: FIXED | STATUS: BLOCKED
	// home é o ClaudeHome: cada sessão grava <home>/projects/<cwd>/<id>.jsonl e um subagente.
	home string
	// timeoutNext faz a próxima sessão estourar o execution.timeout (falha retomável), depois de
	// escrever a spec. O limite de uso não serve mais de exemplo: ele devolve o job à fila.
	timeoutNext bool
}

// saveSession grava os JSONL da sessão como o Claude Code faria.
func (r *fakeRunner) saveSession(cwd, id string) {
	if r.home == "" {
		return
	}
	dir := filepath.Join(r.home, "projects", ProjectDirName(cwd))
	write(dir, id+".jsonl", `{"type":"user"}`+"\n")
	write(dir, id+"/subagents/agent-builder.jsonl", `{"type":"user"}`+"\n")
}

func (r *fakeRunner) Run(_ context.Context, inv Invocation) (RunResult, error) {
	r.mu.Lock()
	r.calls = append(r.calls, inv)
	r.mu.Unlock()
	arg := inv.JobDir
	if inv.Resume == "" {
		arg = strings.TrimSpace(strings.SplitN(inv.Prompt, " ", 2)[1])
	}
	res := RunResult{SessionID: "sess-" + strconv.Itoa(len(r.calls)), Subtype: "success", NumTurns: 7, CostUSD: 1.25,
		Models: map[string]ModelUsage{"claude-opus-5-5": {InputTokens: 10, OutputTokens: 900, CostUSD: 1.0}, "claude-sonnet-5-5": {InputTokens: 5, OutputTokens: 300, CostUSD: 0.25}}}
	if inv.Resume != "" {
		res.SessionID = inv.Resume // --resume continua a mesma sessão
	}
	r.saveSession(inv.Dir, res.SessionID)
	r.mu.Lock()
	limit := r.timeoutNext
	r.timeoutNext = false
	r.mu.Unlock()
	if limit {
		write(arg, "spec.md", "SPEC: READY\n# Spec\n")
		res.Subtype = ""
		return res, fmt.Errorf("claude: %w (timeout 1h0m0s)", context.DeadlineExceeded)
	}
	if strings.HasPrefix(inv.Prompt, "/sentinela-factory:sentinela-shepherd ") {
		verdict := orDash(r.shepherd, "STATUS: FIXED")
		if verdict == "STATUS: FIXED" {
			commit(inv.Dir, "cmd/api/fix.txt", "shepherd "+strconv.Itoa(len(r.calls)), "shepherd")
		}
		write(arg, "result.md", verdict+"\n## O que mudou\n- ajuste para o CI\n")
		return res, nil
	}
	job := arg
	var spec JobSpec
	data, _ := os.ReadFile(filepath.Join(job, "job.json"))
	_ = json.Unmarshal(data, &spec)
	n := 0
	lastChanges := false
	for i, rv := range r.rounds {
		n = i + 1
		round := fmt.Sprintf("round-%d", n)
		commit(inv.Dir, "cmd/api/fix.txt", fmt.Sprintf("rodada %d %s", n, spec.ReworkNote), spec.Issue+": correção rodada "+strconv.Itoa(n))
		write(job, round+"/build.md", "STATUS: READY\n## Mudanças\n- fix.txt\n## Critérios\n- ok\n")
		lastChanges = false
		for _, lens := range Lenses {
			v := orDash(rv[lens], "PASS")
			body := "VERDICT: " + v + "\n## Achados\n- nenhum\n"
			if v == "CHANGES" {
				lastChanges = true
				body = "VERDICT: CHANGES\n## Achados\n- fix.txt:1 falta tratar o erro\n"
			}
			write(job, round+"/review-"+lens+".md", body)
		}
	}
	state := State{Rounds: n}
	switch {
	case lastChanges:
		state.Status, state.Reason = "escalate", "rodadas esgotadas: lentes ainda pedem mudança"
	default:
		first := r.decision
		if r.breakAt == "decision.md" {
			first = "LGTM"
		}
		write(job, "decision.md", first+"\nmotivo do approver\n## O que o humano precisa checar\n- o diff\n")
		state.Status = strings.ToLower(r.decision)
	}
	b, _ := json.Marshal(state)
	write(job, "state.json", string(b))
	return res, nil
}

func (r *fakeRunner) invocations() []Invocation {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Invocation{}, r.calls...)
}

func write(dir, rel, content string) {
	p := filepath.Join(dir, filepath.FromSlash(rel))
	_ = os.MkdirAll(filepath.Dir(p), 0o750)
	_ = os.WriteFile(p, []byte(content), 0o600)
}

func commit(dir, file, content, msg string) {
	f, _ := os.OpenFile(filepath.Join(dir, file), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	_, _ = f.WriteString(content + "\n")
	_ = f.Close()
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-qm", msg}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=builder", "GIT_AUTHOR_EMAIL=b@b", "GIT_COMMITTER_NAME=builder", "GIT_COMMITTER_EMAIL=b@b")
		_ = cmd.Run()
	}
}
