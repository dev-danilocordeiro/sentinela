# 0055: Publicação que não para na primeira falha e retoma depois do limite de uso

## Contexto

Na primeira publicação pelo `linear_mcp` num repo de teste (35 achados pendentes), foram precisas
quatro rodadas:

- o limite de uso da assinatura parou a publicação depois de 4 issues;
- duas vezes o `claude -p` começou sem as ferramentas do conector do Linear carregadas ("a
  ferramenta mcp__claude_ai_Linear__save_issue não está disponível nesta sessão"). Na mão, com o
  mesmo ambiente e os mesmos argumentos, a chamada funcionou em 4 de 4 tentativas;
- cada falha, de uma chamada só, interrompia a publicação inteira (`return res, err` no
  `publishPending`);
- o `scan` saía com código 0 e não mostrava o erro no terminal: ele só aparecia no HTML da rodada.

## Decisão

- **`mcpcall`:** quando a resposta cita uma ferramenta do servidor (`mcp__<servidor>__…`) e diz que
  ela não está disponível ou carregada (`ToolsMissing`), a chamada espera e tenta de novo
  (`DefaultRetryWaits`: 10 s e 30 s). Esgotadas as tentativas, devolve `ErrToolsMissing`. Cada
  tentativa vai para `llm_calls`. Um erro da própria ferramenta não cita o prefixo e não repete.
- **Publisher:**
  - uma issue que falha vira aviso (`fica pendente para a próxima rodada`) e conta em
    `PublishResult.Failed`; a publicação segue com as outras e termina com `ErrPublishFailed`;
  - o limite de uso para na hora: `PublishResult.PausedUntil` recebe o horário do reset
    (`llm.UsageResetAt`, ou uma hora sem horário na mensagem) e o erro é `ErrUsagePaused`.
- **Onde a pausa fica:** em `analysis_runs.stats.issues.paused_until`, sem coluna nova. A rodada de
  um repo grava pelo `finish`. O `scan --all` publica depois de fechar as rodadas e grava a pausa
  nelas com `SetRunPublishPause`.
- **Retomada:** o `serve`, a cada ciclo (e de hora em hora no modo webhook), lista os repos cuja
  última rodada de análise tem pausa vencida (`PausedPublications`) e publica os pendentes com
  `Runner.ResumePublish`. É uma rodada `no_changes`, com o head da última rodada e o CODEOWNERS do
  clone local: não sincroniza nem analisa o repo e não chama modelos. Se a cota acabar de novo, a
  pausa nova fica nela.
- **Terminal:** o `scan` imprime o erro, quantos achados do repo continuam sem issue e como
  retomar, e sai com código diferente de 0, inclusive na rodada sem mudanças e no `scan --all`.

## Consequências

- **O limite por time continua por rodada.** O `serve` só retoma o que uma pausa interrompeu; não
  publica os adiados pelo limite.
- **Testes:**
  - `TestCLIRetriesWhenTheConnectorToolsAreMissing` e `TestToolsMissingRecognizesTheMessagesSeen`;
  - `TestPublishFailureOfOneIssueDoesNotStopTheOthers` e
    `TestPublishStopsAtTheUsageLimitAndKeepsTheResetTime`;
  - `TestPublicationPausedByTheUsageLimitResumesAfterTheReset` e
    `TestSetRunPublishPauseMarksAFinishedRun`;
  - `TestPublishOutcomeShowsTheErrorAndThePending`.
