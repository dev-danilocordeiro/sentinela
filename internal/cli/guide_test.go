package cli

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// O guia commitado (docs/guia/index.html) é o que o binário gera hoje: se uma fonte, um doc, um
// exemplo, o schema, um comando ou a saída do roteamento mudar, este teste falha até alguém rodar
// `go run ./cmd/sentinela docs guide` e commitar. Assim o guia nunca fica desatualizado.
func TestGuideIsUpToDate(t *testing.T) {
	root := filepath.Join("..", "..")
	want, err := BuildGuide(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(root, GuideFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%s está diferente do gerado: rode `go run ./cmd/sentinela docs guide` e commite (%d bytes commitados, %d gerados)", GuideFile, len(got), len(want))
	}
	again, err := BuildGuide(root)
	if err != nil || !bytes.Equal(again, want) {
		t.Fatalf("o guia não é determinístico (%v)", err)
	}
	// O guia não depende de quem o gera: com outro usuário no ambiente, sai igual.
	t.Setenv("USER", "outra-pessoa")
	if other, err := BuildGuide(root); err != nil || !bytes.Equal(other, want) {
		t.Fatalf("com outro USER o guia muda (%v)", err)
	}
	page := string(want)
	// Todo link relativo aponta para um arquivo que existe (o guia fica em docs/guia/).
	for _, m := range regexp.MustCompile(`href="([^"]+)"`).FindAllStringSubmatch(page, -1) {
		href := m[1]
		if strings.HasPrefix(href, "#") || strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
			continue
		}
		p, _, _ := strings.Cut(href, "#")
		if p, err = url.PathUnescape(p); err != nil {
			t.Errorf("link %q: %v", href, err)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "docs", "guia", filepath.FromSlash(p))); err != nil {
			t.Errorf("link relativo quebrado no guia: %s", href)
		}
	}
	for _, m := range regexp.MustCompile(`href="#([a-z0-9-]+)"`).FindAllStringSubmatch(page, -1) {
		if m[1] != "" && !strings.Contains(page, `id="`+m[1]+`"`) {
			t.Errorf("âncora sem destino no guia: #%s", m[1])
		}
	}
	bads := []string{"/tmp/", "sentinela-guia-", "cdn.", "<script src", "mermaid"}
	if home, err := os.UserHomeDir(); err == nil && home != "" && home != "/" {
		bads = append(bads, home+"/") // caminhos da máquina que gerou o guia
	}
	for _, bad := range bads {
		if strings.Contains(page, bad) {
			t.Errorf("o guia contém %q (deve ser autocontido e sem caminhos da máquina)", bad)
		}
	}
}

// O simulador cobre os casos pedidos, com o resultado real do owners explain.
func TestGuideSimulatorCases(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))
	cases, err := guideSimulator()
	if err != nil {
		t.Fatal(err)
	}
	team := func(id string) (string, []string) {
		for _, c := range cases {
			if c.ID == id {
				for _, f := range c.Fields {
					if f.Name == "time" {
						return f.Value, c.Secondary
					}
				}
			}
		}
		t.Fatalf("caso %s não existe", id)
		return "", nil
	}
	for id, want := range map[string]string{
		"comum": "plataforma", "docker-longo": "plataforma", "docker": "plataforma", "longo": "pagamentos",
		"busca": "catalogo", "rascunho": "plataforma", "vitrine": "catalogo", "sdk": "identidade", "cruza": "pagamentos",
	} {
		if got, _ := team(id); got != want {
			t.Errorf("%s: time %q, quer %q", id, got, want)
		}
	}
	if _, sec := team("cruza"); len(sec) != 1 || !strings.HasPrefix(sec[0], "identidade") {
		t.Errorf("achado que cruza times: secundários %v", sec)
	}
}

// Cada caso de Erros comuns produz uma mensagem real e nova do config validate.
func TestGuideErrorCatalog(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))
	errs, err := guideErrors()
	if err != nil {
		t.Fatal(err)
	}
	if len(errs) < len(errSpecs) {
		t.Fatalf("%d mensagens para %d casos", len(errs), len(errSpecs))
	}
	// Cada mensagem tem a causa do próprio caso (ex.: o aviso de identidade do MCP não leva a
	// explicação do fix_trigger).
	for _, e := range errs {
		if strings.Contains(e.Message, "adapters.tracker linear_mcp") && !strings.Contains(e.Fix, "linear_api") {
			t.Errorf("aviso de identidade do MCP com a correção errada: %+v", e)
		}
		if strings.Contains(e.Message, "fix_trigger no modo MCP") && !strings.Contains(e.Cause, "quem aplicou a label") {
			t.Errorf("aviso do fix_trigger no MCP com a causa errada: %+v", e)
		}
	}
	for _, want := range []string{`chave "models" só vale no sentinela.yaml principal`, `está em dois times`, `o bot não está no canal C0ACMEIDN01`, `fix_trigger.allowed não definido`, `adapters.tracker linear_mcp`} {
		found := false
		for _, e := range errs {
			found = found || strings.Contains(e.Message, want)
		}
		if !found {
			t.Errorf("nenhuma mensagem com %q", want)
		}
	}
}
