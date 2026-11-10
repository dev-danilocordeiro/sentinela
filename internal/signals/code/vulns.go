package code

import (
	"context"
	"crypto/sha1" //nolint:gosec // fingerprint, não segurança (mesmo esquema de signals.Fingerprint)
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// Vulnerabilidades de dependência viram um achado por go.mod, montado em código (ADR 0033); as dos
// outros ecossistemas, um por lockfile ou manifesto (vulns_osv.go, ADR 0058). O
// Triager olha o código de um pacote e não vê bug nele: a correção é subir a versão no go.mod.
// Só as vulnerabilidades que o govulncheck marca como chamadas (o código chega na função
// vulnerável) contam; as só importadas ou exigidas entram como resumo no corpo, sem pesar na
// severidade. Sem nenhuma chamada, não há achado. O achado segue para o Judge e a deduplicação.

// VulnAgent é o nome com que a etapa aparece em run_items e no relatório.
const VulnAgent = "govulncheck"

// maxCallSites limita as chamadas listadas como evidência.
const maxCallSites = 5

type vulnEntry struct {
	v     scan.Vuln
	calls []scan.Signal // sinais com posição no código do módulo (quem chega no símbolo)
	refs  []string
}

// VulnFindings monta os achados de vulnerabilidade a partir dos sinais do govulncheck.
func (s *Source) VulnFindings(ctx context.Context) []signals.Finding {
	s.init()
	byMod := map[string][]scan.Signal{}
	for _, sig := range s.ScanSignals {
		if sig.Vuln != nil {
			byMod[sig.Vuln.Manifest] = append(byMod[sig.Vuln.Manifest], sig)
		}
	}
	mods := make([]string, 0, len(byMod))
	for m := range byMod {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	var out []signals.Finding
	for _, manifest := range mods {
		// Go (govulncheck, sem ecossistema) ou os outros (osv-scanner, ADR 0058).
		agent, build := VulnAgent, s.vulnFinding
		if byMod[manifest][0].Vuln.Ecosystem != "" {
			agent, build = OSVAgent, s.osvFinding
		}
		f, why := build(manifest, byMod[manifest])
		if why != "" {
			s.log(ctx, agent, manifest, "skipped", "", nil, why)
			continue
		}
		s.log(ctx, agent, manifest, "ok", "", f, "")
		out = append(out, f)
	}
	return out
}

func (s *Source) vulnFinding(gomod string, sigs []scan.Signal) (signals.Finding, string) {
	called, other := groupVulns(sigs)
	if len(called) == 0 {
		return signals.Finding{}, fmt.Sprintf("nenhuma vulnerabilidade chamada (%d só importada ou exigida)", len(other))
	}
	mf := s.readGoMod(gomod)

	// Alvo por módulo: a maior versão corrigida entre as vulnerabilidades chamadas.
	type target struct{ module, current, fixed string }
	targets := map[string]*target{}
	noFix := []string{}
	for _, e := range called {
		t := targets[e.v.Module]
		if t == nil {
			t = &target{module: e.v.Module, current: e.v.Version}
			targets[e.v.Module] = t
		}
		if e.v.FixedVersion == "" {
			noFix = append(noFix, e.v.OSV)
			continue
		}
		if t.fixed == "" || semver.Compare(e.v.FixedVersion, t.fixed) > 0 {
			t.fixed = e.v.FixedVersion
		}
	}
	order := make([]string, 0, len(targets))
	for m := range targets {
		order = append(order, m)
	}
	sort.Slice(order, func(i, j int) bool { // stdlib primeiro, depois por nome
		if (order[i] == "stdlib") != (order[j] == "stdlib") {
			return order[i] == "stdlib"
		}
		return order[i] < order[j]
	})

	dir := path.Dir(gomod)
	var titleParts, actions, keyParts []string
	for _, m := range order {
		t := targets[m]
		keyParts = append(keyParts, m+"@"+t.fixed)
		name := m
		if m == "stdlib" {
			name = "Go"
		}
		if t.fixed == "" {
			actions = append(actions, fmt.Sprintf("%s %s: sem versão corrigida publicada; avaliar troca do módulo ou mitigação", name, t.current))
			continue
		}
		fixed := t.fixed
		if m == "stdlib" {
			fixed = strings.TrimPrefix(fixed, "v")
			actions = append(actions, fmt.Sprintf("Subir a diretiva `go` do %s de %s para %s (e `toolchain`, se houver), e a imagem de build, se fixar a versão", gomod, strings.TrimPrefix(t.current, "v"), fixed))
		} else {
			actions = append(actions, fmt.Sprintf("`go get %s@%s && go mod tidy` em %s (hoje %s)", m, fixed, dir, t.current))
		}
		titleParts = append(titleParts, name+" para "+fixed)
	}
	title := fmt.Sprintf("Atualiza %s por %d vulnerabilidade(s) chamada(s)", joinPT(titleParts), len(called))
	if len(titleParts) == 0 {
		title = fmt.Sprintf("Mitiga %d vulnerabilidade(s) chamada(s) sem correção publicada", len(called))
	}
	if dir != "." {
		title += " em " + dir
	}

	var ctxb strings.Builder
	where := "do módulo " + dir
	if dir == "." {
		where = "do módulo raiz"
	}
	fmt.Fprintf(&ctxb, "O govulncheck mostra que o código %s chega em funções vulneráveis:\n", where)
	for _, e := range called {
		fix := e.v.FixedVersion
		if fix == "" {
			fix = "sem correção publicada"
		}
		fmt.Fprintf(&ctxb, "- %s (%s %s, corrigido em %s): %s. Símbolo: %s", e.v.OSV, e.v.Module, e.v.Version, fix, e.v.Summary, e.v.Symbol)
		if len(e.calls) > 0 {
			fmt.Fprintf(&ctxb, ", chamado de %s:%d", e.calls[0].File, e.calls[0].Line)
		}
		ctxb.WriteString(".\n")
	}
	if len(other) > 0 {
		type modSummary struct {
			version, fixed string
			ids            []string
		}
		perMod := map[string]*modSummary{}
		for _, e := range other {
			ms := perMod[e.v.Module]
			if ms == nil {
				ms = &modSummary{version: e.v.Version}
				perMod[e.v.Module] = ms
			}
			ms.ids = append(ms.ids, e.v.OSV)
			if e.v.FixedVersion != "" && (ms.fixed == "" || semver.Compare(e.v.FixedVersion, ms.fixed) > 0) {
				ms.fixed = e.v.FixedVersion
			}
		}
		names := make([]string, 0, len(perMod))
		for m := range perMod {
			names = append(names, m)
		}
		sort.Strings(names)
		fmt.Fprintf(&ctxb, "\nSó importadas ou exigidas, sem chamada no código (resumo; não pesam na severidade): %d.", len(other))
		for _, m := range names {
			ms := perMod[m]
			fix := "sem correção publicada"
			if ms.fixed != "" {
				fix = "corrigidas até " + ms.fixed
			}
			fmt.Fprintf(&ctxb, "\n- %s %s (%s): %s", m, ms.version, fix, strings.Join(ms.ids, ", "))
		}
		ctxb.WriteString("\n")
	}

	// Evidência principal: a linha do go.mod que muda (diretiva go ou require do primeiro alvo).
	primary := goModEvidence(mf, gomod, order[0])
	ev := []signals.Evidence{primary}
	seen := map[string]bool{}
	var callFiles []string
	for _, e := range called {
		for _, c := range e.calls {
			k := fmt.Sprintf("%s:%d", c.File, c.Line)
			if seen[k] || len(ev) > maxCallSites {
				continue
			}
			seen[k] = true
			snippet := s.lineAt(c.File, c.Line)
			if snippet == "" {
				continue
			}
			ev = append(ev, signals.Evidence{File: c.File, Line: c.Line, Snippet: snippet})
			callFiles = append(callFiles, c.File)
		}
	}
	for i := range ev {
		ev[i].Permalink = repo.Permalink(s.Checkout.Repo, s.Checkout.HeadSHA, ev[i].File, ev[i].Line)
	}

	scope := []string{gomod, path.Join(dir, "go.sum")}
	res, err := s.Resolver.ResolveFinding(s.Checkout.Repo, repo.FindingPaths{Primary: gomod, Evidence: callFiles})
	if err != nil {
		return signals.Finding{}, err.Error()
	}
	if res.Ignored {
		return signals.Finding{}, "go.mod em módulo ignorado (" + res.Rule + ")"
	}
	criteria := []string{fmt.Sprintf("`govulncheck ./...` em %s não marca como chamada nenhuma de: %s", dir, strings.Join(osvIDs(called), ", "))}
	for _, m := range order {
		if t := targets[m]; t.fixed != "" {
			if m == "stdlib" {
				criteria = append(criteria, fmt.Sprintf("%s declara `go %s` ou maior", gomod, strings.TrimPrefix(t.fixed, "v")))
			} else {
				criteria = append(criteria, fmt.Sprintf("%s exige %s %s ou maior", gomod, m, t.fixed))
			}
		}
	}
	criteria = append(criteria, "Build, testes e lint do repo passam com as versões novas")
	var refs []signals.SignalRef
	for _, e := range called {
		for _, r := range e.refs {
			refs = append(refs, signals.SignalRef{Kind: "scanner", Ref: r, Summary: e.v.OSV + " em " + e.v.Module})
		}
	}
	if len(noFix) > 0 {
		criteria = append(criteria, "Sem correção publicada para "+strings.Join(noFix, ", ")+": mitigação registrada na issue")
	}
	f := signals.Finding{
		Source: signals.SourceCodeAnalysis, Repo: s.Checkout.Repo, Module: dir, Owners: ownersOf(res), Routing: res.Routing,
		Category: "security", Severity: "high", Confidence: 0.9,
		Title: title, Context: strings.TrimSpace(ctxb.String()), Evidence: ev,
		ProposedFix:        strings.Join(actions, "; ") + ". Conferir com `govulncheck ./...` depois da mudança.",
		AcceptanceCriteria: criteria, ScopePaths: scope, Signals: refs,
	}
	// O problema inclui as versões alvo: o fingerprint comum descarta números, e uma correção que
	// passe a exigir outra versão é um achado novo (o Judge vê o anterior pelo go.mod).
	key := "govulncheck " + strings.Join(keyParts, " ")
	sum := sha1.Sum([]byte(f.Repo + "|" + f.Category + "|" + gomod + "|" + key)) //nolint:gosec // ver import
	f.Fingerprint = hex.EncodeToString(sum[:])[:12]
	s.ProblemKeys[f.Fingerprint] = key
	if err := f.Validate(); err != nil {
		return signals.Finding{}, err.Error()
	}
	return f, ""
}

// groupVulns junta os sinais por OSV e módulo. Uma vulnerabilidade é chamada se algum sinal dela
// é de nível called; as demais vão para o resumo.
func groupVulns(sigs []scan.Signal) (called, other []*vulnEntry) {
	idx := map[string]*vulnEntry{}
	var keys []string
	for _, sg := range sigs {
		k := sg.Vuln.OSV + "|" + sg.Vuln.Module
		e := idx[k]
		if e == nil {
			e = &vulnEntry{v: *sg.Vuln}
			idx[k] = e
			keys = append(keys, k)
		}
		if rank(sg.Vuln.Level) > rank(e.v.Level) {
			lvl, sym := sg.Vuln.Level, sg.Vuln.Symbol
			e.v.Level, e.v.Symbol = lvl, sym
		}
		e.refs = append(e.refs, sg.Ref())
		if sg.Vuln.Level == scan.VulnCalled && path.Base(sg.File) != "go.mod" {
			e.calls = append(e.calls, sg)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if e := idx[k]; e.v.Level == scan.VulnCalled {
			called = append(called, e)
		} else {
			other = append(other, e)
		}
	}
	return called, other
}

func rank(level string) int {
	switch level {
	case scan.VulnCalled:
		return 3
	case scan.VulnImported:
		return 2
	}
	return 1
}

func osvIDs(es []*vulnEntry) []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.v.OSV)
	}
	return out
}

func joinPT(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " e " + xs[len(xs)-1]
}

func (s *Source) readGoMod(gomod string) *modfile.File {
	data, err := os.ReadFile(filepath.Join(s.Checkout.Dir, filepath.FromSlash(gomod))) //nolint:gosec // go.mod do checkout
	if err != nil {
		return nil
	}
	mf, err := modfile.ParseLax(gomod, data, nil)
	if err != nil {
		return nil
	}
	return mf
}

// goModEvidence aponta a linha do go.mod que a correção muda: a diretiva go para a stdlib, o
// require do módulo para dependências, ou a linha module quando não há nenhuma das duas.
func goModEvidence(mf *modfile.File, gomod, module string) signals.Evidence {
	ev := signals.Evidence{File: gomod, Line: 1, Snippet: "module"}
	if mf == nil {
		return ev
	}
	if mf.Module != nil && mf.Module.Syntax != nil {
		ev.Line, ev.Snippet = mf.Module.Syntax.Start.Line, "module "+mf.Module.Mod.Path
	}
	if module == "stdlib" {
		if mf.Go != nil && mf.Go.Syntax != nil {
			ev.Line, ev.Snippet = mf.Go.Syntax.Start.Line, "go "+mf.Go.Version
		}
		return ev
	}
	for _, r := range mf.Require {
		if r.Mod.Path == module && r.Syntax != nil {
			ev.Line, ev.Snippet = r.Syntax.Start.Line, r.Mod.Path+" "+r.Mod.Version
		}
	}
	return ev
}

// lineAt devolve o texto da linha (sem espaços nas pontas), ou "" se não existir.
func (s *Source) lineAt(file string, line int) string {
	lines, why := s.evidence.file(file)
	if why != "" || line < 1 || line > len(lines) {
		return ""
	}
	return strings.TrimSpace(lines[line-1])
}
