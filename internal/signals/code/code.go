// Package code é a fonte code_analysis: Scanner → Triager → Analyst → Architect.
// Implementa signals.SignalSource e signals.BatchInterpreter; a rodada usa o caminho
// em lote (Batch API) e Interpret existe para uso pontual e para o contrato da interface.
package code

import (
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"go.opentelemetry.io/otel/trace"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/analyst"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/architect"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/triager"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// Source é a fonte de achados por análise de código de um checkout.
type Source struct {
	Cfg          *config.Config
	Checkout     *repo.Checkout
	Resolver     *repo.Resolver
	Packages     []repo.Package // pacotes a analisar nesta rodada
	AllPackages  []repo.Package // todos, para o grafo e a validação de evidências
	Edges        []repo.Edge
	ScanSignals  []scan.Signal
	Suppressions []agents.Suppression
	Client       llm.Caller
	Tracer       trace.Tracer
	Log          agents.ItemLog

	// Preenchidos durante a interpretação.
	Triage      map[string]triager.Output
	ProblemKeys map[string]string // fingerprint → problem_key, para o embedding
	Warnings    []string
	// InvalidEvidence conta os achados descartados por não ter nenhuma evidência válida;
	// DroppedEvidence guarda, por fingerprint, as evidências removidas e o motivo.
	InvalidEvidence int
	DroppedEvidence map[string][]string
	owners          map[string]repo.Resolution
	evidence        *EvidenceChecker
}

var _ signals.SignalSource = (*Source)(nil)
var _ signals.BatchInterpreter = (*Source)(nil)

func (s *Source) Name() string { return signals.SourceCodeAnalysis }

// Collect devolve um sinal bruto por pacote, com a entrada do Triager já montada.
// since não se aplica: o recorte incremental vem do diff (Packages).
func (s *Source) Collect(_ context.Context, _ time.Time) ([]signals.RawSignal, error) {
	s.init()
	var raws []signals.RawSignal
	for _, p := range s.Packages {
		res := s.ownerOf(p)
		if res.Ignored {
			continue
		}
		in, err := s.triageInput(p, res)
		if err != nil {
			return nil, err
		}
		raws = append(raws, signals.RawSignal{Source: s.Name(), Ref: p.Path, Repo: s.Checkout.Repo, Module: p.Path, Payload: in})
	}
	return raws, nil
}

// Interpret analisa um único pacote (síncrono, fora de rodada).
func (s *Source) Interpret(ctx context.Context, raw signals.RawSignal) ([]signals.Finding, error) {
	return s.InterpretAll(ctx, []signals.RawSignal{raw})
}

// TriageRequests monta as requisições do Triager (usado também na estimativa de custo).
func (s *Source) TriageRequests(raws []signals.RawSignal) ([]agents.Item, map[string]triager.Input, error) {
	items := make([]agents.Item, 0, len(raws))
	sent := map[string]triager.Input{}
	for _, raw := range raws {
		in, ok := raw.Payload.(triager.Input)
		if !ok {
			return nil, nil, fmt.Errorf("sinal %s sem entrada do triager", raw.Ref)
		}
		models := s.Cfg.ModelsFor(in.Repo, in.OwnerTeam)
		req, fitted, err := triager.Request(in, triager.Options{Model: models.Triage, ContextTokens: s.Cfg.Discovery.TriageContextTokens})
		if err != nil {
			return nil, nil, err
		}
		sent[raw.Ref] = fitted
		items = append(items, agents.Item{Key: raw.Ref, Request: req, Fallback: models.RefusalFallback})
	}
	return items, sent, nil
}

// InterpretAll roda Triager, Analyst e Architect em lote e devolve os achados propostos.
// Erros de orçamento sobem (a rodada aborta); itens com problema são pulados e registrados.
func (s *Source) InterpretAll(ctx context.Context, raws []signals.RawSignal) ([]signals.Finding, error) {
	s.init()
	items, sent, err := s.TriageRequests(raws)
	if err != nil {
		return nil, err
	}
	for ref, in := range sent {
		if len(in.Truncated) > 0 {
			s.Warnings = append(s.Warnings, fmt.Sprintf("triager: %s teve %d arquivo(s) cortado(s) para caber no contexto", ref, len(in.Truncated)))
		}
	}
	outcomes, err := agents.RunAll(ctx, s.Client, s.Tracer, triager.Name, items, s.Cfg.Models.Refusal)
	if err != nil && !errors.Is(err, llm.ErrBudgetExceeded) {
		return nil, err
	}
	budgetErr := err
	var deepen []string
	for _, it := range items {
		o := outcomes[it.Key]
		if o.Skipped {
			s.log(ctx, triager.Name, it.Key, "skipped", o.Response.Model, nil, o.Reason)
			continue
		}
		if config.IsHaiku(o.Response.Model) && o.Response.Usage.PromptTokens() > 100_000 {
			s.Warnings = append(s.Warnings, fmt.Sprintf("triager: %s usou %d tokens de prompt (acima da faixa de 100K do Haiku)", it.Key, o.Response.Usage.PromptTokens()))
		}
		out, err := triager.Parse(o.Response)
		if err != nil {
			s.log(ctx, triager.Name, it.Key, "failed", o.Response.Model, nil, err.Error())
			continue
		}
		s.Triage[it.Key] = out
		s.log(ctx, triager.Name, it.Key, "ok", o.Response.Model, out, "")
		if out.Deepen {
			deepen = append(deepen, it.Key)
		}
	}
	if budgetErr != nil {
		return nil, budgetErr
	}

	findings, err := s.analyze(ctx, deepen, sent)
	if err != nil {
		return findings, err
	}
	cross, err := s.architect(ctx, findings)
	findings = append(findings, cross...)
	if err != nil {
		return findings, err
	}
	// Vulnerabilidades de dependência: um achado por go.mod, em código (ADR 0033).
	return append(findings, s.VulnFindings(ctx)...), nil
}

func (s *Source) analyze(ctx context.Context, deepen []string, sent map[string]triager.Input) ([]signals.Finding, error) {
	sort.Strings(deepen)
	var items []agents.Item
	maxF := s.Cfg.Discovery.MaxFindingsPerPackage
	for _, ref := range deepen {
		tin := s.Triage[ref]
		full := s.packageByPath(ref)
		res := s.ownerOf(full)
		in := analyst.Input{
			Repo: s.Checkout.Repo, Package: ref, Language: full.Language, OwnerTeam: res.Team,
			Files: s.files(full), Triage: analyst.Triage{Category: tin.Category, Reason: tin.Reason},
			ScannerSignals: sent[ref].ScannerSignals, Suppressions: s.Suppressions, MaxFindings: maxF,
		}
		models := s.Cfg.ModelsFor(s.Checkout.Repo, res.Team)
		req, _, err := analyst.Request(in, analyst.Options{Model: models.Analysis, ContextTokens: s.Cfg.Discovery.AnalysisContextTokens})
		if err != nil {
			return nil, err
		}
		items = append(items, agents.Item{Key: ref, Request: req, Fallback: models.RefusalFallback})
	}
	outcomes, err := agents.RunAll(ctx, s.Client, s.Tracer, analyst.Name, items, s.Cfg.Models.Refusal)
	if err != nil && !errors.Is(err, llm.ErrBudgetExceeded) {
		return nil, err
	}
	var out []signals.Finding
	for _, it := range items {
		o := outcomes[it.Key]
		if o.Skipped {
			s.log(ctx, analyst.Name, it.Key, "skipped", o.Response.Model, nil, o.Reason)
			continue
		}
		parsed, perr := analyst.Parse(o.Response, maxF)
		if perr != nil {
			s.log(ctx, analyst.Name, it.Key, "failed", o.Response.Model, nil, perr.Error())
			continue
		}
		s.log(ctx, analyst.Name, it.Key, "ok", o.Response.Model, parsed, "")
		pkg := s.packageByPath(it.Key)
		for i, fo := range parsed.Findings {
			f, reason := s.toFinding(fo, pkg.Path, func(file string) bool { return pkgHasFile(pkg, file) })
			if reason != "" {
				s.log(ctx, analyst.Name, fmt.Sprintf("%s#%d", it.Key, i), "skipped", o.Response.Model, fo, reason)
				continue
			}
			out = append(out, f)
		}
	}
	return out, err
}

func (s *Source) architect(ctx context.Context, found []signals.Finding) ([]signals.Finding, error) {
	if len(s.AllPackages) < 2 {
		return nil, nil
	}
	in := architect.Input{Repo: s.Checkout.Repo, MaxFindings: s.Cfg.Discovery.MaxFindingsPerPackage}
	budget := landmarksTotal
	for _, p := range s.AllPackages {
		res := s.ownerOf(p)
		if res.Ignored {
			continue
		}
		summary := "(não analisado nesta rodada)"
		if t, ok := s.Triage[p.Path]; ok {
			summary = t.Summary
		}
		marks := landmarks(s.Checkout.Dir, p, budget)
		budget -= len(marks)
		in.Packages = append(in.Packages, architect.Package{Path: p.Path, Language: p.Language, OwnerTeam: res.Team, Summary: summary, Landmarks: marks})
	}
	for _, e := range s.Edges {
		in.Edges = append(in.Edges, architect.Edge{From: e.From, To: e.To})
	}
	for _, f := range found {
		in.Findings = append(in.Findings, architect.KnownFinding{Title: f.Title, Category: f.Category, File: f.Evidence[0].File})
	}
	models := s.Cfg.ModelsFor(s.Checkout.Repo, "")
	req, err := architect.Request(in, architect.Options{Model: models.Architect})
	if err != nil {
		return nil, err
	}
	const key = "system"
	outcomes, err := agents.RunAll(ctx, s.Client, s.Tracer, architect.Name, []agents.Item{{Key: key, Request: req, Fallback: models.RefusalFallback}}, s.Cfg.Models.Refusal)
	if err != nil && !errors.Is(err, llm.ErrBudgetExceeded) {
		return nil, err
	}
	o := outcomes[key]
	if o.Skipped {
		s.log(ctx, architect.Name, key, "skipped", o.Response.Model, nil, o.Reason)
		return nil, err
	}
	parsed, perr := architect.Parse(o.Response, in.MaxFindings)
	if perr != nil {
		s.log(ctx, architect.Name, key, "failed", o.Response.Model, nil, perr.Error())
		return nil, err
	}
	s.log(ctx, architect.Name, key, "ok", o.Response.Model, parsed, "")
	var out []signals.Finding
	for i, fo := range parsed.Findings {
		if len(fo.Evidence) == 0 {
			s.log(ctx, architect.Name, fmt.Sprintf("system#%d", i), "skipped", o.Response.Model, fo, "sem evidência")
			continue
		}
		owner := path.Dir(fo.Evidence[0].File)
		f, reason := s.toFinding(fo, owner, s.repoHasFile)
		if reason != "" {
			s.log(ctx, architect.Name, fmt.Sprintf("system#%d", i), "skipped", o.Response.Model, fo, reason)
			continue
		}
		for _, m := range fo.AffectedModules {
			if m != f.Module && s.packageByPath(m).Path != "" && !slices.Contains(f.RelatedModules, m) {
				f.RelatedModules = append(f.RelatedModules, m)
			}
		}
		out = append(out, f)
	}
	return out, err
}

// toFinding completa o achado do modelo com o que é determinístico: repo, módulo, dono,
// roteamento, permalinks, sinais e fingerprint. Cada evidência é conferida contra o código no
// SHA analisado (EvidenceChecker); as inválidas saem do achado, e um achado sem nenhuma
// evidência válida é descartado com motivo "evidência inválida" (o motivo volta para run_items).
func (s *Source) toFinding(fo agents.FindingOut, module string, fileOK func(string) bool) (signals.Finding, string) {
	var ev []signals.Evidence
	var dropped []string
	for _, e := range fo.Evidence {
		file := strings.TrimPrefix(path.Clean(e.File), "/")
		se := signals.Evidence{File: file, Line: e.Line, Snippet: e.Snippet}
		why := s.evidence.invalid(se)
		if why == "" && !fileOK(file) {
			why = "fora dos pacotes analisados"
		}
		if why != "" {
			dropped = append(dropped, fmt.Sprintf("%s:%d: %s", e.File, e.Line, why))
			continue
		}
		se.Permalink = repo.Permalink(s.Checkout.Repo, s.Checkout.HeadSHA, file, e.Line)
		ev = append(ev, se)
	}
	if len(ev) == 0 {
		s.InvalidEvidence++
		if len(dropped) == 0 {
			return signals.Finding{}, InvalidEvidenceReason + ": achado sem evidência"
		}
		return signals.Finding{}, InvalidEvidenceReason + ": " + strings.Join(dropped, "; ")
	}
	files := make([]string, 0, len(ev))
	texts := []string{fo.ProposedFix}
	for _, e := range ev {
		files = append(files, e.File)
		texts = append(texts, e.Snippet)
	}
	res, err := s.Resolver.ResolveFinding(s.Checkout.Repo, repo.FindingPaths{
		Primary: ev[0].File, Scope: fo.ScopePaths, Evidence: files[1:], Refs: goRefs(s.Checkout.Dir, files, texts...),
	})
	if err != nil {
		return signals.Finding{}, err.Error()
	}
	if res.Ignored {
		return signals.Finding{}, "evidência principal em módulo ignorado (" + res.Rule + ")"
	}
	f := signals.Finding{
		Source: signals.SourceCodeAnalysis, Repo: s.Checkout.Repo, Module: module,
		Owners: ownersOf(res), Routing: res.Routing,
		Category: fo.Category, Severity: fo.Severity, Confidence: fo.Confidence,
		Title: fo.Title, Context: fo.Context, Evidence: ev, ProposedFix: fo.ProposedFix,
		AcceptanceCriteria: agents.Slice(fo.AcceptanceCriteria), ScopePaths: agents.Slice(fo.ScopePaths),
		Signals: []signals.SignalRef{},
	}
	for _, ref := range fo.SignalRefs {
		for _, sig := range s.ScanSignals {
			if sig.Ref() == ref {
				f.Signals = append(f.Signals, signals.SignalRef{Kind: "scanner", Ref: ref, Summary: sig.Message})
			}
		}
	}
	key := fo.ProblemKey
	if strings.TrimSpace(key) == "" {
		key = fo.Title
	}
	f.Fingerprint = signals.Fingerprint(f.Repo, f.Category, ev[0].File, key)
	s.ProblemKeys[f.Fingerprint] = key
	if err := f.Validate(); err != nil {
		return signals.Finding{}, err.Error()
	}
	if len(dropped) > 0 {
		s.DroppedEvidence[f.Fingerprint] = dropped
	}
	return f, ""
}

// ownersOf usa os donos do CODEOWNERS quando a regra veio de lá; senão, os times resolvidos.
func ownersOf(res repo.Resolution) []string {
	if len(res.Owners) > 0 {
		return append([]string{}, res.Owners...)
	}
	if res.Team == "" {
		return []string{}
	}
	return append([]string{res.Team}, res.Subscribers...)
}

func (s *Source) init() {
	if s.Triage == nil {
		s.Triage = map[string]triager.Output{}
	}
	if s.ProblemKeys == nil {
		s.ProblemKeys = map[string]string{}
	}
	if s.owners == nil {
		s.owners = map[string]repo.Resolution{}
	}
	if s.evidence == nil {
		s.evidence = &EvidenceChecker{Dir: s.Checkout.Dir}
	}
	if s.DroppedEvidence == nil {
		s.DroppedEvidence = map[string][]string{}
	}
}

func (s *Source) ownerOf(p repo.Package) repo.Resolution {
	if r, ok := s.owners[p.Path]; ok {
		return r
	}
	target := p.Path + "/"
	if len(p.Files) > 0 {
		target = p.Files[0].Path
	}
	r, err := s.Resolver.Resolve(s.Checkout.Repo, target)
	if err != nil {
		r = repo.Resolution{Warnings: []string{err.Error()}}
	}
	s.owners[p.Path] = r
	return r
}

func (s *Source) triageInput(p repo.Package, res repo.Resolution) (triager.Input, error) {
	in := triager.Input{
		Repo: s.Checkout.Repo, Package: p.Path, Language: p.Language, OwnerTeam: res.Team,
		Files: s.files(p), Suppressions: s.Suppressions,
	}
	for _, sig := range s.ScanSignals {
		// Vulnerabilidade de dependência não é bug do pacote: vira achado no go.mod (ADR 0033).
		if sig.Vuln == nil && sig.Dir() == p.Path {
			in.ScannerSignals = append(in.ScannerSignals, agents.Signal{
				Ref: sig.Ref(), Scanner: sig.Scanner, Rule: sig.Rule, File: sig.File, Line: sig.Line, Severity: sig.Severity, Message: sig.Message,
			})
		}
	}
	for _, e := range s.Edges {
		switch p.Path {
		case e.From:
			in.Neighbors = append(in.Neighbors, triager.Neighbor{Package: e.To, Relation: "imports", Signatures: s.signatures(e.To)})
		case e.To:
			in.Neighbors = append(in.Neighbors, triager.Neighbor{Package: e.From, Relation: "imported_by", Signatures: s.signatures(e.From)})
		}
	}
	return in, nil
}

func (s *Source) files(p repo.Package) []agents.File {
	out := make([]agents.File, 0, len(p.Files))
	for _, f := range p.Files {
		data, err := repo.ReadFile(s.Checkout.Dir, f.Path)
		if err != nil {
			continue
		}
		out = append(out, agents.File{Path: f.Path, Content: agents.NumberLines(string(data))})
	}
	return out
}

var signatureLine = regexp.MustCompile(`^\s*(public|export|func|def|class|interface|type|record|enum|abstract|protected)\b`)

// signatures extrai linhas de declaração pública de um pacote vizinho (até 60 linhas).
func (s *Source) signatures(pkgPath string) string {
	p := s.packageByPath(pkgPath)
	var b strings.Builder
	n := 0
	for _, f := range p.Files {
		data, err := repo.ReadFile(s.Checkout.Dir, f.Path)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if n >= 60 {
				return b.String()
			}
			if signatureLine.MatchString(line) {
				fmt.Fprintf(&b, "%s: %s\n", path.Base(f.Path), strings.TrimSpace(line))
				n++
			}
		}
	}
	return b.String()
}

func (s *Source) packageByPath(p string) repo.Package {
	for _, pkg := range s.AllPackages {
		if pkg.Path == p {
			return pkg
		}
	}
	return repo.Package{}
}

func (s *Source) repoHasFile(file string) bool {
	return s.packageByPath(path.Dir(file)).Path != "" && pkgHasFile(s.packageByPath(path.Dir(file)), file)
}

func pkgHasFile(p repo.Package, file string) bool {
	for _, f := range p.Files {
		if f.Path == file {
			return true
		}
	}
	return false
}

func (s *Source) log(ctx context.Context, agent, key, status, model string, output any, reason string) {
	if s.Log == nil {
		return
	}
	if err := s.Log.Item(ctx, agent, key, status, model, output, reason); err != nil {
		s.Warnings = append(s.Warnings, "gravando run_item: "+err.Error())
	}
}

// SuppressedBy devolve o motivo se o achado casa com uma supressão (fingerprint ou glob de caminho).
func SuppressedBy(f signals.Finding, sups []agents.Suppression) (string, bool) {
	for _, sp := range sups {
		if sp.Category != "" && sp.Category != f.Category {
			continue
		}
		if sp.Fingerprint != "" && sp.Fingerprint == f.Fingerprint {
			return "suprimido (fingerprint): " + sp.Reason, true
		}
		if sp.Pattern != "" && len(f.Evidence) > 0 {
			if ok, _ := doublestar.Match(sp.Pattern, f.Evidence[0].File); ok {
				return "suprimido (" + sp.Pattern + "): " + sp.Reason, true
			}
		}
	}
	return "", false
}

// FromModel converte um achado vindo de outra fonte que lê código (ex.: o intérprete de notas
// dos reviewers, source review_note) com as mesmas regras dos achados de código: evidência
// conferida no checkout, dono pelo roteamento por achado e fingerprint. O módulo é o diretório
// da primeira evidência; extra entra em Signals.
func (s *Source) FromModel(fo agents.FindingOut, source string, extra []signals.SignalRef) (signals.Finding, string) {
	s.init()
	if len(fo.Evidence) == 0 {
		s.InvalidEvidence++
		return signals.Finding{}, InvalidEvidenceReason + ": achado sem evidência"
	}
	fo.SignalRefs = nil // ids de nota, não sinais de scanner
	f, reason := s.toFinding(fo, path.Dir(strings.TrimPrefix(path.Clean(fo.Evidence[0].File), "/")), s.repoHasFile)
	if reason != "" {
		return f, reason
	}
	f.Source = source
	f.Signals = append(f.Signals, extra...)
	return f, ""
}
