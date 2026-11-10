package discovery

import (
	"reflect"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

const model = "backend/src/main/java/com/example/app/model/UrlData.java"
const service = "backend/src/main/java/com/example/app/service/UrlShortenerService.java"

// ex5 e ex7 são os achados reais publicados na rodada 7 do app-java-angular.
func ex5() openFinding {
	return newOpenFinding("a6a13848ffff", "Limita a fila de eventos de clique por URL", "performance", "open", "EX-5",
		[]string{
			"Após registrar mais que o limite de cliques, clickEvents.size() nunca excede o limite configurado",
			"clickCount continua refletindo o total de cliques registrados",
			"Teste concorrente com múltiplas threads chamando recordClick confirma que o limite é respeitado e não há exceção",
		},
		[]signals.Evidence{{File: model, Line: 42, Snippet: "clickEvents.add(clickEvent);"}, {File: model, Line: 35, Snippet: "private Queue<ClickEvent> clickEvents = new ConcurrentLinkedQueue<>();"}})
}

func ex7() signals.Finding {
	return signals.Finding{
		Fingerprint: "0dfacd4a87f8", Title: "Limita o crescimento de memória de URLs e cliques",
		Context: "URLs expiradas nunca saem do mapa e os eventos de clique crescem sem limite.", ProposedFix: "Remover expiradas e limitar eventos.",
		AcceptanceCriteria: []string{
			"Após cleanupExpiredUrls e o período de retenção, o link expirado não permanece em urlMappings",
			"O número de ClickEvent guardados por URL respeita um limite configurável, e clickCount continua correto",
			"Teste cobre cleanup removendo a entrada do mapa",
		},
		Evidence: []signals.Evidence{
			{File: service, Line: 284, Snippet: "urlData.setActive(false);"},
			{File: service, Line: 176, Snippet: "urlData.recordClick(ClickEvent.builder()"},
			{File: service, Line: 37, Snippet: "private final Map<String, UrlData> urlMappings = new ConcurrentHashMap<>();"},
		},
	}
}

func TestHAY7CriterionTwoIsDetectedAsCoveredByHAY5(t *testing.T) {
	open := map[string]openFinding{"a6a13848ffff": ex5()}
	got := overlaps(ex7(), open)
	want := map[string]judge.Overlap{"a6a13848ffff": {Criteria: []string{ex7().AcceptanceCriteria[1]}}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sobreposição = %+v\nquer %+v", got, want)
	}
}

func TestUnrelatedFindingHasNoOverlap(t *testing.T) {
	f := signals.Finding{
		Fingerprint: "f1d40bb6ddcf", Title: "Impede que redirect concorrente recacheie URL deletada",
		AcceptanceCriteria: []string{"Após DELETE, GET /{code} devolve 404 mesmo com redirect concorrente", "Teste concorrente cobre delete e redirect simultâneos"},
		Evidence:           []signals.Evidence{{File: service, Line: 150, Snippet: "redisTemplate.opsForValue().set("}},
	}
	if got := overlaps(f, map[string]openFinding{"a6a13848ffff": ex5()}); len(got) != 0 {
		t.Errorf("sobreposição inesperada: %+v", got)
	}
}

func TestSameCodeLinesCountAsOverlap(t *testing.T) {
	f := signals.Finding{Fingerprint: "x", Title: "Outro texto", Evidence: []signals.Evidence{{File: model, Line: 44, Snippet: "clickEvents.add(clickEvent);"}}}
	got := overlaps(f, map[string]openFinding{"a6a13848ffff": ex5()})
	if len(got["a6a13848ffff"].Evidence) != 1 {
		t.Errorf("evidência a 2 linhas no mesmo arquivo deveria sobrepor: %+v", got)
	}
}

func TestScope(t *testing.T) {
	open := map[string]openFinding{"a6a13848ffff": ex5()}
	none := judge.Verdict{Decision: judge.Publish}
	narrowed := judge.Verdict{
		Decision: judge.Publish, RelatedTo: []string{"a6a13848ffff", "naoexiste000"},
		Narrowed: judge.Narrowed{
			Title: "Remove URLs expiradas do mapa em memória", Context: "Só a parte das URLs; a fila de cliques é a EX-5.",
			AcceptanceCriteria: []string{ex7().AcceptanceCriteria[0], ex7().AcceptanceCriteria[2]},
		},
	}
	t.Run("o Judge reduziu o escopo", func(t *testing.T) {
		f := ex7()
		res := applyScope(&f, narrowed, overlaps(ex7(), open), open)
		if !res.Narrowed || res.MergeWith != "" || !reflect.DeepEqual(res.RelatedTo, []string{"a6a13848ffff"}) || len(res.Removed) != 0 {
			t.Errorf("resultado = %+v (fingerprint inexistente fica fora de related_to)", res)
		}
		if f.Title != "Remove URLs expiradas do mapa em memória" || len(f.AcceptanceCriteria) != 2 || f.ProposedFix != ex7().ProposedFix {
			t.Errorf("achado = %+v (correção vazia mantém a original)", f)
		}
	})
	t.Run("o Judge ignorou a sobreposição: o código tira o critério e linka", func(t *testing.T) {
		f := ex7()
		res := applyScope(&f, none, overlaps(ex7(), open), open)
		if res.Narrowed || !reflect.DeepEqual(res.Removed, []string{ex7().AcceptanceCriteria[1]}) || !reflect.DeepEqual(res.RelatedTo, []string{"a6a13848ffff"}) {
			t.Errorf("resultado = %+v", res)
		}
		if len(f.AcceptanceCriteria) != 2 {
			t.Errorf("critérios = %v", f.AcceptanceCriteria)
		}
	})
	t.Run("não sobrou nada: é duplicado", func(t *testing.T) {
		f := ex7()
		f.AcceptanceCriteria = []string{ex7().AcceptanceCriteria[1]}
		res := applyScope(&f, none, overlaps(f, open), open)
		if res.MergeWith != "a6a13848ffff" {
			t.Errorf("resultado = %+v", res)
		}
	})
	t.Run("coincidência fraca sem o Judge confirmar só vira pista", func(t *testing.T) {
		// Proposta real da rodada 10, que a regra léxica aproxima da EX-7 sem ser o mesmo problema.
		f := signals.Finding{Fingerprint: "880fa4bbec5f", Title: "Persistir eventos de clique fora da memória da instância",
			AcceptanceCriteria: []string{"Excluir ou expirar uma URL remove também seus eventos de clique"}}
		open := map[string]openFinding{"0dfacd4a87f8": newOpenFinding("0dfacd4a87f8", ex7().Title, "bug", "open", "EX-7", ex7().AcceptanceCriteria, ex7().Evidence)}
		det := overlaps(f, open)
		if len(det) != 1 {
			t.Fatalf("a pista deveria aparecer para o Judge: %+v", det)
		}
		res := applyScope(&f, none, det, open)
		if len(res.Removed) != 0 || len(res.RelatedTo) != 0 || len(f.AcceptanceCriteria) != 1 {
			t.Errorf("resultado = %+v", res)
		}
	})
	t.Run("sem sobreposição nada muda", func(t *testing.T) {
		f := ex7()
		res := applyScope(&f, none, nil, map[string]openFinding{})
		if res.Narrowed || len(res.RelatedTo) != 0 || len(f.AcceptanceCriteria) != 3 {
			t.Errorf("resultado = %+v, achado %+v", res, f)
		}
	})
}
