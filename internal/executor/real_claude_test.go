package executor

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
)

// TestRealClaudeRunsThePluginEndToEnd roda o plugin sentinela-factory de verdade, com o Claude Code
// local (assinatura), num repo Go pequeno, com Linear e GitHub falsos. Todos os papéis em Haiku
// para custar pouco. Só roda com SENTINELA_REAL_CLAUDE=1; não toca em nada externo.
func TestRealClaudeRunsThePluginEndToEnd(t *testing.T) {
	if os.Getenv("SENTINELA_REAL_CLAUDE") != "1" {
		t.Skip("defina SENTINELA_REAL_CLAUDE=1 para rodar o plugin com o claude real")
	}
	e := setup(t, true)
	ctx := context.Background()
	// O repo do setup ganha o problema da EX-12: servidor HTTP sem timeouts.
	src := filepath.Join(filepath.Dir(filepath.Dir(e.remote)), "src")
	files := map[string]string{
		"cmd/api/main.go": "package main\n\nimport (\n\t\"log\"\n\t\"net/http\"\n)\n\nfunc newServer(addr string, h http.Handler) *http.Server {\n\treturn &http.Server{Addr: addr, Handler: h}\n}\n\nfunc main() {\n\tlog.Fatal(newServer(\":8080\", http.NotFoundHandler()).ListenAndServe())\n}\n",
		"AGENTS.md":       "# AGENTS.md\n\nMódulo Go único na raiz.\n\n- Testes: `go test ./...`\n- Lint: `go vet ./...`\n",
	}
	for rel, content := range files {
		_ = os.WriteFile(filepath.Join(src, filepath.FromSlash(rel)), []byte(content), 0o600)
	}
	gitIn(t, src, "add", ".")
	gitIn(t, src, "commit", "-qm", "servidor sem timeouts")
	gitIn(t, src, "push", "-q", e.remote, "main")
	m := &e.cfg.Models
	m.Orchestrator, m.SpecWriter, m.Executor, m.Approver = "claude-haiku-5-5", "claude-haiku-5-5", "claude-haiku-5-5", "claude-haiku-5-5"
	// Reviewers em Sonnet, como no sentinela.yaml: foi com eles que o job 2 da EX-12 quebrou o
	// contrato (dois reviewers devolveram o veredito no texto em vez de escrever o arquivo).
	m.ReviewerSecurity, m.ReviewerCode, m.ReviewerReliability, m.ReviewerDomain = "claude-haiku-5-5", "claude-sonnet-5-5", "claude-haiku-5-5", "claude-sonnet-5-5"
	e.cfg.Execution.MaxBudgetUSD, e.cfg.Execution.Timeout = 5, 30*time.Minute
	e.w.Runner = ClaudeRunner{Binary: "claude"}
	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	job := e.runOnce(t)
	t.Logf("job %d: status %s, decisão %s (%s), rodadas %d, turnos %d, custo equivalente $%s, erro %q",
		job.ID, job.Status, job.Decision.String, job.DecisionReason.String, job.Rounds, job.NumTurns, costOf(job.CostUsd), job.Error.String)
	t.Logf("diretório do job: %s", job.JobDir.String)
	entries, _ := filepath.Glob(filepath.Join(job.JobDir.String, "*"))
	rounds, _ := filepath.Glob(filepath.Join(job.JobDir.String, "round-*", "*"))
	t.Logf("artefatos: %v %v", entries, rounds)
	if job.Status != "succeeded" {
		t.Fatalf("a linha falhou: %s", job.Error.String)
	}
	prs, reviews := e.gh.snapshot()
	if len(prs) != 1 || !prs[0].Draft || len(reviews) != 1 || reviews[0].Event != "COMMENT" {
		t.Fatalf("PR %+v reviews %+v", prs, reviews)
	}
	if !strings.Contains(prs[0].Body, "Fixes EX-1") {
		t.Error("corpo do PR sem Fixes")
	}
	t.Logf("corpo do PR:\n%s", prs[0].Body)
}

func costOf(n pgtype.Numeric) string {
	nano, _ := store.NumericToNanoUSD(n)
	return strconv.FormatFloat(float64(nano)/1e9, 'f', 2, 64)
}

// TestRealClaudeRunsInTheJobHome confere que o Claude Code real faz login só com a credencial
// copiada para o HOME do job (M6), sem o ~/.claude.json nem as configurações da máquina. Uma
// chamada em Haiku. Só roda com SENTINELA_REAL_CLAUDE=1.
func TestRealClaudeRunsInTheJobHome(t *testing.T) {
	if os.Getenv("SENTINELA_REAL_CLAUDE") != "1" {
		t.Skip("defina SENTINELA_REAL_CLAUDE=1 para rodar o claude real no HOME do job")
	}
	src := os.Getenv("CLAUDE_CONFIG_DIR")
	if src == "" {
		h, _ := os.UserHomeDir()
		src = filepath.Join(h, ".claude")
	}
	home := filepath.Join(t.TempDir(), "home")
	res, err := ClaudeRunner{Binary: "claude", Home: JobHome{Dir: home, Source: src}}.Run(context.Background(), Invocation{
		Prompt: "Responda só com a palavra ok.", Dir: t.TempDir(), JobDir: t.TempDir(), PluginDir: t.TempDir(),
		Model: "claude-haiku-5-5", Timeout: 3 * time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(strings.ToLower(res.Result), "ok") {
		t.Fatalf("resultado: %+v", res)
	}
}

// TestRealClaudeRunsInTheContainer confere que o Claude Code real faz login e responde de dentro
// do contêiner do job, só com a lista de saída padrão (ADR 0053). Uma chamada em Haiku. Só roda
// com SENTINELA_REAL_CLAUDE=1, SENTINELA_CONTAINER_TESTS=1 e a imagem construída.
func TestRealClaudeRunsInTheContainer(t *testing.T) {
	if os.Getenv("SENTINELA_REAL_CLAUDE") != "1" {
		t.Skip("defina SENTINELA_REAL_CLAUDE=1 para rodar o claude real no contêiner")
	}
	c, _ := containerForTest(t)
	c.Binary = "claude" // o da imagem
	c.Egress = config.DefaultEgressAllow
	src := os.Getenv("CLAUDE_CONFIG_DIR")
	if src == "" {
		h, _ := os.UserHomeDir()
		src = filepath.Join(h, ".claude")
	}
	home := JobHome{Dir: filepath.Join(t.TempDir(), "home"), Source: src}
	jobDir := t.TempDir()
	res, err := ClaudeRunner{Home: home, Container: c}.Run(context.Background(), Invocation{
		Prompt: "Responda só com a palavra ok.", Dir: t.TempDir(), JobDir: jobDir, PluginDir: t.TempDir(),
		Model: "claude-haiku-5-5", Timeout: 5 * time.Minute,
	})
	if logs, lerr := os.ReadFile(filepath.Join(jobDir, "egress.log")); lerr == nil {
		t.Logf("egress.log:\n%s", logs)
	}
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError || !strings.Contains(strings.ToLower(res.Result), "ok") {
		t.Fatalf("resultado: %+v", res)
	}
}
