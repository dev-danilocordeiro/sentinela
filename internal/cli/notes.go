package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/trace"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

func newNotesCmd(d *deps) *cobra.Command {
	var id int64
	var publish bool
	cmd := &cobra.Command{
		Use:   "notes",
		Short: "Transforma as observações fora do escopo dos reviewers de uma execução em achados candidatos",
		Long: "Lê os itens \"(não bloqueia)\" e \"(fora do escopo)\" dos reviews de uma execução, pede ao\n" +
			"Review Note Interpreter que os transforme em achados (source review_note) e passa cada um pela\n" +
			"validação de evidência, roteamento, deduplicação e Judge, como qualquer achado. Por padrão é\n" +
			"modo sombra: mostra o que o Judge decidiria e não grava achado nem publica. Com --publish, os\n" +
			"aprovados vão para findings, ligados à execução de origem, e saem na próxima publicação.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			cfg, err := d.config()
			if err != nil {
				return err
			}
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			tp, shutdown, err := telemetry.Setup(ctx, cfg.Telemetry)
			if err != nil {
				return err
			}
			defer func() { _ = shutdown(context.WithoutCancel(ctx)) }()
			res, err := runNotes(ctx, d, cfg, st, tp.Tracer(telemetry.TracerName), id, !publish)
			printNotes(c.OutOrStdout(), res, !publish)
			return err
		},
	}
	cmd.Flags().Int64Var(&id, "execution", 0, "id da execução (sentinela job list)")
	cmd.Flags().BoolVar(&publish, "publish", false, "grava os aprovados em findings (sem isso, modo sombra)")
	_ = cmd.MarkFlagRequired("execution")
	return cmd
}

func runNotes(ctx context.Context, d *deps, cfg *config.Config, st *store.Store, tracer trace.Tracer, id int64, shadow bool) (discovery.NotesResult, error) {
	runner, err := d.discovery(cfg, st, tracer)
	if err != nil {
		return discovery.NotesResult{}, err
	}
	return runner.RunReviewNotes(ctx, discovery.NotesOptions{ExecutionID: id, Shadow: shadow})
}

func printNotes(w io.Writer, res discovery.NotesResult, shadow bool) {
	mode := "publicação"
	if shadow {
		mode = "modo sombra (nada gravado em findings, nada publicado)"
	}
	fmt.Fprintf(w, "rodada %d · %s · %d nota(s) de review, %d item(ns) ignorado(s) · %d chamada(s) · %s\n",
		res.RunID, mode, len(res.Notes), len(res.Ignored), res.Calls, res.Elapsed.Round(1e9))
	for _, n := range res.Notes {
		fmt.Fprintf(w, "  %s [%s, rodada %d] %s\n", n.ID, n.Lens, n.Round, truncateText(n.Text, 160))
	}
	if len(res.Dropped) > 0 {
		fmt.Fprintln(w, "\nnotas que não viraram achado (o intérprete não as manda ao Judge):")
		for _, dr := range res.Dropped {
			fmt.Fprintf(w, "  %s [%s]: %s\n    motivo: %s\n", dr.Note.ID, dr.Note.Lens, truncateText(dr.Note.Text, 120), dr.Reason)
		}
	}
	if len(res.Decisions) == 0 {
		fmt.Fprintln(w, "nenhum achado candidato: o intérprete não viu problema concreto nas notas")
	}
	for i, dcs := range res.Decisions {
		fmt.Fprintf(w, "\n%d. %s\n", i+1, dcs.Title)
		if dcs.Category != "" {
			fmt.Fprintf(w, "   %s/%s · time %s · fingerprint %s\n", dcs.Category, dcs.Severity, orDash(dcs.Team), orDash(dcs.Fingerprint))
		}
		for _, n := range dcs.Notes {
			fmt.Fprintf(w, "   nota: %s\n", truncateText(n, 200))
		}
		fmt.Fprintf(w, "   Judge: %s", strings.ToUpper(dcs.Decision))
		if dcs.Confidence > 0 {
			fmt.Fprintf(w, " (confiança %.2f)", dcs.Confidence)
		}
		if dcs.MergeWith != "" {
			fmt.Fprintf(w, " com %s", dcs.MergeWith)
		}
		fmt.Fprintf(w, "\n   motivo: %s\n", dcs.Reason)
		if dcs.Premise != "" {
			fmt.Fprintf(w, "   premissa de produto: %s\n", dcs.Premise)
		}
	}
	for _, wmsg := range res.Warnings {
		fmt.Fprintf(w, "aviso: %s\n", wmsg)
	}
}

func truncateText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
