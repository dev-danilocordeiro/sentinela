package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/local"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
)

// localTracker monta o tracker local com a flag --config que os comandos do relatório repetem.
func (d *deps) localTracker(cfg *config.Config, st *store.Store) *local.Tracker {
	t := &local.Tracker{Cfg: cfg, Store: st}
	if p := *d.cfgPath; p != "" && p != "sentinela.yaml" {
		t.ConfigFlag = "--config " + p
	}
	return t
}

func newFindingCmd(d *deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "finding",
		Short: "Achados e decisões sem tracker: listar, ver, aceitar, recusar e pedir a correção (adapters.tracker local)",
		Long: "No modo local (adapters.tracker local, ADR 0038) as decisões humanas entram por aqui e gravam o\n" +
			"mesmo que o sync do Linear: outcome com decided_by human, supressão na recusa e a execução na\n" +
			"fila no fix. list e show funcionam em qualquer modo; accept, reject, fix, resolve, duplicate e answer, só no local.",
	}
	run := func(fn func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error) func(*cobra.Command, []string) error {
		return func(c *cobra.Command, args []string) error {
			cfg, err := d.config()
			if err != nil {
				return err
			}
			st, err := store.Open(c.Context(), d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			return fn(c.Context(), cfg, st, c.OutOrStdout(), args)
		}
	}
	parseID := func(s string) (int64, error) {
		id, err := strconv.ParseInt(strings.TrimPrefix(strings.ToUpper(s), "F-"), 10, 64)
		if err != nil {
			return 0, fmt.Errorf("id inválido %q (use o número do achado, ex.: 31 ou F-31)", s)
		}
		return id, nil
	}
	var dec local.Decision
	decisionFlags := func(c *cobra.Command) {
		// O padrão é resolvido na hora (não no help), para a ajuda e o guia não dependerem da máquina.
		c.Flags().StringVar(&dec.Actor, "by", "", "quem decidiu (padrão: usuário do sistema)")
		c.PreRun = func(*cobra.Command, []string) {
			if dec.Actor == "" {
				dec.Actor = os.Getenv("USER")
			}
		}
		c.Flags().StringVar(&dec.ExecutedBy, "executed-by", local.ByHuman, "quem executa a decisão: human, ou agent a pedido explícito de quem decidiu (exige --note)")
		c.Flags().StringVar(&dec.Note, "note", "", "nota gravada com a decisão")
	}
	after := func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, repoName string) {
		if !cfg.LocalTracker() {
			return
		}
		if md, err := d.localTracker(cfg, st).Write(ctx, repoName); err != nil {
			fmt.Fprintf(w, "aviso: relatório local: %v\n", err)
		} else {
			fmt.Fprintf(w, "relatório: %s\n", md)
		}
	}

	var repoFlag string
	var all bool
	list := &cobra.Command{
		Use: "list", Short: "Lista os achados (abertos por padrão; --all inclui os decididos)", Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, _ []string) error {
			rows, err := st.Q.LocalFindings(ctx, repoFlag)
			if err != nil {
				return err
			}
			tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tESTADO\tSEV\tCATEGORIA\tREPO\tJOB\tTÍTULO")
			n := 0
			for _, r := range rows {
				f := r.Finding
				if !all && f.State != "open" && f.State != "executing" {
					continue
				}
				n++
				state := f.State
				if r.Accepted && state == "open" {
					state = "open (aceito)"
				}
				job := "—"
				if r.ExecID != 0 {
					job = fmt.Sprintf("%d %s", r.ExecID, r.ExecStatus)
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", f.ID, state, f.Severity, f.Category, f.Repo, job, truncateText(f.Title, 70))
			}
			_ = tw.Flush()
			if n == 0 {
				fmt.Fprintln(w, "nenhum achado")
			}
			return nil
		}),
	}
	list.Flags().StringVar(&repoFlag, "repo", "", "só um repo")
	list.Flags().BoolVar(&all, "all", false, "inclui rejeitados, corrigidos e os outros decididos")

	show := &cobra.Command{
		Use: "show <id>", Short: "Mostra um achado, as execuções e as decisões", Args: cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			row, err := st.Q.GetFinding(ctx, id)
			if err != nil {
				return fmt.Errorf("achado %d: %w", id, err)
			}
			f, err := linear.FindingFromDB(row)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "%s · %s\n%s · %s · %s · estado %s · time %s · módulo %s\n", config.LocalIssueID(id), f.Title, f.Repo, f.Category, f.Severity, row.State, orDash(f.Routing.Team), orDash(f.Module))
			if row.ResolutionReason.Valid {
				fmt.Fprintf(w, "resolução: %s\n", row.ResolutionReason.String)
			}
			fmt.Fprintf(w, "\n%s\n", linear.LocalDescription(f))
			execs, err := st.Q.FindingExecutions(ctx, id)
			if err != nil {
				return err
			}
			for _, e := range execs {
				fmt.Fprintf(w, "job %d (%s): %s %s %s\n", e.ID, e.Kind, e.Status, orDash(e.Decision.String), e.PrUrl.String)
			}
			outs, err := st.Q.FindingOutcomes(ctx, id)
			if err != nil {
				return err
			}
			for _, o := range outs {
				fmt.Fprintf(w, "decisão %s em %s: decided_by %s, executed_by %s, por %s · %s", o.Kind, o.OccurredAt.Time.Local().Format("2006-01-02 15:04"), o.DecidedBy, o.ExecutedBy, orDash(o.Actor.String), o.Reason.String)
				if o.Note.Valid {
					fmt.Fprintf(w, " (nota: %s)", o.Note.String)
				}
				fmt.Fprintln(w)
			}
			return nil
		}),
	}

	accept := &cobra.Command{
		Use: "accept <id>", Short: "Aceita o achado como problema real (fica aberto, esperando a correção)", Args: cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			if err := d.localTracker(cfg, st).Accept(ctx, id, dec); err != nil {
				return err
			}
			fmt.Fprintf(w, "achado %d aceito (decided_by human, executed_by %s)\n", id, orDash(dec.ExecutedBy))
			if f, err := st.Q.GetFinding(ctx, id); err == nil {
				after(ctx, cfg, st, w, f.Repo)
			}
			return nil
		}),
	}
	decisionFlags(accept)

	reject := &cobra.Command{
		Use: `reject <id> "<motivo>"`, Short: "Recusa o achado: o motivo vira supressão e exemplo para o Judge", Args: cobra.ExactArgs(2),
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			sup, err := d.localTracker(cfg, st).Reject(ctx, id, args[1], dec)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "achado %d rejeitado (decided_by human, executed_by %s); supressão %d\n", id, orDash(dec.ExecutedBy), sup)
			if f, err := st.Q.GetFinding(ctx, id); err == nil {
				after(ctx, cfg, st, w, f.Repo)
			}
			return nil
		}),
	}
	decisionFlags(reject)

	fix := &cobra.Command{
		Use: "fix <id>", Short: "Pede a correção (equivale a aplicar a label de gatilho): enfileira a execução", Args: cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			e, err := d.localTracker(cfg, st).Fix(ctx, id, dec)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "job %d enfileirado para o achado %d (branch sentinela/%s); o worker pega no próximo ciclo (`sentinela worker --once` roda agora)\n", e.ID, id, e.IssueIdentifier)
			after(ctx, cfg, st, w, e.Repo)
			return nil
		}),
	}
	decisionFlags(fix)

	resolve := &cobra.Command{
		Use: "resolve <id> --note \"<como foi corrigido>\"", Short: "Registra a correção feita à mão: fecha o achado como fixed (como concluir a issue no Linear)", Args: cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			if err := d.localTracker(cfg, st).Resolve(ctx, id, dec); err != nil {
				return err
			}
			fmt.Fprintf(w, "achado %d resolvido à mão: fixed (decided_by human, executed_by %s)\n", id, orDash(dec.ExecutedBy))
			if f, err := st.Q.GetFinding(ctx, id); err == nil {
				after(ctx, cfg, st, w, f.Repo)
			}
			return nil
		}),
	}
	decisionFlags(resolve)

	var of string
	duplicate := &cobra.Command{
		Use: "duplicate <id> --of <id>", Short: "Marca como duplicata (como Duplicate no Linear): sem supressão, o mais antigo absorve o mais novo", Args: cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			orig, err := parseID(of)
			if err != nil {
				return fmt.Errorf("--of: %w", err)
			}
			dup, kept, err := d.localTracker(cfg, st).Duplicate(ctx, id, orig, dec)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "%s é duplicata de %s (o mais antigo absorve o mais novo; sem supressão; decided_by human, executed_by %s)\n", config.LocalIssueID(dup), config.LocalIssueID(kept), orDash(dec.ExecutedBy))
			if f, err := st.Q.GetFinding(ctx, kept); err == nil {
				after(ctx, cfg, st, w, f.Repo)
			}
			return nil
		}),
	}
	duplicate.Flags().StringVar(&of, "of", "", "o outro achado (F-<id> ou o número)")
	_ = duplicate.MarkFlagRequired("of")
	decisionFlags(duplicate)

	answer := &cobra.Command{
		Use: "answer <id> intentional|not-intentional", Short: "Responde à pergunta de produto (como as labels sentinela-intencional e sentinela-nao-intencional)", Args: cobra.ExactArgs(2),
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			if err := d.localTracker(cfg, st).Answer(ctx, id, args[1], dec); err != nil {
				return err
			}
			what := "é intencional: o achado foi recusado e o motivo virou supressão"
			if args[1] == local.AnswerNotIntentional {
				what = "não é intencional: a pergunta virou achado, aberto esperando a correção"
			}
			fmt.Fprintf(w, "achado %d: %s (decided_by human, executed_by %s)\n", id, what, orDash(dec.ExecutedBy))
			if f, err := st.Q.GetFinding(ctx, id); err == nil {
				after(ctx, cfg, st, w, f.Repo)
			}
			return nil
		}),
	}
	decisionFlags(answer)

	report := &cobra.Command{
		Use: "report", Short: "Regenera os relatórios do tracker local (markdown e HTML) de todos os repos", Args: cobra.NoArgs,
		RunE: run(func(ctx context.Context, cfg *config.Config, st *store.Store, w io.Writer, _ []string) error {
			t := d.localTracker(cfg, st)
			for _, rc := range cfg.Repos {
				md, err := t.Write(ctx, rc.Name)
				if err != nil {
					return err
				}
				fmt.Fprintf(w, "%s\n", md)
			}
			return nil
		}),
	}
	var unlinkIssue, unlinkReason, unlinkBy string
	unlink := &cobra.Command{
		Use:   "unlink <id> --issue <identificador> --reason \"<motivo>\"",
		Short: "Desliga um achado aberto da issue a que ficou ligado por engano: volta a pendente de publicação",
		Long: "Apaga a ligação no banco e grava o registro em finding_unlinks (issue, motivo e quem pediu). Não mexe\n" +
			"no Linear, nos outcomes nem nas supressões. A próxima publicação cria a issue de novo, com o marcador\n" +
			"desta instalação. Vale em qualquer modo.",
		Args: cobra.ExactArgs(1),
		RunE: run(func(ctx context.Context, _ *config.Config, st *store.Store, w io.Writer, args []string) error {
			id, err := parseID(args[0])
			if err != nil {
				return err
			}
			if unlinkBy == "" {
				unlinkBy = os.Getenv("USER")
			}
			rows, err := linear.Unlink(ctx, st, id, unlinkIssue, unlinkReason, unlinkBy)
			if err != nil {
				return err
			}
			for _, li := range rows {
				fmt.Fprintf(w, "achado %d desligado de %s (%s); pendente de publicação\n", id, li.Identifier, li.Role)
			}
			return nil
		}),
	}
	unlink.Flags().StringVar(&unlinkIssue, "issue", "", "identificador da issue (ex.: EX-20); vazio desliga todas as do achado")
	unlink.Flags().StringVar(&unlinkReason, "reason", "", "motivo (obrigatório)")
	unlink.Flags().StringVar(&unlinkBy, "by", "", "quem pediu (padrão: usuário do sistema)")
	_ = unlink.MarkFlagRequired("reason")

	cmd.AddCommand(list, show, accept, reject, fix, resolve, duplicate, answer, report, unlink)
	return cmd
}
