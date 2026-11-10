# ADR 0034: Timeouts de inatividade e de conclusão; worktree preservado

- Status: aceita
- Data: 2026-10-11

## Contexto

O worker só tinha `execution.timeout` (60 min por job). Uma sessão do Claude Code travada esperava
a hora inteira. Um processo que já tinha emitido o `result`, mas não saía (um filho segurando o
stdout, por exemplo), também, e o job ainda falhava por timeout com o trabalho pronto. Além disso,
uma nova tentativa que recomeçava do zero rodava `git worktree remove --force` no worktree da
anterior e apagava commits e mudanças que nunca foram para o GitHub. Item 3 da lista do sandcastle
(github.com/mattpocock/sandcastle, MIT), aprovada pelo dono.

## Decisão

- **`execution.idle_timeout`** (padrão 15m; negativo desliga): um watchdog no `ClaudeRunner`
  conta o tempo desde a última linha do stream-json. Passou do limite sem saída, o grupo de
  processos é encerrado e o job falha com o tipo `idle_timeout` (`ErrIdleTimeout`).
  - É retomável no retry, como `timeout` (ADR 0030). A mensagem da retomada avisa para não repetir
    do mesmo jeito um comando que travou.
  - O padrão de 15 min cobre um comando de teste longo do builder, que não gera linha no stream
    enquanto roda.
- **`execution.completion_timeout`** (padrão 2m; negativo desliga): depois do evento `result`, ou
  do fim do stdout, o processo tem esse tempo para sair. Se não sair, é encerrado:
  - o resultado vale e o job segue normalmente (contratos, push, PR);
  - os commits ficam no worktree;
  - o aviso vai para o log do worker e para o comentário da execução na issue.
- **Encerramento de verdade:**
  - o Claude Code roda num grupo de processos próprio (`Setpgid` em unix), e timeout, idle e
    completion matam o grupo inteiro;
  - o encerramento também fecha a ponta de leitura do stdout, o que destrava a leitura quando um
    neto fora do grupo herdou o pipe;
  - `cmd.WaitDelay` (10 s) limita a espera pelo stderr.
- **O PR Shepherd** usa os mesmos limites.
- **Worktree preservado:** quando uma nova tentativa recomeça do zero e encontra o worktree da
  anterior no caminho do job (`<worktrees>/<repo>/<ISSUE>`):
  - o HEAD é destacado no mesmo commit, e as mudanças não commitadas ficam;
  - o diretório vai para `<caminho>.exec-<id da execução dona>` (`git worktree move`);
  - o caminho novo é gravado em `executions.worktree` da execução dona.

  Nada é apagado. Em qualquer falha, o worktree continua onde estava, e o caminho dele já está
  em `executions.worktree`.

## Consequências

- Uma sessão travada falha em 15 min, não em 60, e pode ser retomada.
- Um processo que não sai depois de terminar não derruba um job que deu certo.
- Os worktrees guardados ocupam disco até alguém removê-los (`git worktree remove <caminho>`, ou
  apagar o diretório e rodar `git worktree prune` no clone). Uma limpeza automática por idade fica
  para depois.
