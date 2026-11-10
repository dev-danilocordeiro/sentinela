// Package scan executa scanners determinísticos sobre um checkout e normaliza a saída
// em ScanSignal. Formatos e flags conferidos rodando cada ferramenta (golangci-lint 2.14,
// gosec v2, govulncheck v1.8, go 1.27) e, para o semgrep, na documentação:
// https://semgrep.dev/docs/cli-reference e https://semgrep.dev/docs/semgrep-ci/json-output
// (ADR 0010).
package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/version"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
)

// Signal é um apontamento de scanner, já com caminho relativo ao repo.
type Signal struct {
	Scanner  string `json:"scanner"`
	Rule     string `json:"rule"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Severity string `json:"severity"`
	Message  string `json:"message"`
	// Package é o diretório do pacote quando o sinal não aponta um arquivo (ex.: teste falhando).
	Package string `json:"package,omitempty"`
	// Vuln detalha um sinal do govulncheck: vira achado por módulo no go.mod, em código, sem
	// passar pela triagem por pacote (ADR 0033).
	Vuln *Vuln `json:"vuln,omitempty"`
}

// Níveis de uma vulnerabilidade no govulncheck: o código chama a função vulnerável, só importa
// o pacote, ou só exige o módulo.
const (
	VulnCalled   = "called"
	VulnImported = "imported"
	VulnRequired = "required"
)

// Vuln é uma vulnerabilidade numa dependência: do govulncheck (Go, com o nível de alcance) ou do
// osv-scanner (os outros ecossistemas, por lockfile ou manifesto; ADR 0058).
type Vuln struct {
	OSV          string `json:"osv"`
	Summary      string `json:"summary,omitempty"`
	Module       string `json:"module"`        // "stdlib", o caminho do módulo Go ou o nome do pacote
	Version      string `json:"version"`       // versão em uso
	FixedVersion string `json:"fixed_version"` // vazio: sem correção publicada
	Level        string `json:"level"`         // Go: called | imported | required; osv-scanner: vazio
	Symbol       string `json:"symbol,omitempty"`
	// Manifest é o go.mod do módulo analisado (Go) ou o lockfile ou manifesto (osv-scanner),
	// relativo ao repo.
	Manifest string `json:"manifest"`
	// Ecosystem é o do osv-scanner (npm, Maven...); vazio no govulncheck (Go).
	Ecosystem string `json:"ecosystem,omitempty"`
	// Severity (osv-scanner): critical | high | moderate | low, do GHSA ou da nota CVSS.
	Severity string `json:"severity,omitempty"`
	// Dev (osv-scanner): a dependência é só de desenvolvimento no lockfile.
	Dev bool `json:"dev,omitempty"`
}

// Dir devolve o diretório (pacote) ao qual o sinal pertence.
func (s Signal) Dir() string {
	if s.Package != "" {
		return s.Package
	}
	return path.Dir(s.File)
}

// Ref identifica o sinal de forma estável (para Finding.signals).
func (s Signal) Ref() string {
	return fmt.Sprintf("%s:%s:%s:%d", s.Scanner, s.Rule, s.File, s.Line)
}

// Scanner é um adaptador para uma ferramenta.
type Scanner interface {
	Name() string
	// Applies diz se o scanner faz sentido para as linguagens presentes no repo.
	Applies(languages map[string]bool) bool
	// Command devolve o binário e os argumentos; o binário precisa estar no PATH.
	Command(dir string) (bin string, args []string)
	Parse(out []byte, dir string) ([]Signal, error)
	// Coverage diz quanto a ferramenta analisou, pela própria saída (arquivos, pacotes). Zero
	// com a ferramenta tendo terminado sem erro vira failed: nunca "ok" sem prova de análise.
	Coverage(out []byte, dir string) (n int, unit string)
}

// Estados de um scanner na rodada.
const (
	StatusOK      = "ok"      // rodou e analisou Analyzed <Unit>
	StatusSkipped = "skipped" // não rodou, com motivo (não se aplica, binário ausente)
	StatusFailed  = "failed"  // rodou e falhou, ou não analisou nada; Detail traz a saída
)

// filteringParser é opcional: adaptadores que descartam sinais já resolvidos dizem quantos.
type filteringParser interface {
	ParseFiltered(out []byte, dir string) (sigs []Signal, dropped int, err error)
}

// preflighter é opcional: o adaptador diz, antes de rodar, por que não se aplica ao checkout
// (falta o arquivo que ele lê); a resposta vira skipped com o motivo.
type preflighter interface {
	preflight(dir string) string
}

// languageAware é opcional: o adaptador se ajusta às linguagens do repo antes de rodar.
type languageAware interface {
	forLanguages(langs map[string]bool) Scanner
}

// noter é opcional: um aviso que acompanha um resultado ok (o que ficou de fora), pela saída da
// ferramenta e pelo checkout.
type noter interface {
	note(out []byte, dir string) string
}

// perUnit marca os adaptadores que rodam uma vez por unidade de build (repo.BuildUnits) do tipo
// deles, com os caminhos voltando relativos à raiz do repo (Fase C, passo 3). Começou com os de Go
// (perGoModule): `./...` na raiz de um monorepo sem go.mod na raiz não acha pacote nenhum
// (golangci-lint e gosec falham; govulncheck devolve zero sinais, um falso "ok"). Visto no
// monorepo-poliglota, cujo módulo Go fica em go/.
type perUnit interface {
	runsIn(u repo.BuildUnit) bool
	// noUnits é o motivo do skipped quando o repo não tem unidade do tipo.
	noUnits() string
}

// unitSkipper é opcional, para os perUnit: o adaptador diz, antes de rodar numa unidade (diretório
// absoluto), por que não roda nela. Com todas puladas, o scanner fica skipped com os motivos; com
// alguma rodando, os motivos vão na nota do ok.
type unitSkipper interface {
	skipUnit(dir string) string
}

// goUnit é o perUnit dos adaptadores de Go: um módulo por go.mod.
type goUnit struct{}

func (goUnit) runsIn(u repo.BuildUnit) bool { return u.Kind == repo.KindGo }
func (goUnit) noUnits() string              { return "nenhum go.mod no repo" }

func (GolangciLint) runsIn(u repo.BuildUnit) bool { return goUnit{}.runsIn(u) }
func (GolangciLint) noUnits() string              { return goUnit{}.noUnits() }
func (Gosec) runsIn(u repo.BuildUnit) bool        { return goUnit{}.runsIn(u) }
func (Gosec) noUnits() string                     { return goUnit{}.noUnits() }
func (Govulncheck) runsIn(u repo.BuildUnit) bool  { return goUnit{}.runsIn(u) }
func (Govulncheck) noUnits() string               { return goUnit{}.noUnits() }
func (GoTest) runsIn(u repo.BuildUnit) bool       { return goUnit{}.runsIn(u) }
func (GoTest) noUnits() string                    { return goUnit{}.noUnits() }

// GoModules lista os diretórios do checkout que têm go.mod, relativos e com "/" ("." é a raiz).
func GoModules(dir string) []string {
	var mods []string
	for _, u := range repo.BuildUnits(dir) {
		if u.Kind == repo.KindGo {
			mods = append(mods, u.Dir)
		}
	}
	return mods
}

// Result resume a execução de um scanner.
type Result struct {
	Scanner string `json:"scanner"`
	Status  string `json:"status"` // ok | skipped | failed (rodadas antigas: unavailable, not_applicable)
	Detail  string `json:"detail,omitempty"`
	Signals int    `json:"signals"`
	// Analyzed é quanto a ferramenta analisou, em Unit ("arquivos", "pacotes"), somado por módulo.
	Analyzed int    `json:"analyzed"`
	Unit     string `json:"unit,omitempty"`
	// Filtered conta sinais descartados pelo adaptador antes da triagem (ex.: vulnerabilidade
	// já corrigida na versão declarada no go.mod).
	Filtered int           `json:"filtered,omitempty"`
	Duration time.Duration `json:"duration"`
	// Units conta as unidades de build em que o scanner rodou (scanners por unidade).
	Units int `json:"units,omitempty"`
}

// State normaliza o status (rodadas antigas gravavam unavailable e not_applicable).
func (r Result) State() string {
	switch r.Status {
	case "unavailable", "not_applicable":
		return StatusSkipped
	}
	return r.Status
}

// Summary descreve o resultado numa linha: o que analisou, por que pulou ou como falhou.
func (r Result) Summary() string {
	switch r.State() {
	case StatusOK:
		if r.Detail != "" {
			return fmt.Sprintf("%s ok (%d %s, %d sinais; %s)", r.Scanner, r.Analyzed, orDefault(r.Unit, "itens"), r.Signals, r.Detail)
		}
		return fmt.Sprintf("%s ok (%d %s, %d sinais)", r.Scanner, r.Analyzed, orDefault(r.Unit, "itens"), r.Signals)
	case StatusSkipped:
		return fmt.Sprintf("%s pulado (%s)", r.Scanner, orDefault(r.Detail, "sem motivo registrado"))
	default:
		return fmt.Sprintf("%s falhou (%s)", r.Scanner, truncate(orDefault(r.Detail, "sem saída"), 200))
	}
}

// Runner executa os scanners habilitados com timeout por ferramenta.
type Runner struct {
	Scanners []Scanner
	Timeout  time.Duration
	LookPath func(string) (string, error)
}

// Run roda cada scanner aplicável e disponível. Falha de um scanner não derruba a rodada:
// vira Result com status failed.
func (r *Runner) Run(ctx context.Context, dir string, languages map[string]bool) ([]Signal, []Result) {
	look := r.LookPath
	if look == nil {
		look = exec.LookPath
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	var all []Signal
	var results []Result
	var units []repo.BuildUnit
	unitsRead := false
	for _, s := range r.Scanners {
		if la, ok := s.(languageAware); ok {
			s = la.forLanguages(languages)
		}
		res := Result{Scanner: s.Name()}
		if !s.Applies(languages) {
			res.Status, res.Detail = StatusSkipped, "não se aplica às linguagens do repo"
			results = append(results, res)
			continue
		}
		bin, _ := s.Command(dir)
		if _, err := look(bin); err != nil {
			res.Status, res.Detail = StatusSkipped, bin+" não está no PATH"
			results = append(results, res)
			continue
		}
		if pf, ok := s.(preflighter); ok {
			if why := pf.preflight(dir); why != "" {
				res.Status, res.Detail = StatusSkipped, why
				results = append(results, res)
				continue
			}
		}
		mods := []string{"."}
		if pu, ok := s.(perUnit); ok {
			if !unitsRead {
				units, unitsRead = repo.BuildUnits(dir), true
			}
			mods = nil
			for _, u := range units {
				if pu.runsIn(u) && !slices.Contains(mods, u.Dir) {
					mods = append(mods, u.Dir)
				}
			}
			if len(mods) == 0 {
				res.Status, res.Detail = StatusSkipped, pu.noUnits()
				results = append(results, res)
				continue
			}
			res.Units = len(mods)
		}
		start := time.Now()
		var failures, empty []string
		var skippedUnits skipReasons
		var lastOut []byte
		ran := 0
		for _, m := range mods {
			if us, ok := s.(unitSkipper); ok {
				if why := us.skipUnit(filepath.Join(dir, filepath.FromSlash(m))); why != "" {
					skippedUnits.add(why, m)
					continue
				}
			}
			ran++
			run, err := runIn(ctx, s, timeout, filepath.Join(dir, filepath.FromSlash(m)))
			if err != nil {
				failures = append(failures, prefixModule(m, err.Error(), len(mods)))
				continue
			}
			if run.analyzed == 0 {
				empty = append(empty, prefixModule(m, "rodou sem analisar nada; saída: "+truncate(strings.TrimSpace(string(run.out)), 300), len(mods)))
			}
			sigs := run.sigs
			for i := range sigs {
				if sigs[i].File != "" {
					sigs[i].File = path.Join(m, sigs[i].File)
				}
				if sigs[i].Package != "" {
					sigs[i].Package = path.Join(m, sigs[i].Package)
				}
				if sigs[i].Vuln != nil {
					sigs[i].Vuln.Manifest = path.Join(m, sigs[i].Vuln.Manifest)
				}
			}
			lastOut = run.out
			res.Filtered += run.filtered
			res.Signals += len(sigs)
			res.Analyzed += run.analyzed
			res.Unit = run.unit
			all = append(all, sigs...)
		}
		res.Duration = time.Since(start)
		res.Status = StatusOK
		if res.Units > 0 {
			res.Units = ran
		}
		switch {
		case ran == 0 && len(skippedUnits.order) > 0:
			res.Status, res.Detail = StatusSkipped, skippedUnits.String(len(mods))
		case len(failures) > 0:
			// Falha em qualquer módulo marca o scanner como failed; os sinais dos módulos que
			// rodaram são mantidos e o detalhe traz a saída de onde falhou.
			res.Status, res.Detail = StatusFailed, strings.Join(failures, "; ")
		case res.Analyzed == 0:
			res.Status, res.Detail = StatusFailed, strings.Join(empty, "; ")
		default:
			var notes []string
			if n, ok := s.(noter); ok {
				if note := n.note(lastOut, dir); note != "" {
					notes = append(notes, note)
				}
			}
			if len(skippedUnits.order) > 0 {
				notes = append(notes, "pulado em "+skippedUnits.String(len(mods)))
			}
			res.Detail = strings.Join(notes, "; ")
		}
		results = append(results, res)
	}
	return all, results
}

// skipReasons agrupa as unidades puladas pelo motivo, na ordem em que apareceram.
type skipReasons struct {
	order []string
	units map[string][]string
}

func (s *skipReasons) add(why, unit string) {
	if s.units == nil {
		s.units = map[string][]string{}
	}
	if _, ok := s.units[why]; !ok {
		s.order = append(s.order, why)
	}
	s.units[why] = append(s.units[why], unit)
}

// String: "motivo: a, b; outro motivo: c"; o motivo sozinho quando a única unidade é a raiz.
func (s skipReasons) String(total int) string {
	parts := make([]string, 0, len(s.order))
	for _, why := range s.order {
		units := s.units[why]
		if total == 1 && len(units) == 1 && units[0] == "." {
			parts = append(parts, why)
			continue
		}
		parts = append(parts, why+": "+strings.Join(units, ", "))
	}
	return strings.Join(parts, "; ")
}

// toolRun é o resultado de um scanner num diretório.
type toolRun struct {
	sigs     []Signal
	filtered int
	analyzed int
	unit     string
	out      []byte
}

// runIn roda o scanner num diretório (a raiz do repo ou um módulo Go) e interpreta a saída.
func runIn(ctx context.Context, s Scanner, timeout time.Duration, dir string) (toolRun, error) {
	bin, args := s.Command(dir)
	out, err := runTool(ctx, timeout, dir, bin, args)
	if err != nil {
		return toolRun{}, err
	}
	run := toolRun{out: out}
	if fp, ok := s.(filteringParser); ok {
		run.sigs, run.filtered, err = fp.ParseFiltered(out, dir)
	} else {
		run.sigs, err = s.Parse(out, dir)
	}
	if err != nil {
		return toolRun{}, fmt.Errorf("saída inválida: %w; saída: %s", err, truncate(strings.TrimSpace(string(out)), 300))
	}
	run.analyzed, run.unit = s.Coverage(out, dir)
	return run, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func prefixModule(m, msg string, n int) string {
	if n == 1 && m == "." {
		return msg
	}
	return "módulo " + m + ": " + msg
}

func runTool(ctx context.Context, timeout time.Duration, dir, bin string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // binário vem da lista fixa de adaptadores
	cmd.Dir = dir
	cmd.Env = processEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	// As ferramentas saem com código != 0 quando acham problemas; só é erro se não houve saída.
	var exitErr *exec.ExitError
	if err != nil && (!errors.As(err, &exitErr) || stdout.Len() == 0) {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return nil, fmt.Errorf("%s: %w: %s", bin, err, msg)
	}
	return stdout.Bytes(), nil
}

// rel normaliza um caminho de ferramenta para relativo ao repo com "/".
func rel(dir, p string) string {
	if filepath.IsAbs(p) && !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	if filepath.IsAbs(p) {
		if r, err := filepath.Rel(dir, p); err == nil {
			p = r
		}
	}
	return path.Clean(filepath.ToSlash(p))
}

// ---- golangci-lint ----

type GolangciLint struct{}

func (GolangciLint) Name() string                       { return "golangci-lint" }
func (GolangciLint) Applies(langs map[string]bool) bool { return langs["go"] }

// Command usa o config de lint do próprio repo-alvo (do módulo até a raiz do checkout) ou
// --no-config. Sem isso, o golangci-lint sobe a árvore e acha o .golangci.yml do Sentinela, já que
// os clones ficam em .sentinela/repos dentro do projeto, e os caminhos saem relativos a ele.
func (GolangciLint) Command(dir string) (string, []string) {
	args := []string{"run", "--path-mode", "abs", "--output.json.path", "stdout", "--output.text.path", os.DevNull, "--issues-exit-code", "0"}
	if cfg := lintConfig(dir); cfg != "" {
		args = append(args, "--config", cfg)
	} else {
		args = append(args, "--no-config")
	}
	return "golangci-lint", append(args, "./...")
}

// lintConfig procura .golangci.{yml,yaml,toml,json} de dir até a raiz do checkout (o diretório
// com .git), sem passar dela.
func lintConfig(dir string) string {
	for d := dir; ; {
		for _, name := range []string{".golangci.yml", ".golangci.yaml", ".golangci.toml", ".golangci.json"} {
			if _, err := os.Stat(filepath.Join(d, name)); err == nil {
				return filepath.Join(d, name)
			}
		}
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return ""
		}
		parent := filepath.Dir(d)
		if parent == d {
			return ""
		}
		d = parent
	}
}

// Coverage: o JSON do golangci-lint não diz quantos pacotes carregou; conta os pacotes do módulo
// com `go list` (a mesma lista que ./... carrega) quando o relatório não traz erro.
func (GolangciLint) Coverage(out []byte, dir string) (int, string) {
	var v struct {
		Report struct {
			Error string `json:"Error"`
		} `json:"Report"`
	}
	if json.NewDecoder(bytes.NewReader(out)).Decode(&v) != nil || v.Report.Error != "" {
		return 0, "pacotes"
	}
	return goPackages(dir), "pacotes"
}

// goPackages conta os pacotes Go de ./... em dir.
func goPackages(dir string) int {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-f", "{{.ImportPath}}", "./...")
	cmd.Dir = dir
	cmd.Env = processEnv()
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(out)))
}

func (GolangciLint) Parse(out []byte, dir string) ([]Signal, error) {
	var v struct {
		Issues []struct {
			FromLinter string `json:"FromLinter"`
			Text       string `json:"Text"`
			Severity   string `json:"Severity"`
			Pos        struct {
				Filename string `json:"Filename"`
				Line     int    `json:"Line"`
			} `json:"Pos"`
		} `json:"Issues"`
	}
	// O golangci-lint pode imprimir um resumo depois do JSON; lê só o primeiro documento.
	if err := json.NewDecoder(bytes.NewReader(out)).Decode(&v); err != nil {
		return nil, err
	}
	sigs := make([]Signal, 0, len(v.Issues))
	for _, i := range v.Issues {
		sigs = append(sigs, Signal{Scanner: "golangci-lint", Rule: i.FromLinter, File: rel(dir, i.Pos.Filename), Line: i.Pos.Line, Severity: orDefault(i.Severity, "medium"), Message: i.Text})
	}
	return sigs, nil
}

// ---- gosec ----

type Gosec struct{}

func (Gosec) Name() string                       { return "gosec" }
func (Gosec) Applies(langs map[string]bool) bool { return langs["go"] }
func (Gosec) Command(string) (string, []string) {
	// Sem -quiet: com ele o gosec não imprime nada quando não acha problema (visto no
	// monorepo-poliglota depois do merge da EX-12), e o relatório com Stats é a prova de análise.
	return "gosec", []string{"-fmt", "json", "-no-fail", "./..."}
}

func (Gosec) Parse(out []byte, dir string) ([]Signal, error) {
	var v struct {
		Issues []struct {
			Severity string `json:"severity"`
			RuleID   string `json:"rule_id"`
			Details  string `json:"details"`
			File     string `json:"file"`
			Line     string `json:"line"` // string; pode ser intervalo "10-12"
		} `json:"Issues"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	sigs := make([]Signal, 0, len(v.Issues))
	for _, i := range v.Issues {
		line, _ := strconv.Atoi(strings.SplitN(i.Line, "-", 2)[0])
		sigs = append(sigs, Signal{Scanner: "gosec", Rule: i.RuleID, File: rel(dir, i.File), Line: line, Severity: strings.ToLower(i.Severity), Message: i.Details})
	}
	return sigs, nil
}

// Coverage usa Stats.files do relatório do gosec.
func (Gosec) Coverage(out []byte, _ string) (int, string) {
	var v struct {
		Stats struct {
			Files int `json:"files"`
		} `json:"Stats"`
	}
	_ = json.Unmarshal(out, &v)
	return v.Stats.Files, "arquivos"
}

// ---- govulncheck ----

type Govulncheck struct{}

func (Govulncheck) Name() string                       { return "govulncheck" }
func (Govulncheck) Applies(langs map[string]bool) bool { return langs["go"] }
func (Govulncheck) Command(string) (string, []string) {
	return "govulncheck", []string{"-format", "json", "./..."}
}

// Parse lê o stream JSON: mensagens "osv" trazem o resumo, "finding" traz o trace.
// Vulnerabilidades sem posição no código (só no módulo) apontam para go.mod.
func (g Govulncheck) Parse(out []byte, dir string) ([]Signal, error) {
	sigs, _, err := g.ParseFiltered(out, dir)
	return sigs, err
}

// ParseFiltered é Parse descartando, antes da triagem, as vulnerabilidades já corrigidas
// no que o módulo usa: para a stdlib, a maior versão entre a diretiva go, a diretiva
// toolchain do go.mod e a toolchain que rodou o scan; para dependências, a versão exigida
// no go.mod (respeitando replace). Devolve quantos sinais foram descartados.
func (Govulncheck) ParseFiltered(out []byte, dir string) ([]Signal, int, error) {
	summaries := map[string]string{}
	type finding struct {
		OSV          string `json:"osv"`
		FixedVersion string `json:"fixed_version"`
		Trace        []struct {
			Module   string `json:"module"`
			Version  string `json:"version"`
			Package  string `json:"package"`
			Function string `json:"function"`
			Receiver string `json:"receiver"`
			Position *struct {
				Filename string `json:"filename"`
				Line     int    `json:"line"`
			} `json:"position"`
		} `json:"trace"`
	}
	var findings []finding
	toolchain := ""
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var msg struct {
			Config *struct {
				GoVersion string `json:"go_version"`
			} `json:"config"`
			OSV *struct {
				ID      string `json:"id"`
				Summary string `json:"summary"`
			} `json:"osv"`
			Finding *finding `json:"finding"`
		}
		if err := dec.Decode(&msg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, 0, err
		}
		if msg.Config != nil {
			toolchain = msg.Config.GoVersion
		}
		if msg.OSV != nil {
			summaries[msg.OSV.ID] = msg.OSV.Summary
		}
		if msg.Finding != nil {
			findings = append(findings, *msg.Finding)
		}
	}
	base, err := readVulnBaseline(dir, toolchain)
	if err != nil {
		return nil, 0, err
	}
	seen := map[string]bool{}
	var sigs []Signal
	dropped := 0
	for _, f := range findings {
		file, line, module := "go.mod", 1, ""
		for _, t := range f.Trace {
			if module == "" {
				module = t.Module
			}
			if t.Position != nil && t.Position.Filename != "" {
				file, line = rel(dir, t.Position.Filename), t.Position.Line
			}
		}
		// O módulo entra na chave: a mesma OSV pode afetar a stdlib e um golang.org/x
		// (ex.: net/http e x/net/http2), com correções em versões diferentes.
		key := f.OSV + "|" + module + "|" + file + ":" + strconv.Itoa(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		if base.fixed(module, f.FixedVersion) {
			dropped++
			continue
		}
		msg := fmt.Sprintf("%s em %s: %s", f.OSV, module, summaries[f.OSV])
		if f.FixedVersion != "" {
			msg += " (corrigido em " + f.FixedVersion + ")"
		}
		v := &Vuln{OSV: f.OSV, Summary: summaries[f.OSV], Module: module, FixedVersion: f.FixedVersion, Level: VulnRequired, Manifest: "go.mod"}
		if len(f.Trace) > 0 {
			// O primeiro quadro do trace é o símbolo vulnerável; os seguintes, quem chega nele.
			t := f.Trace[0]
			v.Version = t.Version
			switch {
			case t.Function != "":
				v.Level, v.Symbol = VulnCalled, t.Package+"."+strings.TrimPrefix(t.Receiver, "*")
				if t.Receiver == "" {
					v.Symbol = t.Package
				}
				v.Symbol += "." + t.Function
			case t.Package != "":
				v.Level, v.Symbol = VulnImported, t.Package
			}
		}
		sigs = append(sigs, Signal{Scanner: "govulncheck", Rule: f.OSV, File: file, Line: line, Severity: "high", Message: msg, Vuln: v})
	}
	return sigs, dropped, nil
}

// vulnBaseline é o que o módulo declara usar, para decidir se uma correção já está nele.
type vulnBaseline struct {
	goVersion string            // formato do go/version, ex.: "go1.27.2"; vazio se desconhecido
	requires  map[string]string // caminho do módulo -> versão semver efetiva
}

func readVulnBaseline(dir, toolchain string) (vulnBaseline, error) {
	b := vulnBaseline{goVersion: goVersion(toolchain), requires: map[string]string{}}
	data, err := os.ReadFile(filepath.Join(dir, "go.mod")) //nolint:gosec // go.mod do checkout
	if errors.Is(err, os.ErrNotExist) {
		return b, nil
	}
	if err != nil {
		return b, err
	}
	// Parse lê as diretivas go e toolchain; ParseLax as ignora, mas tolera diretivas
	// desconhecidas de um Go mais novo, então fica como segunda tentativa.
	mf, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		if mf, err = modfile.ParseLax("go.mod", data, nil); err != nil {
			return b, fmt.Errorf("lendo go.mod: %w", err)
		}
	}
	if mf.Go != nil {
		b.goVersion = maxGoVersion(b.goVersion, goVersion(mf.Go.Version))
	}
	if mf.Toolchain != nil {
		b.goVersion = maxGoVersion(b.goVersion, goVersion(mf.Toolchain.Name))
	}
	for _, r := range mf.Require {
		b.requires[r.Mod.Path] = r.Mod.Version
	}
	for _, r := range mf.Replace {
		if r.New.Version != "" { // replace por caminho local não tem versão: fica a do require
			b.requires[r.Old.Path] = r.New.Version
		}
	}
	return b, nil
}

// fixed diz se fixedVersion já está coberta. Sem versão de correção, nada está corrigido.
func (b vulnBaseline) fixed(module, fixedVersion string) bool {
	if fixedVersion == "" {
		return false
	}
	if module == "stdlib" || module == "toolchain" {
		fv := goVersion(fixedVersion)
		return b.goVersion != "" && version.IsValid(fv) && version.Compare(b.goVersion, fv) >= 0
	}
	have, ok := b.requires[module]
	return ok && semver.IsValid(have) && semver.IsValid(fixedVersion) && semver.Compare(have, fixedVersion) >= 0
}

// goVersion normaliza "1.27", "go1.27.2" e "v1.27.2" para o formato do go/version.
func goVersion(v string) string {
	v = strings.TrimPrefix(strings.TrimPrefix(v, "go"), "v")
	if v == "" {
		return ""
	}
	if gv := "go" + v; version.IsValid(gv) {
		return gv
	}
	return ""
}

func maxGoVersion(a, b string) string {
	if a == "" || (b != "" && version.Compare(b, a) > 0) {
		return b
	}
	return a
}

// Coverage conta os pacotes raiz que o govulncheck analisou (mensagem SBOM do stream).
func (Govulncheck) Coverage(out []byte, _ string) (int, string) {
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var m struct {
			SBOM *struct {
				Roots []string `json:"roots"`
			} `json:"SBOM"`
		}
		if err := dec.Decode(&m); err != nil {
			return 0, "pacotes"
		}
		if m.SBOM != nil {
			return len(m.SBOM.Roots), "pacotes"
		}
	}
}

// ---- semgrep ----

type Semgrep struct {
	Config string // pacote de regras; vazio usa "p/default" ("auto" exige métricas ligadas)
	// Languages são os pacotes de regras por linguagem (discovery.semgrep_languages), somados a
	// Config quando a linguagem está no repo.
	Languages map[string][]string
	active    []string // os de Languages escolhidos por forLanguages
}

func (Semgrep) Name() string                 { return "semgrep" }
func (Semgrep) Applies(map[string]bool) bool { return true }
func (s Semgrep) Command(string) (string, []string) {
	args := []string{"scan", "--json", "--quiet", "--metrics", "off", "--config", orDefault(s.Config, "p/default")}
	for _, c := range s.active {
		args = append(args, "--config", c)
	}
	return "semgrep", args
}

// note mostra no resumo os pacotes de regras usados (a base e os das linguagens do repo).
func (s Semgrep) note([]byte, string) string {
	if len(s.active) == 0 {
		return ""
	}
	return "regras: " + orDefault(s.Config, "p/default") + ", " + strings.Join(s.active, ", ")
}

// forLanguages escolhe os pacotes de regras das linguagens do repo, em ordem e sem repetir a base.
func (s Semgrep) forLanguages(langs map[string]bool) Scanner {
	names := make([]string, 0, len(s.Languages))
	for l := range s.Languages {
		if langs[l] {
			names = append(names, l)
		}
	}
	slices.Sort(names)
	seen := map[string]bool{orDefault(s.Config, "p/default"): true}
	s.active = nil
	for _, l := range names {
		for _, c := range s.Languages[l] {
			if !seen[c] {
				seen[c] = true
				s.active = append(s.active, c)
			}
		}
	}
	return s
}

func (Semgrep) Parse(out []byte, dir string) ([]Signal, error) {
	var v struct {
		Results []struct {
			CheckID string `json:"check_id"`
			Path    string `json:"path"`
			Start   struct {
				Line int `json:"line"`
			} `json:"start"`
			Extra struct {
				Message  string `json:"message"`
				Severity string `json:"severity"`
			} `json:"extra"`
		} `json:"results"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return nil, err
	}
	sigs := make([]Signal, 0, len(v.Results))
	for _, r := range v.Results {
		sigs = append(sigs, Signal{Scanner: "semgrep", Rule: r.CheckID, File: rel(dir, r.Path), Line: r.Start.Line, Severity: strings.ToLower(r.Extra.Severity), Message: r.Extra.Message})
	}
	return sigs, nil
}

// Coverage conta paths.scanned do relatório do semgrep.
func (Semgrep) Coverage(out []byte, _ string) (int, string) {
	var v struct {
		Paths struct {
			Scanned []string `json:"scanned"`
		} `json:"paths"`
	}
	_ = json.Unmarshal(out, &v)
	return len(v.Paths.Scanned), "arquivos"
}

// ---- go test ----

// GoTest roda os testes do repo-alvo; teste falhando vira sinal no pacote.
// Executa código do repo: só ligue para repos confiáveis (config scan.scanners).
type GoTest struct{}

func (GoTest) Name() string                       { return "go-test" }
func (GoTest) Applies(langs map[string]bool) bool { return langs["go"] }
func (GoTest) Command(string) (string, []string) {
	return "go", []string{"test", "-json", "-count=1", "./..."}
}

func (GoTest) Parse(out []byte, dir string) ([]Signal, error) {
	module := ""
	if data, err := os.ReadFile(filepath.Join(dir, "go.mod")); err == nil { //nolint:gosec // go.mod do checkout
		for _, line := range strings.Split(string(data), "\n") {
			if f := strings.Fields(line); len(f) == 2 && f[0] == "module" {
				module = f[1]
				break
			}
		}
	}
	type event struct {
		Action  string `json:"Action"`
		Package string `json:"Package"`
		Test    string `json:"Test"`
		Output  string `json:"Output"`
	}
	output := map[string]*strings.Builder{}
	var sigs []Signal
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var e event
		if err := dec.Decode(&e); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		key := e.Package + "." + e.Test
		switch e.Action {
		case "output":
			if output[key] == nil {
				output[key] = &strings.Builder{}
			}
			if output[key].Len() < 500 {
				_, _ = output[key].WriteString(e.Output)
			}
		case "fail":
			if e.Test == "" {
				continue // falha do pacote já está representada pelos testes
			}
			msg := "teste falhando"
			if b := output[key]; b != nil {
				msg = strings.TrimSpace(b.String())
			}
			pkg := "."
			if module != "" && e.Package != module {
				pkg = strings.TrimPrefix(e.Package, module+"/")
			}
			sigs = append(sigs, Signal{Scanner: "go-test", Rule: e.Test, File: pkg, Package: pkg, Severity: "high", Message: msg})
		}
	}
	return sigs, nil
}

// Coverage conta os pacotes com resultado (pass, fail ou skip do pacote) no stream de go test.
func (GoTest) Coverage(out []byte, _ string) (int, string) {
	pkgs := map[string]bool{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var e struct {
			Action, Package, Test string
		}
		if err := dec.Decode(&e); err != nil {
			return len(pkgs), "pacotes"
		}
		if e.Test == "" && (e.Action == "pass" || e.Action == "fail" || e.Action == "skip") {
			pkgs[e.Package] = true
		}
	}
}

// ByName monta a lista de scanners a partir da config. nodeTools é o diretório das ferramentas de
// Node do Sentinela (discovery.node_tools), do eslint e do tsc.
func ByName(names []string, semgrepConfig string, semgrepLanguages map[string][]string, nodeTools string) ([]Scanner, error) {
	if nodeTools != "" {
		if abs, err := filepath.Abs(nodeTools); err == nil {
			nodeTools = abs
		}
	}
	var out []Scanner
	for _, n := range names {
		switch n {
		case "golangci-lint":
			out = append(out, GolangciLint{})
		case "gosec":
			out = append(out, Gosec{})
		case "govulncheck":
			out = append(out, Govulncheck{})
		case "semgrep":
			out = append(out, Semgrep{Config: semgrepConfig, Languages: semgrepLanguages})
		case "go-test":
			out = append(out, GoTest{})
		case "osv-scanner":
			out = append(out, OSVScanner{})
		case "eslint":
			out = append(out, ESLint{Tools: nodeTools})
		case "tsc":
			out = append(out, TSC{Tools: nodeTools})
		default:
			return nil, fmt.Errorf("scanner desconhecido %q", n)
		}
	}
	return out, nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
