package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// unownedConfig é o sentinela.yaml do modo local com os times backend e frontend, o repo
// acme/mono por módulos e o workdir dado; extra entra no fim do repo (linhas recuadas).
func unownedConfig(t *testing.T, workdir, extraRepo string) *config.Config {
	t.Helper()
	y := LocalConfig([]string{"acme/mono"})
	y = strings.Replace(y, "  dono:                   # um time só; troque e acrescente times quando o CODEOWNERS pedir\n    policy: { auto_approve: false }\n",
		"  backend: {}\n  frontend: {}\n", 1)
	y = strings.Replace(y, "discovery:\n", "discovery:\n  workdir: "+workdir+"\n", 1)
	y = strings.Replace(y, "  - { name: acme/mono, team: dono }\n",
		"  - name: acme/mono\n    modules:\n      - { path: \"api/**\", team: backend }\n      - { path: \"web/**\", team: frontend }\n"+extraRepo, 1)
	cfg, err := config.Parse([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeClone(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, f := range files {
		p := filepath.Join(root, "acme", "mono", filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// Caminhos que caem em defaults sem time viram aviso com exemplos e a sugestão de fallback_team;
// com fallback_team (ou um módulo que cobre tudo), não há aviso. Sem clone, o aviso é pela config.
func TestUnownedPathsWarnAndSuggestAFallbackTeam(t *testing.T) {
	for _, tc := range []struct {
		name  string
		clone bool
		extra string
		want  []string
	}{
		{"clone com arquivos da raiz", true, "", []string{"acme/mono: 3 arquivo(s) caem em defaults, sem time", "Makefile", ".github/workflows/ci.yml", "fallback_team: backend"}},
		{"com fallback_team", true, "    fallback_team: backend\n", nil},
		{"módulo que cobre tudo", true, "      - { path: \"**\", team: backend }\n", nil},
		{"sem clone", false, "", []string{"acme/mono: sem team nem fallback_team", "caem em defaults, sem time", "fallback_team: backend"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.clone {
				writeClone(t, root, "api/src/A.java", "web/src/b.ts", "Makefile", "docker-compose.yml", ".github/workflows/ci.yml", ".git/HEAD")
			}
			cfg := unownedConfig(t, root, tc.extra)
			got := strings.Join(unownedPathWarnings(cfg), "\n")
			if tc.want == nil && got != "" {
				t.Errorf("sem aviso, veio: %s", got)
			}
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("aviso sem %q: %s", w, got)
				}
			}
		})
	}
}
