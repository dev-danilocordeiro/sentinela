# 0002: Postgres (with pgvector) as the feedback memory

- Status: accepted
- Detailed records: [ADR 0005](../adr/0005-banco-e-migrations.md),
  [ADR 0011](../adr/0011-dedup-embedding-e-finding.md),
  [ADR 0015](../adr/0015-linear-feedback-e-slack.md),
  [ADR 0022](../adr/0022-quem-decide-e-quem-executa.md)

## Context

A finding generator that forgets is worse than none. It reopens issues that a team closed, repeats
the same false positive every week, and cannot tell which modules it gets wrong. Human decisions
(an issue canceled, a PR merged or reverted) must change the next run.

## Decision

One Postgres 16 database with pgvector holds both the work and its feedback. Migrations are goose
SQL embedded in the binary, every one with a `down`.

- **`findings`:**
  - every published finding with its state (`open | rejected | executing | fixed | reverted |
    stale`), its routing, and an embedding (`vector(1024)`, HNSW cosine index);
  - before the Judge runs, a new finding is compared by fingerprint and by similarity against
    what is already there;
  - the Judge's own discards and merges are kept per run, so the same idea is not re-judged from
    scratch.
- **`outcomes`:**
  - what humans decided (accepted, rejected, merged, reverted), read from Linear and GitHub
    webhooks or polling;
  - `decided_by` and `executed_by` are stored separately;
  - only human decisions feed the loop.
- **`suppressions`:**
  - a rejected finding suppresses its fingerprint at once;
  - the weekly Feedback Curator adds path-glob suppressions that expire after
    `feedback.suppression_ttl`.
- **Few-shot examples and thresholds:**
  - the Curator turns the week's outcomes into examples (with a short lesson) and a calibrated
    `min_confidence` per module and category, clamped to [0.5, 0.95];
  - the Judge receives these examples, and the most specific threshold replaces the default.
- **Accounting and queue:**
  - `llm_calls` and `prompt_versions` record every model call;
  - `executions` is the fix queue (`FOR UPDATE SKIP LOCKED`);
  - `webhook_events` deduplicates deliveries by `event_id`.

## Consequences

- One dependency covers storage, vector search, the job queue and audit, without a separate
  vector store or broker.
- The loop is inspectable with SQL: every suppression, example and threshold points back to the
  outcome that created it.
- Feedback is per install. Two installs do not share what they learned.
- Schema changes after the discovery phase need explicit review, because the data is the memory.
