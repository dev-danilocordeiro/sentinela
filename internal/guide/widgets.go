package guide

import (
	"fmt"
	"html"
	"strings"
)

var esc = html.EscapeString

// breakable deixa uma chave longa quebrar só depois de ponto ou de [].
func breakable(path string) string {
	s := esc(path)
	s = strings.ReplaceAll(s, ".", ".<wbr>")
	return strings.ReplaceAll(s, "[]", "[]<wbr>")
}

// simulatorHTML: a lista de casos reais do owners explain; o JS mostra o escolhido. Sem JS, cada
// caso aparece como um bloco com a saída.
func simulatorHTML(cases []SimCase) string {
	var b strings.Builder
	b.WriteString(`<div class="sim" data-sim><label class="field"><span>Caminho no exemplo acme</span><select data-sim-select>`)
	group := ""
	for _, c := range cases {
		if c.Group != group {
			if group != "" {
				b.WriteString(`</optgroup>`)
			}
			group = c.Group
			fmt.Fprintf(&b, `<optgroup label="%s">`, esc(group))
		}
		fmt.Fprintf(&b, `<option value="%s">%s</option>`, esc(c.ID), esc(c.Label))
	}
	if group != "" {
		b.WriteString(`</optgroup>`)
	}
	b.WriteString(`</select></label><div class="sim-out" data-sim-out aria-live="polite"></div><noscript>`)
	for _, c := range cases {
		b.WriteString(codeBlock("$ "+c.Command+"\n"+c.Output, "console", c.Label))
	}
	b.WriteString(`</noscript></div>`)
	return b.String()
}

type formField struct {
	key, label, value, help, kind string // kind: text | textarea | select:<a>|<b> | number
}

func formHTML(id, title, schema, out string, fields []formField) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form class="gen" data-gen="%s" data-schema="%s" data-out="%s" autocomplete="off"><fieldset><legend>%s</legend>`, esc(id), esc(schema), esc(out), esc(title))
	for _, f := range fields {
		fid := id + "-" + strings.ReplaceAll(f.key, ".", "-")
		fmt.Fprintf(&b, `<label class="field" for="%s"><span>%s</span>`, fid, esc(f.label))
		switch {
		case f.kind == "textarea":
			fmt.Fprintf(&b, `<textarea id="%s" name="%s" rows="3" spellcheck="false">%s</textarea>`, fid, esc(f.key), esc(f.value))
		case strings.HasPrefix(f.kind, "select:"):
			fmt.Fprintf(&b, `<select id="%s" name="%s">`, fid, esc(f.key))
			for _, o := range strings.Split(strings.TrimPrefix(f.kind, "select:"), "|") {
				sel := ""
				if o == f.value {
					sel = " selected"
				}
				fmt.Fprintf(&b, `<option%s>%s</option>`, sel, esc(o))
			}
			b.WriteString(`</select>`)
		case f.kind == "number":
			fmt.Fprintf(&b, `<input id="%s" name="%s" type="number" step="0.5" value="%s">`, fid, esc(f.key), esc(f.value))
		default:
			fmt.Fprintf(&b, `<input id="%s" name="%s" type="text" value="%s" spellcheck="false">`, fid, esc(f.key), esc(f.value))
		}
		if f.help != "" {
			fmt.Fprintf(&b, `<small>%s</small>`, esc(f.help))
		}
		b.WriteString(`</label>`)
	}
	b.WriteString(`</fieldset><div class="gen-result"><div class="gen-status" data-gen-status aria-live="polite"></div>`)
	fmt.Fprintf(&b, `<div class="code"><div class="code-title" data-gen-file>%s</div><pre><code class="language-yaml" data-gen-yaml></code></pre></div>`, esc(out))
	b.WriteString(`<p class="hint">Antes do PR, confira na sua máquina: <code>sentinela config validate --offline</code> (no repo da config, com <code>--changed</code> e o arquivo do time para ver o roteamento antes e depois). O navegador só confere o formato contra o JSON Schema; o <code>config validate</code> confere também as regras (módulo em dois times, chave da organização no arquivo do time, projeto repetido) e, sem <code>--offline</code>, se o Linear e o Slack têm o que o arquivo cita.</p></div></form>`)
	return b.String()
}

func generatorHTML() string {
	small := formHTML("pequena", "Empresa pequena: sentinela.yaml", "main", "sentinela.yaml", []formField{
		{key: "modo", label: "Modo", value: "completo", kind: "select:completo|mcp|local", help: "completo: apps próprias; mcp: piloto pela sua conta; local: sem Linear"},
		{key: "time", label: "Nome do time no Sentinela", value: "engenharia", help: "minúsculas, sem espaço"},
		{key: "github", label: "Time no GitHub (como no CODEOWNERS)", value: "@empresa/engenharia"},
		{key: "linear", label: "Chave do time no Linear", value: "ENG", help: "a do identificador das issues (ENG-123)"},
		{key: "projeto", label: "Projeto do Linear", value: "Sentinela", help: "vazio: as issues ficam no time, sem projeto"},
		{key: "canal", label: "Canal do Slack (ID C...)", value: "C0EMPRESA01", help: "o ID está em Detalhes do canal; o bot precisa estar nele"},
		{key: "grupo", label: "Grupo do Slack para menção (ID S...)", value: "S0EMPRESA01", help: "vazio: ninguém é mencionado; @grupo como texto não notifica"},
		{key: "repos", label: "Repos (um por linha, owner/repo)", value: "empresa/api\nempresa/web", kind: "textarea"},
		{key: "provider", label: "Provider dos agentes", value: "anthropic_api", kind: "select:anthropic_api|claude_code", help: "empresa: anthropic_api, com a chave da empresa"},
		{key: "per_run", label: "Teto por rodada de descoberta (US$)", value: "5", kind: "number"},
		{key: "per_pr", label: "Teto por correção (US$)", value: "3", kind: "number"},
		{key: "gatilho", label: "Quem pode pedir a correção", value: "team_members", kind: "select:team_members|anyone|lista", help: "lista: os e-mails abaixo"},
		{key: "lista", label: "E-mails (com gatilho = lista), um por linha", value: "", kind: "textarea"},
		{key: "label_gatilho", label: "Label de gatilho", value: "sentinela-fix"},
	})
	team := formHTML("time", "Time novo: config.d/teams/<time>.yaml", "team", "config.d/teams/financeiro.yaml", []formField{
		{key: "nome", label: "Nome do time (nome do arquivo)", value: "financeiro", help: "minúsculas; vira config.d/teams/<nome>.yaml"},
		{key: "github", label: "Time no GitHub", value: "@acme/financeiro"},
		{key: "linear", label: "Chave do time no Linear", value: "FIN"},
		{key: "projeto", label: "Projeto do time no Linear", value: "Financeiro", help: "vazio: cai no do repo ou no defaults.linear.project"},
		{key: "seguranca", label: "Projeto para achados de segurança (opcional)", value: "", help: "vira linear.category_projects.security"},
		{key: "canal", label: "Canal do Slack (ID C...)", value: "C0ACMEFIN01"},
		{key: "grupo", label: "Grupo para menção (ID S...)", value: "S0ACMEFIN01"},
		{key: "assignee", label: "Responsável pelas issues", value: "none", kind: "select:none|lead|round_robin"},
		{key: "lead", label: "E-mail do líder (com lead)", value: ""},
		{key: "modulos", label: "Módulos: repo | caminho | projeto | override (um por linha)", value: "acme/plataforma | modules/faturamento/** | Financeiro · Faturamento", kind: "textarea", help: "projeto e override são opcionais; override só para arquivos transversais (Dockerfile, CI)"},
		{key: "repos", label: "Repos inteiros do time (um por linha)", value: "", kind: "textarea"},
		{key: "label_gatilho", label: "Label de gatilho própria (opcional)", value: "", help: "vazio: usa a da organização"},
		{key: "label_done", label: "Label de PR aberto própria (opcional)", value: ""},
		{key: "label_failed", label: "Label de falha própria (opcional)", value: ""},
	})
	return `<div class="tabs" data-tabs><div class="tab-list" role="tablist">` +
		`<button type="button" role="tab" id="t-gen-pequena" aria-controls="gen-pequena" aria-selected="true">Empresa pequena</button>` +
		`<button type="button" role="tab" id="t-gen-time" aria-controls="gen-time" aria-selected="false">Time novo</button></div>` +
		`<div class="tab-panel" role="tabpanel" id="gen-pequena" aria-labelledby="t-gen-pequena">` + small + `</div>` +
		`<div class="tab-panel" role="tabpanel" id="gen-time" aria-labelledby="t-gen-time" hidden>` + team + `</div></div>`
}

func errorsHTML(cases []ErrorCase) string {
	var b strings.Builder
	b.WriteString(`<div class="filterable" data-filter><label class="search inline"><span class="sr">Filtrar mensagens</span><input type="search" placeholder="Cole aqui a mensagem do config validate" data-filter-input></label><p class="count" data-filter-count></p>`)
	for _, c := range cases {
		fmt.Fprintf(&b, `<article class="err-card %s" data-filter-item><p class="err-msg"><span class="tag">%s</span> <span class="where">%s</span></p><pre><code>%s</code></pre><dl><dt>Causa</dt><dd>%s</dd><dt>Correção</dt><dd>%s</dd></dl></article>`,
			esc(c.Kind), esc(c.Kind), esc(c.Where), esc(c.Message), esc(c.Cause), esc(c.Fix))
	}
	b.WriteString(`</div>`)
	return b.String()
}

func refTable(title string, refs []KeyRef, where bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<h4>%s</h4><div class="table-scroll"><table class="ref"><thead><tr><th>Chave</th><th>Tipo</th><th>Padrão</th>`, esc(title))
	if where {
		b.WriteString(`<th>Onde pode ficar</th>`)
	}
	b.WriteString(`<th>O que é</th></tr></thead><tbody>`)
	for _, r := range refs {
		fmt.Fprintf(&b, `<tr data-filter-item><td><code class="k">%s</code></td><td>%s</td><td>%s</td>`, breakable(r.Path), esc(r.Type), esc(r.Default))
		if where {
			fmt.Fprintf(&b, `<td>%s</td>`, esc(r.Where))
		}
		fmt.Fprintf(&b, `<td>%s</td></tr>`, esc(r.Desc))
	}
	b.WriteString(`</tbody></table></div>`)
	return b.String()
}

func referenceHTML(main, team []KeyRef) string {
	return `<div class="filterable" data-filter><label class="search inline"><span class="sr">Filtrar chaves</span><input type="search" placeholder="Filtrar chaves (ex.: notify, poll_interval)" data-filter-input></label><p class="count" data-filter-count></p>` +
		refTable(fmt.Sprintf("sentinela.yaml principal (%d chaves)", len(main)), main, true) +
		refTable(fmt.Sprintf("config.d/teams/<time>.yaml (%d chaves)", len(team)), team, false) + `</div>`
}

func commandsHTML(cmds []CommandRef) string {
	var b strings.Builder
	b.WriteString(`<div class="filterable" data-filter><label class="search inline"><span class="sr">Filtrar comandos</span><input type="search" placeholder="Filtrar comandos e flags (ex.: --offline)" data-filter-input></label><p class="count" data-filter-count></p>`)
	for _, c := range cmds {
		fmt.Fprintf(&b, `<article class="cmd" data-filter-item><h4><code>%s</code></h4><p>%s</p>`, esc(c.Path), esc(c.Short))
		if len(c.Flags) > 0 {
			b.WriteString(`<div class="table-scroll"><table class="ref"><thead><tr><th>Flag</th><th>Tipo</th><th>Padrão</th><th>O que faz</th></tr></thead><tbody>`)
			for _, fl := range c.Flags {
				name := "--" + fl.Name
				if fl.Short != "" {
					name = "-" + fl.Short + ", " + name
				}
				fmt.Fprintf(&b, `<tr><td><code class="f">%s</code></td><td>%s</td><td>%s</td><td>%s</td></tr>`, esc(name), esc(fl.Type), esc(fl.Default), esc(fl.Usage))
			}
			b.WriteString(`</tbody></table></div>`)
		}
		b.WriteString(`</article>`)
	}
	b.WriteString(`</div>`)
	return b.String()
}
