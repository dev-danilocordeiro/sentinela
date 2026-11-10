package guide

import (
	"fmt"
	"html"
	"math"
	"strings"
)

// Os diagramas do guia são SVG gerados aqui, no build: nada de mermaid no navegador. As cores vêm
// de classes CSS do guia, então o mesmo SVG serve ao tema claro e ao escuro.

type box struct {
	id    string
	x, y  float64
	w, h  float64
	label string // linhas separadas por \n
	kind  string // "" (passo), "human" (decisão humana), "store" (dado ou serviço externo), "ask" (pergunta)
}

type arrow struct {
	from, to string
	label    string
	dashed   bool
	via      [][2]float64 // pontos intermediários, para contornar caixas
	end      *[2]float64  // ponto final fixo na borda do destino (sem ele, a reta mira o centro)
}

type diagramSpec struct {
	title  string
	w, h   float64
	boxes  []box
	arrows []arrow
}

func (d diagramSpec) box(id string) box {
	for _, b := range d.boxes {
		if b.id == id {
			return b
		}
	}
	panic("diagrama " + d.title + ": caixa inexistente " + id)
}

// clip devolve o ponto em que a reta do centro da caixa até (tx, ty) cruza a borda dela.
func clip(b box, tx, ty float64) (float64, float64) {
	cx, cy := b.x+b.w/2, b.y+b.h/2
	dx, dy := tx-cx, ty-cy
	if dx == 0 && dy == 0 {
		return cx, cy
	}
	sx, sy := math.Inf(1), math.Inf(1)
	if dx != 0 {
		sx = (b.w / 2) / math.Abs(dx)
	}
	if dy != 0 {
		sy = (b.h / 2) / math.Abs(dy)
	}
	s := math.Min(sx, sy)
	return cx + dx*s, cy + dy*s
}

func f(v float64) string { return fmt.Sprintf("%.1f", v) }

// textWidth é uma estimativa da largura do texto (13px, fonte sem serifa), para o fundo dos rótulos.
func textWidth(s string, size float64) float64 { return float64(len([]rune(s))) * size * 0.56 }

func (d diagramSpec) render() string {
	var b strings.Builder
	fmt.Fprintf(&b, `<figure class="diagram"><div class="diagram-scroll"><svg viewBox="0 0 %s %s" role="img" aria-label="%s" style="min-width:%dpx">`,
		f(d.w), f(d.h), html.EscapeString(d.title), int(math.Min(d.w, 640)))
	fmt.Fprintf(&b, `<title>%s</title><defs><marker id="seta-%s" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse"><path d="M0,0 L10,5 L0,10 z" class="dg-head"/></marker></defs>`,
		html.EscapeString(d.title), slugASCII(d.title))
	var labels strings.Builder
	for _, a := range d.arrows {
		from, to := d.box(a.from), d.box(a.to)
		pts := [][2]float64{}
		first := [2]float64{to.x + to.w/2, to.y + to.h/2}
		if len(a.via) > 0 {
			first = a.via[0]
		}
		sx, sy := clip(from, first[0], first[1])
		pts = append(pts, [2]float64{sx, sy})
		pts = append(pts, a.via...)
		last := [2]float64{from.x + from.w/2, from.y + from.h/2}
		if len(a.via) > 0 {
			last = a.via[len(a.via)-1]
		}
		ex, ey := clip(to, last[0], last[1])
		if a.end != nil {
			ex, ey = a.end[0], a.end[1]
		}
		pts = append(pts, [2]float64{ex, ey})
		var p []string
		for _, pt := range pts {
			p = append(p, f(pt[0])+","+f(pt[1]))
		}
		cls := "dg-edge"
		if a.dashed {
			cls += " dg-dashed"
		}
		fmt.Fprintf(&b, `<polyline class="%s" points="%s" marker-end="url(#seta-%s)"/>`, cls, strings.Join(p, " "), slugASCII(d.title))
		if a.label != "" {
			// O rótulo fica no meio do segmento central.
			i := (len(pts) - 1) / 2
			mx, my := (pts[i][0]+pts[i+1][0])/2, (pts[i][1]+pts[i+1][1])/2
			lines := strings.Split(a.label, "\n")
			wmax := 0.0
			for _, l := range lines {
				wmax = math.Max(wmax, textWidth(l, 11.5))
			}
			hh := float64(len(lines)) * 14
			fmt.Fprintf(&labels, `<rect class="dg-label-bg" x="%s" y="%s" width="%s" height="%s" rx="3"/>`, f(mx-wmax/2-4), f(my-hh/2-2), f(wmax+8), f(hh+4))
			for j, l := range lines {
				fmt.Fprintf(&labels, `<text class="dg-label" x="%s" y="%s" text-anchor="middle">%s</text>`, f(mx), f(my-hh/2+11+float64(j)*14), html.EscapeString(l))
			}
		}
	}
	for _, bx := range d.boxes {
		cls := "dg-box"
		if bx.kind != "" {
			cls += " dg-" + bx.kind
		}
		rx := 8.0
		if bx.kind == "store" {
			rx = 18
		}
		fmt.Fprintf(&b, `<rect class="%s" x="%s" y="%s" width="%s" height="%s" rx="%s"/>`, cls, f(bx.x), f(bx.y), f(bx.w), f(bx.h), f(rx))
		lines := strings.Split(bx.label, "\n")
		top := bx.y + bx.h/2 - float64(len(lines)-1)*8
		for j, l := range lines {
			tc := "dg-text"
			if j > 0 {
				tc += " dg-sub"
			}
			fmt.Fprintf(&b, `<text class="%s" x="%s" y="%s" text-anchor="middle" dominant-baseline="middle">%s</text>`, tc, f(bx.x+bx.w/2), f(top+float64(j)*16), html.EscapeString(l))
		}
	}
	b.WriteString(labels.String())
	fmt.Fprintf(&b, `</svg></div><figcaption>%s</figcaption></figure>`, html.EscapeString(d.title))
	return b.String()
}

// Grade dos diagramas de modo: 4 colunas de 180 px (60 px entre elas) e 3 linhas de 60 px.
func col(i int) float64 { return 20 + float64(i)*240 }
func row(i int) float64 { return 30 + float64(i)*130 }

func cell(id string, c, r int, label, kind string) box {
	return box{id: id, x: col(c), y: row(r), w: 180, h: 60, label: label, kind: kind}
}

var diagrams = map[string]diagramSpec{
	"ciclo": {
		title: "O ciclo do Sentinela", w: 930, h: 330,
		boxes: []box{
			{id: "achado", x: 20, y: 40, w: 230, h: 70, label: "1. Achado\nscanners e agentes acham"},
			{id: "issue", x: 350, y: 40, w: 230, h: 70, label: "2. Issue\nno board do time dono"},
			{id: "decisao", x: 680, y: 40, w: 230, h: 70, label: "3. Decisão humana\nrecusar · pedir correção · responder", kind: "human"},
			{id: "aprende", x: 20, y: 220, w: 230, h: 70, label: "6. Aprendizado\nsupressões, exemplos, limiares"},
			{id: "pr", x: 350, y: 220, w: 230, h: 70, label: "5. PR em draft\nreviewers e gate comentam"},
			{id: "correcao", x: 680, y: 220, w: 230, h: 70, label: "4. Correção\nworktree isolado, Claude Code"},
		},
		arrows: []arrow{
			{from: "achado", to: "issue", label: "Judge aprova"},
			{from: "issue", to: "decisao"},
			{from: "decisao", to: "correcao", label: "label de gatilho"},
			{from: "correcao", to: "pr"},
			{from: "pr", to: "aprende", label: "merge ou revert\n(uma pessoa)"},
			{from: "aprende", to: "achado", label: "próxima rodada"},
			{from: "decisao", to: "aprende", label: "recusa vira supressão", dashed: true},
		},
	},
	"modo-local": {
		title: "Modo local: relatório na máquina, decisões pela CLI", w: 940, h: 370,
		boxes: []box{
			cell("repo", 0, 0, "Repo no GitHub", "store"),
			cell("scan", 1, 0, "sentinela scan", ""),
			cell("pg", 2, 0, "Postgres", "store"),
			cell("rel", 3, 0, "reports/local\nmd e html, com os comandos", ""),
			cell("pessoa", 3, 1, "Pessoa", "human"),
			cell("worker", 2, 2, "sentinela worker", ""),
			cell("wt", 1, 2, "worktree\nclaude -p e o plugin", ""),
			cell("gh", 3, 2, "GitHub: PR em draft (gh)\no gate só comenta", "store"),
		},
		arrows: []arrow{
			{from: "repo", to: "scan", label: "clone"},
			{from: "scan", to: "pg"},
			{from: "pg", to: "rel"},
			{from: "pessoa", to: "rel", label: "lê"},
			{from: "pessoa", to: "pg", label: "sentinela finding\naccept · reject · fix ..."},
			{from: "pg", to: "worker", label: "fila"},
			{from: "worker", to: "wt"},
			{from: "worker", to: "gh"},
		},
	},
	"modo-mcp": {
		title: "Modo MCP: o time decide no Linear, sem criar apps", w: 940, h: 400,
		boxes: []box{
			cell("repo", 0, 0, "Repo no GitHub", "store"),
			cell("scan", 1, 0, "sentinela scan", ""),
			cell("pg", 2, 0, "Postgres", "store"),
			cell("slack", 3, 0, "Slack (opcional)\nslack_mcp, como você", "store"),
			cell("pessoa", 0, 1, "Pessoa", "human"),
			cell("lin", 1, 1, "Linear\nissues como você", "store"),
			cell("serve", 2, 1, "sentinela serve\nclaude -p, 1 leitura/ciclo", ""),
			cell("worker", 3, 1, "sentinela worker", ""),
			cell("gh", 3, 2, "GitHub: PR em draft (gh)\no gate só comenta", "store"),
		},
		arrows: []arrow{
			{from: "repo", to: "scan", label: "clone"},
			{from: "scan", to: "pg"},
			{from: "scan", to: "slack", label: "avisos", dashed: true, via: [][2]float64{{350, 12}, {830, 12}}},
			{from: "scan", to: "lin", label: "claude -p\nMCP do Linear"},
			{from: "pessoa", to: "lin", label: "decide"},
			{from: "serve", to: "lin"},
			{from: "serve", to: "pg"},
			{from: "pg", to: "worker", label: "fila"},
			{from: "worker", to: "gh"},
			{from: "worker", to: "lin", label: "comentários **Sentinela", via: [][2]float64{{590, 330}, {350, 330}}},
		},
	},
	"modo-completo": {
		title: "Modo completo: identidade própria, webhook e as apps", w: 940, h: 400,
		boxes: []box{
			cell("repo", 0, 0, "Repo no GitHub", "store"),
			cell("scan", 1, 0, "sentinela scan", ""),
			cell("pg", 2, 0, "Postgres", "store"),
			cell("slack", 3, 0, "Slack\nbot, canal por time", "store"),
			cell("pessoa", 0, 1, "Pessoa", "human"),
			cell("lin", 1, 1, "Linear\napp Sentinela", "store"),
			cell("serve", 2, 1, "sentinela serve\nwebhook ou polling", ""),
			cell("worker", 3, 1, "sentinela worker", ""),
			cell("gh", 3, 2, "GitHub\napp bot abre, revisora comenta", "store"),
		},
		arrows: []arrow{
			{from: "repo", to: "scan", label: "clone"},
			{from: "scan", to: "pg"},
			{from: "scan", to: "slack", label: "bot", via: [][2]float64{{350, 12}, {830, 12}}},
			{from: "scan", to: "lin", label: "API, como o app"},
			{from: "pessoa", to: "lin", label: "decide"},
			{from: "lin", to: "serve"},
			{from: "serve", to: "pg"},
			{from: "pg", to: "worker", label: "fila"},
			{from: "worker", to: "gh", label: "PR em draft"},
			{from: "worker", to: "lin", label: "comentários e estado", via: [][2]float64{{590, 330}, {350, 330}}},
		},
	},
	"precedencia": {
		title: "Como o Sentinela decide time, projeto, canal e menção", w: 880, h: 640,
		boxes: []box{
			{id: "a", x: 20, y: 20, w: 300, h: 50, label: "caminho do arquivo da evidência"},
			{id: "m", x: 20, y: 110, w: 300, h: 64, label: "1. casa com um módulo da config?\noverride vence; senão o glob mais longo", kind: "ask"},
			{id: "c", x: 20, y: 214, w: 300, h: 64, label: "2. casa com o CODEOWNERS?\na última regra que casa vence", kind: "ask"},
			{id: "r", x: 20, y: 318, w: 300, h: 50, label: "3. repos[].team?", kind: "ask"},
			{id: "fb", x: 20, y: 408, w: 300, h: 50, label: "4. fallback_team, senão defaults"},
			{id: "t1", x: 400, y: 117, w: 220, h: 50, label: "time do módulo"},
			{id: "t2", x: 400, y: 221, w: 220, h: 50, label: "time do dono\nteams.*.github ou members"},
			{id: "t3", x: 400, y: 318, w: 220, h: 50, label: "time do repo"},
			{id: "time", x: 660, y: 260, w: 200, h: 60, label: "TIME DONO\ngravado no achado", kind: "human"},
			{id: "p", x: 20, y: 520, w: 840, h: 44, label: "projeto: 1. do módulo → 2. por categoria do time → 3. do time → 4. do repo → 5. defaults.linear.project"},
			{id: "s", x: 20, y: 580, w: 410, h: 44, label: "canal: notify.slack do time → defaults.notify.slack"},
			{id: "n", x: 450, y: 580, w: 410, h: 44, label: "menção: notify.mention do time (ID S...) → ninguém"},
		},
		arrows: []arrow{
			{from: "a", to: "m"},
			{from: "m", to: "t1", label: "sim"},
			{from: "m", to: "c", label: "não"},
			{from: "c", to: "t2", label: "sim"},
			{from: "c", to: "r", label: "não, ou sem donos"},
			{from: "r", to: "t3", label: "sim"},
			{from: "r", to: "fb", label: "não"},
			{from: "t1", to: "time"},
			{from: "t2", to: "time"},
			{from: "t3", to: "time"},
			{from: "fb", to: "time", via: [][2]float64{{640, 433}, {640, 300}}},
			{from: "time", to: "p", end: &[2]float64{770, 520}},
		},
	},
	"pr-do-time": {
		title: "Uma mudança de config pedida por um time", w: 880, h: 250,
		boxes: []box{
			{id: "edita", x: 20, y: 30, w: 250, h: 64, label: "1. edita só o seu arquivo\nconfig.d/teams/<time>.yaml"},
			{id: "local", x: 315, y: 30, w: 250, h: 64, label: "2. confere na máquina\nconfig validate --offline --changed"},
			{id: "pr", x: 610, y: 30, w: 250, h: 64, label: "3. abre o PR"},
			{id: "ci", x: 610, y: 160, w: 250, h: 64, label: "4. o workflow comenta\no roteamento antes e depois"},
			{id: "rev", x: 315, y: 160, w: 250, h: 64, label: "5. CODEOWNERS pede o time\ndono do arquivo", kind: "human"},
			{id: "merge", x: 20, y: 160, w: 250, h: 64, label: "6. aprovado e mergeado: os\nserviços releem no ciclo seguinte"},
		},
		arrows: []arrow{
			{from: "edita", to: "local"},
			{from: "local", to: "pr"},
			{from: "pr", to: "ci"},
			{from: "ci", to: "rev"},
			{from: "rev", to: "merge"},
		},
	},
}

// slugASCII gera um id só com [a-z0-9-], sem acentos.
func slugASCII(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(fold(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

var accents = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "é", "e", "ê", "e", "è", "e", "í", "i", "î", "i",
	"ó", "o", "ô", "o", "õ", "o", "ö", "o", "ú", "u", "ü", "u", "ç", "c",
	"Á", "a", "À", "a", "Â", "a", "Ã", "a", "É", "e", "Ê", "e", "Í", "i", "Ó", "o", "Ô", "o", "Õ", "o", "Ú", "u", "Ç", "c",
)

func fold(s string) string { return accents.Replace(s) }
