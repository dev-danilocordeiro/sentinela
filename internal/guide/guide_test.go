package guide

import (
	"strings"
	"testing"
)

func TestSplitComment(t *testing.T) {
	for in, want := range map[string][2]string{
		`  team: PAG  # o board`:                      {"  team: PAG  ", "o board"},
		`path: "**/Dockerfile"  # todo Dockerfile`:    {`path: "**/Dockerfile"  `, "todo Dockerfile"},
		`project: "Pagamentos # 2"`:                   {`project: "Pagamentos # 2"`, ""},
		`# só comentário`:                             {"", "só comentário"},
		`/modules/cobranca/         @acme/pagamentos`: {`/modules/cobranca/         @acme/pagamentos`, ""},
	} {
		code, comment := splitComment(in)
		if code != want[0] || comment != want[1] {
			t.Errorf("%q: %q / %q", in, code, comment)
		}
	}
}

func TestRewriteLinksAndSections(t *testing.T) {
	src := "[modos](modos.md) [guia](#x) [site](https://exemplo.com) [ex](../examples/a.yaml#L2)"
	got := rewriteLinks(src, "docs/empresa.md")
	want := "[modos](../modos.md) [guia](../empresa.md#x) [site](https://exemplo.com) [ex](../../examples/a.yaml#L2)"
	if got != want {
		t.Errorf("doc incluído:\n%s\n%s", got, want)
	}
	if got := rewriteLinks("[a](#x) [b](../../modos.md)", "docs/guia/fontes/01.md"); got != "[a](#x) [b](../modos.md)" {
		t.Errorf("fonte do guia: %s", got)
	}
	body, err := sectionOf("# T\n## A\na\n### A1\na1\n## B\nb\n```\n## não\n```\n", "A")
	if err != nil || !strings.Contains(body, "a1") || strings.Contains(body, "\nb\n") {
		t.Errorf("seção: %q %v", body, err)
	}
	if got := shiftHeadings("# a\n```\n# b\n```\n## c", 1); got != "## a\n```\n# b\n```\n### c" {
		t.Errorf("títulos: %q", got)
	}
	if got := splitArgs(`--config a.yaml owners explain "x y" z`); strings.Join(got, "|") != "--config|a.yaml|owners|explain|x y|z" {
		t.Errorf("args: %v", got)
	}
}

// Os diagramas citam só caixas que existem e geram SVG com título.
func TestDiagramsRender(t *testing.T) {
	for name, d := range diagrams {
		svg := d.render()
		if !strings.HasPrefix(svg, `<figure class="diagram">`) || !strings.Contains(svg, "<title>") {
			t.Errorf("%s: %.80s", name, svg)
		}
	}
}
