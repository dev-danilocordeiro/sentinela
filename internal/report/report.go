// Package report gera o relatório HTML local de uma rodada: achados, custo por agente,
// decisões do juiz, triagem e itens pulados.
package report

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"sort"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Data é o que o template recebe; também serve para saída em texto no terminal.
type Data struct {
	Run       db.AnalysisRun
	Stats     map[string]any
	Cost      llm.NanoUSD
	Budget    llm.NanoUSD
	Agents    []AgentCost
	Findings  []Finding
	Judge     []JudgeDecision
	Triage    []TriageRow
	Skipped   []db.RunItem
	Scanners  []map[string]any
	Issues    []db.ListLinearIssuesByRunRow // issues criadas nesta rodada (inclui achados pendentes de antes)
	Warnings  []string
	Generated time.Time
}

type AgentCost struct {
	Agent      string
	Models     string
	Providers  string
	Latency    time.Duration
	Calls      int64
	Input      int64
	Output     int64
	CacheRead  int64
	CacheWrite int64
	Cost       llm.NanoUSD
}

type Finding struct {
	db.ListFindingsByRunRow
	Ev      []signals.Evidence
	Route   repo.Routing
	JudgeCf float32
	Issues  []db.LinearIssue
}

type JudgeDecision struct {
	Fingerprint string
	Title       string
	Decision    string
	Confidence  float64
	Reason      string
	Status      string
}

type TriageRow struct {
	Package  string
	Deepen   bool
	Category string
	Reason   string
	Summary  string
	Status   string
}

// Load reúne os dados da rodada.
func Load(ctx context.Context, q *db.Queries, runID int64) (*Data, error) {
	run, err := q.GetRun(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("rodada %d: %w", runID, err)
	}
	d := &Data{Run: run, Generated: time.Now()}
	if len(run.Stats) > 0 {
		_ = json.Unmarshal(run.Stats, &d.Stats)
	}
	if v, ok := d.Stats["scanners"].([]any); ok {
		for _, s := range v {
			if m, ok := s.(map[string]any); ok {
				// Estado normalizado: ok | skipped | failed (rodadas antigas gravavam
				// unavailable e not_applicable).
				st, _ := m["status"].(string)
				if st == "unavailable" || st == "not_applicable" {
					st = "skipped"
				}
				m["state"] = st
				if _, ok := m["analyzed"]; !ok {
					m["analyzed"] = 0
				}
				d.Scanners = append(d.Scanners, m)
			}
		}
	}
	if v, ok := d.Stats["warnings"].([]any); ok {
		for _, w := range v {
			d.Warnings = append(d.Warnings, fmt.Sprint(w))
		}
	}
	c, _ := store.NumericToNanoUSD(run.CostUsd)
	b, _ := store.NumericToNanoUSD(run.BudgetUsd)
	d.Cost, d.Budget = llm.NanoUSD(c), llm.NanoUSD(b)

	costs, err := q.CostByAgent(ctx, pgtype.Int8{Int64: runID, Valid: true})
	if err != nil {
		return nil, err
	}
	for _, row := range costs {
		n, _ := store.NumericToNanoUSD(row.CostUsd)
		d.Agents = append(d.Agents, AgentCost{
			Agent: row.Agent, Models: row.Models, Providers: row.Providers, Latency: time.Duration(row.LatencyMs) * time.Millisecond, Calls: row.Calls, Input: row.InputTokens, Output: row.OutputTokens,
			CacheRead: row.CacheReadTokens, CacheWrite: row.CacheWriteTokens, Cost: llm.NanoUSD(n),
		})
	}

	findings, err := q.ListFindingsByRun(ctx, pgtype.Int8{Int64: runID, Valid: true})
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(findings))
	for _, f := range findings {
		ids = append(ids, f.ID)
	}
	byFinding := map[int64][]db.LinearIssue{}
	if len(ids) > 0 {
		lis, err := q.ListLinearIssuesByFindings(ctx, ids)
		if err != nil {
			return nil, err
		}
		for _, li := range lis {
			byFinding[li.FindingID] = append(byFinding[li.FindingID], li)
		}
	}
	for _, f := range findings {
		row := Finding{ListFindingsByRunRow: f, JudgeCf: f.JudgeConfidence.Float32, Issues: byFinding[f.ID]}
		_ = json.Unmarshal(f.Evidence, &row.Ev)
		_ = json.Unmarshal(f.Routing, &row.Route)
		d.Findings = append(d.Findings, row)
	}
	if d.Issues, err = q.ListLinearIssuesByRun(ctx, pgtype.Int8{Int64: runID, Valid: true}); err != nil {
		return nil, err
	}

	items, err := q.ListRunItems(ctx, runID)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		switch {
		case it.Agent == "judge" && it.Status == "ok":
			var out struct {
				Decision string `json:"decision"`
				Verdict  struct {
					Confidence float64 `json:"confidence"`
					Reason     string  `json:"reason"`
				} `json:"verdict"`
				Finding struct {
					Title string `json:"title"`
				} `json:"finding"`
			}
			_ = json.Unmarshal(it.Output, &out)
			d.Judge = append(d.Judge, JudgeDecision{Fingerprint: it.ItemKey, Title: out.Finding.Title, Decision: out.Decision, Confidence: out.Verdict.Confidence, Reason: out.Verdict.Reason, Status: it.Status})
		case it.Agent == "triager" && it.Status == "ok":
			var out struct {
				Category string `json:"category"`
				Deepen   bool   `json:"deepen"`
				Reason   string `json:"reason"`
				Summary  string `json:"summary"`
			}
			_ = json.Unmarshal(it.Output, &out)
			d.Triage = append(d.Triage, TriageRow{Package: it.ItemKey, Deepen: out.Deepen, Category: out.Category, Reason: out.Reason, Summary: out.Summary, Status: it.Status})
		case it.Status != "ok":
			d.Skipped = append(d.Skipped, it)
		}
	}
	sort.SliceStable(d.Triage, func(i, j int) bool { return d.Triage[i].Deepen && !d.Triage[j].Deepen })
	return d, nil
}

// Write renderiza o HTML.
func Write(w io.Writer, d *Data) error {
	return page.Execute(w, d)
}

var page = template.Must(template.New("report").Funcs(template.FuncMap{
	"usd":  func(n llm.NanoUSD) string { return fmt.Sprintf("$%.4f", n.Dollars()) },
	"pct":  func(f float64) string { return fmt.Sprintf("%.0f%%", f*100) },
	"pctf": func(f float32) string { return fmt.Sprintf("%.0f%%", f*100) },
	"text": func(t pgtype.Text) string { return t.String },
	"time": func(t pgtype.Timestamptz) string {
		if !t.Valid {
			return "-"
		}
		return t.Time.Local().Format("2006-01-02 15:04:05")
	},
	"dur": func(v any) string {
		switch x := v.(type) {
		case time.Duration:
			return x.Round(time.Second).String()
		case float64:
			return (time.Duration(x) * time.Millisecond).Round(time.Second).String()
		}
		return "-"
	},
	"short": func(s string) string {
		if len(s) > 10 {
			return s[:10]
		}
		return s
	},
	"jsonText": func(b []byte) string { return string(b) },
}).Parse(htmlTemplate))

const htmlTemplate = `<!doctype html>
<html lang="pt-BR">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sentinela · rodada {{.Run.ID}}</title>
<style>
:root { --bg:#fbfaf8; --fg:#1d1d1b; --muted:#6b6862; --line:#e4e1db; --card:#fff; --accent:#2f5d8a;
  --crit:#a3271f; --high:#c2571a; --med:#a07a12; --low:#4f6d4a; }
@media (prefers-color-scheme: dark) { :root { --bg:#161615; --fg:#ecebe8; --muted:#a29f98; --line:#33322f; --card:#1f1f1d; --accent:#8db4dc; } }
* { box-sizing:border-box }
body { margin:0; background:var(--bg); color:var(--fg); font:15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
main { max-width:1100px; margin:0 auto; padding:24px 16px 64px; }
h1 { font-size:22px; margin:0 0 4px } h2 { font-size:17px; margin:32px 0 8px; } .muted { color:var(--muted) }
.kpis { display:grid; grid-template-columns:repeat(auto-fit,minmax(150px,1fr)); gap:8px; margin:16px 0 }
.kpi { background:var(--card); border:1px solid var(--line); border-radius:8px; padding:10px 12px }
.kpi b { display:block; font-size:20px; font-variant-numeric:tabular-nums }
table { width:100%; border-collapse:collapse; background:var(--card); border:1px solid var(--line); border-radius:8px; overflow:hidden; font-size:14px }
th, td { text-align:left; padding:7px 10px; border-bottom:1px solid var(--line); vertical-align:top }
th { font-weight:600; color:var(--muted); font-size:12px; text-transform:uppercase; letter-spacing:.03em }
td.num { text-align:right; font-variant-numeric:tabular-nums; white-space:nowrap }
.wrap { overflow-x:auto }
.finding { background:var(--card); border:1px solid var(--line); border-radius:8px; padding:14px 16px; margin:10px 0 }
.finding h3 { margin:0 0 6px; font-size:16px }
.tag { display:inline-block; font-size:12px; padding:1px 7px; border-radius:10px; border:1px solid var(--line); margin-right:4px; color:var(--muted) }
.sev-critical { color:#fff; background:var(--crit); border-color:var(--crit) } .sev-high { color:#fff; background:var(--high); border-color:var(--high) }
.sev-medium { color:#fff; background:var(--med); border-color:var(--med) } .sev-low { color:#fff; background:var(--low); border-color:var(--low) }
code, pre { font-family:ui-monospace, SFMono-Regular, Menlo, monospace; font-size:13px }
pre { background:var(--bg); border:1px solid var(--line); border-radius:6px; padding:8px; overflow-x:auto; margin:6px 0 }
a { color:var(--accent) } ul { margin:4px 0 4px 18px; padding:0 }
.dec-publish { color:var(--low); font-weight:600 } .dec-discard { color:var(--muted) } .dec-merge { color:var(--accent) }
</style>
</head>
<body><main>
<h1>Rodada {{.Run.ID}} · {{.Run.Repo}}</h1>
<div class="muted">modo {{.Run.Mode}} · status <b>{{.Run.Status}}</b> · head <code>{{short (text .Run.HeadSha)}}</code>{{with text .Run.BaseSha}} · base <code>{{short .}}</code>{{end}} · {{time .Run.StartedAt}} → {{time .Run.FinishedAt}} · modo sombra (nada publicado no Linear)</div>
{{with text .Run.Error}}<p><b>Erro:</b> {{.}}</p>{{end}}

<div class="kpis">
  <div class="kpi"><span class="muted">Provider</span><b>{{or (index .Stats "provider") "anthropic_api"}}</b></div>
  <div class="kpi"><span class="muted">Chamadas</span><b>{{index .Stats "calls"}}</b>{{with index .Stats "max_calls"}}<span class="muted">de {{.}}</span>{{end}}</div>
  <div class="kpi"><span class="muted">Tempo total</span><b>{{dur (index .Stats "elapsed_ms")}}</b></div>
  <div class="kpi"><span class="muted">Custo</span><b>{{usd .Cost}}</b><span class="muted">{{if .Budget}}de {{usd .Budget}}{{else}}assinatura{{end}}</span></div>
  <div class="kpi"><span class="muted">Pacotes analisados</span><b>{{index .Stats "packages_analyzed"}}</b><span class="muted">de {{index .Stats "packages_total"}}</span></div>
  <div class="kpi"><span class="muted">Aprofundados</span><b>{{index .Stats "deepened"}}</b></div>
  <div class="kpi"><span class="muted">Propostos</span><b>{{index .Stats "proposed"}}</b></div>
  <div class="kpi"><span class="muted">Publicados (sombra)</span><b>{{index .Stats "published"}}</b></div>
  <div class="kpi"><span class="muted">Descartados · duplicados</span><b>{{index .Stats "discarded"}} · {{index .Stats "duplicates"}}</b></div>
  <div class="kpi"><span class="muted">Evidência inválida</span><b>{{or (index .Stats "invalid_evidence") 0}}</b><span class="muted">{{or (index .Stats "evidence_dropped") 0}} evidência(s) removidas</span></div>
  <div class="kpi"><span class="muted">Escopo reduzido</span><b>{{or (index .Stats "narrowed") 0}}</b><span class="muted">sobreposição parcial</span></div>
</div>

<h2>Custo e chamadas por agente</h2>
<div class="wrap"><table>
<tr><th>Agente</th><th>Modelo</th><th>Provider</th><th class="num">Tempo (soma)</th><th class="num">Chamadas</th><th class="num">Entrada</th><th class="num">Saída</th><th class="num">Cache lido</th><th class="num">Cache escrito</th><th class="num">Custo</th></tr>
{{range .Agents}}<tr><td>{{.Agent}}</td><td>{{.Models}}</td><td>{{.Providers}}</td><td class="num">{{dur .Latency}}</td><td class="num">{{.Calls}}</td><td class="num">{{.Input}}</td><td class="num">{{.Output}}</td><td class="num">{{.CacheRead}}</td><td class="num">{{.CacheWrite}}</td><td class="num">{{usd .Cost}}</td></tr>
{{else}}<tr><td colspan="10" class="muted">Nenhuma chamada de modelo nesta rodada.</td></tr>{{end}}
<tr><th colspan="9">Total</th><td class="num"><b>{{usd .Cost}}</b></td></tr>
</table></div>

<h2>Issues no Linear ({{len .Issues}})</h2>
<div class="table-wrap"><table>
<tr><th>Issue</th><th>Papel</th><th>Time</th><th>Achado</th><th>Estado</th></tr>
{{range .Issues}}<tr><td><a href="{{.Url}}">{{.Identifier}}</a></td><td>{{if eq .Role "primary"}}principal{{else}}relacionada{{end}}</td><td>{{.Team}} · {{.LinearTeam}}</td><td>{{.Title}} <span class="muted">({{.Category}}, {{.Severity}})</span></td><td>{{.StateName}}</td></tr>
{{else}}<tr><td colspan="5" class="muted">Nenhuma issue criada nesta rodada (modo sombra, nada aprovado ou limite por time).</td></tr>{{end}}
</table></div>

<h2>Achados ({{len .Findings}})</h2>
{{range .Findings}}<div class="finding">
<h3>{{.Title}}</h3>
<span class="tag sev-{{.Severity}}">{{.Severity}}</span><span class="tag">{{.Category}}</span><span class="tag">confiança {{pctf .Confidence}}</span><span class="tag">juiz {{pctf .JudgeCf}}</span><span class="tag">{{.Module}}</span><span class="tag">time {{or .Route.Team "—"}} · {{.Route.LinearTeam}} / {{.Route.LinearProject}} · {{.Route.Rule}}</span><span class="tag"><code>{{.Fingerprint}}</code></span>{{range .Issues}}<span class="tag"><a href="{{.Url}}">{{.Identifier}}</a> {{.StateName}}</span>{{end}}<span class="tag">estado {{.State}}</span>
<p style="white-space:pre-line">{{.Context}}</p>
{{range .Ev}}<div><a href="{{.Permalink}}">{{.File}}:{{.Line}}</a>{{with .Snippet}}<pre>{{.}}</pre>{{end}}</div>{{end}}
<p><b>Correção proposta:</b> {{.ProposedFix}}</p>
{{with .AcceptanceCriteria}}<b>Critérios de aceite</b><ul>{{range .}}<li>{{.}}</li>{{end}}</ul>{{end}}
{{with .RelatedModules}}<p class="muted">Módulos relacionados: {{range .}}<code>{{.}}</code> {{end}}</p>{{end}}
{{with text .JudgeReason}}<p class="muted"><b>Juiz:</b> {{.}}</p>{{end}}
</div>{{else}}<p class="muted">Nenhum achado aprovado pelo juiz.</p>{{end}}

<h2>Decisões do juiz</h2>
<div class="wrap"><table>
<tr><th>Decisão</th><th class="num">Confiança</th><th>Achado</th><th>Motivo</th></tr>
{{range .Judge}}<tr><td class="dec-{{.Decision}}">{{.Decision}}</td><td class="num">{{pct .Confidence}}</td><td>{{.Title}}<br><code class="muted">{{.Fingerprint}}</code></td><td>{{.Reason}}</td></tr>
{{else}}<tr><td colspan="4" class="muted">Nenhum achado chegou ao juiz.</td></tr>{{end}}
</table></div>

<h2>Triagem</h2>
<div class="wrap"><table>
<tr><th>Pacote</th><th>Aprofundar</th><th>Categoria</th><th>Motivo</th><th>Resumo</th></tr>
{{range .Triage}}<tr><td><code>{{.Package}}</code></td><td>{{if .Deepen}}<b>sim</b>{{else}}não{{end}}</td><td>{{.Category}}</td><td>{{.Reason}}</td><td class="muted">{{.Summary}}</td></tr>{{end}}
</table></div>

{{if .Skipped}}<h2>Itens pulados ou com falha</h2>
<div class="wrap"><table>
<tr><th>Agente</th><th>Item</th><th>Status</th><th>Motivo</th></tr>
{{range .Skipped}}<tr><td>{{.Agent}}</td><td><code>{{.ItemKey}}</code></td><td>{{.Status}}</td><td>{{text .Reason}}</td></tr>{{end}}
</table></div>{{end}}

<h2>Scanners</h2>
<div class="wrap"><table>
<tr><th>Scanner</th><th>Estado</th><th class="num">Analisou</th><th class="num">Sinais</th><th class="num">Já corrigidos (descartados)</th><th>Motivo ou saída</th></tr>
{{range .Scanners}}<tr><td>{{index . "scanner"}}</td><td>{{index . "state"}}</td><td class="num">{{if eq (index . "state") "ok"}}{{index . "analyzed"}} {{index . "unit"}}{{else}}-{{end}}</td><td class="num">{{index . "signals"}}</td><td class="num">{{or (index . "filtered") 0}}</td><td class="muted">{{index . "detail"}}</td></tr>{{end}}
</table></div>
{{with .Warnings}}<h2>Avisos</h2><ul>{{range .}}<li>{{.}}</li>{{end}}</ul>{{end}}
<p class="muted">Gerado em {{.Generated.Format "2006-01-02 15:04:05"}} pelo Sentinela.</p>
</main></body></html>
`
