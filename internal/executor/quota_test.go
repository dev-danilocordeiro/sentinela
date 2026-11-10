package executor

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/notify"
)

// recordingSlack guarda os avisos postados.
type recordingSlack struct {
	mu    sync.Mutex
	posts []string // "canal: texto"
}

func (s *recordingSlack) Post(_ context.Context, channel, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.posts = append(s.posts, channel+": "+text)
	return nil
}

func (s *recordingSlack) matching(sub string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, p := range s.posts {
		if strings.Contains(p, sub) {
			out = append(out, p)
		}
	}
	return out
}

// Bug de 2026-10-14: a cota semanal da assinatura acabou e o worker marcou 7 jobs como failed,
// tirou a sentinela-fix e comentou falha nas issues (execuções 24 a 30). Cota esgotada não é falha
// do job: ele volta para a fila até o reset, com a label e sem comentário; o worker para de pegar
// jobs até lá e avisa uma vez no canal padrão. Depois do reset, o mesmo job continua a sessão.
func TestUsageLimitRequeuesTheJobAndPausesTheWorker(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	sp, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Skip("sem tzdata: ", err)
	}
	reset := time.Now().In(sp).AddDate(0, 0, 2).Truncate(time.Hour)
	msg := fmt.Sprintf("You've hit your weekly limit · resets %s, %s (America/Sao_Paulo)", reset.Format("Jan 2"), strings.ToLower(reset.Format("3pm")))
	slack := &recordingSlack{}
	e.cfg.Defaults.Notify.Slack = "C0SENTINELA"
	e.w.Notify = &notify.Router{Notifier: slack, Cfg: e.cfg}

	// O claude falso abre a sessão s-cota (gravando o JSONL onde o Claude Code grava) e termina com
	// a mensagem do limite semanal, como no dia 14.
	calls := filepath.Join(t.TempDir(), "calls")
	sessions := filepath.Join(e.runner.home, "projects")
	e.w.Runner = ClaudeRunner{Binary: scriptClaude(t, `echo x >> `+calls+`
d="`+sessions+`/$(pwd | sed 's/[^a-zA-Z0-9]/-/g')"; mkdir -p "$d"; echo '{"type":"user"}' > "$d/s-cota.jsonl"
echo '{"type":"system","subtype":"init","session_id":"s-cota","apiKeySource":"none"}'
cat <<'EOF'
{"type":"result","subtype":"success","is_error":true,"num_turns":1,"total_cost_usd":0,"session_id":"s-cota","result":"`+msg+`"}
EOF`)}

	e.lin.AddLabel("EX-1", "sentinela-fix")
	if _, err := e.w.Enqueue(ctx); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.w.RunNext(ctx); err != nil || !ran {
		t.Fatalf("RunNext = %v, %v", ran, err)
	}
	job, err := e.st.Q.GetExecution(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != "queued" || job.Decision.Valid || job.Error.Valid {
		t.Fatalf("job = %s, decision %q, erro %q; quer queued, sem decision nem erro", job.Status, job.Decision.String, job.Error.String)
	}
	wantReason := "cota da assinatura até " + reset.Format("02/01 15:04") + " (America/Sao_Paulo)"
	if job.WaitReason.String != wantReason {
		t.Errorf("wait_reason = %q, quer %q", job.WaitReason.String, wantReason)
	}
	var runAfter time.Time
	if err := e.st.Pool.QueryRow(ctx, "SELECT run_after FROM executions WHERE id = 1").Scan(&runAfter); err != nil {
		t.Fatal(err)
	}
	if runAfter.Before(reset) || runAfter.After(reset.Add(30*time.Minute)) {
		t.Errorf("run_after = %s; quer o reset (%s) com uma margem", runAfter, reset)
	}
	issue := e.lin.Snapshot()[0]
	if !slicesContains(issue.LabelIDs, e.lin.Labels["sentinela-fix"]) {
		t.Error("a sentinela-fix saiu da issue")
	}
	if slicesContains(issue.LabelIDs, e.lin.Labels["sentinela-failed"]) {
		t.Error("a issue ganhou sentinela-failed")
	}
	if c := e.lin.CommentsOf("EX-1"); len(c) != 0 {
		t.Errorf("comentou na issue: %+v", c)
	}
	if got := slack.matching("cota da assinatura"); len(got) != 1 || !strings.HasPrefix(got[0], "C0SENTINELA: ") {
		t.Errorf("aviso da cota no Slack = %q; quer um, no canal padrão", got)
	}

	// Até o reset, o worker não pega job nenhum, nem com o run_after vencido (outro job pronto).
	if _, err := e.st.Pool.Exec(ctx, "UPDATE executions SET run_after = now() - interval '1 second' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if ran, err := e.w.RunNext(ctx); err != nil || ran {
		t.Fatalf("pausado pela cota, RunNext = %v, %v", ran, err)
	}
	// Um worker novo (restart da unit) lê a pausa do banco.
	if _, err := e.st.Pool.Exec(ctx, "UPDATE executions SET run_after = $1 WHERE id = 1", runAfter); err != nil {
		t.Fatal(err)
	}
	restarted := &Worker{Cfg: e.w.Cfg, Store: e.w.Store, Linear: e.w.Linear, Runner: e.w.Runner, ClaudeHome: e.w.ClaudeHome, Git: e.w.Git, Bot: e.w.Bot, Reviewer: e.w.Reviewer, Notify: e.w.Notify}
	if ran, err := restarted.RunNext(ctx); err != nil || ran {
		t.Fatalf("worker reiniciado, RunNext = %v, %v", ran, err)
	}
	if raw, _ := os.ReadFile(calls); strings.Count(string(raw), "x") != 1 {
		t.Errorf("o claude rodou %d vezes; quer 1", strings.Count(string(raw), "x"))
	}

	// Depois do reset, o mesmo job continua a sessão s-cota e termina com PR.
	if _, err := e.st.Pool.Exec(ctx, "UPDATE executions SET run_after = now() - interval '1 second' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	restarted.Runner = e.runner
	if ran, err := restarted.RunNext(ctx); err != nil || !ran {
		t.Fatalf("depois do reset, RunNext = %v, %v", ran, err)
	}
	inv := e.runner.invocations()[0]
	if inv.Resume != "s-cota" || inv.Dir != job.Worktree.String || inv.JobDir != job.JobDir.String {
		t.Errorf("continuação: resume %q dir %q job %q", inv.Resume, inv.Dir, inv.JobDir)
	}
	done, _ := e.st.Q.GetExecution(ctx, 1)
	if done.Status != "succeeded" || !done.PrUrl.Valid || done.WaitReason.Valid {
		t.Fatalf("job = %s, pr %v, wait %q: %s", done.Status, done.PrUrl, done.WaitReason.String, done.Error.String)
	}
	if strings.HasPrefix(done.ResumeNote.String, "retomou a sessão") {
		t.Errorf("a pausa da cota não conta como retomada (execution.max_resumes): %q", done.ResumeNote.String)
	}
	if got := slack.matching("cota da assinatura"); len(got) != 1 {
		t.Errorf("avisos da cota = %d; quer 1", len(got))
	}
}
