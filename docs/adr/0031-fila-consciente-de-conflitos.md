# ADR 0031: Fila consciente de conflitos

- Status: aceita
- Data: 2026-10-11

## Contexto

Com `execution.concurrency` acima de 1, o worker pegava qualquer job da fila (`FOR UPDATE SKIP
LOCKED`). Dois achados no mesmo arquivo viravam dois PRs que partiam da mesma base e se
conflitavam no merge. O segundo, além disso, era corrigido sem ver a correção do primeiro. O
mesmo acontecia com concorrência 1: o segundo job começava enquanto o PR do primeiro ainda estava
aberto. A ideia vem do sandcastle (github.com/mattpocock/sandcastle, MIT). O desenho foi aprovado,
incluindo a coluna nova `wait_reason`.

## Decisão

- **Caminhos de um job:** `scope_paths` do achado mais os arquivos da evidência. Um caminho é
  arquivo, diretório (vale como prefixo) ou glob. Achado sem nenhum caminho pode tocar o repo
  inteiro, então conflita com todos os jobs do repo.
- **Quem segura caminhos**, no mesmo repo e de outro achado:
  - job `running`;
  - job com PR aberto (`pr_state = open`), até o PR ser mergeado ou fechado. O acompanhamento de
    PRs (`FollowUp`, webhook do GitHub) já grava essa mudança.
- **Ordem na fila:** um job na fila também espera pelos jobs que estão à frente dele e têm caminho
  em comum, mesmo os que ainda esperam alguma coisa. Assim a ordem de chegada se mantém entre
  jobs que mexem nos mesmos arquivos. A fila é ordenada por `run_after, id`, com uma exceção: o
  job cujo achado já tem PR aberto (rework ou nova tentativa na mesma branch) vai para a frente.
  Sem a exceção, um rework ficaria esperando um job que espera o PR desse mesmo rework.
- **Sem sobreposição**, os jobs rodam em paralelo até `execution.concurrency`, cada um no seu
  worktree.
- **Atomicidade:** o planejamento e o claim rodam numa transação com
  `pg_advisory_xact_lock`. Dois workers, sejam goroutines ou processos, não escolhem ao mesmo
  tempo dois jobs que se cruzam. O claim continua com `FOR UPDATE SKIP LOCKED`.
- **Motivo visível:** `executions.wait_reason` (migration 00015). Exemplo: `aguarda job 12
  (EX-17), PR #4 aberto: caminho em comum src/.../service`. O motivo aparece:
  - no `job list` (última coluna) e no `job show`;
  - no ciclo do worker (`aguardando: ...`).

  O claim limpa o motivo.
- **Base atualizada:** o job que esperava começa como qualquer outro. O `prepare` sincroniza o
  clone e cria o worktree a partir de `origin/<branch padrão>`, que já tem o merge do anterior.
  Quando o PR anterior é fechado sem merge, a base é a mesma de antes.

## Consequências

- Um PR esquecido aberto segura os jobs que se cruzam com ele, sem prazo. O motivo fica visível
  e quem decide é o humano: mergear, fechar ou cancelar o job que espera.
- O conflito é estimado pelos caminhos do achado, não pelo diff real do PR. Um builder que sai do
  `scope_paths` pode conflitar sem que a fila perceba. Nesse caso o PR também cai no gate
  ("arquivo fora de scope_paths").
- `ClaimExecution` deu lugar a `QueueSnapshot`, `ClaimExecutionByID`, `SetWaitReason` e
  `LockQueue`. A regra fica numa função pura (`executor.PlanQueue`), coberta por testes de tabela.
