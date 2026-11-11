# Judge evals

The Judge is the step that decides whether a finding becomes an issue on a team's board. This
directory measures how often its decision matches a reviewer's.

## What is measured

- **Input:** each case in `dataset/findings.jsonl` is a finding exactly as the Judge receives it
  (title, context, evidence with code snippets, proposed fix, acceptance criteria).
- **Label:** `accepted` means a reviewer would keep the issue. `rejected` means they would close it.
- **Prediction:** the runner applies the same rule as discovery (`discovery.Decide`):
  - publish at or above `min_confidence`;
  - three votes near the threshold, median wins;
  - a finding that depends on a product decision is published as a question.
  - Published counts as `accepted`; anything else counts as `rejected`.
- **Report:** accuracy per category, precision and recall of `accepted`, a confusion matrix, and
  every disagreement with the Judge's reason. Errors (refusal, invalid output) count as wrong.

## The dataset

- 40 cases: 10 per category (bug, performance, improvement, security), 5 accepted and 5 rejected
  in each.
- The findings and code snippets were written for this dataset, in fictional repos
  (`example/shop-api`, `example/shop-web`, `example/billing-service`). They are not production
  data.
- **Labels were assigned by hand. They are not decisions recorded from real teams.**
- The `rationale` field says why each label was chosen.
- Rejected cases are the kinds of findings reviewers close in practice:
  - false positives that the snippet itself refutes (the lock is already there, the query is
    already parameterized);
  - micro-optimizations on cold paths;
  - style-only changes;
  - suggestions that cannot apply (a JPA entity as a Java record).
- Findings that depend on a product decision are left out of the rejected class. The pipeline
  always publishes them as a question to the team, by design (ADR 0054), so they would measure
  the rule, not the Judge.

What this does not measure:

- Similar findings and few-shot examples from past decisions. Production runs pass both to the
  Judge, which matters for duplicates and merges. Every case here is judged in isolation.
- The Analyst's ability to find problems. Only the gate after it is measured.

## Running

```bash
make eval          # live: provider from evals/config.yaml (claude_code by default)
make eval EVAL_FLAGS="--provider anthropic_api"   # Batch API, needs ANTHROPIC_API_KEY
make eval-replay   # no model calls: replays recordings/ and checks results/latest.json
```

- A live run writes `results/latest.json` and `results/latest.md`, plus one recording per model
  response in `recordings/`.
- The replay answers from those recordings and fails if the prompt, the input schema or the
  dataset changed since the live run, or if the scoring no longer reproduces the recorded
  predictions.
- CI runs the replay on every push. The `eval-live` workflow runs a live eval by hand when the
  repository has an `ANTHROPIC_API_KEY` secret.
- `config.yaml` carries the Judge settings of the default quality profile (Sonnet, threshold
  0.6, three votes near the threshold), so the eval scores the shipped defaults.
