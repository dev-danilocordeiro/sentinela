package executor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// D-12 da revisão de prontidão: os artefatos dos jobs (com o stream.jsonl da sessão) e o corpo
// dos webhooks não ficam para sempre. jobs/<id> sai execution.jobs_retention depois do desfecho
// (PR mergeado ou fechado, falha ou cancelamento), nunca com o achado na fila; o payload dos
// eventos processados há mais de execution.webhook_events_retention é esvaziado, e a linha fica
// para o dedup.
func TestRetentionSweepsJobDirsAfterTheOutcomeAndWebhookPayloads(t *testing.T) {
	e := setup(t, true)
	ctx := context.Background()
	e.cfg.Execution.JobsRetention = 90 * 24 * time.Hour
	e.cfg.Execution.WebhookEventsRetention = 30 * 24 * time.Hour
	addFinding(t, e, "cccccccccccc", "Achado da retenção", []string{"cmd/x"}, "cmd/x/x.go")
	var fid int64
	if err := e.st.Pool.QueryRow(ctx, "SELECT id FROM findings WHERE fingerprint = 'cccccccccccc'").Scan(&fid); err != nil {
		t.Fatal(err)
	}
	addFinding(t, e, "dddddddddddd", "Achado com retry na fila", []string{"cmd/y"}, "cmd/y/y.go")
	var active int64
	if err := e.st.Pool.QueryRow(ctx, "SELECT id FROM findings WHERE fingerprint = 'dddddddddddd'").Scan(&active); err != nil {
		t.Fatal(err)
	}
	type job struct {
		name, status, pr, ago string
		finding               int64
		keep                  bool
	}
	jobs := []job{
		{"merge-antigo", "succeeded", "merged", "100 days", fid, false},
		{"fechado-antigo", "succeeded", "closed", "100 days", fid, false},
		{"merge-recente", "succeeded", "merged", "10 days", fid, true},
		{"pr-aberto-antigo", "succeeded", "open", "200 days", fid, true},
		{"falha-antiga", "failed", "", "100 days", fid, false},
		{"falha-recente", "failed", "", "5 days", fid, true},
		{"falha-antiga-com-retry", "failed", "", "100 days", active, true},
	}
	dirs := map[string]string{}
	for _, j := range jobs {
		dir := filepath.Join(e.cfg.Execution.Jobs, j.name)
		write(dir, "stream.jsonl", strings.Repeat("y", 100))
		dirs[j.name] = dir
		var pr any
		if j.pr != "" {
			pr = j.pr
		}
		if _, err := e.st.Pool.Exec(ctx, `INSERT INTO executions (finding_id, status, repo, issue_identifier, job_dir, pr_state, finished_at, updated_at)
			VALUES ($1, $2, 'acme/svc', 'EX-5', $3, $4, now() - $5::interval, now() - $5::interval)`, j.finding, j.status, dir, pr, j.ago); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.st.Pool.Exec(ctx, `INSERT INTO executions (finding_id, status, repo, issue_identifier) VALUES ($1, 'queued', 'acme/svc', 'EX-6')`, active); err != nil {
		t.Fatal(err)
	}
	// Fora da raiz dos jobs: nunca é apagado.
	outside := filepath.Join(t.TempDir(), "fora")
	write(outside, "stream.jsonl", "z")
	if _, err := e.st.Pool.Exec(ctx, `INSERT INTO executions (finding_id, status, repo, issue_identifier, job_dir, finished_at, updated_at) VALUES ($1, 'failed', 'acme/svc', 'EX-7', $2, now() - interval '200 days', now() - interval '200 days')`, fid, outside); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []struct {
		id, ago   string
		processed bool
	}{{"velho", "40 days", true}, {"novo", "5 days", true}, {"velho-pendente", "40 days", false}} {
		q := `INSERT INTO webhook_events (provider, event_id, event_type, payload, received_at, processed_at) VALUES ('linear', $1, 'Issue', '{"data":{"description":"código"}}', now() - $2::interval, NULL)`
		if _, err := e.st.Pool.Exec(ctx, q, ev.id, ev.ago); err != nil {
			t.Fatal(err)
		}
		if ev.processed {
			if _, err := e.st.Pool.Exec(ctx, `UPDATE webhook_events SET processed_at = now() - $2::interval WHERE event_id = $1`, ev.id, ev.ago); err != nil {
				t.Fatal(err)
			}
		}
	}

	res, err := e.w.SweepRetention(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if got := isDir(dirs[j.name]); got != j.keep {
			t.Errorf("%s: no disco %v, quer %v", j.name, got, j.keep)
		}
	}
	if !isDir(outside) || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "fora de") {
		t.Errorf("fora da raiz: no disco %v, avisos %v", isDir(outside), res.Warnings)
	}
	if res.JobDirs != 3 || res.WebhookPayloads != 1 {
		t.Errorf("resultado = %+v", res)
	}
	payload := func(id string) string {
		var p string
		if err := e.st.Pool.QueryRow(ctx, "SELECT payload::text FROM webhook_events WHERE event_id = $1", id).Scan(&p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if payload("velho") != "{}" || payload("novo") == "{}" || payload("velho-pendente") == "{}" {
		t.Errorf("payloads: velho %s, novo %s, pendente %s", payload("velho"), payload("novo"), payload("velho-pendente"))
	}
	// Desligada (negativa): nada sai.
	e.cfg.Execution.JobsRetention, e.cfg.Execution.WebhookEventsRetention = -1, -1
	if res, err := e.w.SweepRetention(ctx); err != nil || res.JobDirs != 0 || res.WebhookPayloads != 0 {
		t.Errorf("retenção desligada: %+v %v", res, err)
	}
}
