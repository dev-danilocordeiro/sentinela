package linear

import (
	"fmt"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
)

// judgeInfo é a decisão do Judge que acompanha o achado na issue.
type judgeInfo struct {
	Confidence float64
	Valid      bool
	Reason     string
}

// issueBody monta a descrição (markdown) da issue principal. A última linha carrega o
// fingerprint, que o polling e a publicação idempotente procuram.
func issueBody(f signals.Finding, marker string, j judgeInfo, related []relatedTarget, overlaps []overlapIssue, also ...repo.Secondary) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("**Categoria:** %s · **Severidade:** %s · **Confiança:** %.2f", f.Category, f.Severity, f.Confidence)
	if j.Valid {
		w(" (juiz %.2f)", j.Confidence)
	}
	w("\n**Repo:** `%s` · **Módulo:** `%s`", f.Repo, orDash(f.Module))
	if f.Routing.Team != "" {
		w(" · **Time:** %s", f.Routing.Team)
	}
	if f.Routing.Rule != "" {
		w(" (regra: `%s`)", f.Routing.Rule)
	}
	w("\n\n## Contexto\n\n%s\n", strings.TrimSpace(f.Context))
	if len(f.Evidence) > 0 {
		w("\n## Evidência\n\n")
		for _, e := range f.Evidence {
			ref := fmt.Sprintf("%s:%d", e.File, e.Line)
			if e.Permalink != "" {
				ref = fmt.Sprintf("[%s](%s)", ref, e.Permalink)
			}
			w("- %s", ref)
			if s := strings.TrimSpace(e.Snippet); s != "" {
				w("\n  ```\n  %s\n  ```", strings.ReplaceAll(s, "\n", "\n  "))
			}
			w("\n")
		}
	}
	if len(f.Signals) > 0 {
		w("\n## Sinais\n\n")
		for _, s := range f.Signals {
			w("- %s `%s`: %s\n", s.Kind, s.Ref, s.Summary)
		}
	}
	if strings.TrimSpace(f.ProposedFix) != "" {
		w("\n## Correção proposta\n\n%s\n", strings.TrimSpace(f.ProposedFix))
	}
	if len(f.AcceptanceCriteria) > 0 {
		w("\n## Critérios de aceite\n\n")
		for _, c := range f.AcceptanceCriteria {
			w("- [ ] %s\n", c)
		}
	}
	if len(f.ScopePaths) > 0 {
		w("\n**Escopo:** `%s`\n", strings.Join(f.ScopePaths, "`, `"))
	}
	if len(f.RelatedModules) > 0 {
		w("\n**Módulos relacionados:** `%s`", strings.Join(f.RelatedModules, "`, `"))
		if len(related) > 0 {
			var teams []string
			for _, r := range related {
				teams = append(teams, r.Team)
			}
			w(" (issues relacionadas nos boards de %s)", strings.Join(teams, ", "))
		}
		w("\n")
	}
	if len(also) > 0 {
		w("\n## Também afeta\n\nO dono é o time %s. As evidências também tocam código de outros times, avisados no Slack; nenhuma issue foi criada para eles.\n\n", orDash(f.Routing.Team))
		for _, s := range also {
			w("- **%s**, módulo `%s`: `%s`\n", s.Team, strings.Join(s.Modules, "`, `"), strings.Join(s.Paths, "`, `"))
		}
	}
	if len(overlaps) > 0 {
		w("\n## Fora do escopo\n\nParte do problema já está coberta por outra issue; esta trata só do que sobra.\n\n")
		for _, o := range overlaps {
			if o.Issue != nil {
				w("- [%s](%s): %s\n", o.Issue.Identifier, o.Issue.Url, o.Title)
			} else {
				w("- %s (achado `%s`, ainda sem issue)\n", o.Title, o.Fingerprint)
			}
		}
	}
	if j.Reason != "" {
		w("\n**Juiz:** %s\n", strings.TrimSpace(j.Reason))
	}
	w("\n---\n%s\n\n`%s`\n", linearFooter, marker)
	return b.String()
}

// relatedBody é a descrição da issue relacionada, no board de outro time.
func relatedBody(f signals.Finding, marker string, primary Issue, r relatedTarget) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Um achado do Sentinela no time %s afeta o módulo `%s`, do time %s.\n\n", orDash(f.Routing.Team), r.Module, orDash(r.Team))
	fmt.Fprintf(&b, "Issue principal: [%s](%s), %s.\n\n", primary.Identifier, primary.URL, f.Title)
	fmt.Fprintf(&b, "## Contexto\n\n%s\n", strings.TrimSpace(f.Context))
	if strings.TrimSpace(f.ProposedFix) != "" {
		fmt.Fprintf(&b, "\n## Correção proposta\n\n%s\n", strings.TrimSpace(f.ProposedFix))
	}
	fmt.Fprintf(&b, "\n---\nA decisão sobre o achado fica na issue principal.\n\n`%s`\n", marker)
	return b.String()
}

// noCommandsDocNote é a linha da issue de um repo sem AGENTS.md nem CLAUDE.md no escopo do achado
// nem na raiz: a linha de execução tira deles os comandos de setup, build, teste e lint, e falha
// sem eles.
func noCommandsDocNote(repoName, trigger string) string {
	how := "A correção pelo Sentinela"
	if trigger != "" {
		how += " (label `" + trigger + "`)"
	}
	return "> **Atenção:** o repo `" + repoName + "` não tem AGENTS.md nem CLAUDE.md na raiz nem no workspace do achado. " + how +
		" só roda depois que esse arquivo existir, com os comandos de setup, build, teste e lint.\n"
}

// withNote põe a nota antes do rodapé (o último "---") do corpo da issue.
func withNote(body, note string) string {
	i := strings.LastIndex(body, "\n---\n")
	if i < 0 {
		return body + "\n" + note
	}
	return body[:i] + "\n" + note + body[i:]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// QuestionTitle tira da premissa (escrita pelo Judge como pergunta) o título da issue: a
// primeira frase que termina em "?", com no máximo 120 caracteres.
func QuestionTitle(premise string) string {
	t := strings.TrimSpace(premise)
	if i := strings.Index(t, "?"); i >= 0 {
		t = t[:i+1]
	} else {
		t = strings.TrimRight(t, ".! ") + "?"
	}
	if r := []rune(t); len(r) > 120 {
		t = string(r[:119]) + "…?"
	}
	return t
}

// questionBody é a issue de um achado com premissa de produto: a pergunta ao time, o achado
// que depende dela (com a evidência) e como responder. O fingerprint fica no fim, como nas outras.
func questionBody(f signals.Finding, marker string, j judgeInfo, premise string, labels config.Labels) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	w("## Pergunta ao time\n\n%s\n\n", strings.TrimSpace(premise))
	w("O Sentinela encontrou um possível problema que só existe se a resposta for \"não é intencional\". Ele não abre a correção até o time responder.\n\n")
	w("## O achado\n\n**%s** · %s/%s · `%s` · módulo `%s`\n\n%s\n", f.Title, f.Category, f.Severity, f.Repo, orDash(f.Module), strings.TrimSpace(f.Context))
	if len(f.Evidence) > 0 {
		w("\n## Evidência\n\n")
		for _, e := range f.Evidence {
			ref := fmt.Sprintf("%s:%d", e.File, e.Line)
			if e.Permalink != "" {
				ref = fmt.Sprintf("[%s](%s)", ref, e.Permalink)
			}
			w("- %s", ref)
			if s := strings.TrimSpace(e.Snippet); s != "" {
				w("\n  ```\n  %s\n  ```", strings.ReplaceAll(s, "\n", "\n  "))
			}
			w("\n")
		}
	}
	if strings.TrimSpace(f.ProposedFix) != "" {
		w("\n## Se não for intencional, a correção seria\n\n%s\n", strings.TrimSpace(f.ProposedFix))
	}
	w("\n## Como responder\n\n")
	w("- **Não é intencional:** aplique a label `%s`. A issue vira o achado normal (título, critérios de aceite) e segue o fluxo.\n", labels.NotIntentional)
	w("- **É intencional:** aplique a label `%s` e comente o motivo, ou cancele a issue com o motivo no comentário. O motivo vira supressão e a pergunta não volta.\n", labels.Intentional)
	if j.Reason != "" {
		w("\n**Juiz:** %s\n", strings.TrimSpace(j.Reason))
	}
	w("\n---\nPergunta do Sentinela (descoberta).\n\n`%s`\n", marker)
	return b.String()
}

const (
	linearFooter = "Achado do Sentinela (descoberta). Para recusar, cancele a issue e comente o motivo: ele vira supressão e o achado não volta."
	localFooter  = "Achado do Sentinela (descoberta, tracker local). As decisões são pela CLI: `sentinela finding accept | reject | fix`."
)

// LocalDescription é o corpo da issue montado do próprio achado, para o tracker local (ADR 0038):
// o mesmo texto que iria ao Linear, sem relacionadas nem veredito, e com o rodapé do modo local.
func LocalDescription(f signals.Finding) string {
	return strings.Replace(issueBody(f, primaryMarker(f.Fingerprint), judgeInfo{}, nil, nil), linearFooter, localFooter, 1)
}
