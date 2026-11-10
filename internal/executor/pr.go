package executor

import (
	"fmt"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

var lensTitle = map[string]string{"security": "Segurança", "code": "Código", "reliability": "Confiabilidade", "domain": "Domínio"}

// PRBody monta o corpo do PR: "Fixes <ID>" (o Linear liga o PR à issue) e os artefatos do job
// resumidos. Os arquivos completos ficam no diretório do job.
func PRBody(e db.Execution, spec JobSpec, res Result, run RunResult) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	if e.LinearIssueID != "" {
		w("Fixes %s\n\n", spec.Issue)
	} else {
		// Tracker local (ADR 0038): não há issue para o GitHub ligar; o achado fica no Postgres.
		w("Achado %s do Sentinela (`sentinela finding show %s`)\n\n", spec.Issue, strings.TrimPrefix(spec.Issue, "F-"))
	}
	w("**Sentinela** · execução #%d (%s) · approver: **%s** · rodadas %d · %d turnos · custo equivalente $%.2f\n\n",
		e.ID, e.Kind, orDash(res.Approver, res.Decision), res.State.Rounds, run.NumTurns, run.CostUSD)
	w("> PR em draft aberto pelo Sentinela. A revisora só comenta a decisão do approver e do Policy Gate; aprovação e merge são de um humano.\n\n")
	if spec.ReworkNote != "" {
		w("**Nota de rework:** %s\n\n", spec.ReworkNote)
	}
	if len(spec.Instructions) > 0 {
		w("### Instruções da equipe\n\nComentários da issue que entraram na spec com prioridade sobre a correção proposta pelo achado (%s):\n\n", spec.InstructionWindow)
		for _, in := range spec.Instructions {
			w("- **%s**, %s:\n", in.Author, in.CreatedAt.UTC().Format("2006-01-02 15:04 UTC"))
			for _, l := range strings.Split(in.Body, "\n") {
				w("  > %s\n", l)
			}
		}
		w("\n")
	}
	section := func(title, file string) {
		if a, ok := res.Artifacts[file]; ok {
			w("<details><summary>%s · <code>%s</code></summary>\n\n%s\n\n</details>\n\n", title, a.Verdict, a.Summary)
		}
	}
	section("Spec", "spec.md")
	last := res.State.Rounds
	section(fmt.Sprintf("Build (rodada %d)", last), roundFile(last, "build.md"))
	if len(res.Reviews) > 0 {
		w("### Reviews\n\n| Rodada | Segurança | Código | Confiabilidade | Domínio |\n|---|---|---|---|---|\n")
		for n := 1; n <= last; n++ {
			w("| %d |", n)
			for _, lens := range Lenses {
				v := "-"
				for _, r := range res.Reviews {
					if r.Round == n && r.Lens == lens {
						v = r.Verdict
						if len(r.Items) > 0 && r.Verdict == "CHANGES" {
							v += fmt.Sprintf(" (%d)", len(r.Items))
						}
					}
				}
				w(" %s |", v)
			}
			w("\n")
		}
		w("\n")
		for _, lens := range Lenses {
			section("Review "+lensTitle[lens]+fmt.Sprintf(" (rodada %d)", last), roundFile(last, "review-"+lens+".md"))
		}
	}
	section("Decisão do approver", "decision.md")
	return b.String()
}

func orDash(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return "-"
}
