package code

import (
	"crypto/sha1" //nolint:gosec // fingerprint, não segurança (mesmo esquema de signals.Fingerprint)
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// Vulnerabilidades do osv-scanner viram um achado por lockfile ou manifesto (ADR 0058,
// generalizando a ADR 0033). O osv-scanner não diz se o código chama o trecho vulnerável, então
// entram no achado as vulnerabilidades de produção com versão corrigida e severidade alta ou
// crítica; as outras (moderadas e baixas, só de desenvolvimento ou teste, sem correção) vão como
// resumo no corpo. Sem nenhuma que entre, não há achado. A correção é subir a dependência, e a
// evidência é a linha dela no lockfile ou no manifesto.

// OSVAgent é o nome com que a etapa aparece em run_items e no relatório.
const OSVAgent = "osv-scanner"

// osvTarget é a dependência a subir: a maior das versões corrigidas das vulnerabilidades dela.
type osvTarget struct {
	name, current, fixed string
	ids                  []string
	severity             string
}

func (s *Source) osvFinding(manifest string, sigs []scan.Signal) (signals.Finding, string) {
	seen := map[string]bool{}
	var actionable, other []scan.Vuln
	for _, sg := range sigs {
		v := *sg.Vuln
		k := v.OSV + "|" + v.Module
		if seen[k] {
			continue
		}
		seen[k] = true
		if !v.Dev && v.FixedVersion != "" && (v.Severity == "high" || v.Severity == "critical") {
			actionable = append(actionable, v)
		} else {
			other = append(other, v)
		}
	}
	if len(actionable) == 0 {
		return signals.Finding{}, fmt.Sprintf("nenhuma vulnerabilidade alta ou crítica de produção com correção (%d no resumo)", len(other))
	}
	eco := actionable[0].Ecosystem
	targets := map[string]*osvTarget{}
	for _, v := range actionable {
		t := targets[v.Module]
		if t == nil {
			t = &osvTarget{name: v.Module, current: v.Version, severity: v.Severity}
			targets[v.Module] = t
		}
		t.ids = append(t.ids, v.OSV)
		if t.fixed == "" || osvVersionLess(t.fixed, v.FixedVersion) {
			t.fixed = v.FixedVersion
		}
		if v.Severity == "critical" {
			t.severity = "critical"
		}
	}
	names := make([]string, 0, len(targets))
	for n := range targets {
		names = append(names, n)
	}
	sort.Strings(names)

	dir := path.Dir(manifest)
	severity := "high"
	var titleParts, actions, keyParts, criteria []string
	for _, n := range names {
		t := targets[n]
		if t.severity == "critical" {
			severity = "critical"
		}
		titleParts = append(titleParts, n+" para "+t.fixed)
		keyParts = append(keyParts, n+"@"+t.fixed)
		actions = append(actions, osvAction(eco, manifest, t))
		criteria = append(criteria, fmt.Sprintf("%s resolve %s %s ou maior (hoje %s)", manifest, n, t.fixed, orDash(t.current)))
	}
	title := fmt.Sprintf("Atualiza %s por %d vulnerabilidade(s) alta(s) ou crítica(s)", joinPT(titleParts), len(actionable))
	if dir != "." {
		title += " em " + dir
	}

	var ctxb strings.Builder
	fmt.Fprintf(&ctxb, "O osv-scanner encontrou em %s (%s) dependências de produção com vulnerabilidade alta ou crítica e versão corrigida:\n", manifest, eco)
	for _, v := range actionable {
		fmt.Fprintf(&ctxb, "- %s (%s %s, %s, corrigido em %s): %s.\n", v.OSV, v.Module, v.Version, v.Severity, v.FixedVersion, strings.TrimSuffix(v.Summary, "."))
	}
	ctxb.WriteString("O osv-scanner não diz se o código chama o trecho vulnerável; a correção é subir a versão.\n")
	if len(other) > 0 {
		fmt.Fprintf(&ctxb, "\nNo resumo, sem pesar na severidade (moderadas ou baixas, só de desenvolvimento ou teste, ou sem correção publicada): %d.", len(other))
		sort.Slice(other, func(i, j int) bool { return other[i].Module+other[i].OSV < other[j].Module+other[j].OSV })
		for _, v := range other {
			why := v.Severity
			if v.Dev {
				why += ", só desenvolvimento ou teste"
			}
			fix := "corrigido em " + v.FixedVersion
			if v.FixedVersion == "" {
				fix = "sem correção publicada"
			}
			fmt.Fprintf(&ctxb, "\n- %s %s: %s (%s; %s)", v.Module, v.Version, v.OSV, why, fix)
		}
		ctxb.WriteString("\n")
	}

	lines := s.manifestLines(manifest)
	var ev []signals.Evidence
	for _, n := range names {
		line, snippet := dependencyLine(lines, n)
		ev = append(ev, signals.Evidence{File: manifest, Line: line, Snippet: snippet})
		if len(ev) >= maxCallSites {
			break
		}
	}
	for i := range ev {
		ev[i].Permalink = repo.Permalink(s.Checkout.Repo, s.Checkout.HeadSHA, ev[i].File, ev[i].Line)
	}
	res, err := s.Resolver.ResolveFinding(s.Checkout.Repo, repo.FindingPaths{Primary: manifest})
	if err != nil {
		return signals.Finding{}, err.Error()
	}
	if res.Ignored {
		return signals.Finding{}, manifest + " em módulo ignorado (" + res.Rule + ")"
	}
	var ids []string
	for _, v := range actionable {
		ids = append(ids, v.OSV)
	}
	criteria = append([]string{fmt.Sprintf("`osv-scanner scan source -r .` não lista em %s nenhuma de: %s", manifest, strings.Join(ids, ", "))}, criteria...)
	criteria = append(criteria, "Build, testes e lint do repo passam com as versões novas")
	var refs []signals.SignalRef
	for _, v := range actionable {
		refs = append(refs, signals.SignalRef{Kind: "scanner", Ref: "osv-scanner:" + v.OSV + ":" + manifest, Summary: v.OSV + " em " + v.Module})
	}
	f := signals.Finding{
		Source: signals.SourceCodeAnalysis, Repo: s.Checkout.Repo, Module: dir, Owners: ownersOf(res), Routing: res.Routing,
		Category: "security", Severity: severity, Confidence: 0.9,
		Title: title, Context: strings.TrimSpace(ctxb.String()), Evidence: ev,
		ProposedFix:        strings.Join(actions, "; ") + ". Conferir com `osv-scanner scan source -r .` depois da mudança.",
		AcceptanceCriteria: criteria, ScopePaths: osvScope(manifest), Signals: refs,
	}
	// Como no Go (ADR 0033): o problema inclui as versões alvo, e uma correção que passe a exigir
	// outra versão é um achado novo.
	key := "osv-scanner " + strings.Join(keyParts, " ")
	sum := sha1.Sum([]byte(f.Repo + "|" + f.Category + "|" + manifest + "|" + key)) //nolint:gosec // ver import
	f.Fingerprint = hex.EncodeToString(sum[:])[:12]
	s.ProblemKeys[f.Fingerprint] = key
	if err := f.Validate(); err != nil {
		return signals.Finding{}, err.Error()
	}
	return f, ""
}

// osvAction diz como subir a dependência no ecossistema do manifesto.
func osvAction(eco, manifest string, t *osvTarget) string {
	dir := path.Dir(manifest)
	switch strings.ToLower(eco) {
	case "npm":
		return fmt.Sprintf("Subir %s para %s ou maior em %s: dependência direta, `npm install %s@%s`; transitiva, `npm update %s` ou `overrides` no package.json (hoje %s)", t.name, t.fixed, dir, t.name, t.fixed, t.name, orDash(t.current))
	case "maven":
		return fmt.Sprintf("Subir %s para %s ou maior no %s, ou no parent ou BOM que gerencia a versão (hoje %s)", t.name, t.fixed, manifest, orDash(t.current))
	}
	return fmt.Sprintf("Subir %s para %s ou maior em %s (hoje %s)", t.name, t.fixed, manifest, orDash(t.current))
}

// osvScope são os arquivos que a correção muda: o manifesto e, no npm, o package.json ao lado.
func osvScope(manifest string) []string {
	dir := path.Dir(manifest)
	switch path.Base(manifest) {
	case "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "npm-shrinkwrap.json":
		return []string{path.Join(dir, "package.json"), manifest}
	}
	return []string{manifest}
}

func (s *Source) manifestLines(manifest string) []string {
	data, err := os.ReadFile(filepath.Join(s.Checkout.Dir, filepath.FromSlash(manifest))) //nolint:gosec // arquivo do checkout
	if err != nil {
		return nil
	}
	return strings.Split(string(data), "\n")
}

// dependencyLine acha a linha da dependência no lockfile ou manifesto; sem ela, a linha 1.
func dependencyLine(lines []string, name string) (int, string) {
	artifact := name
	if i := strings.LastIndex(name, ":"); i >= 0 {
		artifact = name[i+1:]
	}
	patterns := []string{
		`"node_modules/` + name + `"`,      // package-lock.json
		"/" + name + "@", " " + name + "@", // pnpm-lock.yaml
		`"` + name + "@", "\n" + name + "@", // yarn.lock
		"<artifactId>" + artifact + "</artifactId>", // pom.xml
		name + ":", // gradle.lockfile
	}
	for _, p := range patterns {
		for i, l := range lines {
			if strings.Contains("\n"+l, p) {
				return i + 1, strings.TrimSpace(l)
			}
		}
	}
	for i, l := range lines {
		if strings.Contains(l, name) {
			return i + 1, strings.TrimSpace(l)
		}
	}
	first := ""
	if len(lines) > 0 {
		first = strings.TrimSpace(lines[0])
	}
	return 1, first
}

// osvVersionLess compara versões com ou sem "v" pelos números (semver ou não).
func osvVersionLess(a, b string) bool {
	pa, pb := strings.FieldsFunc(a, notDigitRune), strings.FieldsFunc(b, notDigitRune)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}

func notDigitRune(r rune) bool { return r < '0' || r > '9' }

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
