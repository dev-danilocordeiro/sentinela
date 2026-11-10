package code

import (
	"strings"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// testdata/app-java-angular-ede6c91 tem os arquivos reais do app-java-angular no SHA analisado nas rodadas 7 a 10.
const appJava = "testdata/app-java-angular-ede6c91"

const service = "backend/src/main/java/com/example/app/service/UrlShortenerService.java"

func TestHAY9EvidenceIsDescriptionNotCode(t *testing.T) {
	// As duas evidências da EX-9, como o Architect as produziu: linha 1 com uma descrição.
	ex9 := []signals.Evidence{
		{File: service, Line: 1, Snippet: "mapa em memória de UrlData como armazenamento principal, Redis apenas como cache de redirecionamento"},
		{File: "docker-compose.yml", Line: 1, Snippet: "redis com persistência AOF habilitada"},
	}
	kept, dropped := (&EvidenceChecker{Dir: appJava}).Check(ex9)
	if len(kept) != 0 || len(dropped) != 2 {
		t.Fatalf("mantidas %v, removidas %v; a EX-9 não tem nenhuma evidência válida", kept, dropped)
	}
	for _, d := range dropped {
		if !strings.Contains(d, "snippet não aparece") {
			t.Errorf("motivo = %q", d)
		}
	}
}

func TestEvidenceChecker(t *testing.T) {
	cases := []struct {
		name    string
		ev      signals.Evidence
		invalid string // trecho do motivo; "" = válida
	}{
		{"linha exata", signals.Evidence{File: service, Line: 37, Snippet: "private final Map<String, UrlData> urlMappings = new ConcurrentHashMap<>();"}, ""},
		{"linha errada por pouco", signals.Evidence{File: service, Line: 40, Snippet: "private final Map<String, UrlData> urlMappings"}, ""},
		{"espaços diferentes", signals.Evidence{File: service, Line: 37, Snippet: "private final Map<String,UrlData>   urlMappings =\n new ConcurrentHashMap<>();"}, ""},
		{"reticências", signals.Evidence{File: service, Line: 37, Snippet: "private final Map<String, UrlData> ... new ConcurrentHashMap<>();"}, ""},
		{"compose real", signals.Evidence{File: "docker-compose.yml", Line: 7, Snippet: `command: ["redis-server", "--appendonly", "yes"]`}, ""},
		{"longe demais da linha", signals.Evidence{File: service, Line: 200, Snippet: "private final Map<String, UrlData> urlMappings"}, "não aparece"},
		{"linha fora do arquivo", signals.Evidence{File: service, Line: 999, Snippet: "x"}, "fora do arquivo"},
		{"linha zero", signals.Evidence{File: service, Line: 0, Snippet: "package"}, "fora do arquivo"},
		{"arquivo inexistente", signals.Evidence{File: "backend/Nada.java", Line: 1, Snippet: "x"}, "não existe"},
		{"fora do repo", signals.Evidence{File: "../../evidence.go", Line: 1, Snippet: "package code"}, "fora do repositório"},
		{"snippet vazio", signals.Evidence{File: service, Line: 1, Snippet: "  "}, "vazio"},
		{"parte inventada", signals.Evidence{File: service, Line: 37, Snippet: "private final Map<String, UrlData> ... redis.persist()"}, "não aparece"},
	}
	c := &EvidenceChecker{Dir: appJava}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := c.invalid(tc.ev)
			if (tc.invalid == "") != (got == "") || !strings.Contains(got, tc.invalid) {
				t.Errorf("motivo = %q, quer %q", got, tc.invalid)
			}
		})
	}
}

func TestOneValidEvidenceKeepsTheFindingWithoutTheInvalidOnes(t *testing.T) {
	kept, dropped := (&EvidenceChecker{Dir: appJava}).Check([]signals.Evidence{
		{File: service, Line: 37, Snippet: "urlMappings = new ConcurrentHashMap<>();"},
		{File: "docker-compose.yml", Line: 1, Snippet: "redis com persistência AOF habilitada"},
	})
	if len(kept) != 1 || kept[0].Line != 37 || len(dropped) != 1 {
		t.Errorf("mantidas %v, removidas %v", kept, dropped)
	}
}
