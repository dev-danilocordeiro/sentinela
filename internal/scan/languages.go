package scan

import (
	"os/exec"
	"slices"
	"strings"
)

// Scanners por linguagem, para o sentinela.yaml que o init e o config bootstrap geram e para o
// aviso do config validate: Go tem golangci-lint, gosec e govulncheck; TypeScript e JavaScript têm
// eslint, tsc e osv-scanner (lockfiles do npm, pnpm e yarn); Java e Kotlin têm osv-scanner
// (Maven e Gradle com lockfile); o semgrep vale sempre, com os pacotes de regras de cada linguagem
// (discovery.semgrep_languages).

// AllScanners é a ordem canônica dos scanners da descoberta, sem o go-test (executa código do repo
// e só entra à mão).
var AllScanners = []string{"golangci-lint", "gosec", "govulncheck", "semgrep", "osv-scanner", "eslint", "tsc"}

var scannersByLanguage = map[string][]string{
	"go":         {"golangci-lint", "gosec", "govulncheck"},
	"typescript": {"eslint", "tsc", "osv-scanner"},
	"javascript": {"eslint", "tsc", "osv-scanner"},
	"java":       {"osv-scanner"},
	"kotlin":     {"osv-scanner"},
}

// ScannerLanguages são as linguagens com scanner próprio, em ordem.
var ScannerLanguages = []string{"go", "java", "javascript", "kotlin", "typescript"}

// ForLanguages devolve os scanners das linguagens dadas, na ordem de AllScanners, com o semgrep
// sempre. Num pacote JS sem tsconfig, o tsc só aparece pulado.
func ForLanguages(langs map[string]bool) []string {
	want := map[string]bool{"semgrep": true}
	for l, ok := range langs {
		if !ok {
			continue
		}
		for _, s := range scannersByLanguage[l] {
			want[s] = true
		}
	}
	var out []string
	for _, s := range AllScanners {
		if want[s] {
			out = append(out, s)
		}
	}
	return out
}

// ScannersOf diz quais linguagens de langs pedem o scanner (para o aviso do config validate).
func ScannersOf(langs map[string]bool) map[string][]string {
	out := map[string][]string{}
	for _, l := range ScannerLanguages {
		if !langs[l] {
			continue
		}
		for _, s := range scannersByLanguage[l] {
			out[s] = append(out[s], l)
		}
	}
	return out
}

// githubLanguages traduz os nomes do linguist (a API /repos/{repo}/languages do GitHub).
var githubLanguages = map[string]string{
	"Go": "go", "TypeScript": "typescript", "JavaScript": "javascript", "Java": "java", "Kotlin": "kotlin",
}

// LanguagesFromGitHub lê a resposta de GET /repos/{repo}/languages (bytes por linguagem,
// https://docs.github.com/en/rest/repos/repos#list-repository-languages): ficam as linguagens com
// ao menos 1% do código, para um script solto (um eslint.config.mjs num repo Go) não puxar os
// scanners de outra linguagem.
func LanguagesFromGitHub(bytes map[string]int64) map[string]bool {
	var total int64
	for _, n := range bytes {
		total += n
	}
	out := map[string]bool{}
	for name, n := range bytes {
		if l, ok := githubLanguages[name]; ok && total > 0 && n*100 >= total {
			out[l] = true
		}
	}
	return out
}

// Installed diz se o scanner roda nesta máquina: o binário no PATH e, para eslint e tsc, o node e
// as ferramentas de Node do Sentinela (nodeTools).
func Installed(name, tools string, look func(string) (string, error)) bool {
	if look == nil {
		look = exec.LookPath
	}
	switch name {
	case "eslint", "tsc":
		bin := eslintBin
		if name == "tsc" {
			bin = tscBin
		}
		if _, err := look("node"); err != nil {
			return false
		}
		return tools != "" && nodeTools(tools).missing(bin) == ""
	case "":
		return false
	}
	_, err := look(name)
	return err == nil
}

// LanguageList escreve as linguagens em ordem ("go, typescript").
func LanguageList(langs map[string]bool) string {
	var out []string
	for l, ok := range langs {
		if ok {
			out = append(out, l)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}
