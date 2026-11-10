package cli

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
)

// liveConfig relê o sentinela.yaml (e config.d) a cada ciclo do serve, do worker e do scan --all,
// sem reiniciar (ADR 0041). Uma config inválida não para nada: fica a última válida, e o aviso vai
// para o log e para o canal padrão (#sentinela), uma vez por erro diferente. Adapters, banco e
// credenciais só mudam no reinício: os clientes já montados continuam os mesmos.
type liveConfig struct {
	d       *deps
	cur     *config.Config
	lastErr string
	log     io.Writer
}

func newLiveConfig(d *deps, cur *config.Config, log io.Writer) *liveConfig {
	return &liveConfig{d: d, cur: cur, log: log}
}

// Reload devolve a config atual e se ela mudou desde o último ciclo.
func (l *liveConfig) Reload(ctx context.Context) (*config.Config, bool) {
	cfg, err := l.d.config()
	if err != nil {
		msg := strings.ReplaceAll(err.Error(), "\n", "; ")
		if msg != l.lastErr {
			l.lastErr = msg
			text := fmt.Sprintf("*Sentinela*: a config em %s ficou inválida; continuo com a última válida até ela ser corrigida. Erro: %s", *l.d.cfgPath, msg)
			fmt.Fprintf(l.log, "aviso: %s\n", text)
			for _, w := range l.d.router(l.cur).Summary(ctx, text) {
				fmt.Fprintf(l.log, "aviso: %s\n", w)
			}
		}
		return l.cur, false
	}
	if l.lastErr != "" {
		fmt.Fprintf(l.log, "config válida de novo: %s\n", *l.d.cfgPath)
		l.lastErr = ""
	}
	if sameConfig(l.cur, cfg) {
		return l.cur, false
	}
	l.cur = cfg
	return cfg, true
}

// sameConfig compara as configs pelo conteúdo, para não refazer clientes à toa.
func sameConfig(a, b *config.Config) bool { return reflect.DeepEqual(a, b) }
