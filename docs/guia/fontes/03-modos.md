# Os três modos lado a lado

Os modos só trocam as pontas: por onde os achados saem, por onde as decisões voltam, quem avisa e quem abre o PR. Essas pontas são os `adapters` do `sentinela.yaml`. O resto (descoberta, juiz, fila, linha de execução, supressões, métricas) é igual.

| | Local | MCP | Completo |
|---|---|---|---|
| `adapters` | `tracker: local`, `notifier: none`, `git_host: gh_cli` | `tracker: linear_mcp`, `notifier: none` ou `slack_mcp`, `git_host: gh_cli` | `tracker: linear_api`, `notifier: slack_api`, `git_host: github_apps` |
| Exige | Docker, `claude` e `gh` logados | O mesmo, mais o MCP do Linear (e o do Slack) conectado no Claude Code | App OAuth do Linear, bot do Slack, duas GitHub Apps, um servidor |
| Entrega | Relatório em `reports/local`, decisões pela CLI, PR em draft pela sua conta | Issues no Linear com a sua conta, decisões no Linear, PR em draft pela sua conta | Issues como o app Sentinela, webhook, aviso e menção por time, PR pela app bot |
| Não faz | Não fala com Linear nem Slack; o gate só comenta | Não confere quem aplicou a label de gatilho; não tem webhook; o gate só comenta | — |

### Diagramas

<!-- guia:tabs diagrama -->
### Local
<!-- guia:diagram modo-local -->
### MCP
<!-- guia:diagram modo-mcp -->
### Completo
<!-- guia:diagram modo-completo -->
<!-- guia:end -->

### O que cada modo suporta

A tabela abaixo vem de `docs/modos.md` e foi conferida no código pelos smoke tests de cada modo. Repare na linha do `fix_trigger`: no modo MCP, o gatilho não é conferido, porque o MCP do Linear não mostra quem aplicou a label.

<!-- guia:include docs/modos.md#O que cada modo suporta -->

### Quanto custa

Há dois custos: o dos agentes (descoberta e correção) e o das leituras do tracker.

| | Local | MCP | Completo |
|---|---|---|---|
| Leituras do tracker | Nenhuma | Uma chamada `claude -p` por ciclo em cada processo (`serve` e `worker`), qualquer que seja o número de issues: cerca de 192 por dia com `poll_interval: 15m` | Chamadas de API do Linear, sem cota de modelo; webhook evita até o polling |
| Avisos | Nenhum | Com `slack_mcp`, uma chamada `claude -p` por aviso | Chamadas de API do Slack |
| Descoberta e correção | Igual nos três: depende de `llm.provider` e dos modelos | Igual | Igual |

- **Por rodada de descoberta:** de 20 a 70 chamadas, conforme o repo. Com `anthropic_api`, uma descoberta completa custou de US$ 0,40 a US$ 1,16 (esperado) nas medições de referência. Rodadas sem commits novos não fazem nenhuma chamada.
- **Por correção:** uma correção pequena em Go custou US$ 0,67 em preço de lista, em 91 s.
- **Onde ver:** `sentinela scan --repo R --estimate-only` estima antes de rodar, e `sentinela status` mostra o custo de 30 dias por repo e por time. Com a assinatura (`claude_code`), o valor é o equivalente em preço de lista, não cobrança.

<!-- guia:include docs/modos.md#Quanto custa o polling no modo MCP -->

As medições completas e como limitar o gasto estão em [custos.md](../../custos.md).
