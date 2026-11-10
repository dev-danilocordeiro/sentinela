# ADR 0044: Achado que cruza módulos de times diferentes

- Status: aceita
- Data: 2026-10-11

## Contexto

Um achado com a evidência principal em `modules/cobranca` (pagamentos) e outra evidência em
`modules/contas` (identidade) tinha um dono só, como manda a precedência do roteamento, e o outro
time não ficava sabendo. Uma issue para cada time duplicaria o trabalho e a decisão.

## Decisão

- **O dono continua um só,** pela precedência atual (ADR 0017): o time do arquivo da evidência
  principal, ou o caminho mais específico quando a regra é genérica.
- **Times secundários:** na publicação, cada evidência é roteada. Os times diferentes do dono são
  os secundários (`repo.SecondaryTeams`).
- **"Também afeta":** a issue do dono ganha a seção, com cada time secundário, o módulo (o glob da
  config ou o padrão do CODEOWNERS) e os caminhos (`arquivo:linha`).
- **Aviso no Slack:** o aviso da issue, no canal do dono, menciona o grupo de cada time
  secundário (`notify.mention`). Nenhuma issue é criada para eles, e o canal deles não recebe
  aviso separado.
- **Issues relacionadas:** os módulos que o System Architect cita em `related_modules` continuam
  gerando issue relacionada no board do outro time (comportamento de antes). Um time que já tem
  issue relacionada não entra em "Também afeta".
- **`owners explain <repo> <caminho> [<caminho>...]`:** o primeiro caminho decide o dono; os
  outros mostram os secundários, com módulo, caminhos e menção.

## Teste

O cenário acme cobre um achado entre `modules/cobranca` (pagamentos) e `modules/contas`
(identidade), com a publicação pelo MCP falso e o `owners explain`. Ele confere:

- uma issue só, no time pagamentos;
- a seção "Também afeta" com identidade;
- a menção `<!subteam^S0ACMEIDN01>` no aviso do canal de pagamentos;
- nada no canal de identidade.
