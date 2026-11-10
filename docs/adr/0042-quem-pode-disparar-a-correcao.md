# ADR 0042: Quem pode disparar a correção (fix_trigger)

- Status: aceita
- Data: 2026-10-11

## Contexto

Numa empresa com vários times, qualquer pessoa com acesso à issue podia aplicar a label de gatilho
e pedir uma correção no código de outro time. Também podia comentar e ter o comentário tratado
como "Instruções da equipe", com prioridade sobre a correção proposta (ADR 0026).

## Decisão

### A chave

```yaml
fix_trigger:
  allowed: team_members      # anyone | team_members | [team_members, lider@acme.com] | [ana@acme.com]
teams:
  pagamentos:
    fix_trigger: { allowed: [lider.pagamentos@acme.com] }   # sobrescreve para o time
```

- É política da organização: vale no `sentinela.yaml` principal (`fix_trigger` e
  `teams.<time>.fix_trigger`). Num arquivo de `config.d/teams`, o `config validate` recusa.
- **Sem a chave, vale `anyone`, como antes.** O `config validate` avisa e recomenda
  `team_members`. O `init` (local e MCP) e o `config bootstrap` (completo) já geram
  `team_members`.

### O gatilho

- **Quem aplicou:** o worker lê, no histórico da issue, a última aplicação da label de gatilho e
  quem a fez (`IssueHistory.actor` e `addedLabels`, conferidos por introspecção do schema do
  Linear em 2026-10-11).
- **Quem pode:** a pessoa precisa ser membro do time do Linear dono do achado (`team_members`) ou
  estar na lista (por e-mail ou nome).
- **Leitura dos membros:** uma vez por ciclo do gatilho e por time, qualquer que seja o número de
  issues. No modo completo, pela API (`team.members`); no modo MCP, numa chamada `list_users`.
- **Gatilho recusado:**
  - o Sentinela não mexe na label nem na issue, porque tirar a label seria executar uma decisão
    humana;
  - comenta uma vez por aplicação da label, explicando quem pode disparar;
  - grava a recusa em `trigger_refusals` (migration 00017, com down). A entrada do histórico é a
    chave, por isso o comentário não se repete;
  - ignora a issue até alguém autorizado aplicar a label de novo, o que gera uma entrada nova no
    histórico.

### Instruções da equipe

Com `fix_trigger` restrito, só entram os comentários de membros do time dono, dos usuários da
lista e de quem aplicou a label. Os autores ignorados aparecem na seção "Instruções da equipe" da
spec, para quem comentou saber por que o comentário não valeu.

## Limitações

- **Modo MCP:** as ferramentas do MCP do Linear não dão o histórico de labels. O gatilho não é
  conferido (vale como `anyone`), mas as instruções são filtradas pelos membros, comparados pelo
  nome do autor. O `config validate` avisa.
- **Modo local:** não se aplica, porque o gatilho é o `sentinela finding fix`, que já é a pessoa
  na linha de comando.

## Conferência

A instalação real não tem a chave: nada muda, e o `config validate` ganha só o aviso de
`fix_trigger`. `sentinela linear members HAY` confirmou que o dono é membro do time HAY, o único
usado.
