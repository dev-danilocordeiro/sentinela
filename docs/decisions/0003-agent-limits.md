# 0003: What the agent never does without a human

- Status: accepted
- Detailed records: [ADR 0022](../adr/0022-quem-decide-e-quem-executa.md),
  [ADR 0042](../adr/0042-quem-pode-disparar-a-correcao.md),
  [ADR 0054](../adr/0054-premissa-de-produto-nunca-descarta.md),
  [security notes](../seguranca.md)

## Context

The system opens issues and writes code on its own. That is useful only if the decisions that
carry accountability stay with people. Those decisions are: what gets fixed, what ships, and
what is declared "not a problem". It must also be impossible for a bad model output to reach the
default branch.

## Decision

**Decisions that are always human.** Neither Sentinela nor an assistant working on the project
takes these actions on its own:

- cancel or close an issue;
- apply the `sentinela-fix` label (the trigger for a fix);
- approve or merge a pull request;
- revoke a suppression.

Sentinela reads these decisions (webhook or polling) and reacts to them. An agent may execute a
decision a person made and asked for. That is recorded as `decided_by = human`,
`executed_by = agent`, with a note. A decision made by an agent (`decided_by = agent`) is a
violation: it is excluded from metrics, the Curator and suppressions, and `config validate`
flags it.

**Guardrails in code, not in prompts:**

- Each fix runs in its own git worktree, on a `sentinela/<issue>` branch. It never pushes to the
  default branch.
- The coding session cannot push, call `gh`, or fetch from the web. The worker pushes with the
  bot app's token after the run.
- Pull requests are always opened as drafts.
- The Policy Gate is deterministic code. It approves only when:
  - the Approver agent said `APPROVE`;
  - the team policy allows it (size, files, category, green CI, the module's revert history).
- Teams with `blocked: true`, and diffs that touch blocked paths (payments, billing,
  migrations, auth, CI), are never auto-approved.
- `fix_trigger.allowed` limits who can request a fix for a team's code.
- A finding that depends on a product decision is never discarded by the Judge. It is published
  as a question to the team, because whether the current behavior is intended is the team's call.

## Consequences

- Nothing merges without a person, and every outcome says who decided it.
- Auto-approval is opt-in per team. Even then, it only approves; auto-merge is a separate flag
  per category.
- Some work stays manual by design: triaging the issues, choosing what to fix, and reviewing
  escalations.
