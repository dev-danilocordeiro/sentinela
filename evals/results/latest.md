# Judge eval: 2026-11-11

- Model: `claude-sonnet-5-5` (prompt `judge@8`), provider `claude_code`
- Decision rule: publish at confidence >= 0.60, 3 vote(s) near the threshold (median)
- Dataset: 48 cases, sha256 `949e3d83f548`
- Cost: 50 calls, 218338 input / 15607 output tokens, $0.2076 (list-price equivalent reported by Claude Code; not charged on a subscription)

| Category | Cases | Correct | Accuracy | Precision (accepted) | Recall (accepted) | Errors |
|---|---|---|---|---|---|---|
| bug | 12 | 12 | 100% | 100% | 100% | 0 |
| performance | 12 | 12 | 100% | 100% | 100% | 0 |
| improvement | 12 | 12 | 100% | 100% | 100% | 0 |
| security | 12 | 12 | 100% | 100% | 100% | 0 |
| **all** | 48 | 48 | 100% | 100% | 100% | 0 |

Confusion matrix (rows: label, columns: Judge):

| | published | not published |
|---|---|---|
| accepted | 24 | 0 |
| rejected | 0 | 24 |
