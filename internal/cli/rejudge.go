package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/telemetry"
)

func newRejudgeCmd(d *deps) *cobra.Command {
	var repos []string
	var jsonOut string
	var only []string
	var apply bool
	cmd := &cobra.Command{
		Use:   "rejudge",
		Short: "Julga de novo os achados já decididos com o Judge e as regras atuais, sem gravar nem publicar",
		Long: "Mede o efeito de uma mudança no Judge: para cada achado já decidido (publicado, descartado ou\n" +
			"mesclado), roda o Judge atual com o mesmo tipo de entrada da descoberta, aplica as regras da\n" +
			"casa (premissa de produto vira pergunta, limiar, votação perto dele e direção determinística da\n" +
			"mesclagem) e compara. Não grava\n" +
			"achado, não mexe no Linear e não entra na memória de decisões da descoberta. Com --apply,\n" +
			"grava como achado aberto o que estava descartado ou mesclado e agora publica ou vira pergunta;\n" +
			"`sentinela publish` (ou a próxima rodada) o leva ao Linear.",
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
			runner, err := d.discovery(cfg, st, tp.Tracer(telemetry.TracerName))
			if err != nil {
				return err
			}
			runID, items, err := runner.Rejudge(ctx, repos, only, apply)
			w := c.OutOrStdout()
			fmt.Fprintf(w, "rodada %d (rejudge) · %d achado(s)\n", runID, len(items))
			for _, it := range items {
				mark := " "
				if changed(it) {
					mark = "*"
				}
				fmt.Fprintf(w, "%s %-6s %-8s %.2f -> %-26s %.2f (limiar %.2f) %s\n", mark, orDash(it.Issue), it.Before, it.BeforeConf, after(it), it.AfterConf, it.Threshold, truncateText(it.Title, 70))
				if len(it.Votes) > 0 {
					var vs []string
					for _, v := range it.Votes {
						vs = append(vs, fmt.Sprintf("%s %.2f", v.Decision, v.Confidence))
					}
					fmt.Fprintf(w, "    votos: %s\n", strings.Join(vs, " · "))
				}
				if it.Merge != nil {
					fmt.Fprintf(w, "    mesclagem: %s absorve %s (%s)\n", it.Merge.Absorber, it.Merge.Absorbed, it.Merge.Why)
				}
				if it.Applied {
					fmt.Fprintf(w, "    gravado como achado aberto (--apply): `sentinela publish --repo %s` publica\n", it.Repo)
				}
				if it.DependsOnProduct {
					fmt.Fprintf(w, "    depende de decisão de produto: sim · %s\n", truncateText(it.Premise, 160))
				} else if it.After != "skipped" {
					fmt.Fprintf(w, "    depende de decisão de produto: não\n")
				}
			}
			if jsonOut != "" {
				data, _ := json.MarshalIndent(items, "", "  ")
				if werr := os.WriteFile(jsonOut, data, 0o600); werr != nil {
					return werr
				}
			}
			return err
		},
	}
	cmd.Flags().StringSliceVar(&repos, "repo", nil, "repos (owner/nome), pode repetir")
	cmd.Flags().StringVar(&jsonOut, "json", "", "grava a comparação completa em JSON")
	cmd.Flags().StringSliceVar(&only, "only", nil, "só estes achados (fingerprint ou issue, ex.: EX-10)")
	cmd.Flags().BoolVar(&apply, "apply", false, "grava como achado aberto o descartado ou mesclado que agora publica ou vira pergunta")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}

func after(it discovery.RejudgeItem) string {
	if it.After == "merge" && it.Merge != nil {
		return "merge"
	}
	return it.After
}

func changed(it discovery.RejudgeItem) bool {
	b, a := it.Before, it.After
	if b == "publish" && a == "question" {
		return true
	}
	return b != a && (b != "discard" || !strings.HasPrefix(a, "discard"))
}

func newRefreshVulnCmd(d *deps) *cobra.Command {
	return &cobra.Command{
		Use:   "refresh-vuln <finding-id>",
		Short: "Regenera o texto de um achado de vulnerabilidade ainda sem issue, com o código atual",
		Long: "Roda o govulncheck no clone, no commit da rodada que gravou o achado, e reescreve título,\n" +
			"contexto, evidências, ação sugerida, critérios e escopo. Exige o mesmo fingerprint. Estado,\n" +
			"roteamento, severidade e o veredito do Judge não mudam (ADR 0036). Sem modelo, sem Linear.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ctx := c.Context()
			id, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil {
				return fmt.Errorf("id inválido %q", args[0])
			}
			cfg, err := d.config()
			if err != nil {
				return err
			}
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			runner, err := d.discovery(cfg, st, noop.NewTracerProvider().Tracer(""))
			if err != nil {
				return err
			}
			before, after, err := runner.RefreshVulnFinding(ctx, id)
			if err != nil {
				return err
			}
			w := c.OutOrStdout()
			fmt.Fprintf(w, "achado %d (%s) regenerado; estado %s, Judge %.2f mantidos\n", id, before.Fingerprint, before.State, before.JudgeConfidence.Float32)
			fmt.Fprintf(w, "título: %s\nmódulo: %s -> %s\n\n%s\n\nação: %s\n", after.Title, before.Module, after.Module, after.Context, after.ProposedFix)
			return nil
		},
	}
}

func newPublishCmd(d *deps) *cobra.Command {
	var repoName string
	cmd := &cobra.Command{
		Use:   "publish",
		Short: "Publica no Linear os achados pendentes de um repo, sem analisar o código",
		Long: "Rodada sem chamadas (no_changes), com o head da última rodada e o CODEOWNERS do clone: publica\n" +
			"os achados abertos sem issue, com as mesmas regras do scan (limite por time, pergunta de\n" +
			"produto com labels.question, aviso no Slack). Exige publish.enabled e um tracker do Linear.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			cfg, err := d.config()
			if err != nil {
				return err
			}
			if cfg.LocalTracker() || !cfg.Publish.Enabled {
				return errors.New("publish precisa de publish.enabled: true e de um tracker do Linear (linear_api ou linear_mcp)")
			}
			if _, ok := cfg.Repo(repoName); !ok {
				return fmt.Errorf("repo %s não está no sentinela.yaml", repoName)
			}
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				return err
			}
			defer st.Close()
			runner, err := d.discovery(cfg, st, noop.NewTracerProvider().Tracer(""))
			if err != nil {
				return err
			}
			pub, err := linearPublisher(d, cfg, st)
			if err != nil {
				return err
			}
			runner.Publisher = pub
			w := c.OutOrStdout()
			res, err := runner.ResumePublish(ctx, repoName)
			if err != nil {
				return err
			}
			fmt.Fprintf(w, "rodada %d sem chamadas: publicação dos pendentes de %s\n", res.RunID, repoName)
			printPublish(w, res, false)
			pending, perr := st.Q.PendingFindings(context.WithoutCancel(ctx), repoName)
			if perr != nil {
				return perr
			}
			if err := publishOutcome(w, repoName, res, len(pending)); err != nil {
				return err
			}
			fmt.Fprintf(w, "pendentes de %s: %d\n", repoName, len(pending))
			return nil
		},
	}
	cmd.Flags().StringVar(&repoName, "repo", "", "repo owner/nome do sentinela.yaml")
	_ = cmd.MarkFlagRequired("repo")
	return cmd
}
