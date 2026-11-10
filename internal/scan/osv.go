package scan

import (
	"encoding/json"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/mod/semver"
)

// OSVScanner lê os lockfiles e manifestos do repo (npm, pnpm, yarn, Maven, Gradle com lockfile e
// outros) e consulta a base OSV (Fase C, passo 4; ADR 0058). Cada vulnerabilidade de cada pacote
// vira um sinal com Vuln, que vai para o achado por manifesto (signals/code), sem passar pela
// triagem por pacote. Go fica com o govulncheck, que sabe se o código chama a função vulnerável.
//
// Flags conferidas em `osv-scanner scan source --help` (v2.6.0):
//   - -r e --format json;
//   - --no-ignore: na v2.6.0, sem ela, a varredura de um clone git para no primeiro diretório
//     ("No package sources found", 1 diretório visitado), mesmo com --include-git-root e um
//     .gitignore que não exclui os lockfiles. Visto em 2026-10-17 num monorepo Java e TS;
//   - --experimental-exclude: node_modules (dependências baixadas, se houver) e .git;
//   - --all-packages: lista também os manifestos sem vulnerabilidade, para a cobertura contar o
//     que foi lido (sem ela, o JSON só traz os manifestos com alguma vulnerabilidade).
//
// O osv-scanner envia nomes e versões dos pacotes para a API do OSV e, para manifestos Maven sem
// lockfile, resolve as dependências transitivas no Maven Central (docs/seguranca.md).
type OSVScanner struct{}

func (OSVScanner) Name() string                 { return "osv-scanner" }
func (OSVScanner) Applies(map[string]bool) bool { return true }
func (OSVScanner) Command(string) (string, []string) {
	return "osv-scanner", []string{"scan", "source", "-r", "--no-ignore",
		"--experimental-exclude", "node_modules", "--experimental-exclude", ".git", "--all-packages", "--format", "json", "."}
}

// osvFiles são os lockfiles e manifestos que o osv-scanner lê, fora Go (o govulncheck cobre).
var osvFiles = map[string]bool{
	"package-lock.json": true, "npm-shrinkwrap.json": true, "pnpm-lock.yaml": true, "yarn.lock": true,
	"pom.xml": true, "gradle.lockfile": true, "buildscript-gradle.lockfile": true, "verification-metadata.xml": true,
	"requirements.txt": true, "poetry.lock": true, "Pipfile.lock": true, "uv.lock": true,
	"Cargo.lock": true, "Gemfile.lock": true, "composer.lock": true, "packages.lock.json": true,
}

// preflight diz por que não rodar (vazio: roda). Sem lockfile nem manifesto, o osv-scanner sai
// com erro; Gradle sem gradle.lockfile não tem as versões resolvidas, e resolvê-las rodaria o
// Gradle, que executa código do repo.
func (OSVScanner) preflight(dir string) string {
	var lock, gradle, gradleLock, goMod bool
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // diretório ilegível não impede achar os outros arquivos
		}
		if d.IsDir() {
			if p != dir && (d.Name() == "node_modules" || d.Name() == "vendor" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		switch n := d.Name(); {
		case n == "gradle.lockfile" || n == "verification-metadata.xml":
			gradleLock, lock = true, true
		case n == "build.gradle" || n == "build.gradle.kts":
			gradle = true
		case n == "go.mod":
			goMod = true
		case osvFiles[n]:
			lock = true
		}
		return nil
	})
	switch {
	case gradle && !gradleLock && !lock:
		return "sem lockfile do Gradle (ative dependency locking: gradle.lockfile); resolver as versões rodaria o Gradle, que executa código do repo"
	case !lock && goMod:
		return "sem lockfile nem manifesto fora de Go (as dependências Go ficam com o govulncheck)"
	case !lock:
		return "sem lockfile nem manifesto (package-lock.json, pnpm-lock.yaml, yarn.lock, pom.xml, gradle.lockfile...)"
	}
	return ""
}

// note é o aviso que acompanha um resultado ok: o que o osv-scanner leu sem conseguir conferir.
// Pacote sem versão (no pom.xml, a versão vem do parent ou de um BOM que ele não resolveu) não casa
// com vulnerabilidade nenhuma; módulos Gradle sem gradle.lockfile ficam de fora.
func (OSVScanner) note(out []byte, dir string) string {
	var notes []string
	var o osvOutput
	if json.Unmarshal(out, &o) == nil {
		unresolved := 0
		for _, r := range o.Results {
			for _, p := range r.Packages {
				if p.Package.Version == "" && !strings.EqualFold(p.Package.Ecosystem, "Go") {
					unresolved++
				}
			}
		}
		if unresolved > 0 {
			notes = append(notes, strconv.Itoa(unresolved)+" pacote(s) sem versão resolvida (gerenciada por parent ou BOM), não conferidos")
		}
	}
	var gradle, gradleLock bool
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // diretório ilegível não impede achar os outros arquivos
		}
		if d.IsDir() && p != dir && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		switch d.Name() {
		case "build.gradle", "build.gradle.kts":
			gradle = true
		case "gradle.lockfile":
			gradleLock = true
		}
		return nil
	})
	if gradle && !gradleLock {
		notes = append(notes, "módulos Gradle sem gradle.lockfile ficaram de fora")
	}
	return strings.Join(notes, "; ")
}

type osvOutput struct {
	Results []struct {
		Source struct {
			Path string `json:"path"`
			Type string `json:"type"`
		} `json:"source"`
		Packages []struct {
			Package struct {
				Name      string `json:"name"`
				Version   string `json:"version"`
				Ecosystem string `json:"ecosystem"`
			} `json:"package"`
			DependencyGroups []string `json:"dependency_groups"`
			Vulnerabilities  []struct {
				ID       string `json:"id"`
				Summary  string `json:"summary"`
				Affected []struct {
					Package struct {
						Name string `json:"name"`
					} `json:"package"`
					Ranges []struct {
						Events []struct {
							Fixed string `json:"fixed"`
						} `json:"events"`
					} `json:"ranges"`
				} `json:"affected"`
				DatabaseSpecific struct {
					Severity string `json:"severity"`
				} `json:"database_specific"`
			} `json:"vulnerabilities"`
			Groups []struct {
				IDs         []string `json:"ids"`
				MaxSeverity string   `json:"max_severity"`
			} `json:"groups"`
		} `json:"packages"`
	} `json:"results"`
}

func (OSVScanner) Parse(out []byte, dir string) ([]Signal, error) {
	var o osvOutput
	if err := json.Unmarshal(out, &o); err != nil {
		return nil, err
	}
	var sigs []Signal
	for _, r := range o.Results {
		manifest := rel(dir, r.Source.Path)
		for _, p := range r.Packages {
			if strings.EqualFold(p.Package.Ecosystem, "Go") {
				continue // o govulncheck cobre Go, com o alcance da chamada
			}
			cvss := map[string]string{}
			for _, g := range p.Groups {
				for _, id := range g.IDs {
					cvss[id] = g.MaxSeverity
				}
			}
			dev := false
			for _, g := range p.DependencyGroups {
				if g == "dev" || g == "test" {
					dev = true // npm devDependencies, Maven <scope>test</scope>
				}
			}
			for _, v := range p.Vulnerabilities {
				// A correção é a menor versão corrigida acima da que está em uso: uma vulnerabilidade
				// com faixas por série (15.x corrigida em 15.5.27, 16.x em 16.3.8) não manda subir de série.
				fixed := ""
				for _, a := range v.Affected {
					if a.Package.Name != "" && a.Package.Name != p.Package.Name {
						continue
					}
					for _, rg := range a.Ranges {
						for _, e := range rg.Events {
							if e.Fixed == "" || (p.Package.Version != "" && !versionLess(p.Package.Version, e.Fixed)) {
								continue
							}
							if fixed == "" || versionLess(e.Fixed, fixed) {
								fixed = e.Fixed
							}
						}
					}
				}
				sev := osvSeverity(v.DatabaseSpecific.Severity, cvss[v.ID])
				sigs = append(sigs, Signal{
					Scanner: "osv-scanner", Rule: v.ID, File: manifest, Line: 1, Severity: sev,
					Message: p.Package.Name + " " + p.Package.Version + ": " + v.Summary,
					Vuln: &Vuln{OSV: v.ID, Summary: v.Summary, Module: p.Package.Name, Version: p.Package.Version,
						FixedVersion: fixed, Manifest: manifest, Ecosystem: p.Package.Ecosystem, Severity: sev, Dev: dev},
				})
			}
		}
	}
	return sigs, nil
}

// Coverage conta os lockfiles e manifestos lidos.
func (OSVScanner) Coverage(out []byte, _ string) (int, string) {
	var o osvOutput
	if json.Unmarshal(out, &o) != nil {
		return 0, "manifestos"
	}
	return len(o.Results), "manifestos"
}

// osvSeverity usa o nível do GHSA (CRITICAL, HIGH, MODERATE, LOW) e, sem ele, a nota CVSS.
func osvSeverity(ghsa, cvss string) string {
	switch strings.ToUpper(ghsa) {
	case "CRITICAL":
		return "critical"
	case "HIGH":
		return "high"
	case "MODERATE", "MEDIUM":
		return "moderate"
	case "LOW":
		return "low"
	}
	score, err := strconv.ParseFloat(cvss, 64)
	switch {
	case err != nil:
		return "unknown"
	case score >= 9:
		return "critical"
	case score >= 7:
		return "high"
	case score >= 4:
		return "moderate"
	}
	return "low"
}

// versionLess compara versões semver com ou sem "v"; fora de semver, pela ordem dos números.
func versionLess(a, b string) bool {
	va, vb := "v"+strings.TrimPrefix(a, "v"), "v"+strings.TrimPrefix(b, "v")
	if semver.IsValid(va) && semver.IsValid(vb) {
		return semver.Compare(va, vb) < 0
	}
	pa, pb := strings.FieldsFunc(a, notDigit), strings.FieldsFunc(b, notDigit)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

func notDigit(r rune) bool { return r < '0' || r > '9' }
