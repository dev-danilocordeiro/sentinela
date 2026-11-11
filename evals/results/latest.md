# Judge eval: 2026-11-11

- Model: `claude-sonnet-5-5` (prompt `judge@8`), provider `claude_code`
- Decision rule: publish at confidence >= 0.60, 3 vote(s) near the threshold (median)
- Dataset: 40 cases, sha256 `96bf854c839f`
- Cost: 40 calls, 174276 input / 12248 output tokens, $0.2219 (list-price equivalent reported by Claude Code; not charged on a subscription)

| Category | Cases | Correct | Accuracy | Precision (accepted) | Recall (accepted) | Errors |
|---|---|---|---|---|---|---|
| bug | 10 | 10 | 100% | 100% | 100% | 0 |
| performance | 10 | 10 | 100% | 100% | 100% | 0 |
| improvement | 10 | 10 | 100% | 100% | 100% | 0 |
| security | 10 | 10 | 100% | 100% | 100% | 0 |
| **all** | 40 | 40 | 100% | 100% | 100% | 0 |

Confusion matrix (rows: label, columns: Judge):

| | published | not published |
|---|---|---|
| accepted | 20 | 0 |
| rejected | 0 | 20 |
