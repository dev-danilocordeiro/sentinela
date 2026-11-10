package discovery

import (
	"context"
	"errors"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// Limiar e variância do Judge (ADR 0020 e ADR 0054).
//
// Achado que depende de premissa de produto vira pergunta ao time, sempre: nem o limiar nem um
// discard do Judge o descartam, porque quem decide se o comportamento é intencional é o time, não
// a confiança do Judge (ADR 0054). Os demais precisam do limiar. Veredito de publicação sem
// premissa a menos de discovery.revote_band do limiar roda o Judge discovery.judge_votes vezes;
// vale a mediana, e os votos ficam em run_items.

// Question é a decisão registrada para um achado que sai como pergunta de produto.
const Question = "question"

// decide aplica as regras da casa ao veredito: devolve a decisão final (publish, discard ou
// merge) e o limiar que valeu (0 para a pergunta, que não passa por limiar).
func decide(minConf float64, v judge.Verdict) (string, float64) {
	switch {
	case v.Decision == judge.Merge && v.MergeWith != "":
		return judge.Merge, minConf
	case v.Premise != "":
		return judge.Publish, 0
	case v.Decision == judge.Publish && v.Confidence < minConf:
		return judge.Discard, minConf
	}
	return v.Decision, minConf
}

// publishScore é a confiança em publicar: a do veredito, se ele publica; zero, se não.
func publishScore(v judge.Verdict) float64 {
	if v.Decision == judge.Publish {
		return v.Confidence
	}
	return 0
}

// borderline diz se o veredito está perto demais do limiar para confiar num voto só.
func (r *Runner) borderline(v judge.Verdict, th float64) bool {
	return v.Decision == judge.Publish && v.Premise == "" && math.Abs(v.Confidence-th) < r.Cfg.Discovery.RevoteBand
}

// MedianVote ordena os votos pela confiança em publicar e devolve o do meio (com número par, o
// de baixo: na dúvida, não publica). A ordem estável mantém o primeiro voto no empate.
func MedianVote(votes []judge.Verdict) judge.Verdict {
	sorted := append([]judge.Verdict{}, votes...)
	sort.SliceStable(sorted, func(i, j int) bool { return publishScore(sorted[i]) < publishScore(sorted[j]) })
	return sorted[(len(sorted)-1)/2]
}

// revote roda os votos extras dos vereditos perto do limiar e devolve, por chave, todos os votos
// válidos (o primeiro incluído). thFor dá o limiar de cada chave.
func (r *Runner) revote(ctx context.Context, client llm.Caller, reqs map[string]agents.Item, first map[string]judge.Verdict, thFor func(key string, v judge.Verdict) float64) (map[string][]judge.Verdict, error) {
	out := map[string][]judge.Verdict{}
	var items []agents.Item
	for key, v := range first {
		out[key] = []judge.Verdict{v}
		if r.Cfg.Discovery.JudgeVotes < 2 || !r.borderline(v, thFor(key, v)) {
			continue
		}
		for n := 2; n <= r.Cfg.Discovery.JudgeVotes; n++ {
			items = append(items, agents.Item{Key: key + "#voto" + strconv.Itoa(n), Request: reqs[key].Request, Fallback: reqs[key].Fallback})
		}
	}
	if len(items) == 0 {
		return out, nil
	}
	outcomes, err := agents.RunAll(ctx, client, r.Tracer, judge.Name, items, r.Cfg.Models.Refusal)
	if err != nil && !errors.Is(err, llm.ErrBudgetExceeded) {
		return out, err
	}
	for _, it := range items {
		o := outcomes[it.Key]
		if o.Skipped {
			continue
		}
		v, perr := judge.Parse(o.Response)
		if perr != nil {
			continue
		}
		key := it.Key[:strings.LastIndex(it.Key, "#voto")]
		out[key] = append(out[key], v)
	}
	return out, err
}
