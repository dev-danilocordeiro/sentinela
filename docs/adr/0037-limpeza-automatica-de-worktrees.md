# ADR 0037: Limpeza automática de worktrees

- Status: aceita
- Data: 2026-10-11

## Contexto

Cada job deixa um git worktree em `execution.worktrees`. Depois da ADR 0034, uma nova tentativa
que recomeça do zero também guarda o worktree anterior em `<worktree>.exec-<id>`. Nada removia
esses diretórios, e o disco crescia a cada job. O dono pediu a regra:

- remover depois do merge ou do fechamento do PR;
- os de falha ficam 7 dias, configurável, e depois saem;
- o `sentinela status` mostra o espaço liberado.

## Decisão

- **Quem limpa:** o worker, a cada ciclo, depois do acompanhamento dos PRs (que grava o merge e o
  fechamento). `sentinela job sweep` faz a mesma limpeza na hora.
- **Unidade:** o caminho. Várias execuções da mesma issue usam o mesmo worktree, então vale a
  execução mais recente de cada caminho:
  - `succeeded` com PR `merged` ou `closed`: sai no ciclo seguinte;
  - `failed` ou `canceled`, terminada há mais de `execution.failed_worktree_retention` (padrão
    168h; negativo nunca remove): sai;
  - qualquer outro caso fica, incluindo PR aberto e sucesso sem PR (escalado ou bloqueado, à
    espera de uma pessoa).
- **Trava:** um achado com execução na fila ou rodando segura todos os seus caminhos, porque o
  retry pode retomar a sessão no worktree (ADR 0030).
- **Remoção:** `git worktree remove --force` no clone dono, seguido de `worktree prune`. Se o
  caminho não é mais um worktree registrado, o diretório é apagado. Caminho fora de
  `execution.worktrees` nunca é tocado e vira aviso. A branch local e o diretório do job
  (`execution.jobs`, com os artefatos e o stream) ficam.
- **Registro:** migration aditiva `00016` com `executions.worktree_removed_at` e
  `worktree_freed_bytes`. Os bytes ficam só na execução mais recente do caminho, para a soma não
  contar o mesmo diretório duas vezes.
- **`sentinela status`:** mostra os worktrees guardados e o disco que ocupam, quantos foram
  removidos, o total liberado e o liberado nos últimos 7 dias.

## Consequências

- Commits de um job que falhou e nunca foram para o GitHub somem depois do prazo. Quem quiser
  guardá-los copia antes, ou aumenta `failed_worktree_retention`.
- Erro numa remoção vira aviso no log do worker, e a próxima limpeza tenta de novo.
