// Comando sentinela: binário único com os subcomandos do Sentinela.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/dev-danilocordeiro/sentinela/internal/cli"
)

func main() {
	if err := cli.LoadDotEnv(".env"); err != nil {
		fmt.Fprintln(os.Stderr, "erro lendo .env:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	err := cli.NewRootCmd(nil).ExecuteContext(ctx)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "erro:", err)
		os.Exit(1)
	}
}
