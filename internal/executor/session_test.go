package executor

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestProjectDirNameMatchesClaudeCode(t *testing.T) {
	// Pastas reais de ~/.claude/projects (Claude Code 2.1.296).
	cases := map[string]string{
		"/srv/u/.cache/sentinela/worktrees/acme/monorepo/EX-15": "-srv-u--cache-sentinela-worktrees-acme-monorepo-EX-15",
		"/srv/u/projetos/sentinela":                             "-srv-u-projetos-sentinela",
		"/tmp/a_b.c":                                            "-tmp-a-b-c",
	}
	for in, want := range cases {
		if got := ProjectDirName(in); got != want {
			t.Errorf("ProjectDirName(%q) = %q, quer %q", in, got, want)
		}
	}
}

func TestClassifyFailure(t *testing.T) {
	cases := []struct {
		err  error
		run  RunResult
		want string
	}{
		{fmt.Errorf("claude: %w (timeout 1h0m0s)", context.DeadlineExceeded), RunResult{}, FailTimeout},
		{fmt.Errorf("%w: decision.md ausente", ErrContract), RunResult{}, FailContract},
		{errors.New("claude terminou com success: You've hit your session limit"), RunResult{Result: "You've hit your session limit · resets 8:50pm"}, FailUsageLimit},
		{errors.New("o repo acme/svc não tem AGENTS.md nem CLAUDE.md na raiz"), RunResult{}, FailOther},
	}
	for _, c := range cases {
		if got := classifyFailure(c.err, c.run); got != c.want {
			t.Errorf("classifyFailure(%v) = %q, quer %q", c.err, got, c.want)
		}
		// O texto gravado em executions.error dá o mesmo tipo (execuções antigas).
		if got := ClassifyFailureText(c.err.Error()); got != c.want {
			t.Errorf("ClassifyFailureText(%q) = %q, quer %q", c.err.Error(), got, c.want)
		}
	}
	if Resumable(FailOther) || !Resumable(FailUsageLimit) || !Resumable(FailTimeout) || !Resumable(FailContract) {
		t.Error("retomáveis: limite de uso, timeout e contrato")
	}
}

func TestFindSessionListsSubagents(t *testing.T) {
	home := t.TempDir()
	cwd := "/w/EX-1"
	if _, ok := FindSession(home, cwd, "s1"); ok {
		t.Fatal("sessão inexistente encontrada")
	}
	dir := filepath.Join(home, "projects", ProjectDirName(cwd))
	write(dir, "s1.jsonl", "{}\n")
	write(dir, "s1/subagents/agent-b.jsonl", "{}\n")
	write(dir, "s1/subagents/agent-a.jsonl", "{}\n")
	write(dir, "s1/subagents/agent-a.meta.json", "{}\n")
	log, ok := FindSession(home, cwd, "s1")
	if !ok || log.Main != filepath.Join(dir, "s1.jsonl") || len(log.Subagents) != 2 || filepath.Base(log.Subagents[0]) != "agent-a.jsonl" {
		t.Fatalf("FindSession = %+v %v", log, ok)
	}
}
