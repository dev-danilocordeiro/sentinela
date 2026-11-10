package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/discovery"
	"github.com/dev-danilocordeiro/sentinela/internal/linear"
	"github.com/dev-danilocordeiro/sentinela/internal/linear/lineartest"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/local"
	"github.com/dev-danilocordeiro/sentinela/internal/scan"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Smoke test de cada modo (completo, local e MCP) com adaptadores falsos: um scan curto (respostas
// gravadas em testdata/replay, sem modelo), a publicação, uma decisão humana (recusa) e um pedido
// de correção até o PR em draft (GitHub falso). init e config validate de cada modo ficam nos
// testes da CLI (TestInitLocalMode..., TestInitMCPMode..., TestConfigValidateIn...).

// smokeScan roda a descoberta no acme/svc do setup com o Publisher do modo e devolve os achados
// novos, na ordem do id.
func smokeScan(t *testing.T, e *env, pub discovery.Publisher) []db.Finding {
	t.Helper()
	ctx := context.Background()
	r := &discovery.Runner{
		Cfg: e.cfg, Store: e.st, Git: e.w.Git, Scanners: &scan.Runner{}, Prices: llm.NewPriceTable(e.cfg.Pricing),
		Tracer: noop.NewTracerProvider().Tracer(""), Embedder: discovery.HashEmbedder{}, Publisher: pub,
		NewLLM: func(runID int64, _ llm.NanoUSD) (llm.Caller, error) {
			return &llm.Replay{Dir: filepath.Join("testdata", "replay"), Recorder: store.NewLLMRecorder(e.st), B: llm.NewCallBudget(100), RunID: runID}, nil
		},
	}
	res, err := r.Run(ctx, discovery.Options{Repo: "acme/svc"})
	if err != nil || res.Status != "succeeded" {
		t.Fatalf("scan: %+v, %v", res, err)
	}
	if res.PublishErr != nil {
		t.Fatalf("publicação: %v", res.PublishErr)
	}
	rows, err := e.st.Q.ListFindingsByRun(ctx, pgtype.Int8{Int64: res.RunID, Valid: true})
	if err != nil || len(rows) != 2 {
		t.Fatalf("achados da rodada = %d, %v", len(rows), err)
	}
	out := make([]db.Finding, 0, len(rows))
	for _, r := range rows {
		f, err := e.st.Q.GetFinding(ctx, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f)
	}
	return out
}

// smokeChecks confere a recusa (rejected, outcome humano, supressão) e o PR em draft do fix.
func smokeChecks(t *testing.T, e *env, rejected int64, job db.Execution, prPrefix string) {
	t.Helper()
	ctx := context.Background()
	f, _ := e.st.Q.GetFinding(ctx, rejected)
	outs, _ := e.st.Q.FindingOutcomes(ctx, rejected)
	sups, _ := e.st.Q.ActiveSuppressions(ctx, "acme/svc")
	if f.State != "rejected" || len(outs) == 0 || outs[len(outs)-1].Kind != "rejected" || outs[len(outs)-1].DecidedBy != "human" || len(sups) == 0 {
		t.Errorf("recusa: achado %s, outcomes %+v, supressões %d", f.State, outs, len(sups))
	}
	if job.Status != "succeeded" || !job.PrNumber.Valid {
		t.Fatalf("fix: job %s, PR %v (%s)", job.Status, job.PrNumber.Valid, job.Error.String)
	}
	prs, _ := e.gh.snapshot()
	pr := prs[len(prs)-1]
	if !pr.Draft || pr.Merged || !strings.HasPrefix(pr.Body, prPrefix) {
		t.Errorf("PR: draft %v, merged %v, corpo %q", pr.Draft, pr.Merged, firstLine(pr.Body))
	}
}

func TestSmokeFullMode(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	pub := &linear.Publisher{Cfg: e.cfg, Store: e.st, Linear: e.w.Linear}
	fs := smokeScan(t, e, pub)
	issueOf := map[int64]string{}
	for _, f := range fs {
		li, err := e.st.Q.PrimaryIssueForFinding(ctx, f.ID)
		if err != nil {
			t.Fatalf("achado %d sem issue no Linear: %v", f.ID, err)
		}
		issueOf[f.ID] = li.Identifier
	}
	// Uma pessoa cancela a primeira, com o motivo; o sync grava a recusa.
	e.lin.Move(issueOf[fs[0].ID], "Canceled")
	syncer := &linear.Syncer{Store: e.st, Linear: e.w.Linear}
	if r, err := syncer.Poll(ctx); err != nil || !hasRejection(r) {
		t.Fatalf("sync: %+v, %v", r, err)
	}
	// E aplica a label de gatilho na segunda: o worker enfileira e abre o PR em draft.
	e.lin.AddLabel(issueOf[fs[1].ID], "sentinela-fix")
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 1 || q[0].FindingID != fs[1].ID {
		t.Fatalf("gatilho: %+v, %v", q, err)
	}
	smokeChecks(t, e, fs[0].ID, e.runOnce(t), "Fixes "+issueOf[fs[1].ID])
}

func TestSmokeLocalMode(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.Adapters.Tracker, e.cfg.Adapters.GitHost = config.TrackerLocal, config.GitHostGHCLI
	e.cfg.Adapters.LocalDir = filepath.Join(t.TempDir(), "reports")
	e.w.Linear = nil
	tr := &local.Tracker{Cfg: e.cfg, Store: e.st}
	fs := smokeScan(t, e, tr)
	md, err := os.ReadFile(filepath.Join(e.cfg.Adapters.LocalDir, "acme__svc.md"))
	if err != nil || !strings.Contains(string(md), config.LocalIssueID(fs[1].ID)) || !strings.Contains(string(md), "finding fix") {
		t.Fatalf("relatório local: %v\n%s", err, md)
	}
	if _, err := tr.Reject(ctx, fs[0].ID, "não é bug", local.Decision{Actor: "dono"}); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Fix(ctx, fs[1].ID, local.Decision{Actor: "dono"}); err != nil {
		t.Fatal(err)
	}
	smokeChecks(t, e, fs[0].ID, e.runOnce(t), "Achado "+config.LocalIssueID(fs[1].ID)+" do Sentinela")
}

func TestSmokeMCPMode(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.Adapters.Tracker = config.TrackerLinearMCP
	now := time.Now()
	fake := &lineartest.FakeMCP{Labels: []string{"sentinela", "sentinela-fix", "sentinela-done", "sentinela-failed"}, Clock: func() time.Time { return now }}
	tr := &linear.MCPTracker{Run: fake, Server: "claude.ai Linear", PollInterval: 15 * time.Minute, Teams: []string{"EX"}, FindingLabel: "sentinela", Now: func() time.Time { return now }}
	e.w.Linear = tr
	pub := &linear.Publisher{Cfg: e.cfg, Store: e.st, Linear: tr}
	fs := smokeScan(t, e, pub)
	if len(fake.Issues) != 2 {
		t.Fatalf("issues criadas pelo MCP = %d", len(fake.Issues))
	}
	syncer := &linear.Syncer{Store: e.st, Linear: tr}
	if _, err := syncer.Poll(ctx); err != nil {
		t.Fatal(err)
	}
	byFinding := map[int64]*lineartest.FakeMCPIssue{}
	for _, f := range fs {
		for _, is := range fake.Issues {
			if strings.Contains(is.Desc, f.Fingerprint) {
				byFinding[f.ID] = is
			}
		}
	}
	// A pessoa cancela a primeira (com o motivo) e aplica o gatilho na segunda; o próximo ciclo lê.
	now = now.Add(time.Minute)
	a, b := byFinding[fs[0].ID], byFinding[fs[1].ID]
	a.State, a.StateType, a.Updated = "Canceled", "canceled", now
	a.Comments = append(a.Comments, map[string]any{"body": "Não é bug.", "createdAt": now.UTC().Format(time.RFC3339), "author": "Pessoa"})
	b.Labels, b.Updated = append(b.Labels, "sentinela-fix"), now
	now = now.Add(16 * time.Minute)
	if r, err := syncer.Poll(ctx); err != nil || !hasRejection(r) {
		t.Fatalf("sync pelo MCP: %+v, %v", r, err)
	}
	if q, err := e.w.Enqueue(ctx); err != nil || len(q) != 1 || q[0].FindingID != fs[1].ID {
		t.Fatalf("gatilho pelo MCP: %+v, %v", q, err)
	}
	smokeChecks(t, e, fs[0].ID, e.runOnce(t), "Fixes "+b.Identifier)
	if !containsLabel(b.Labels, "sentinela-done") {
		t.Errorf("label de resultado não aplicada pelo MCP: %v", b.Labels)
	}
}

func hasRejection(r linear.SyncResult) bool {
	for _, ch := range r.Changes {
		if ch.Outcome == "rejected" {
			return true
		}
	}
	return false
}

func containsLabel(ls []string, l string) bool {
	for _, x := range ls {
		if x == l {
			return true
		}
	}
	return false
}
