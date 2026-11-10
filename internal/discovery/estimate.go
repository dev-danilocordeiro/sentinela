package discovery

import (
	"sort"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/analyst"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/architect"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/triager"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/signals/code"
)

// AgentEstimate é a estimativa de custo de um agente antes da rodada.
type AgentEstimate struct {
	Agent    string      `json:"agent"`
	Model    string      `json:"model"`
	Calls    int         `json:"calls"`
	Expected llm.NanoUSD `json:"expected_nano_usd"`
	Worst    llm.NanoUSD `json:"worst_nano_usd"`
	// WorstCalls conta o pior caso de chamadas (todos aprofundados, máximo de achados).
	WorstCalls int `json:"worst_calls"`
}

// Estimate soma as estimativas por agente.
type Estimate struct {
	Agents        []AgentEstimate `json:"agents"`
	Expected      llm.NanoUSD     `json:"expected_nano_usd"`
	Worst         llm.NanoUSD     `json:"worst_nano_usd"`
	ExpectedCalls int             `json:"expected_calls"`
	WorstCalls    int             `json:"worst_calls"`
}

// Premissas da estimativa esperada (ADR 0009): fração de pacotes aprofundados, saída
// típica de cada agente (raciocínio + JSON) e achados por pacote aprofundado. O pior
// caso usa todos os pacotes aprofundados, max_tokens de saída e o máximo de achados.
const (
	expectedDeepenRatio        = 0.5
	expectedTriageOutput       = 1_000
	expectedAnalystOutput      = 6_000
	expectedArchitectOutput    = 6_000
	expectedJudgeOutput        = 1_000
	expectedFindingsPerDeep    = 2
	judgeInputTokensPerFinding = 3_000
)

func (r *Runner) estimate(src *code.Source, raws []signals.RawSignal) (Estimate, error) {
	items, sent, err := src.TriageRequests(raws)
	if err != nil {
		return Estimate{}, err
	}
	batch := r.Cfg.Models.UseBatch
	cost := func(model string, in, out int) (llm.NanoUSD, error) {
		return r.Prices.Cost(model, llm.Usage{InputTokens: int64(in), OutputTokens: int64(out)}, batch)
	}
	var est Estimate
	add := func(a AgentEstimate) {
		est.Agents = append(est.Agents, a)
		est.Expected += a.Expected
		est.Worst += a.Worst
		est.ExpectedCalls += a.Calls
		est.WorstCalls += a.WorstCalls
	}

	// Cada pacote é custeado com os modelos do seu time dono (teams.<time>.models vence
	// repos[].models). Architect e os achados dele usam o nível do repo.
	repoModels := r.Cfg.ModelsFor(src.Checkout.Repo, "")
	scaled := func(c llm.NanoUSD, f float64) llm.NanoUSD { return llm.NanoUSD(float64(c)*f + 0.5) }
	n := len(items)
	maxF := r.Cfg.Discovery.MaxFindingsPerPackage
	expDeep := int(float64(n)*expectedDeepenRatio + 0.5)
	tri := AgentEstimate{Agent: triager.Name, Calls: n, WorstCalls: n}
	ana := AgentEstimate{Agent: analyst.Name, Calls: expDeep, WorstCalls: n}
	jud := AgentEstimate{Agent: judge.Name, Calls: expDeep*expectedFindingsPerDeep + 1, WorstCalls: n*maxF + maxF}
	var triModels, anaModels, judModels []string
	for _, it := range items {
		m := r.Cfg.ModelsFor(src.Checkout.Repo, sent[it.Key].OwnerTeam)
		triModels, anaModels, judModels = append(triModels, it.Request.Model), append(anaModels, m.Analysis), append(judModels, m.Judge)
		in := agents.EstimateTokens(it.Request.System) + agents.EstimateTokens(it.Request.User)

		// Triager: entrada medida na requisição já cortada.
		e, err := cost(it.Request.Model, in, expectedTriageOutput)
		if err != nil {
			return Estimate{}, err
		}
		w, err := cost(it.Request.Model, in, triager.MaxTokens)
		if err != nil {
			return Estimate{}, err
		}
		tri.Expected, tri.Worst = tri.Expected+e, tri.Worst+w

		// Analyst: entrada parecida com a do Triager (mesmos arquivos, sem vizinhos); no esperado,
		// só a fração aprofundada de cada pacote.
		if e, err = cost(m.Analysis, in, expectedAnalystOutput); err != nil {
			return Estimate{}, err
		}
		if w, err = cost(m.Analysis, in, analyst.MaxTokens); err != nil {
			return Estimate{}, err
		}
		ana.Expected, ana.Worst = ana.Expected+scaled(e, expectedDeepenRatio), ana.Worst+w

		// Judge: um julgamento por achado do pacote.
		if e, err = cost(m.Judge, judgeInputTokensPerFinding, expectedJudgeOutput); err != nil {
			return Estimate{}, err
		}
		if w, err = cost(m.Judge, judgeInputTokensPerFinding, judge.MaxTokens); err != nil {
			return Estimate{}, err
		}
		jud.Expected, jud.Worst = jud.Expected+scaled(e, expectedDeepenRatio*expectedFindingsPerDeep), jud.Worst+scaled(w, float64(maxF))
	}
	tri.Model, ana.Model = modelLabel(triModels), modelLabel(anaModels)
	add(tri)
	add(ana)

	// Architect: uma chamada com resumos (~80 tokens por pacote) e o grafo.
	if len(src.AllPackages) >= 2 {
		archIn := 2_000 + 80*len(src.AllPackages) + 20*len(src.Edges)
		arc := AgentEstimate{Agent: architect.Name, Model: repoModels.Architect, Calls: 1, WorstCalls: 1}
		if arc.Expected, err = cost(arc.Model, archIn, expectedArchitectOutput); err != nil {
			return Estimate{}, err
		}
		if arc.Worst, err = cost(arc.Model, archIn, architect.MaxTokens); err != nil {
			return Estimate{}, err
		}
		add(arc)
	}

	// Judge dos achados do Architect: 1 no esperado, max_findings no pior caso, no modelo do repo.
	e, err := cost(repoModels.Judge, judgeInputTokensPerFinding, expectedJudgeOutput)
	if err != nil {
		return Estimate{}, err
	}
	w, err := cost(repoModels.Judge, judgeInputTokensPerFinding, judge.MaxTokens)
	if err != nil {
		return Estimate{}, err
	}
	jud.Expected, jud.Worst = jud.Expected+e, jud.Worst+scaled(w, float64(maxF))
	jud.Model = modelLabel(append(judModels, repoModels.Judge))
	add(jud)
	return est, nil
}

// modelLabel junta os modelos distintos de um agente, para a tabela da estimativa.
func modelLabel(ids []string) string {
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "+")
}
