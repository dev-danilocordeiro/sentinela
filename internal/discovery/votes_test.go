package discovery

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
)

func TestMedianVoteUsesThePublishScore(t *testing.T) {
	p := func(c float64) judge.Verdict { return judge.Verdict{Decision: judge.Publish, Confidence: c} }
	d := judge.Verdict{Decision: judge.Discard, Confidence: 0.8}
	cases := []struct {
		votes []judge.Verdict
		want  float64
		dec   string
	}{
		{[]judge.Verdict{p(0.65), p(0.75), p(0.72)}, 0.72, judge.Publish},
		{[]judge.Verdict{p(0.65), p(0.55), d}, 0.55, judge.Publish}, // mediana publica, mas com 0,55
		{[]judge.Verdict{p(0.65), d, d}, 0.8, judge.Discard},
		{[]judge.Verdict{p(0.62), p(0.70)}, 0.62, judge.Publish}, // par: o de baixo
	}
	for _, c := range cases {
		got := MedianVote(c.votes)
		if got.Confidence != c.want || got.Decision != c.dec {
			t.Errorf("MedianVote(%v) = %+v", c.votes, got)
		}
	}
}

// Achado sem premissa perto do limiar (0,6): o Judge vota três vezes, os votos ficam em
// run_items e vale a mediana, nos dois sentidos.
func TestBorderlineVerdictsVoteThreeTimesAndUseTheMedian(t *testing.T) {
	for _, tc := range []struct {
		name    string
		script  []string
		publish bool
		median  float64
	}{
		{"mediana acima do limiar publica", []string{verdict("publish", 0.55, ""), verdict("publish", 0.65, ""), verdict("publish", 0.62, "")}, true, 0.62},
		{"mediana abaixo descarta", []string{verdict("publish", 0.65, ""), verdict("publish", 0.55, ""), verdict("discard", 0.8, "")}, false, 0.55},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			e.runner.Cfg.Discovery.JudgeVotes = 3 // votação ligada explicitamente
			e.fake.judgeScript = tc.script
			res, err := e.runner.Run(context.Background(), Options{Repo: "acme/svc"})
			if err != nil {
				t.Fatal(err)
			}
			if e.fake.judgeCalls != 3 {
				t.Fatalf("chamadas do Judge = %d, quer 3", e.fake.judgeCalls)
			}
			if (res.Stats.Published == 1) != tc.publish {
				t.Errorf("publicados = %d, quer publicar = %v", res.Stats.Published, tc.publish)
			}
			items, _ := e.st.Q.ListRunItems(context.Background(), res.RunID)
			for _, it := range items {
				if it.Agent != "judge" || it.Status != "ok" {
					continue
				}
				var out struct {
					Votes     []judge.Verdict `json:"votes"`
					Verdict   judge.Verdict   `json:"verdict"`
					Threshold float64         `json:"effective_threshold"`
				}
				_ = json.Unmarshal(it.Output, &out)
				if len(out.Votes) != 3 || out.Verdict.Confidence != tc.median || out.Threshold != 0.6 {
					t.Errorf("run_item: %d votos, mediana %v, limiar %v", len(out.Votes), out.Verdict.Confidence, out.Threshold)
				}
			}
		})
	}
}

// Rejudge compara a decisão registrada com o Judge atual, com as mesmas regras (premissa vira
// pergunta mesmo abaixo do limiar, sem votar), sem gravar achado nem entrar na memória de
// decisões anteriores.
func TestRejudgeAppliesTheCurrentRulesWithoutWriting(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if _, err := e.runner.Run(ctx, Options{Repo: "acme/svc"}); err != nil {
		t.Fatal(err)
	}
	before := e.findingCount(t)
	e.runner.Cfg.Discovery.JudgeVotes = 3
	p := "O erro de Save deve mesmo subir ao chamador?"
	e.fake.judgeScript = []string{verdict("discard", 0.55, p)}
	runID, items, err := e.runner.Rejudge(ctx, []string{"acme/svc"}, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].Before != "publish" || items[0].After != Question || items[0].AfterConf != 0.55 || len(items[0].Votes) != 0 || e.fake.judgeCalls != 1 {
		t.Fatalf("rejulgamento = %+v", items)
	}
	if e.findingCount(t) != before {
		t.Error("rejudge não grava achado")
	}
	prior, err := e.runner.priorRejections(ctx, "acme/svc")
	if err != nil || len(prior.list) != 0 {
		t.Errorf("o rejulgamento não pode virar decisão anterior: %+v (%v)", prior.list, err)
	}
	var mode string
	_ = e.st.Pool.QueryRow(ctx, "SELECT mode FROM analysis_runs WHERE id = $1", runID).Scan(&mode)
	if mode != "rejudge" {
		t.Errorf("modo = %s", mode)
	}
}

// Achado que depende de decisão de produto sai como pergunta, nunca como descarte (ADR 0054):
// nem pelo limiar nem pela decisão discard do Judge. Sem premissa, o limiar continua valendo.
func TestProductPremiseIsAlwaysAQuestion(t *testing.T) {
	const p = "O canal do KDS deve exigir identidade autenticada?"
	for _, tc := range []struct {
		name    string
		verdict string
		publish bool
		premise string
	}{
		{"publish com premissa abaixo do limiar", verdict("publish", 0.62, p), true, p},
		{"discard com premissa", verdict("discard", 0.62, p), true, p},
		{"publish sem premissa abaixo do limiar", verdict("publish", 0.55, ""), false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := setup(t)
			e.fake.judgeScript = []string{tc.verdict}
			res, err := e.runner.Run(context.Background(), Options{Repo: "acme/svc"})
			if err != nil {
				t.Fatal(err)
			}
			if (res.Stats.Published == 1) != tc.publish || (res.Stats.Discarded == 1) == tc.publish {
				t.Fatalf("publicados = %d, descartados = %d, quer publicar = %v", res.Stats.Published, res.Stats.Discarded, tc.publish)
			}
			if !tc.publish {
				return
			}
			var premise string
			_ = e.st.Pool.QueryRow(context.Background(), "SELECT premise FROM findings").Scan(&premise)
			if premise != tc.premise {
				t.Errorf("premissa gravada = %q", premise)
			}
		})
	}
}

// Desligada por padrão: um veredito perto do limiar não vota de novo.
func TestVotingIsOffByDefault(t *testing.T) {
	e := setup(t)
	if e.runner.Cfg.Discovery.JudgeVotes != 1 {
		t.Fatalf("judge_votes padrão = %d, quer 1", e.runner.Cfg.Discovery.JudgeVotes)
	}
	e.fake.judgeScript = []string{verdict("publish", 0.72, "O produto exige login?"), verdict("publish", 0.9, "")}
	if _, err := e.runner.Run(context.Background(), Options{Repo: "acme/svc"}); err != nil {
		t.Fatal(err)
	}
	if e.fake.judgeCalls != 1 {
		t.Errorf("chamadas do Judge = %d, quer 1", e.fake.judgeCalls)
	}
}

// rejudge --apply grava como achado aberto (com a premissa) o que estava descartado e agora
// publica ou vira pergunta, para a publicação normal levar ao Linear; sem --apply, só mede. Não
// mexe nos achados já gravados.
func TestRejudgeApplyRecordsADiscardedFindingThatNowIsAQuestion(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.fake.judgeScript = []string{verdict("discard", 0.8, "")}
	if _, err := e.runner.Run(ctx, Options{Repo: "acme/svc"}); err != nil {
		t.Fatal(err)
	}
	if n := e.findingCount(t); n != 0 {
		t.Fatalf("o descarte não grava achado: %d", n)
	}
	p := "O canal do KDS deve exigir identidade autenticada?"
	e.fake.judgeScript = []string{verdict("discard", 0.6, p)}
	if _, items, err := e.runner.Rejudge(ctx, []string{"acme/svc"}, nil, false); err != nil || len(items) != 1 || items[0].After != Question || e.findingCount(t) != 0 {
		t.Fatalf("sem --apply: %+v, %v, achados %d", items, err, e.findingCount(t))
	}
	e.fake.judgeScript = []string{verdict("discard", 0.6, p)}
	_, items, err := e.runner.Rejudge(ctx, []string{"acme/svc"}, nil, true)
	if err != nil || len(items) != 1 || !items[0].Applied {
		t.Fatalf("com --apply: %+v, %v", items, err)
	}
	var premise, state string
	_ = e.st.Pool.QueryRow(ctx, "SELECT premise, state FROM findings").Scan(&premise, &state)
	if e.findingCount(t) != 1 || premise != p || state != "open" {
		t.Fatalf("achado gravado: %d, premissa %q, estado %q", e.findingCount(t), premise, state)
	}
	pending, err := e.st.Q.PendingFindings(ctx, "acme/svc")
	if err != nil || len(pending) != 1 {
		t.Errorf("pendente para a publicação: %d (%v)", len(pending), err)
	}
}
