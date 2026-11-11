# ADR 0022: Quem decide e quem executa um desfecho

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## Contexto
O campo `outcomes.performed_by` (`human` | `agent_on_behalf`, migration 00008) misturava duas
perguntas. Ele tirava das métricas a EX-9, que você cancelou pelo Claude Code com um motivo seu e
depois confirmou. Também não tinha como registrar as decisões de EX-5, EX-8 e EX-10: escolhidas
por você no chat e executadas pelo Claude do chat via conector. A decisão era humana; a execução,
não.

## Decisão
- **`outcomes.performed_by` vira duas colunas** (migration 00013, com `down`):
  - `decided_by` (`human` | `agent`): quem tomou a decisão;
  - `executed_by` (`human` | `agent`): quem fez a ação no Linear ou no GitHub.
- **Conversão:** `agent_on_behalf` vira `decided_by human` + `executed_by agent`.
- **Métricas, Curator, supressões e exemplos do Judge usam `decided_by`.** Decisão humana executada
  por agente conta normalmente.
- **`decided_by = agent` viola o princípio 6.** Esses desfechos ficam fora:
  - do Curator (`OutcomesSince`);
  - das supressões ativas (`ActiveSuppressions` ignora a supressão de uma recusa decidida por
    agente);
  - dos exemplos do Judge.
  
  O `config validate` avisa sobre cada um quando há `DATABASE_URL`.
- **O sync grava `decided_by human`, `executed_by human`** para o que lê do Linear. Pela chave
  pessoal, ele não distingue quem clicou. Uma execução por agente é corrigida no banco com a nota
  de quem pediu, até a identidade própria do Sentinela no Linear (docs/roadmap.md).
- **Princípio 6 reescrito** no `CLAUDE.md` e no `SENTINELA_BUILD_PROMPT.md`, separando decidir de
  executar.

## Registros corrigidos
- **EX-9** (outcome 1): `decided_by human`, `executed_by agent`. Volta a contar.
- **EX-10** (3), **EX-8** (4) e **EX-5** (5): `decided_by human`, `executed_by agent`, com a nota
  dizendo que a decisão foi humana, tomada no chat, e executada pelo Claude do chat via conector.
