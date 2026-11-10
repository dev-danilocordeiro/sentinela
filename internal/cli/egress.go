package cli

import (
	"log"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/egress"
)

// newEgressProxyCmd é o proxy de saída do job em contêiner (ADR 0053). Roda dentro da imagem do
// job, numa rede ligada à rede interna do job e à rede com saída; o worker o sobe e derruba.
func newEgressProxyCmd() *cobra.Command {
	var listen, allow string
	cmd := &cobra.Command{
		Use:    "egress-proxy",
		Short:  "Proxy de saída do job em contêiner: só deixa passar os destinos da lista permitida",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			p := &egress.Proxy{Allow: egress.NewAllowlist(strings.Split(allow, ",")), Log: log.New(os.Stdout, "", log.LstdFlags)}
			return egress.Serve(c.Context(), listen, p)
		},
	}
	cmd.Flags().StringVar(&listen, "listen", ":3128", "endereço de escuta")
	cmd.Flags().StringVar(&allow, "allow", "", "destinos permitidos, separados por vírgula (host exato ou .domínio)")
	return cmd
}
