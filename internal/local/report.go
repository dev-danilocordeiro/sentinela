package local

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Item é um achado no relatório local.
type Item struct {
	ID         int64
	Issue      string // F-<id>
	Finding    signals.Finding
	State      string
	Resolution string
	Judge      string
	Premise    string
	Answered   bool // a pergunta de produto já foi respondida
	Accepted   bool
	ExecID     int64
	ExecStatus string
	ExecResult string
	PRURL      string
	PRState    string
	Commands   []string // as ações possíveis no estado atual
}

// Section agrupa os achados por situação.
type Section struct {
	Title string
	Items []Item
}

// Page é o relatório de um repo.
type Page struct {
	Repo      string
	Generated time.Time
	Sections  []Section
	Open      int
	Total     int
	Help      []string
}

func (t *Tracker) cmd(format string, args ...any) string {
	c := "sentinela "
	if t.ConfigFlag != "" {
		c += t.ConfigFlag + " "
	}
	return c + fmt.Sprintf(format, args...)
}

// Build monta o relatório do repo a partir do Postgres.
func (t *Tracker) Build(ctx context.Context, repoName string) (Page, error) {
	rows, err := t.Store.Q.LocalFindings(ctx, repoName)
	if err != nil {
		return Page{}, err
	}
	p := Page{Repo: repoName, Generated: time.Now(), Help: []string{
		t.cmd("finding show <id>") + "   detalhes, execuções e decisões",
		t.cmd("finding accept <id>") + "   é um problema real (fica aberto)",
		t.cmd(`finding reject <id> "<motivo>"`) + "   recusa; o motivo vira supressão",
		t.cmd("finding fix <id>") + "   pede a correção (PR em draft); o worker pega no próximo ciclo",
		t.cmd(`finding resolve <id> --note "<como>"`) + "   a correção foi feita à mão: fecha o achado",
		t.cmd("finding duplicate <id> --of F-<outro>") + "   duplicata: o mais antigo absorve o mais novo, sem supressão",
		t.cmd("finding answer <id> intentional | not-intentional") + "   responde à pergunta de produto",
	}}
	open, running, decided := Section{Title: "Abertos"}, Section{Title: "Em correção"}, Section{Title: "Decididos"}
	for _, r := range rows {
		it, err := t.item(r)
		if err != nil {
			return p, err
		}
		p.Total++
		switch {
		case it.State == "open" && (it.ExecStatus == "queued" || it.ExecStatus == "running"):
			running.Items = append(running.Items, it)
		case it.State == "open":
			p.Open++
			open.Items = append(open.Items, it)
		case it.State == "executing":
			running.Items = append(running.Items, it)
		default:
			decided.Items = append(decided.Items, it)
		}
	}
	p.Sections = []Section{open, running, decided}
	return p, nil
}

func (t *Tracker) item(r db.LocalFindingsRow) (Item, error) {
	f, err := linear.FindingFromDB(r.Finding)
	if err != nil {
		return Item{}, err
	}
	id := r.Finding.ID
	it := Item{
		ID: id, Issue: config.LocalIssueID(id), Finding: f, State: r.Finding.State, Resolution: r.Finding.ResolutionReason.String,
		Premise: r.Finding.Premise.String, Answered: r.Finding.PremiseAnswer.Valid, Accepted: r.Accepted, ExecID: r.ExecID, ExecStatus: r.ExecStatus,
		ExecResult: r.ExecDecision, PRURL: r.PrUrl, PRState: r.PrState,
	}
	if r.Finding.JudgeConfidence.Valid {
		it.Judge = strconv.FormatFloat(float64(r.Finding.JudgeConfidence.Float32), 'f', 2, 32)
	}
	active := r.ExecStatus == "queued" || r.ExecStatus == "running"
	if it.State == "open" || it.State == "executing" {
		it.Commands = append(it.Commands, t.cmd("finding show %d", id))
		if !it.Accepted {
			it.Commands = append(it.Commands, t.cmd("finding accept %d", id))
		}
		it.Commands = append(it.Commands, t.cmd(`finding reject %d "<motivo>"`, id))
		if !active && (it.PRURL == "" || it.PRState == "closed") {
			it.Commands = append(it.Commands, t.cmd("finding fix %d", id))
		}
		if !active {
			it.Commands = append(it.Commands, t.cmd(`finding resolve %d --note "<como>"`, id))
			it.Commands = append(it.Commands, t.cmd("finding duplicate %d --of F-<outro>", id))
		}
		if it.Premise != "" && !it.Answered {
			it.Commands = append(it.Commands, t.cmd(`finding answer %d intentional --note "<por quê>"`, id), t.cmd("finding answer %d not-intentional", id))
		}
		if !active && it.PRURL != "" && it.PRState == "open" {
			// PR em draft esperando gente: aprovar o ESCALATE ou devolver ao builder. O merge é no GitHub.
			if it.ExecResult == "escalate" {
				it.Commands = append(it.Commands, t.cmd("job approve %d", it.ExecID))
			}
			it.Commands = append(it.Commands, t.cmd(`job rework %d "<nota>"`, it.ExecID))
		}
	} else {
		it.Commands = append(it.Commands, t.cmd("finding show %d", id))
	}
	return it, nil
}

// slug é o nome do arquivo do repo: owner__nome.
func slug(repoName string) string { return strings.ReplaceAll(repoName, "/", "__") }

// Write regenera o relatório do repo (markdown e HTML) e o índice; devolve o caminho do markdown.
func (t *Tracker) Write(ctx context.Context, repoName string) (string, error) {
	p, err := t.Build(ctx, repoName)
	if err != nil {
		return "", err
	}
	dir := t.Cfg.Adapters.LocalDir
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	md := filepath.Join(dir, slug(repoName)+".md")
	if err := os.WriteFile(md, []byte(Markdown(p)), 0o600); err != nil {
		return "", err
	}
	var h bytes.Buffer
	if err := pageHTML.Execute(&h, p); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, slug(repoName)+".html"), h.Bytes(), 0o600); err != nil {
		return "", err
	}
	return md, t.writeIndex(ctx)
}

// writeIndex lista os repos do sentinela.yaml com os relatórios já gerados.
func (t *Tracker) writeIndex(ctx context.Context) error {
	var b strings.Builder
	b.WriteString("# Sentinela · relatórios locais\n\n| Repo | Abertos | Total | Relatório |\n|---|---|---|---|\n")
	for _, rc := range t.Cfg.Repos {
		p, err := t.Build(ctx, rc.Name)
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "| %s | %d | %d | [md](%s.md) · [html](%s.html) |\n", rc.Name, p.Open, p.Total, slug(rc.Name), slug(rc.Name))
	}
	return os.WriteFile(filepath.Join(t.Cfg.Adapters.LocalDir, "index.md"), []byte(b.String()), 0o600)
}

// Publish é a publicação do tracker local ao fim da rodada (discovery.Publisher): regenera o
// relatório e lista como "issues" os achados abertos desta rodada.
func (t *Tracker) Publish(ctx context.Context, repoName string, runID int64, _ linear.Resolve) (linear.PublishResult, error) {
	var res linear.PublishResult
	md, err := t.Write(ctx, repoName)
	if err != nil {
		return res, err
	}
	rows, err := t.Store.Q.LocalFindings(ctx, repoName)
	if err != nil {
		return res, err
	}
	for _, r := range rows {
		f := r.Finding
		if f.State != "open" || !f.RunID.Valid || f.RunID.Int64 != runID {
			continue
		}
		res.Issues = append(res.Issues, linear.PublishedIssue{
			FindingID: f.ID, Fingerprint: f.Fingerprint, Title: f.Title, Category: f.Category, Severity: f.Severity,
			Identifier: config.LocalIssueID(f.ID), URL: md + "#" + strings.ToLower(config.LocalIssueID(f.ID)),
		})
	}
	return res, nil
}

// Markdown renderiza o relatório.
func Markdown(p Page) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("# Sentinela · %s\n\n", p.Repo)
	w("Gerado em %s pelo tracker local. %d achado(s), %d aberto(s) esperando decisão.\n\n", p.Generated.Format("2006-01-02 15:04"), p.Total, p.Open)
	w("**Como decidir** (cada decisão grava `decided_by human` no Postgres, como no Linear):\n\n```\n%s\n```\n\n", strings.Join(p.Help, "\n"))
	for _, s := range p.Sections {
		w("## %s (%d)\n\n", s.Title, len(s.Items))
		if len(s.Items) == 0 {
			w("Nenhum.\n\n")
		}
		for _, it := range s.Items {
			f := it.Finding
			w("### %s · %s\n\n", it.Issue, f.Title)
			w("- **Achado:** #%d · %s · %s · confiança %.2f", it.ID, f.Category, f.Severity, f.Confidence)
			if it.Judge != "" {
				w(" · Judge %s", it.Judge)
			}
			w("\n- **Time:** %s (%s) · **módulo:** `%s`\n", orDash(f.Routing.Team), orDash(f.Routing.Rule), orDash(f.Module))
			w("- **Situação:** %s\n", status(it))
			if it.Premise != "" {
				w("- **Depende de premissa de produto:** %s\n", it.Premise)
			}
			if len(f.Evidence) > 0 {
				var ev []string
				for _, e := range f.Evidence {
					ev = append(ev, fmt.Sprintf("`%s:%d`", e.File, e.Line))
				}
				w("- **Evidência:** %s\n", strings.Join(ev, ", "))
			}
			w("\n%s\n\n", strings.TrimSpace(f.Context))
			if f.ProposedFix != "" {
				w("**Correção proposta:** %s\n\n", strings.TrimSpace(f.ProposedFix))
			}
			if len(f.AcceptanceCriteria) > 0 {
				w("**Critérios de aceite:**\n\n")
				for _, c := range f.AcceptanceCriteria {
					w("- %s\n", c)
				}
				w("\n")
			}
			w("```\n%s\n```\n\n", strings.Join(it.Commands, "\n"))
		}
	}
	return b.String()
}

func status(it Item) string {
	var parts []string
	switch it.State {
	case "open":
		parts = append(parts, "aberto")
		if it.Accepted {
			parts = append(parts, "aceito")
		}
	case "rejected":
		parts = append(parts, "rejeitado: "+it.Resolution)
	default:
		parts = append(parts, it.State)
		if it.Resolution != "" {
			parts = append(parts, it.Resolution)
		}
	}
	if it.ExecID != 0 {
		e := fmt.Sprintf("job %d %s", it.ExecID, it.ExecStatus)
		if it.ExecResult != "" {
			e += " (" + it.ExecResult + ")"
		}
		parts = append(parts, e)
	}
	if it.PRURL != "" {
		parts = append(parts, fmt.Sprintf("PR %s %s", it.PRURL, orDash(it.PRState)))
	}
	return strings.Join(parts, " · ")
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

var pageHTML = template.Must(template.New("page").Funcs(template.FuncMap{"status": status, "dash": orDash, "lower": strings.ToLower}).Parse(`<!doctype html>
<html lang="pt-BR"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Sentinela · {{.Repo}}</title>
<style>
:root{--bg:#fff;--fg:#1c1c1e;--muted:#6b6b70;--line:#e3e3e6;--card:#f7f7f8;--accent:#2b59c3}
@media (prefers-color-scheme:dark){:root{--bg:#141416;--fg:#ececee;--muted:#9a9aa0;--line:#2c2c30;--card:#1d1d20;--accent:#7aa2ff}}
body{background:var(--bg);color:var(--fg);font:15px/1.5 system-ui,sans-serif;margin:0 auto;max-width:960px;padding:16px}
h1{font-size:1.5rem}h2{margin-top:2rem;border-bottom:1px solid var(--line);padding-bottom:.3rem}
.card{background:var(--card);border:1px solid var(--line);border-radius:8px;padding:12px 16px;margin:12px 0}
.meta{color:var(--muted);font-size:.9rem}code,pre{font-family:ui-monospace,monospace;font-size:.85rem}
pre{background:var(--bg);border:1px solid var(--line);border-radius:6px;padding:8px;overflow-x:auto}
a{color:var(--accent)}
</style></head><body>
<h1>Sentinela · {{.Repo}}</h1>
<p class="meta">Gerado em {{.Generated.Format "2006-01-02 15:04"}} pelo tracker local · {{.Total}} achado(s), {{.Open}} aberto(s) esperando decisão.</p>
<p>Como decidir (cada decisão grava <code>decided_by human</code> no Postgres, como no Linear):</p>
<pre>{{range .Help}}{{.}}
{{end}}</pre>
{{range .Sections}}<h2>{{.Title}} ({{len .Items}})</h2>
{{if not .Items}}<p class="meta">Nenhum.</p>{{end}}
{{range .Items}}<div class="card" id="{{lower .Issue}}">
<h3>{{.Issue}} · {{.Finding.Title}}</h3>
<p class="meta">#{{.ID}} · {{.Finding.Category}} · {{.Finding.Severity}} · confiança {{printf "%.2f" .Finding.Confidence}}{{if .Judge}} · Judge {{.Judge}}{{end}} · time {{dash .Finding.Routing.Team}} · módulo <code>{{dash .Finding.Module}}</code></p>
<p><strong>Situação:</strong> {{status .}}</p>
{{if .Premise}}<p><strong>Depende de premissa de produto:</strong> {{.Premise}}</p>{{end}}
{{if .Finding.Evidence}}<p><strong>Evidência:</strong> {{range $i, $e := .Finding.Evidence}}{{if $i}}, {{end}}<code>{{$e.File}}:{{$e.Line}}</code>{{end}}</p>{{end}}
<p style="white-space:pre-wrap">{{.Finding.Context}}</p>
{{if .Finding.ProposedFix}}<p><strong>Correção proposta:</strong> {{.Finding.ProposedFix}}</p>{{end}}
{{if .Finding.AcceptanceCriteria}}<p><strong>Critérios de aceite:</strong></p><ul>{{range .Finding.AcceptanceCriteria}}<li>{{.}}</li>{{end}}</ul>{{end}}
<pre>{{range .Commands}}{{.}}
{{end}}</pre>
</div>{{end}}{{end}}
</body></html>
`))
