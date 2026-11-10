// Package executor é a linha de execução (Fase 4): prepara o worktree e o diretório do job,
// invoca o plugin sentinela-factory em modo headless, confere os contratos dos artefatos, faz
// push, abre o PR em draft e passa pelo Policy Gate. O julgamento fica nos agentes do plugin;
// tudo aqui é código determinístico.
package executor

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Lentes dos reviewers, na ordem do relatório.
var Lenses = []string{"security", "code", "reliability", "domain"}

// Primeiras linhas aceitas por arquivo (contratos da skill sentinela-fix).
var (
	specVerdicts     = []string{"SPEC: READY", "SPEC: BLOCKED"}
	buildVerdicts    = []string{"STATUS: READY", "STATUS: BLOCKED"}
	reviewVerdicts   = []string{"VERDICT: PASS", "VERDICT: CHANGES"}
	decisionVerdicts = []string{"APPROVE", "ESCALATE"}
	shepherdVerdicts = []string{"STATUS: FIXED", "STATUS: BLOCKED"}
)

// State é o JOB/state.json, escrito só pela skill orquestradora.
type State struct {
	Status         string `json:"status"` // approve | escalate | blocked | failed
	Rounds         int    `json:"rounds"`
	Reason         string `json:"reason"`
	BuilderAgentID string `json:"builder_agent_id"`
}

// Artifact é um arquivo do job: o veredito (primeira linha) e um resumo para o PR.
type Artifact struct {
	File    string   `json:"file"`
	Verdict string   `json:"verdict"`
	Summary string   `json:"summary"`
	Items   []string `json:"items,omitempty"` // itens "- " da seção de achados (reviews)
}

// ReviewResult é um review de uma rodada.
type ReviewResult struct {
	Round   int
	Lens    string
	Verdict string // PASS | CHANGES
	Items   []string
}

// Result é o que o job produziu, já conferido contra os contratos.
type Result struct {
	State     State
	Decision  string // approve | escalate | blocked | failed
	Reason    string
	Approver  string // APPROVE | ESCALATE | "" (approver não rodou)
	Artifacts map[string]Artifact
	Reviews   []ReviewResult
}

// ErrContract indica arquivo faltando ou primeira linha fora do contrato: o job vira failed.
var ErrContract = errors.New("contrato da linha de execução quebrado")

// Collect lê JOB/state.json e confere cada arquivo que o estado implica. Qualquer divergência
// entre o estado declarado pela skill e os arquivos é ErrContract.
func Collect(jobDir string) (Result, error) {
	res := Result{Artifacts: map[string]Artifact{}}
	data, err := os.ReadFile(filepath.Join(jobDir, "state.json")) //nolint:gosec // diretório do job criado pelo worker
	if err != nil {
		return res, fmt.Errorf("%w: state.json ausente (a skill não terminou)", ErrContract)
	}
	if err := json.Unmarshal(data, &res.State); err != nil {
		return res, fmt.Errorf("%w: state.json inválido: %w", ErrContract, err)
	}
	st := res.State
	switch st.Status {
	case "approve", "escalate", "blocked", "failed":
	default:
		return res, fmt.Errorf("%w: state.json com status %q", ErrContract, st.Status)
	}
	if st.Rounds < 0 || st.Rounds > 3 {
		return res, fmt.Errorf("%w: state.json com %d rodadas (máximo 3)", ErrContract, st.Rounds)
	}
	res.Decision, res.Reason = st.Status, st.Reason
	if st.Status == "failed" {
		return res, nil // a skill já declarou a quebra; o motivo vai para o job
	}

	spec, err := read(jobDir, "spec.md", specVerdicts)
	if err != nil {
		return res, err
	}
	res.Artifacts["spec.md"] = spec
	if spec.Verdict == "SPEC: BLOCKED" {
		if st.Status != "blocked" {
			return res, fmt.Errorf("%w: spec bloqueada mas state.json diz %s", ErrContract, st.Status)
		}
		return res, nil
	}
	if st.Rounds == 0 {
		return res, fmt.Errorf("%w: state.json %s sem nenhuma rodada", ErrContract, st.Status)
	}
	lastChanges := false
	for n := 1; n <= st.Rounds; n++ {
		build, err := read(jobDir, roundFile(n, "build.md"), buildVerdicts)
		if err != nil {
			return res, err
		}
		res.Artifacts[roundFile(n, "build.md")] = build
		if build.Verdict == "STATUS: BLOCKED" {
			if n != st.Rounds || st.Status != "blocked" {
				return res, fmt.Errorf("%w: build da rodada %d bloqueado mas o job seguiu (%s)", ErrContract, n, st.Status)
			}
			return res, nil
		}
		lastChanges = false
		for _, lens := range Lenses {
			f := roundFile(n, "review-"+lens+".md")
			rv, err := read(jobDir, f, reviewVerdicts)
			if err != nil {
				return res, err
			}
			res.Artifacts[f] = rv
			verdict := strings.TrimPrefix(rv.Verdict, "VERDICT: ")
			res.Reviews = append(res.Reviews, ReviewResult{Round: n, Lens: lens, Verdict: verdict, Items: rv.Items})
			if verdict == "CHANGES" {
				lastChanges = true
			}
		}
		if lastChanges && n < st.Rounds {
			continue
		}
		if !lastChanges && n < st.Rounds {
			return res, fmt.Errorf("%w: rodada %d passou nas quatro lentes mas o job seguiu para a %d", ErrContract, n, n+1)
		}
	}
	if st.Status == "blocked" {
		return res, fmt.Errorf("%w: state.json blocked sem spec ou build bloqueado", ErrContract)
	}
	if lastChanges {
		// Rodadas esgotadas: ESCALATE automático, sem approver.
		if st.Status != "escalate" || st.Rounds != 3 {
			return res, fmt.Errorf("%w: lentes pedindo mudança na rodada %d e state.json %s", ErrContract, st.Rounds, st.Status)
		}
		return res, nil
	}
	dec, err := read(jobDir, "decision.md", decisionVerdicts)
	if err != nil {
		return res, err
	}
	res.Artifacts["decision.md"] = dec
	res.Approver = dec.Verdict
	if want := strings.ToLower(dec.Verdict); want != st.Status {
		return res, fmt.Errorf("%w: decision.md diz %s e state.json diz %s", ErrContract, dec.Verdict, st.Status)
	}
	if res.Reason == "" {
		res.Reason = secondLine(dec.Summary)
	}
	return res, nil
}

// CollectShepherd confere o result.md de um ciclo do shepherd.
func CollectShepherd(cycleDir string) (Artifact, error) {
	return read(cycleDir, "result.md", shepherdVerdicts)
}

func roundFile(n int, name string) string { return fmt.Sprintf("round-%d/%s", n, name) }

// read lê um artefato e confere a primeira linha.
func read(dir, rel string, allowed []string) (Artifact, error) {
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))) //nolint:gosec // diretório do job criado pelo worker
	if err != nil {
		return Artifact{}, fmt.Errorf("%w: %s ausente", ErrContract, rel)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	first, _, _ := strings.Cut(text, "\n")
	first = strings.TrimSpace(first)
	ok := false
	for _, a := range allowed {
		if first == a {
			ok = true
		}
	}
	if !ok {
		return Artifact{}, fmt.Errorf("%w: %s começa com %q; esperado um de %v", ErrContract, rel, truncate(first, 80), allowed)
	}
	return Artifact{File: rel, Verdict: first, Summary: summarize(text, 1200), Items: items(text)}, nil
}

// items pega os itens "- " da seção "## Achados" (reviews).
func items(text string) []string {
	var out []string
	in := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "## "):
			in = strings.EqualFold(strings.TrimSpace(t[3:]), "Achados")
		case in && strings.HasPrefix(t, "- "):
			if item := strings.TrimSpace(t[2:]); item != "" && !strings.EqualFold(item, "nenhum") {
				out = append(out, item)
			}
		}
	}
	return out
}

func summarize(text string, n int) string {
	text = strings.TrimSpace(text)
	if len(text) <= n {
		return text
	}
	cut := strings.LastIndex(text[:n], "\n")
	if cut < n/2 {
		cut = n
	}
	return text[:cut] + "\n…"
}

func secondLine(s string) string {
	lines := strings.SplitN(s, "\n", 3)
	if len(lines) < 2 {
		return ""
	}
	return strings.TrimSpace(lines[1])
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
