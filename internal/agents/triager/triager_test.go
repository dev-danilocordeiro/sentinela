package triager

import (
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/agentstest"
)

func sampleInput() Input {
	return Input{
		Repo: "acme/app-java-angular", Package: "src", Language: "java", OwnerTeam: "time-exemplo",
		Files:          []File{{Path: "src/UrlService.java", Content: agents.NumberLines("class UrlService {\n  void shorten() {\n    redis.save(url);\n  }\n}\n")}},
		ScannerSignals: []Signal{{Ref: "semgrep:x:src/UrlService.java:3", Scanner: "semgrep", Rule: "x", File: "src/UrlService.java", Line: 3, Severity: "warning", Message: "retorno ignorado"}},
	}
}

func TestRequestMatchesGolden(t *testing.T) {
	req, _, err := Request(sampleInput(), Options{Model: "claude-haiku-5-5", ContextTokens: 90_000})
	if err != nil {
		t.Fatal(err)
	}
	agentstest.Golden(t, "request", agentstest.View(req))
}

func TestParseRecordedResponse(t *testing.T) {
	out, err := Parse(agentstest.Recorded(t, "handler"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Deepen || out.Category != "bug" || out.Summary == "" {
		t.Errorf("out = %+v", out)
	}
}

func TestParseRejectsOutputOutsideContract(t *testing.T) {
	for _, text := range []string{
		`{"category":"bug","deepen":true,"reason":"x"}`,
		`{"category":"style","deepen":false,"reason":"x","summary":"y"}`,
		`{"category":"none","deepen":true,"reason":"x","summary":"y"}`,
		`{"category":"bug","deepen":true,"reason":"x","summary":"y","extra":1}`,
	} {
		resp := agentstest.Recorded(t, "handler")
		resp.Text = text
		if _, err := Parse(resp); err == nil {
			t.Errorf("deveria rejeitar %s", text)
		}
	}
}

// O preço do Haiku 5.5 muda de faixa acima de 100K tokens de prompt. Um pacote enorme
// precisa ser cortado para caber no teto configurado, com os cortes declarados.
func TestContextIsCutBelowTheHaikuPriceTier(t *testing.T) {
	in := sampleInput()
	for i := 0; i < 6; i++ {
		in.Files = append(in.Files, File{Path: "src/Big" + string(rune('A'+i)) + ".java", Content: agents.NumberLines(strings.Repeat("int x = compute(a, b) + other(c);\n", 3000))})
	}
	for i := 0; i < 30; i++ {
		in.Neighbors = append(in.Neighbors, Neighbor{Package: "lib", Relation: "imports", Signatures: strings.Repeat("public void m();\n", 200)})
	}
	if est := agents.EstimateTokens(mustJSON(t, in)); est < 100_000 {
		t.Fatalf("a entrada de teste deveria passar de 100K tokens estimados, tem %d", est)
	}

	req, sent, err := Request(in, Options{Model: "claude-haiku-5-5", ContextTokens: 90_000})
	if err != nil {
		t.Fatal(err)
	}
	total := agents.EstimateTokens(req.System) + agents.EstimateTokens(req.User)
	if total > 90_000 {
		t.Errorf("contexto estimado %d acima do teto de 90000", total)
	}
	// Mesmo a 2 bytes por token (mais pessimista que os tokenizers práticos para código),
	// a chamada fica abaixo de 100K.
	if bytes := len(req.System) + len(req.User); bytes/2 >= 100_000 {
		t.Errorf("%d bytes podem passar de 100K tokens", bytes)
	}
	if len(sent.Truncated) == 0 || len(sent.Neighbors) != 0 {
		t.Errorf("cortes não declarados: truncated=%v vizinhos=%d", sent.Truncated, len(sent.Neighbors))
	}
	if !strings.Contains(req.User, "src/UrlService.java") {
		t.Error("o arquivo pequeno deveria sobreviver ao corte")
	}
}

func TestSmallPackageIsNotCut(t *testing.T) {
	_, sent, err := Request(sampleInput(), Options{Model: "claude-haiku-5-5", ContextTokens: 90_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.Truncated) != 0 {
		t.Errorf("nada deveria ser cortado: %v", sent.Truncated)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	s, err := agents.EncodeInputUnchecked(v)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
