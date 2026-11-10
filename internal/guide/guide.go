// Package guide gera o guia interativo do Sentinela (docs/guia/index.html): um HTML único,
// autocontido, montado a partir das fontes reais (docs/guia/fontes/*.md, docs/*.md, examples/, o
// JSON Schema e a saída do próprio binário). Quem coleta a saída do binário é o pacote cli; este
// pacote só monta a página. Veja a ADR 0046.
//
//go:generate go run ../../cmd/sentinela docs guide --root ../..
package guide

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	gmhtml "github.com/yuin/goldmark/renderer/html"
)

//go:embed assets/style.css assets/app.js
var assets embed.FS

// OutDir é onde o guia fica, relativo à raiz do repo; os links são reescritos a partir dele.
const OutDir = "docs/guia"

// SourcesDir tem as fontes do guia: um arquivo por seção, na ordem do nome.
const SourcesDir = "docs/guia/fontes"

// Data é tudo o que o guia mostra e que vem do código ou do binário.
type Data struct {
	Root string                              // raiz do repo
	Run  func(args []string) (string, error) // roda o sentinela em processo (diretiva run)
	// RunInit roda o init em processo, num diretório vazio, com o ambiente de um cenário
	// (diretiva init: "ok", "gh-sem-login", "mcp-sem-linear", "mcp-varios-times").
	RunInit   func(scenario string, args []string) (string, error)
	Simulator []SimCase
	Errors    []ErrorCase
	ConfigRef []KeyRef // sentinela.yaml principal
	TeamRef   []KeyRef // config.d/teams/<time>.yaml
	Commands  []CommandRef
	Schemas   map[string]json.RawMessage // "main" e "team"
	Bases     map[string]json.RawMessage // objetos de partida do gerador, na ordem do arquivo
}

// SimCase é um caso do simulador de roteamento: a saída real do owners explain.
type SimCase struct {
	ID        string   `json:"id"`
	Group     string   `json:"group"`
	Label     string   `json:"label"`
	Why       string   `json:"why"`
	Command   string   `json:"command"`
	Output    string   `json:"output"`
	Fields    []Field  `json:"fields"`
	Secondary []string `json:"secondary"`
}

// Field é uma linha do resultado do owners explain (time, projeto, canal, ...), com a fonte.
type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Source string `json:"source"`
}

// ErrorCase é uma mensagem real do config validate, com a causa e a correção.
type ErrorCase struct {
	Kind    string `json:"kind"` // erro | aviso
	Where   string `json:"where"`
	Message string `json:"message"`
	Cause   string `json:"cause"`
	Fix     string `json:"fix"`
}

// KeyRef é uma chave de config.
type KeyRef struct {
	Path, Type, Default, Where, Desc string
}

// CommandRef é um comando do binário com as flags.
type CommandRef struct {
	Path, Short string
	Flags       []FlagRef
}

// FlagRef é uma flag de um comando.
type FlagRef struct {
	Name, Short, Type, Default, Usage string
}

type section struct {
	id, title string
	html      string
	subs      [][2]string // id, título dos h3
}

// Render monta o index.html.
func Render(d Data) ([]byte, error) {
	files, err := filepath.Glob(filepath.Join(d.Root, SourcesDir, "*.md"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("nenhuma fonte em %s", SourcesDir)
	}
	g := &gen{d: d, ids: map[string]int{}}
	var secs []section
	for _, file := range files {
		src, err := os.ReadFile(file) //nolint:gosec // fontes do próprio repo
		if err != nil {
			return nil, err
		}
		rel, _ := filepath.Rel(d.Root, file)
		body, err := g.markdown(string(src), filepath.ToSlash(rel), 1, true)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		secs = append(secs, splitSection(body))
	}
	css, _ := assets.ReadFile("assets/style.css")
	js, _ := assets.ReadFile("assets/app.js")
	payload, err := json.Marshal(map[string]any{
		"simulator": d.Simulator,
		"schemas":   d.Schemas,
		"bases":     d.Bases,
	})
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Guia do Sentinela</title>
<meta name="description" content="Guia completo e interativo do Sentinela: avaliar, instalar, configurar e usar no dia a dia.">
<!-- Gerado por: sentinela docs guide --out docs/guia/index.html. Não edite à mão: edite docs/guia/fontes e rode de novo. -->
<style>
`)
	b.Write(css)
	b.WriteString(`</style>
</head>
<body>
<a class="skip" href="#conteudo">Pular para o conteúdo</a>
<header class="top">
  <button class="menu-btn" type="button" aria-controls="nav" aria-expanded="false" title="Seções">☰</button>
  <a class="brand" href="#">Sentinela <span>guia</span></a>
  <button class="theme-btn" type="button" title="Alternar tema claro e escuro" aria-label="Alternar tema">◐</button>
</header>
<div class="layout">
<nav id="nav" class="nav" aria-label="Seções do guia">
  <label class="search"><span class="sr">Buscar no guia</span><input id="busca" type="search" placeholder="Buscar no guia (ex.: fix_trigger)" autocomplete="off"></label>
  <ol id="resultados" class="results" hidden></ol>
  <ol class="toc">
`)
	for _, s := range secs {
		fmt.Fprintf(&b, `    <li><a href="#%s">%s</a>`, s.id, html.EscapeString(s.title))
		if len(s.subs) > 0 {
			b.WriteString(`<ol>`)
			for _, sub := range s.subs {
				fmt.Fprintf(&b, `<li><a href="#%s">%s</a></li>`, sub[0], html.EscapeString(sub[1]))
			}
			b.WriteString(`</ol>`)
		}
		b.WriteString("</li>\n")
	}
	b.WriteString(`  </ol>
</nav>
<main id="conteudo" class="content">
`)
	for _, s := range secs {
		fmt.Fprintf(&b, "<section class=\"sec\" id=\"%s\">\n%s\n</section>\n", s.id, s.html)
	}
	b.WriteString(`<footer class="foot">Gerado por <code>sentinela docs guide</code> a partir de <code>docs/guia/fontes</code>, <code>docs/*.md</code>, <code>examples/</code>, <code>schema/</code> e da saída do binário. Um teste falha se este arquivo ficar diferente do gerado.</footer>
</main>
</div>
<script id="guia-dados" type="application/json">`)
	b.Write(payload)
	b.WriteString(`</script>
<script>
`)
	b.Write(js)
	b.WriteString(`</script>
</body>
</html>
`)
	return b.Bytes(), nil
}

var (
	h2Re = regexp.MustCompile(`(?s)^\s*<h2 id="([^"]+)">(.*?)</h2>`)
	h3Re = regexp.MustCompile(`<h3 id="([^"]+)">(.*?)</h3>`)
	tags = regexp.MustCompile(`<[^>]+>`)
)

func plain(s string) string { return html.UnescapeString(tags.ReplaceAllString(s, "")) }

func splitSection(body string) section {
	s := section{html: body}
	if m := h2Re.FindStringSubmatch(body); m != nil {
		s.id, s.title = m[1], plain(m[2])
	}
	for _, m := range h3Re.FindAllStringSubmatch(body, -1) {
		s.subs = append(s.subs, [2]string{m[1], plain(m[2])})
	}
	return s
}

type gen struct {
	d   Data
	ids map[string]int
}

// ids únicos no guia inteiro, sem acento.
type guideIDs struct{ g *gen }

func (i guideIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := slugASCII(string(value))
	if base == "" {
		base = "secao"
	}
	id := base
	if n := i.g.ids[base]; n > 0 {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	i.g.ids[base]++
	return []byte(id)
}

func (i guideIDs) Put(value []byte) { i.g.ids[string(value)]++ }

var (
	directive = regexp.MustCompile(`(?m)^<!--\s*guia:([a-z-]+)\s*(.*?)\s*-->[ \t]*$`)
	tabsRe    = regexp.MustCompile(`(?s)<!--\s*guia:tabs\s*([a-z-]*)\s*-->\n(.*?)\n<!--\s*guia:end\s*-->`)
	mermaidRe = regexp.MustCompile("(?s)```mermaid\n.*?```\n?")
	linkRe    = regexp.MustCompile(`\]\(([^)\s]+)\)`)
	tabHead   = regexp.MustCompile(`(?m)^### (.+)$`)
)

// markdown converte uma fonte (com as diretivas) em HTML. srcFile é o arquivo de origem relativo
// à raiz (para reescrever os links, quando rewrite); shift soma níveis aos títulos (# vira <h2>
// com shift 1).
func (g *gen) markdown(src, srcFile string, shift int, rewrite bool) (string, error) {
	if rewrite {
		src = rewriteLinks(src, srcFile)
	}
	var blocks []string
	placeholder := func(h string) string {
		blocks = append(blocks, h)
		return fmt.Sprintf("\n\nGUIABLOCO%dFIM\n\n", len(blocks)-1)
	}
	var ferr error
	src = tabsRe.ReplaceAllStringFunc(src, func(m string) string {
		sm := tabsRe.FindStringSubmatch(m)
		h, err := g.tabs(sm[1], sm[2], srcFile, shift)
		if err != nil {
			ferr = err
		}
		return placeholder(h)
	})
	src = directive.ReplaceAllStringFunc(src, func(m string) string {
		sm := directive.FindStringSubmatch(m)
		h, err := g.directive(sm[1], sm[2], shift)
		if err != nil && ferr == nil {
			ferr = fmt.Errorf("guia:%s %s: %w", sm[1], sm[2], err)
		}
		return placeholder(h)
	})
	if ferr != nil {
		return "", ferr
	}
	out, err := g.render(shiftHeadings(src, shift))
	if err != nil {
		return "", err
	}
	for i, h := range blocks {
		tok := fmt.Sprintf("GUIABLOCO%dFIM", i)
		out = strings.Replace(out, "<p>"+tok+"</p>", h, 1)
	}
	return out, nil
}

func (g *gen) render(src string) (string, error) {
	md := goldmark.New(
		goldmark.WithExtensions(extension.GFM),
		goldmark.WithParserOptions(parser.WithAutoHeadingID()),
		goldmark.WithRendererOptions(gmhtml.WithUnsafe()),
	)
	ctx := parser.NewContext(parser.WithIDs(guideIDs{g}))
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf, parser.WithContext(ctx)); err != nil {
		return "", err
	}
	out := buf.String()
	// Tabelas largas rolam dentro de si mesmas, nunca a página.
	out = strings.ReplaceAll(out, "<table>", `<div class="table-scroll"><table>`)
	out = strings.ReplaceAll(out, "</table>", "</table></div>")
	return out, nil
}

// shiftHeadings soma níveis aos títulos fora de blocos de código.
func shiftHeadings(src string, shift int) string {
	if shift == 0 {
		return src
	}
	lines := strings.Split(src, "\n")
	inFence := false
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(l, "#") {
			n := len(l) - len(strings.TrimLeft(l, "#"))
			if n <= 6 && len(l) > n && l[n] == ' ' {
				lvl := n + shift
				if lvl > 6 {
					lvl = 6
				}
				lines[i] = strings.Repeat("#", lvl) + l[n:]
			}
		}
	}
	return strings.Join(lines, "\n")
}

// rewriteLinks torna os links relativos à pasta do guia (docs/guia). Numa fonte do guia, "#x" é
// uma âncora do próprio guia; num documento incluído, aponta para o documento original.
func rewriteLinks(src, srcFile string) string {
	srcDir := filepath.ToSlash(filepath.Dir(srcFile))
	guideSource := srcDir == SourcesDir
	return linkRe.ReplaceAllStringFunc(src, func(m string) string {
		target := linkRe.FindStringSubmatch(m)[1]
		if strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
			return m
		}
		if strings.HasPrefix(target, "#") {
			if guideSource {
				return m
			}
			return "](" + relTo(srcFile) + target + ")"
		}
		path, frag, _ := strings.Cut(target, "#")
		joined := filepath.ToSlash(filepath.Clean(filepath.Join(srcDir, path)))
		rel, err := filepath.Rel(OutDir, joined)
		if err != nil {
			return m
		}
		rel = filepath.ToSlash(rel)
		if frag != "" {
			rel += "#" + frag
		}
		return "](" + rel + ")"
	})
}

// relTo é o caminho de um arquivo do repo visto de docs/guia.
func relTo(file string) string {
	rel, _ := filepath.Rel(OutDir, file)
	return filepath.ToSlash(rel)
}

func (g *gen) tabs(name, inner, srcFile string, shift int) (string, error) {
	idx := tabHead.FindAllStringSubmatchIndex(inner, -1)
	if len(idx) == 0 {
		return "", fmt.Errorf("guia:tabs sem ### títulos")
	}
	var head, body strings.Builder
	if name == "" {
		return "", fmt.Errorf("guia:tabs precisa de um nome (ids estáveis para os links)")
	}
	group := name
	head.WriteString(`<div class="tabs" data-tabs><div class="tab-list" role="tablist">`)
	for i, ix := range idx {
		title := inner[ix[2]:ix[3]]
		end := len(inner)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		content := inner[ix[1]:end]
		h, err := g.markdown(content, srcFile, shift, false)
		if err != nil {
			return "", err
		}
		id := fmt.Sprintf("%s-%s", group, slugASCII(title))
		sel := "false"
		hidden := " hidden"
		if i == 0 {
			sel, hidden = "true", ""
		}
		fmt.Fprintf(&head, `<button type="button" role="tab" id="t-%s" aria-controls="%s" aria-selected="%s">%s</button>`, id, id, sel, html.EscapeString(title))
		fmt.Fprintf(&body, `<div class="tab-panel" role="tabpanel" id="%s" aria-labelledby="t-%s"%s>%s</div>`, id, id, hidden, h)
	}
	head.WriteString(`</div>`)
	return head.String() + body.String() + `</div>`, nil
}

func (g *gen) directive(name, arg string, shift int) (string, error) {
	switch name {
	case "include":
		return g.include(arg)
	case "file":
		file, lang, _ := strings.Cut(arg, " ")
		data, err := os.ReadFile(filepath.Join(g.d.Root, file)) //nolint:gosec // arquivo do próprio repo
		if err != nil {
			return "", err
		}
		return codeBlock(string(data), lang, file), nil
	case "annotated":
		data, err := os.ReadFile(filepath.Join(g.d.Root, arg)) //nolint:gosec // arquivo do próprio repo
		if err != nil {
			return "", err
		}
		return annotated(string(data), arg), nil
	case "run":
		if g.d.Run == nil {
			return "", fmt.Errorf("sem Run")
		}
		args := splitArgs(arg)
		out, err := g.d.Run(args)
		if err != nil {
			return "", err
		}
		return codeBlock("$ sentinela "+arg+"\n"+out, "console", "saída real do binário"), nil
	case "init":
		scenario, rest, _ := strings.Cut(arg, " ")
		if g.d.RunInit == nil {
			return "", fmt.Errorf("sem RunInit")
		}
		out, err := g.d.RunInit(scenario, splitArgs(rest))
		if err != nil {
			return "", err
		}
		return codeBlock("$ sentinela "+rest+"\n"+out, "console", "saída real do binário (ambiente simulado: "+initScenarios[scenario]+")"), nil
	case "diagram":
		d, ok := diagrams[arg]
		if !ok {
			return "", fmt.Errorf("diagrama desconhecido")
		}
		return d.render(), nil
	case "simulator":
		return simulatorHTML(g.d.Simulator), nil
	case "generator":
		return generatorHTML(), nil
	case "errors":
		return errorsHTML(g.d.Errors), nil
	case "reference":
		return referenceHTML(g.d.ConfigRef, g.d.TeamRef), nil
	case "commands":
		return commandsHTML(g.d.Commands), nil
	}
	return "", fmt.Errorf("diretiva desconhecida")
}

// include traz um documento (ou uma seção dele, depois de #) como parte do guia, com os títulos
// abaixo do nível atual e os links reescritos. Os blocos mermaid saem: o guia usa os SVGs.
func (g *gen) include(arg string) (string, error) {
	file, heading, _ := strings.Cut(arg, "#")
	data, err := os.ReadFile(filepath.Join(g.d.Root, file)) //nolint:gosec // arquivo do próprio repo
	if err != nil {
		return "", err
	}
	src := string(data)
	if heading != "" {
		src, err = sectionOf(src, heading)
		if err != nil {
			return "", err
		}
	} else if i := strings.Index(src, "\n"); strings.HasPrefix(src, "# ") && i > 0 {
		src = src[i+1:] // o título do documento já é o da seção do guia
	}
	src = mermaidRe.ReplaceAllString(src, "")
	// Os títulos do trecho ficam no nível 4 (abaixo do ### da fonte).
	minLvl := 7
	inFence := false
	for _, l := range strings.Split(src, "\n") {
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(l, "#") {
			n := len(l) - len(strings.TrimLeft(l, "#"))
			if n < minLvl {
				minLvl = n
			}
		}
	}
	shift := 0
	if minLvl < 7 {
		shift = 4 - minLvl
	}
	h, err := g.markdown(src, file, shift, true)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf(`<div class="included">%s<p class="source">Fonte: <a href="%s">%s</a></p></div>`, h, relTo(file), html.EscapeString(file)), nil
}

// sectionOf devolve o corpo da seção com o título dado, até o próximo título do mesmo nível ou
// acima.
func sectionOf(src, heading string) (string, error) {
	lines := strings.Split(src, "\n")
	inFence := false
	start, lvl := -1, 0
	for i, l := range lines {
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
		}
		if inFence || !strings.HasPrefix(l, "#") {
			continue
		}
		n := len(l) - len(strings.TrimLeft(l, "#"))
		title := strings.TrimSpace(l[n:])
		if start < 0 {
			if title == heading {
				start, lvl = i+1, n
			}
			continue
		}
		if n <= lvl {
			return strings.Join(lines[start:i], "\n"), nil
		}
	}
	if start < 0 {
		return "", fmt.Errorf("título %q não encontrado", heading)
	}
	return strings.Join(lines[start:], "\n"), nil
}

var initScenarios = map[string]string{
	"ok":               "Docker, gh logado, claude e os scanners instalados",
	"gh-sem-login":     "gh instalado, mas sem login",
	"mcp-sem-linear":   "claude sem o MCP do Linear conectado",
	"mcp-varios-times": "o Linear tem mais de um time",
}

func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	quote := rune(0)
	has := false
	for _, r := range s {
		switch {
		case quote != 0 && r == quote:
			quote = 0
		case quote == 0 && (r == '"' || r == '\''):
			quote, has = r, true
		case quote == 0 && r == ' ':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func codeBlock(code, lang, title string) string {
	code = strings.TrimRight(code, "\n")
	var b strings.Builder
	b.WriteString(`<div class="code">`)
	if title != "" {
		fmt.Fprintf(&b, `<div class="code-title">%s</div>`, html.EscapeString(title))
	}
	fmt.Fprintf(&b, `<pre><code class="language-%s">%s</code></pre></div>`, html.EscapeString(lang), html.EscapeString(code))
	return b.String()
}

// annotated mostra um yaml linha a linha, com o comentário de cada linha ao lado (o porquê).
func annotated(src, title string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<div class="annotated"><div class="code-title">%s <span>· cada linha e o porquê</span></div>`, html.EscapeString(title))
	fmt.Fprintf(&b, `<pre class="raw" hidden><code>%s</code></pre><div class="annot-rows">`, html.EscapeString(strings.TrimRight(src, "\n")))
	var note []string // comentários de linha inteira seguidos viram um parágrafo só
	flush := func() {
		if len(note) > 0 {
			fmt.Fprintf(&b, `<div class="annot-note">%s</div>`, html.EscapeString(strings.Join(note, " ")))
			note = nil
		}
	}
	for _, l := range strings.Split(strings.TrimRight(src, "\n"), "\n") {
		code, comment := splitComment(l)
		switch {
		case strings.TrimSpace(code) == "" && comment == "":
			flush()
			b.WriteString(`<div class="annot-gap"></div>`)
		case strings.TrimSpace(code) == "":
			note = append(note, comment)
		case comment == "":
			flush()
			fmt.Fprintf(&b, `<div class="annot-row full"><code>%s</code></div>`, html.EscapeString(strings.TrimRight(code, " ")))
		default:
			flush()
			fmt.Fprintf(&b, `<div class="annot-row"><code>%s</code><span>%s</span></div>`, html.EscapeString(strings.TrimRight(code, " ")), html.EscapeString(comment))
		}
	}
	flush()
	b.WriteString(`</div></div>`)
	return b.String()
}

// splitComment separa o código do comentário de uma linha de yaml (fora de aspas).
func splitComment(l string) (string, string) {
	quote := rune(0)
	prev := ' '
	for i, r := range l {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '#' && (prev == ' ' || prev == '\t' || i == 0):
			return l[:i], strings.TrimSpace(l[i+1:])
		}
		prev = r
	}
	return l, ""
}
