# ADR 0039: Modo MCP (Linear e Slack pelos MCPs do Claude Code do usuário)

- Status: aceita (etapa 2 dos modos de operação; a etapa 1 é a ADR 0038)
- Data: 2026-10-11

## Contexto

Entre o modo local, só com a CLI, e o completo, com o app OAuth, o bot do Slack e as GitHub Apps,
faltava um meio-termo. O objetivo é pôr o time para decidir no Linear sem criar nenhuma app. Quem
usa o Claude Code em geral já tem o MCP do Linear (e o do Slack) conectado.

## Decisão

### Transporte: `internal/mcpcall`

- Cada operação é uma chamada `claude -p`. A combinação de flags abaixo foi conferida em chamadas
  reais em 2026-10-11:
  - **`--tools ToolSearch`:** os conectores do claude.ai entram sob demanda pela ToolSearch. Com
    `--tools ""` o MCP some, e a resposta vinha vazia.
  - **`--allowedTools mcp__<servidor>`:** dá permissão só às ferramentas daquele servidor. No modo
    não interativo, as de outros servidores são negadas.
  - **`--json-schema`:** saída estruturada.
  - **Sem `ANTHROPIC_*`:** a chamada usa a assinatura.
- **O prompt leva os argumentos exatos de cada ferramenta, em JSON:** o modelo só executa e
  devolve no schema. O modelo é o mais barato de `pricing.models` (`adapters.mcp.model`).
- **Registro:** cada chamada vai para `llm_calls` com o agente `linear_mcp` ou `slack_mcp`. Uma
  falha (servidor fora, ferramenta negada, resposta fora do schema) vira `ErrMCP`, com o motivo e
  as ferramentas negadas.

### `adapters.tracker: linear_mcp`

- **Interface:** `linear.Tracker` passa a ser a interface usada pelo Publisher, pelo Syncer e pelo
  Worker. `Client` (a API) e `MCPTracker` a implementam, e o fluxo de publicação, sync e execução
  não muda.
- **Como o `MCPTracker` economiza chamadas:**
  - **Nomes no lugar de IDs:** o `save_issue` do MCP aceita time, projeto, estado, labels e
    responsável pelo nome. `ProjectID` e `UserIDByEmail` não chamam nada. `TeamRefs` faz uma
    chamada por time e por processo, que lê os estados e as labels; `LabelIDs` confere nelas.
  - **Estado guardado:** as issues lidas ficam guardadas por `adapters.mcp.poll_interval` (padrão
    15m). O gatilho do worker, que roda a cada minuto, e o sync usam esse estado e só voltam ao
    Linear quando ele vence. Uma escrita do Sentinela na issue invalida o que estava guardado. O
    `serve` usa o mesmo intervalo como período de polling, e webhook não existe nesse modo
    (o `config validate` recusa).
  - **Publicação idempotente:** a busca do MCP é aproximada e trunca descrições. Por isso a busca
    vai só pelo hash do marcador e os candidatos são conferidos com `get_issue`. Não achar nada
    devolve `found=false`, e não erro.
- **Identidade:**
  - As ações saem como a pessoa dona do MCP.
  - Todo comentário do Sentinela começa com `**Sentinela`: o `Comment` acrescenta o prefixo se
    faltar.
  - Comentários com o prefixo são do Sentinela (`BySentinela`) e não entram como instrução da
    equipe nem como motivo de recusa.
  - O `config validate` avisa que, para uso real, o certo é o app OAuth (ADR 0032).

### `adapters.notifier: slack_mcp`

`notify.SlackMCP` posta com `slack_send_message` pelo MCP do Slack, como a pessoa. É uma chamada
por aviso, e o texto começa com "Sentinela". Os canais não são conferidos pelo `config validate`.

### `sentinela init --mode mcp`

- Faz o mesmo que o modo local.
- Confere em `claude mcp list` se o MCP do Linear está conectado (e o do Slack, com
  `--slack-channel`).
- Descobre o time do Linear pelo MCP quando há um só; com mais de um, pede `--linear-team`.
- Gera o `sentinela.yaml` com `tracker: linear_mcp` e `publish.enabled: true`.

## Teste de ponta a ponta

O teste rodou em 2026-10-11, no banco `sentinela_eval`, com um projeto de teste no Linear. Este
foi o fluxo:

1. **`init --mode mcp`:** achou o MCP e o time HAY.
2. **`config validate`:** leu o workspace pelo MCP.
3. **`scan`:** publicou o F-1 como EX-20, com a descrição intacta, a label e a autoria da pessoa.
4. **`linear test-comment`:** o comentário saiu como a pessoa e foi reconhecido pelo prefixo.
5. **`serve --once`:** leu a issue pelo MCP.

O que o teste mostrou:

- **A primeira publicação falhou:** a busca pelo marcador inteiro trouxe 10 issues sem relação, e
  o modelo respondeu com erro em vez de `found=false`. O motivo e a correção estão na seção acima.
- **O modelo do `init --mode mcp` não ligava `publish.enabled`:** sem isso, o modo não publica.
  Corrigido.
- **Custo:** média de 9,6 s por chamada.

A decisão humana pelo Linear (cancelar, aplicar `sentinela-fix`) não foi feita no Linear real,
porque é decisão humana. Ela fica coberta pelo teste com o MCP falso, que roda o Publisher e o
Syncer reais pelo `MCPTracker`.

O `slack_mcp` não foi testado no Slack real. O bot não tem `channels:write` para criar e arquivar
um canal de teste, e o MCP do Slack não arquiva nem apaga canal. Sem conseguir apagar o canal no
fim, não criei nenhum.

## Ajustes depois da revisão (2026-10-11)

- **Mudanças de estado do próprio Sentinela:** o sync não depende do autor da mudança. Ele só
  transforma o estado de uma issue em decisão quando o achado ainda está pendente.
  - O Sentinela move para revisão, um estado do tipo `started` que o sync nunca trata como
    decisão.
  - O Sentinela só move para concluído depois de gravar o desfecho do merge e fechar o achado como
    `fixed`.

  Por isso não foi preciso gravar as escritas do Sentinela. Um teste no Linear em memória confere
  os três casos: Done feito pelo Sentinela não vira decisão, e Done ou Canceled feitos pela pessoa
  viram.
- **Risco que a revisão revelou:** com o estado guardado por 15 minutos, o worker podia mover para
  revisão uma issue que a pessoa acabou de cancelar. Agora ele lê o estado na hora antes de mudar
  (`IssuesFresh`).
- **Polling:** antes, cada ciclo era uma chamada com um `get_issue` por issue aberta. Agora é uma
  chamada que lista, por time, as issues com a label de achado atualizadas desde a listagem
  anterior; as que nenhuma listagem traz saem numa leitura só.
  - Cada chamada grava a operação em `llm_calls.custom_id` (`poll`, `get`, `closure`...).
  - O `sentinela status` mostra as chamadas por ciclo e por dia.
  - A estimativa para 10, 50 e 200 issues está em [modos.md](../modos.md).
- **Projeto:** o resolvedor já aplicava time > repo > `defaults.linear.project`, mas o roteamento
  fica gravado no achado. Um achado gravado sem projeto (como o F-1, criado no modo local) não
  pegava o de defaults. Agora a publicação usa `defaults.linear.project` como último recurso.
  - O `owners explain` e o `config validate` mostram o projeto efetivo e a fonte.
  - O `owners pending` mostra, só lendo, o projeto de cada achado pendente.
  - Os 13 achados pendentes da instalação real não mudam de projeto.

## Consequências

- Os três modos usam o mesmo fluxo e o mesmo Postgres. A migração entre eles é trocar `adapters`
  ([modos.md](../modos.md)).
- **Cada leitura gasta cota da assinatura.** Com 15 minutos, são cerca de 200 chamadas por dia
  entre o `serve` e o `worker`, quando há issues abertas.
- **As decisões chegam com até um intervalo de atraso.**
- **O MCP é uma dependência externa sem contrato estável:** as ferramentas e os campos podem
  mudar. Os prompts nomeiam as ferramentas e os campos atuais, e uma mudança aparece como `ErrMCP`
  com o motivo, sem gravar nada errado.
