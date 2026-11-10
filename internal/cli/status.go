package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/spf13/cobra"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/executor"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/repo"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
)

// Units do systemd de usuário que deploy/systemd instala (docs/operacao.md).
// statusUnits são as units mostradas, pelo sufixo depois do prefixo (deploy/install.sh --prefix).
var statusUnits = []string{"-serve.service", "-worker.service", "-scan.timer"}

// workerUnits lista as units *-worker.service carregadas no escopo, com a sentinela-worker.service
// primeiro (a de prefixo padrão), para achar a desta instalação pelo SENTINELA_HOME.
func workerUnits(ctx context.Context, run func(context.Context, ...string) (string, error), scope []string) []string {
	units := []string{"sentinela-worker.service"}
	out, err := run(ctx, append(scope, "list-units", "--all", "--plain", "--no-legend", "--type=service", "*-worker.service")...)
	if err != nil {
		return units
	}
	for _, line := range strings.Split(out, "\n") {
		if f := strings.Fields(line); len(f) > 0 && strings.HasSuffix(f[0], "-worker.service") && f[0] != units[0] {
			units = append(units, f[0])
		}
	}
	return units
}

func newStatusCmd(d *deps) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Resumo de operação: serviços, última rodada por repo, fila, PRs abertos e token do Linear",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			ctx := c.Context()
			w := c.OutOrStdout()
			cfg, err := d.config()
			if err != nil {
				return err
			}
			if _, err := exec.LookPath("systemctl"); err == nil {
				if dir, err := store.HomeDir(); err == nil {
					printUnits(ctx, w, dir, systemctl)
				}
			}
			printLinearToken(w, cfg, d)
			printCommandsDocs(w, cfg)
			st, err := store.Open(ctx, d.env("DATABASE_URL"))
			if err != nil {
				fmt.Fprintf(w, "banco: %v\n", err)
				return err
			}
			defer st.Close()
			return printStoreStatus(ctx, w, cfg, st)
		},
	}
}

// printCommandsDocs lista os repos sem AGENTS.md nem CLAUDE.md na raiz, pelo clone do último scan
// (sem rede): neles a correção só roda nos workspaces que têm o próprio arquivo, e falha no resto.
// O config validate confere no GitHub.
func printCommandsDocs(w io.Writer, cfg *config.Config) {
	workdir, err := filepath.Abs(cfg.Discovery.Workdir)
	if err != nil {
		return
	}
	var missing, partial, unknown []string
	for _, r := range cfg.Repos {
		switch doc, cloned := repo.LocalCommandsDoc(workdir, r.Name); {
		case !cloned:
			unknown = append(unknown, r.Name)
		case doc == "":
			if ws := repo.WorkspaceCommandsDocs(filepath.Join(workdir, filepath.FromSlash(r.Name))); len(ws) > 0 {
				partial = append(partial, r.Name+" (só "+strings.Join(ws, ", ")+")")
			} else {
				missing = append(missing, r.Name)
			}
		}
	}
	switch {
	case len(missing) == 0 && len(partial) == 0:
		fmt.Fprintf(w, "AGENTS.md/CLAUDE.md: todos os repos escaneados têm (clone do último scan)\n")
	case len(missing) > 0:
		fmt.Fprintf(w, "sem AGENTS.md nem CLAUDE.md (a correção não roda; clone do último scan): %s\n", strings.Join(missing, ", "))
	}
	if len(partial) > 0 {
		fmt.Fprintf(w, "sem AGENTS.md nem CLAUDE.md na raiz (a correção só roda nos workspaces com o arquivo): %s\n", strings.Join(partial, "; "))
	}
	if len(unknown) > 0 {
		fmt.Fprintf(w, "  ainda sem clone (confira com config validate): %s\n", strings.Join(unknown, ", "))
	}
}

// systemctl roda o systemctl e devolve a saída sem espaços nas pontas.
func systemctl(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, "systemctl", args...).Output() //nolint:gosec // argumentos fixos do status
	return strings.TrimSpace(string(out)), err
}

// unitHome lê o SENTINELA_HOME do ambiente de uma unit carregada ("" se a unit não existe).
func unitHome(ctx context.Context, run func(context.Context, ...string) (string, error), scope []string, unit string) (loaded bool, home string) {
	state, _ := run(ctx, append(scope, "show", unit, "-p", "LoadState", "--value")...)
	if state != "loaded" {
		return false, ""
	}
	env, _ := run(ctx, append(scope, "show", unit, "-p", "Environment", "--value")...)
	for _, kv := range strings.Fields(env) {
		if v, ok := strings.CutPrefix(kv, "SENTINELA_HOME="); ok {
			return true, v
		}
	}
	return true, ""
}

// printUnits mostra o estado das units do systemd desta instalação: as de usuário ou as de
// sistema cujo SENTINELA_HOME é o diretório atual (D-19 da revisão de prontidão). Units de outra
// instalação na mesma máquina não aparecem como se fossem desta.
func printUnits(ctx context.Context, w io.Writer, dir string, run func(context.Context, ...string) (string, error)) {
	for _, sc := range []struct {
		label string
		args  []string
	}{{"systemd --user", []string{"--user"}}, {"systemd", nil}} {
		var others []string
		prefix := ""
		for _, u := range workerUnits(ctx, run, sc.args) {
			loaded, home := unitHome(ctx, run, sc.args, u)
			switch {
			case !loaded:
			case home == dir:
				prefix = strings.TrimSuffix(u, "-worker.service")
			default:
				others = append(others, fmt.Sprintf("as units %s-* são da instalação em %s", strings.TrimSuffix(u, "-worker.service"), orDash(home)))
			}
			if prefix != "" {
				break
			}
		}
		if prefix == "" {
			if len(others) > 0 {
				fmt.Fprintf(w, "serviços (%s): %s, não desta (%s)\n", sc.label, strings.Join(others, "; "), dir)
			}
			continue
		}
		fmt.Fprintf(w, "serviços (%s):\n", sc.label)
		for _, suffix := range statusUnits {
			u := prefix + suffix
			state, _ := run(ctx, append(sc.args, "is-active", u)...)
			if state == "" {
				state = "desconhecido"
			}
			fmt.Fprintf(w, "  %-32s %s\n", u, state)
		}
		if line, err := run(ctx, append(sc.args, "list-timers", prefix+"-scan.timer", "--no-legend")...); err == nil && line != "" {
			f := strings.Fields(line)
			fmt.Fprintf(w, "  próximo scan: %s\n", strings.Join(f[:min(4, len(f))], " "))
		}
		return
	}
	fmt.Fprintf(w, "serviços: nenhuma unit do systemd desta instalação (%s); veja deploy/install.sh\n", dir)
}

// pendingLine é a linha dos achados abertos que ainda não viraram decisão: no modo local eles
// estão no relatório; nos outros, sem issue (pelo limite por time ou em modo sombra).
func pendingLine(local bool, localDir string, n int64) string {
	if local {
		return fmt.Sprintf("achados abertos no relatório local (%s), à espera de decisão: %d", localDir, n)
	}
	return fmt.Sprintf("achados abertos sem issue (pendentes pelo limite ou de modo sombra): %d", n)
}

func printLinearToken(w io.Writer, cfg *config.Config, d *deps) {
	a := cfg.Adapters
	fmt.Fprintf(w, "modo: tracker %s · notifier %s · git_host %s\n", a.Tracker, a.Notifier, a.GitHost)
	if cfg.LocalTracker() {
		fmt.Fprintf(w, "tracker local: relatórios em %s; decisões com `sentinela finding`\n", a.LocalDir)
		return
	}
	if cfg.LinearMCP() {
		fmt.Fprintf(w, "linear: pelo MCP %q do Claude Code (ações como você, com o prefixo **Sentinela); leitura a cada %s com %s\n", a.MCP.LinearServer, a.MCP.PollInterval, a.MCP.Model)
		return
	}
	if d.linearMode(cfg) != config.LinearAuthApp {
		fmt.Fprintln(w, "linear: chave pessoal (linear_auth.mode api_key)")
		return
	}
	raw, err := os.ReadFile(cfg.LinearAuth.TokenFile) //nolint:gosec // caminho do sentinela.yaml
	if err != nil {
		fmt.Fprintf(w, "linear: app sem token (%v); rode `sentinela linear auth`\n", err)
		return
	}
	var t linear.Token
	if err := json.Unmarshal(raw, &t); err != nil {
		fmt.Fprintf(w, "linear: token inválido em %s: %v\n", cfg.LinearAuth.TokenFile, err)
		return
	}
	renew := "renova sozinho (refresh token)"
	if t.RefreshToken == "" {
		renew = "pede outro ao vencer (client_credentials)"
		if t.Grant != config.GrantClientCreds {
			renew = "sem refresh token: rode `sentinela linear auth` antes de vencer"
		}
	}
	fmt.Fprintf(w, "linear: app autorizado em %s; access token até %s; %s\n",
		t.AuthorizedAt.Local().Format("2006-01-02 15:04"), t.ExpiresAt.Local().Format("2006-01-02 15:04"), renew)
}

func printStoreStatus(ctx context.Context, w io.Writer, cfg *config.Config, st *store.Store) error {
	runs, err := st.Q.LastRunPerRepo(ctx)
	if err != nil {
		return err
	}
	byRepo := map[string]int{}
	for i, r := range runs {
		byRepo[r.Repo] = i
	}
	fmt.Fprintln(w, "última rodada por repo:")
	for _, rc := range cfg.Repos {
		i, ok := byRepo[rc.Name]
		if !ok {
			fmt.Fprintf(w, "  %-45s nunca rodou\n", rc.Name)
			continue
		}
		r := runs[i]
		when := r.StartedAt.Time.Local().Format("2006-01-02 15:04")
		line := fmt.Sprintf("  %-45s #%d %s em %s", rc.Name, r.ID, r.Status, when)
		if r.Error != "" {
			msg := r.Error
			if len(msg) > 80 {
				msg = msg[:80] + "…"
			}
			line += " · " + msg
		}
		fmt.Fprintln(w, line)
	}
	pending, err := st.Q.OpenFindingsWithoutIssue(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, pendingLine(cfg.LocalTracker(), cfg.Adapters.LocalDir, int64(pending)))
	counts, err := st.Q.ExecutionStatusCounts(ctx)
	if err != nil {
		return err
	}
	var parts []string
	for _, c := range counts {
		parts = append(parts, fmt.Sprintf("%s %d", c.Status, c.N))
	}
	fmt.Fprintf(w, "execuções: %s\n", orDash(strings.Join(parts, " · ")))
	if tc, err := st.Q.TriggerConsumedCounts(ctx); err != nil {
		return err
	} else if tc.Consumed > 0 {
		fmt.Fprintf(w, "gatilhos consumidos (label de gatilho tirada pelo worker depois de execução sem PR, ADR 0029): %d (%d nos últimos 30 dias); %d já estavam sem a label\n",
			tc.Removed, tc.Removed30d, tc.Consumed-tc.Removed)
	}
	snap, err := st.Q.QueueSnapshot(ctx)
	if err != nil {
		return err
	}
	for _, r := range snap {
		if r.Status == "queued" && r.WaitReason.Valid {
			fmt.Fprintf(w, "  aguardando: job %d · %s: %s\n", r.ID, r.IssueIdentifier, r.WaitReason.String)
		}
	}
	prs, err := st.Q.OpenPRExecutions(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "PRs abertos do Sentinela: %d\n", len(prs))
	for _, e := range prs {
		age := time.Since(e.UpdatedAt.Time).Round(time.Hour)
		fmt.Fprintf(w, "  %s %s (%s, gate %s, sem mudança há %s)\n", e.IssueIdentifier, e.PrUrl.String, orDash(e.Decision.String), orDash(e.Gate.String), age)
	}
	if err := printMCPCalls(ctx, w, cfg, st); err != nil {
		return err
	}
	if err := printCosts(ctx, w, cfg, st); err != nil {
		return err
	}
	return printWorktrees(ctx, w, st)
}

// printMCPCalls mostra o custo das leituras pelo MCP (linear_mcp, slack_mcp): chamadas por ciclo
// de leitura e por dia, nos últimos 7 dias.
func printMCPCalls(ctx context.Context, w io.Writer, cfg *config.Config, st *store.Store) error {
	if !cfg.LinearMCP() && cfg.Adapters.Notifier != config.NotifierSlackMCP {
		return nil
	}
	s, err := st.Q.MCPCallStats(ctx)
	if err != nil {
		return err
	}
	if s.Total == 0 {
		fmt.Fprintln(w, "MCP: nenhuma chamada nos últimos 7 dias")
		return nil
	}
	days := max(s.SpanSeconds/86400, 1)
	perCycle := 0.0
	if s.Cycles > 0 {
		perCycle = float64(s.Reads) / float64(s.Cycles)
	}
	fmt.Fprintf(w, "MCP (7 dias): %d chamada(s) claude -p, %.0f/dia; %d ciclo(s) de leitura, %.1f chamada(s) de leitura por ciclo; %.1f s em média; %d com erro\n",
		s.Total, float64(s.Total)/days, s.Cycles, perCycle, s.AvgLatencyMs/1000, s.Failed)
	return nil
}

// printWorktrees mostra o disco dos worktrees guardados e o que a limpeza já liberou (ADR 0037).
func printWorktrees(ctx context.Context, w io.Writer, st *store.Store) error {
	cands, err := st.Q.WorktreeSweepCandidates(ctx)
	if err != nil {
		return err
	}
	kept, keptBytes := 0, int64(0)
	for _, c := range cands {
		if _, err := os.Stat(c.Worktree); err == nil {
			kept++
			keptBytes += executor.DirSize(c.Worktree)
		}
	}
	t, err := st.Q.WorktreeCleanupTotals(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(w, "worktrees: %d guardado(s) ocupando %s; limpeza: %d removido(s), %s liberados (%s nos últimos 7 dias)\n",
		kept, humanBytes(keptBytes), t.Removed, humanBytes(t.FreedBytes), humanBytes(t.FreedWeek))
	return nil
}

// printCosts mostra o custo dos últimos 30 dias: a descoberta por repo e as correções por time
// dono. Com llm.provider anthropic_api é dólar cobrado; com claude_code, o equivalente que o
// Claude Code reporta (a assinatura não cobra por chamada). Base para estimar o custo por time
// (docs/empresa.md, docs/custos.md).
func printCosts(ctx context.Context, w io.Writer, cfg *config.Config, st *store.Store) error {
	repos, err := st.Q.CostByRepo30d(ctx)
	if err != nil {
		return err
	}
	teams, err := st.Q.CostByTeam30d(ctx)
	if err != nil {
		return err
	}
	unit := "US$"
	if cfg.LLM.Provider != config.ProviderAnthropicAPI {
		unit = "US$ equivalente (assinatura)"
	}
	usd := func(n pgtype.Numeric) string {
		nano, _ := store.NumericToNanoUSD(n)
		return fmt.Sprintf("%.2f", float64(nano)/1e9)
	}
	if len(repos) == 0 && len(teams) == 0 {
		fmt.Fprintln(w, "custo (30 dias): nenhuma chamada registrada")
		return nil
	}
	var parts []string
	for _, r := range repos {
		parts = append(parts, fmt.Sprintf("%s %s (%d chamadas)", r.Repo, usd(r.CostUsd), r.Calls))
	}
	fmt.Fprintf(w, "custo da descoberta por repo (30 dias, %s): %s\n", unit, orDash(strings.Join(parts, " · ")))
	parts = nil
	for _, t := range teams {
		parts = append(parts, fmt.Sprintf("%s %s (%d jobs)", orDash(t.Team), usd(t.CostUsd), t.Jobs))
	}
	fmt.Fprintf(w, "custo das correções por time (30 dias, %s): %s\n", unit, orDash(strings.Join(parts, " · ")))
	return nil
}
