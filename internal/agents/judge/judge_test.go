package judge

import (
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/agentstest"
)

func TestRequestMatchesGolden(t *testing.T) {
	req, err := Request(Input{
		Finding: Finding{
			Source: "code_analysis", Repo: "acme/app-java-angular", Module: "src", Category: "bug",
			Severity: "high", Confidence: 0.82, Title: "Propaga falha ao gravar URL no Redis", Context: "c",
			Evidence:    []Evidence{{File: "src/UrlService.java", Line: 3, Snippet: "redis.save(url);"}},
			ProposedFix: "f", AcceptanceCriteria: []string{"503 com Redis fora"}, Fingerprint: "abc123def456",
		},
		Similar:       []Similar{{Fingerprint: "000000000000", Title: "Outro", Category: "bug", State: "rejected", Similarity: 0.5}},
		MinConfidence: 0.6,
	}, Options{Model: "claude-sonnet-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	agentstest.Golden(t, "request", agentstest.View(req))
}

func TestParseAndThreshold(t *testing.T) {
	v, err := Parse(agentstest.Recorded(t, "finding"))
	if err != nil {
		t.Fatal(err)
	}
	if !v.Publishable(0.6) || v.Publishable(0.9) {
		t.Errorf("limiar mal aplicado: %+v", v)
	}
	resp := agentstest.Recorded(t, "finding")
	resp.Text = `{"decision":"merge","confidence":0.9,"reason":"r","merge_with":"","related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]},"product_decision":{"depends":false,"decision":""}}`
	if _, err := Parse(resp); err == nil {
		t.Error("merge sem merge_with deveria falhar")
	}
}

func TestProductPremiseGoesToContext(t *testing.T) {
	resp := agentstest.Recorded(t, "finding")
	resp.Text = `{"decision":"publish","confidence":0.62,"reason":"r","merge_with":"","product_decision":{"depends":true,"decision":"O app deveria exigir login para excluir links?"},"related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]}}`
	v, err := Parse(resp)
	if err != nil {
		t.Fatal(err)
	}
	got := v.ApplyPremise("Qualquer cliente exclui qualquer link.")
	want := "Qualquer cliente exclui qualquer link.\n\nPremissa de produto: O app deveria exigir login para excluir links?"
	if got != want {
		t.Errorf("contexto = %q", got)
	}
	if v2 := (Verdict{}); v2.ApplyPremise("c") != "c" {
		t.Error("sem premissa o contexto não muda")
	}
}

// A pergunta de produto é obrigatória e coerente: depends sem decisão é erro; sem dependência, a
// decisão é ignorada.
func TestProductDecisionIsRequiredAndConsistent(t *testing.T) {
	resp := agentstest.Recorded(t, "finding")
	for _, tc := range []struct {
		body    string
		ok      bool
		premise string
	}{
		{`{"decision":"discard","confidence":0.8,"reason":"r","merge_with":"","related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]},"product_decision":{"depends":true,"decision":"O link deve ser anônimo?"}}`, true, "O link deve ser anônimo?"},
		{`{"decision":"publish","confidence":0.8,"reason":"r","merge_with":"","related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]},"product_decision":{"depends":true,"decision":" "}}`, false, ""},
		{`{"decision":"publish","confidence":0.8,"reason":"r","merge_with":"","related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]},"product_decision":{"depends":false,"decision":"sobra"}}`, true, ""},
		{`{"decision":"publish","confidence":0.8,"reason":"r","merge_with":"","related_to":[],"narrowed":{"title":"","context":"","proposed_fix":"","acceptance_criteria":[]}}`, false, ""},
	} {
		resp.Text = tc.body
		v, err := Parse(resp)
		if (err == nil) != tc.ok || v.Premise != tc.premise {
			t.Errorf("%s: premissa %q, erro %v", tc.body[:40], v.Premise, err)
		}
	}
}
