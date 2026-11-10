package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
)

// newOwnersPendingCmd mostra, só lendo, o projeto de cada achado pendente de publicação: o
// gravado no roteamento do achado, o que o roteamento daria hoje e o que a publicação usaria
// (módulo > categoria do time > o gravado > defaults.linear.project, ADR 0040).
func newOwnersPendingCmd(d *deps) *cobra.Command {
	return &cobra.Command{
		Use:   "pending",
		Short: "Mostra, só lendo, o projeto do Linear de cada achado pendente de publicação e de onde ele vem",
		Args:  cobra.NoArgs,
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
			workdir, _ := filepath.Abs(cfg.Discovery.Workdir)
			tw := tabwriter.NewWriter(c.OutOrStdout(), 0, 2, 2, ' ', 0)
			fmt.Fprintln(tw, "ACHADO\tREPO\tTIME\tPROJETO GRAVADO\tROTEAMENTO HOJE\tNA PUBLICAÇÃO\tMUDA?")
			n, changed := 0, 0
			for _, rc := range cfg.Repos {
				rows, err := st.Q.PendingFindings(ctx, rc.Name)
				if err != nil {
					return err
				}
				if len(rows) == 0 {
					continue
				}
				res := repo.NewResolver(cfg, repo.StaticCodeowners{rc.Name: localCodeowners(filepath.Join(workdir, filepath.FromSlash(rc.Name)))})
				for _, r := range rows {
					var rt repo.Routing
					_ = json.Unmarshal(r.Routing, &rt)
					var ev []struct {
						File string `json:"file"`
					}
					_ = json.Unmarshal(r.Evidence, &ev)
					now := "—"
					if len(ev) > 0 {
						if rr, err := res.Resolve(rc.Name, ev[0].File); err == nil {
							now = fmt.Sprintf("%s (de %s)", orDash(rr.LinearProject), rr.ProjectSource)
						}
					}
					publish := cfg.PublishProject(rt.Team, r.Category, rt.LinearProject)
					muda := "não"
					if publish != rt.LinearProject {
						muda, changed = "sim", changed+1
					}
					n++
					fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, rc.Name, orDash(rt.Team), orDash(rt.LinearProject), now, orDash(publish), muda)
				}
			}
			_ = tw.Flush()
			fmt.Fprintf(c.OutOrStdout(), "%d achado(s) pendente(s); %d mudariam de projeto na publicação\n", n, changed)
			return nil
		},
	}
}

// localCodeowners lê o CODEOWNERS do clone local, se houver (sem ir ao GitHub).
func localCodeowners(dir string) *repo.Codeowners {
	for _, p := range repo.CodeownersPaths {
		f, err := os.Open(filepath.Join(dir, filepath.FromSlash(p))) //nolint:gosec // caminhos fixos do GitHub
		if err != nil {
			continue
		}
		co, err := repo.ParseCodeowners(f)
		_ = f.Close()
		if err == nil {
			return co
		}
	}
	return &repo.Codeowners{}
}
