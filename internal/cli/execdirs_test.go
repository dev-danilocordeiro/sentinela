package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// Instalação de antes da ADR 0056, com execuções nos diretórios compartilhados: continua neles,
// para não perder worktrees, artefatos e sessões dos jobs em andamento. Instalação nova, ou com
// diretórios no arquivo, fica onde a config diz.
func TestLegacyExecutionDirsKeepAnInstallationThatAlreadyUsesThem(t *testing.T) {
	t.Run("só o home no padrão", func(t *testing.T) {
		// A instalação fixa worktrees e jobs nos diretórios antigos e deixa o home no padrão: as
		// sessões dos jobs estão no HOME antigo, então ele também continua lá.
		wt, jobs, home := config.LegacyExecutionDirs()
		cfg := &config.Config{}
		cfg.Execution.Worktrees, cfg.Execution.Jobs, cfg.Execution.Home = wt, jobs, "/novo/job-home"
		cfg.Execution.DefaultDirs = config.DirHome
		got, err := useLegacyExecutionDirs(context.Background(), cfg, func(context.Context, string) (bool, error) { return true, nil })
		if err != nil || !got || cfg.Execution.Home != home || cfg.Execution.Worktrees != wt {
			t.Errorf("legado = %v (%v), diretórios = %+v", got, err, cfg.Execution)
		}
	})
	wt, jobs, home := config.LegacyExecutionDirs()
	for _, tc := range []struct {
		name     string
		defaults bool
		inUse    bool
		legacy   bool
	}{
		{"padrão e execuções nos antigos", true, true, true},
		{"padrão sem execuções nos antigos", true, false, false},
		{"diretórios no arquivo", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			if tc.defaults {
				cfg.Execution.DefaultDirs = config.DirWorktrees | config.DirJobs | config.DirHome
			}
			cfg.Execution.Worktrees, cfg.Execution.Jobs, cfg.Execution.Home = "/novo/worktrees", "/novo/jobs", "/novo/job-home"
			var asked string
			got, err := useLegacyExecutionDirs(context.Background(), cfg, func(_ context.Context, prefix string) (bool, error) {
				asked = prefix
				return tc.inUse, nil
			})
			if err != nil || got != tc.legacy {
				t.Fatalf("legado = %v (%v), quer %v", got, err, tc.legacy)
			}
			e := cfg.Execution
			if tc.legacy && (e.Worktrees != wt || e.Jobs != jobs || e.Home != home || !strings.HasSuffix(asked, "/worktrees/")) {
				t.Errorf("diretórios = %+v, consulta = %q", e, asked)
			}
			if !tc.legacy && e.Worktrees != "/novo/worktrees" {
				t.Errorf("não deveria mudar: %+v", e)
			}
		})
	}
}
