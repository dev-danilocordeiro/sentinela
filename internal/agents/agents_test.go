package agents

import (
	"strings"
	"testing"
)

func TestEveryAgentHasVersionedPromptAndSchemas(t *testing.T) {
	for _, a := range []string{"triager", "analyst", "architect", "judge"} {
		p, err := LoadPrompt(a)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p.Version, a+"@") || len(p.SHA256) != 64 || p.Body == "" {
			t.Errorf("prompt %s = %+v", a, p)
		}
		for _, kind := range []string{".input", ".output"} {
			s, err := LoadSchema(a + kind)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := s.Raw["$schema"]; ok {
				t.Errorf("%s%s: $schema não deveria ir para a API", a, kind)
			}
		}
	}
}

func TestCustomIDFitsBatchAPIFormat(t *testing.T) {
	for _, key := range []string{"backend/src/main/java/com/example/app/service", "a", "ç ã/x"} {
		id := customID(key)
		if len(id) > 64 || strings.IndexFunc(id, func(r rune) bool {
			ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
			return !ok
		}) >= 0 {
			t.Errorf("custom_id inválido %q", id)
		}
	}
	if customID("a/b") == customID("a_b") {
		t.Error("chaves diferentes não podem colidir")
	}
}
