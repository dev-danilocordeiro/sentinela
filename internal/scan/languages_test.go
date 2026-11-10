package scan

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestScannersFollowTheLanguagesOfTheRepo(t *testing.T) {
	for _, c := range []struct {
		langs map[string]bool
		want  []string
	}{
		{map[string]bool{"go": true}, []string{"golangci-lint", "gosec", "govulncheck", "semgrep"}},
		{map[string]bool{"typescript": true}, []string{"semgrep", "osv-scanner", "eslint", "tsc"}},
		{map[string]bool{"javascript": true}, []string{"semgrep", "osv-scanner", "eslint", "tsc"}},
		{map[string]bool{"java": true, "kotlin": true}, []string{"semgrep", "osv-scanner"}},
		{map[string]bool{"go": true, "typescript": true}, []string{"golangci-lint", "gosec", "govulncheck", "semgrep", "osv-scanner", "eslint", "tsc"}},
		{map[string]bool{"python": true, "config": true}, []string{"semgrep"}},
		{nil, []string{"semgrep"}},
	} {
		if got := ForLanguages(c.langs); !slices.Equal(got, c.want) {
			t.Errorf("ForLanguages(%v) = %v, quer %v", c.langs, got, c.want)
		}
	}
	if got := ScannersOf(map[string]bool{"go": true, "typescript": true})["osv-scanner"]; !slices.Equal(got, []string{"typescript"}) {
		t.Errorf("osv-scanner por = %v", got)
	}
}

// A API de linguagens do GitHub conta bytes; linguagem com menos de 1% do código não puxa scanner.
func TestLanguagesFromGitHubIgnoresTinyShares(t *testing.T) {
	got := LanguagesFromGitHub(map[string]int64{"Go": 90_000, "TypeScript": 9_000, "JavaScript": 500, "Shell": 500, "Kotlin": 0})
	if !got["go"] || !got["typescript"] || got["javascript"] || got["kotlin"] || len(got) != 2 {
		t.Errorf("linguagens = %v", got)
	}
	if len(LanguagesFromGitHub(nil)) != 0 {
		t.Error("sem resposta, sem linguagem")
	}
}

func TestInstalledChecksTheBinaryAndTheNodeTools(t *testing.T) {
	tools := t.TempDir()
	look := func(name string) (string, error) {
		if name == "golangci-lint" || name == "node" {
			return "/usr/bin/" + name, nil
		}
		return "", errors.New("não achou")
	}
	if !Installed("golangci-lint", tools, look) || Installed("gosec", tools, look) {
		t.Error("binário no PATH")
	}
	if Installed("eslint", tools, look) {
		t.Error("eslint sem tools/node instalado")
	}
	p := filepath.Join(tools, filepath.FromSlash(eslintBin))
	_ = os.MkdirAll(filepath.Dir(p), 0o750)
	_ = os.WriteFile(p, []byte("x"), 0o600)
	if !Installed("eslint", tools, look) || Installed("tsc", tools, look) {
		t.Error("eslint com tools/node; tsc sem o typescript")
	}
	noNode := func(string) (string, error) { return "", errors.New("não achou") }
	if Installed("eslint", tools, noNode) {
		t.Error("eslint sem node")
	}
}
