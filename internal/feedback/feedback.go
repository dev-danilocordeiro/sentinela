// Package feedback fecha o ciclo com as decisões humanas: o Feedback Curator transforma
// desfechos em exemplos few-shot, supressões e limiares, e a descoberta lê tudo isso de volta.
package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"go.opentelemetry.io/otel/trace"

	"github.com/dev-danilocordeiro/sentinela/internal/agents"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/curator"
	"github.com/dev-danilocordeiro/sentinela/internal/agents/judge"
	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/llm"
	"github.com/dev-danilocordeiro/sentinela/internal/signals"
	"github.com/dev-danilocordeiro/sentinela/internal/store"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// Curation é o que o Curator mudou num repo.
type Curation struct {
	Repo         string                `json:"repo"`
	Outcomes     int                   `json:"outcomes"`
	Examples     []curator.ExampleOut  `json:"examples"`
	Suppressions []curator.Suppression `json:"suppressions"`
	Thresholds   []curator.Threshold   `json:"thresholds"`
	Notes        []string              `json:"notes,omitempty"`
	Skipped      string                `json:"skipped,omitempty"`
}

// Curator roda o Feedback Curator de um repo e aplica o resultado.
type Curator struct {
	Cfg    *config.Config
	Store  *store.Store
	Tracer trace.Tracer
	Now    func() time.Time
}

// Due diz se a curadoria semanal está vencida: a última chamada do curator foi há mais de
// feedback.curator_interval. Devolve também o início da janela de desfechos.
func (c *Curator) Due(ctx context.Context) (bool, time.Time, error) {
	last, err := c.Store.Q.LastCuratorCall(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	since := last.Time
	if since.IsZero() || since.Unix() <= 0 {
		since = c.now().Add(-c.Cfg.Feedback.CuratorInterval)
	}
	return c.now().Sub(last.Time) >= c.Cfg.Feedback.CuratorInterval, since, nil
}

// Repos devolve os repos com desfechos novos desde since.
func (c *Curator) Repos(ctx context.Context, since time.Time) ([]string, error) {
	return c.Store.Q.ReposWithOutcomesSince(ctx, ts(since))
}

// Curate lê os desfechos do repo desde since, chama o Curator e aplica a saída numa transação.
// Sem desfechos novos, não chama modelo nenhum.
func (c *Curator) Curate(ctx context.Context, client llm.Caller, repoName string, since time.Time) (Curation, error) {
	res := Curation{Repo: repoName}
	rows, err := c.Store.Q.OutcomesSince(ctx, db.OutcomesSinceParams{Repo: repoName, OccurredAt: ts(since)})
	if err != nil {
		return res, err
	}
	res.Outcomes = len(rows)
	if len(rows) == 0 {
		res.Skipped = "sem desfechos novos"
		return res, nil
	}
	in := curator.Input{Repo: repoName, DefaultMinConfidence: c.Cfg.Discovery.MinConfidence, MaxExamples: c.Cfg.Feedback.MaxExamples}
	known := map[int64]exampleSource{}
	for _, r := range rows {
		if r.Kind == "duplicate_of" {
			continue // merge humano para outra issue: não é aceite nem recusa do problema
		}
		kind := "accepted"
		if r.Kind == "rejected" || r.Kind == "closed" || r.Kind == "reverted" {
			kind = "rejected"
		}
		in.Outcomes = append(in.Outcomes, curator.Outcome{
			FindingID: r.FindingID, Module: r.Module, Category: r.Category, Severity: r.Severity, Title: r.Title,
			File: mainFile(r.Evidence), Outcome: kind, Reason: r.Reason,
			AnalystConfidence: float64(r.Confidence), JudgeConfidence: float64(r.JudgeConfidence.Float32),
		})
		known[r.FindingID] = exampleSource{module: r.Module, category: r.Category, outcome: kind, title: r.Title}
	}
	current, err := c.Store.Q.ActiveExamples(ctx, db.ActiveExamplesParams{Repo: repoName, Limit: int32(c.Cfg.Feedback.MaxExamples)}) //nolint:gosec // limite pequeno da config
	if err != nil {
		return res, err
	}
	for _, e := range current {
		if !e.FindingID.Valid {
			continue
		}
		in.CurrentExamples = append(in.CurrentExamples, curator.Example{FindingID: e.FindingID.Int64, Outcome: e.Outcome, Title: e.Title, Lesson: e.Lesson})
		if _, ok := known[e.FindingID.Int64]; !ok {
			known[e.FindingID.Int64] = exampleSource{module: e.Module, category: e.Category, outcome: e.Outcome, title: e.Title}
		}
	}
	ths, err := c.Store.Q.ThresholdsByRepo(ctx, repoName)
	if err != nil {
		return res, err
	}
	for _, t := range ths {
		in.CurrentThresholds = append(in.CurrentThresholds, curator.Threshold{Module: t.Module, Category: t.Category, MinConfidence: float64(t.MinConfidence), Reason: t.Reason})
	}

	pv := curator.PromptVersion()
	if err := c.Store.Q.UpsertPromptVersion(ctx, db.UpsertPromptVersionParams{Agent: pv.Agent, Version: pv.Version, Sha256: pv.SHA256}); err != nil {
		return res, err
	}
	models := c.Cfg.ModelsFor(repoName, "")
	req, err := curator.Request(in, curator.Options{Model: models.Curator})
	if err != nil {
		return res, err
	}
	outcomes, err := agents.RunAll(ctx, client, c.Tracer, curator.Name, []agents.Item{{Key: repoName, Request: req, Fallback: models.RefusalFallback}}, c.Cfg.Models.Refusal)
	if err != nil {
		return res, err
	}
	o := outcomes[repoName]
	if o.Skipped {
		res.Skipped = o.Reason
		return res, nil
	}
	out, notes, err := curator.Parse(o.Response, in)
	if err != nil {
		return res, err
	}
	res.Examples, res.Suppressions, res.Thresholds, res.Notes = out.Examples, out.Suppressions, out.Thresholds, notes
	return res, c.apply(ctx, repoName, out, known)
}

type exampleSource struct{ module, category, outcome, title string }

func (c *Curator) apply(ctx context.Context, repoName string, out curator.Output, known map[int64]exampleSource) error {
	expires := pgtype.Timestamptz{Time: c.now().Add(c.Cfg.Feedback.SuppressionTTL), Valid: true}
	return pgx.BeginFunc(ctx, c.Store.Pool, func(tx pgx.Tx) error {
		q := c.Store.Q.WithTx(tx)
		if len(out.Examples) > 0 {
			if err := q.DeactivateExamples(ctx, repoName); err != nil {
				return err
			}
		}
		for _, e := range out.Examples {
			src := known[e.FindingID]
			if err := q.InsertExample(ctx, db.InsertExampleParams{
				Repo: repoName, Module: src.module, Category: src.category, FindingID: pgtype.Int8{Int64: e.FindingID, Valid: true},
				Outcome: src.outcome, Title: src.title, Lesson: e.Lesson,
			}); err != nil {
				return err
			}
		}
		for _, s := range out.Suppressions {
			if _, err := q.InsertSuppression(ctx, db.InsertSuppressionParams{
				Repo: repoName, Module: s.Module, Category: pgtype.Text{String: s.Category, Valid: s.Category != ""},
				Pattern: pgtype.Text{String: s.Pattern, Valid: true}, Reason: "curator: " + s.Reason, ExpiresAt: expires,
			}); err != nil {
				return err
			}
		}
		for _, t := range out.Thresholds {
			if err := q.UpsertThreshold(ctx, db.UpsertThresholdParams{
				Repo: repoName, Module: t.Module, Category: t.Category, MinConfidence: float32(t.MinConfidence), Reason: t.Reason,
			}); err != nil {
				return err
			}
		}
		return nil
	})
}

func (c *Curator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

func ts(t time.Time) pgtype.Timestamptz { return pgtype.Timestamptz{Time: t, Valid: true} }

func mainFile(evidence []byte) string {
	var ev []signals.Evidence
	if err := json.Unmarshal(evidence, &ev); err != nil || len(ev) == 0 {
		return ""
	}
	return ev[0].File
}

// Thresholds resolve o min_confidence de cada achado: o limiar calibrado do módulo mais
// específico (prefixo de caminho mais longo) para a categoria, depois o do repo inteiro
// (module ”), depois o padrão da config.
type Thresholds struct {
	Default float64
	rows    []db.Threshold
}

func LoadThresholds(ctx context.Context, q *db.Queries, repoName string, def float64) (Thresholds, error) {
	rows, err := q.ThresholdsByRepo(ctx, repoName)
	return Thresholds{Default: def, rows: rows}, err
}

// For devolve o limiar e de onde ele veio.
func (t Thresholds) For(module, category string) (float64, string) {
	best, bestLen, from := t.Default, -1, "padrão"
	for _, r := range t.rows {
		if r.Category != category {
			continue
		}
		m := strings.TrimSuffix(r.Module, "/")
		if m != "" && module != m && !strings.HasPrefix(module, m+"/") {
			continue
		}
		if len(m) > bestLen {
			best, bestLen, from = float64(r.MinConfidence), len(m), fmt.Sprintf("calibrado %s/%s", orRepo(m), category)
		}
	}
	return best, from
}

func orRepo(m string) string {
	if m == "" {
		return "(repo)"
	}
	return m
}

// JudgeExamples monta os exemplos do Judge: primeiro os curados (com a lição do Curator),
// depois as decisões humanas mais recentes que ainda não foram curadas.
func JudgeExamples(ctx context.Context, q *db.Queries, repoName string, maxExamples int) ([]judge.Example, error) {
	curated, err := q.ActiveExamples(ctx, db.ActiveExamplesParams{Repo: repoName, Limit: int32(maxExamples)}) //nolint:gosec // limite pequeno da config
	if err != nil {
		return nil, err
	}
	out := make([]judge.Example, 0, maxExamples)
	seen := map[string]bool{}
	for _, e := range curated {
		out = append(out, judge.Example{Title: e.Title, Category: e.Category, Outcome: outcomeLabel(e.Outcome), Reason: e.Lesson})
		seen[e.Title] = true
	}
	if len(out) >= maxExamples {
		return out, nil
	}
	raw, err := q.JudgeExamples(ctx, db.JudgeExamplesParams{Repo: repoName, Limit: int32(maxExamples)}) //nolint:gosec // idem
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	for _, r := range raw {
		if len(out) >= maxExamples || seen[r.Title] {
			continue
		}
		outcome := "accepted"
		if r.State == "rejected" {
			outcome = "rejected"
		}
		out = append(out, judge.Example{Title: r.Title, Category: r.Category, Outcome: outcomeLabel(outcome), Reason: r.Reason})
	}
	return out, nil
}

func outcomeLabel(o string) string {
	if o == "rejected" {
		return "recusado"
	}
	return "aceito"
}
