package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Sem execution.worktrees, jobs e home, os três ficam num diretório desta instalação em
// ~/.cache/sentinela/<chave>, com a chave do diretório atual (ADR 0056); explícitos, valem como
// estão.
func TestExecutionDirsDefaultToADirectoryOfThisInstallation(t *testing.T) {
	base := example(t)
	for _, k := range []string{"worktrees:", "jobs:", "home:"} {
		if strings.Contains(base, "\n  "+k) {
			t.Fatalf("fixture: o exemplo não pode fixar execution.%s", k)
		}
	}
	cfg, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	if real, err := filepath.EvalSymlinks(wd); err == nil {
		wd = real
	}
	home, _ := os.UserHomeDir()
	root := filepath.Join(home, ".cache", "sentinela", InstallKey(wd))
	e := cfg.Execution
	if e.Worktrees != filepath.Join(root, "worktrees") || e.Jobs != filepath.Join(root, "jobs") || e.Home != filepath.Join(root, "job-home") || e.DefaultDirs != DirWorktrees|DirJobs|DirHome {
		t.Errorf("diretórios = %s, %s, %s (padrão %v), quer em %s", e.Worktrees, e.Jobs, e.Home, e.DefaultDirs, root)
	}
	if len(InstallKey(wd)) != 8 || InstallKey(wd) == InstallKey(wd+"-outra") {
		t.Errorf("chave = %q", InstallKey(wd))
	}

	cfg, err = Parse([]byte(strings.Replace(base, "\nexecution:", "\nexecution:\n  worktrees: /srv/wt", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Execution.Worktrees != "/srv/wt" || cfg.Execution.DefaultDirs != DirJobs|DirHome || !strings.HasPrefix(cfg.Execution.Jobs, root) {
		t.Errorf("explícito: %+v", cfg.Execution)
	}
}
