# ADR 0030: O retry retoma a sessão do job que falhou

- Status: aceita
- Data: 2026-10-11

## Contexto

Os jobs EX-13, EX-15 e EX-16 (execuções #8 a #10) falharam no limite de uso da assinatura no
meio da linha. O `job retry` recomeçava do zero: worktree novo a partir da branch padrão, spec,
build e reviews de novo. O trabalho já feito se perdia, e o custo se repetia. A ideia vem do
sandcastle (github.com/mattpocock/sandcastle, MIT), que retoma a sessão do agente em vez de
recomeçar.

## Decisão

- **Registro da sessão:** cada execução grava `session_id` (já existia) e `session_log` (JSONB,
  migration 00014): o JSONL da sessão principal e os dos subagentes. O Claude Code guarda a sessão
  em `<CLAUDE_CONFIG_DIR ou ~/.claude>/projects/<cwd>/<id>.jsonl` e os subagentes em
  `<id>/subagents/agent-*.jsonl`. `<cwd>` é o worktree com cada caractere fora de `[a-zA-Z0-9]`
  trocado por `-`. Conferido no Claude Code 2.1.296.
- **Tipo da falha:** o worker classifica a falha e grava em `executions.artifacts["failure"]`:
  - `usage_limit`: limite de uso da assinatura;
  - `timeout`: `execution.timeout` estourou;
  - `contract`: arquivo do contrato faltando ou com a primeira linha inválida;
  - `other`: o resto.

  Execuções anteriores a esta ADR são classificadas pela mensagem gravada em `executions.error`.
- **Retomada:** uma nova tentativa de uma execução que falhou (`job retry` ou a label aplicada de
  novo, ADR 0029) retoma a sessão quando todas estas condições valem:
  - a falha é `usage_limit`, `timeout` ou `contract`;
  - a cadeia não passou de `execution.max_resumes` retomadas seguidas (padrão 2);
  - a sessão foi gravada e o JSONL principal ainda existe;
  - o worktree e o diretório do job da execução anterior ainda existem;
  - o worktree está como a execução o deixou: no mesmo commit e com as mesmas mudanças não
    commitadas. Ao falhar, o worker grava em `artifacts["worktree"]` o HEAD e um sha256 de
    `git status --porcelain -uall` com `git diff HEAD`, e compara antes de retomar.

  A retomada roda `claude -p "<mensagem>" --resume <id>` (flag conferida em `claude --help`,
  2.1.296), no mesmo worktree, na mesma branch e no mesmo diretório do job, que são os caminhos que
  a sessão conhece. `job.json`, a spec e o plugin não são reescritos. A mensagem traz:
  - o erro, curto;
  - o pedido para continuar de onde parou sem refazer o que está pronto;
  - os comentários da equipe feitos desde o fim da execução anterior, se houver.

  O stream desta execução vai para `stream-<id>.jsonl`, sem sobrescrever o da anterior.
- **Limite de retomadas:** se a execução anterior já é a `max_resumes`-ésima retomada seguida, a
  nova tentativa falha sem chamar o Claude Code (tipo `resume_limit`) e o `resume_note` explica.
  Para seguir, uma pessoa pede outro retry (`job retry` ou a label), que recomeça do zero, porque
  `resume_limit` não é retomável. `max_resumes` negativo desliga a retomada.
- **Sem retomada:** a tentativa recomeça do zero, como antes, quando:
  - a falha não é retomável;
  - a sessão, o worktree ou o diretório do job sumiram;
  - o worktree mudou depois da falha (outro commit, mudanças de fora) ou a execução é anterior ao
    registro do estado do worktree;
  - a execução anterior foi bloqueada (`blocked`). Nesse caso a correção precisa de outra
    instrução, não de continuar a sessão.
- `resume_note` (migration 00014) grava o que aconteceu: `retomou a sessão <id> da execução #N
  (<tipo>)` ou `recomeçou do zero: <motivo>`. O `job show` e o `job retry` mostram a nota. A
  decisão final é do worker, quando o job começa.

## Fora desta ADR

- `--fork-session`: a retomada continua a mesma sessão. Se a nova tentativa também falhar, a
  seguinte retoma de novo.
- Timeout por inatividade e por fim sem saída do processo: ficam para o item 3 (dois timeouts).
