# Dia a dia

### No Linear (modos completo e MCP)

**Triagem.** As issues nascem no estado inicial do time (`triage_state`, por exemplo Triage), com a label `sentinela`. Cada uma traz:

- o porquê e a evidência (`arquivo:linha`, com link);
- a correção proposta e os critérios de aceite;
- a decisão do juiz, com a confiança;
- quando for o caso, a seção "Também afeta".

O canal do time recebe o aviso, com a menção do grupo.

**Decisões.** Quem decide é sempre uma pessoa; o Sentinela lê e reage:

<!-- guia:include docs/operacao.md#Decisões do time no Linear -->

**Labels de resposta.** Um achado que depende de uma decisão de produto ("isto é intencional?") sai como pergunta, com a label `sentinela-pergunta`.

- **`sentinela-intencional`, com um comentário do porquê:** vira recusa com supressão.
- **`sentinela-nao-intencional`:** a pergunta vira o achado normal, na mesma issue, com a correção e os critérios de aceite. O desfecho fica registrado como aceito, por uma pessoa: quem aplicou a label, quando o tracker sabe dizer.

**Label de gatilho, e quem pode aplicá-la.** `sentinela-fix` (ou a label do seu time, como `pagamentos-fix`) pede a correção. Com `fix_trigger: { allowed: team_members }`:

- só os membros do time do Linear dono do achado disparam a correção;
- se outra pessoa aplicar a label, o Sentinela comenta uma vez e não mexe em nada;
- vale de novo quando alguém autorizado aplica a label;
- `sentinela linear members <time>` lista quem pode.

No modo MCP, o gatilho não é conferido, porque o MCP não mostra quem aplicou a label.

**Instruções na issue.** Comente na issue antes de o worker pegar o job: o comentário entra na spec com prioridade sobre a correção proposta. Com `fix_trigger` restrito, entram só os comentários de quem pode disparar. Os comentários do próprio Sentinela começam com `**Sentinela` e nunca contam como instrução.

**Revisão do PR.** O PR sai em draft, na branch `sentinela/<issue>`, com `Fixes <issue>` e o resumo da spec, do build, dos quatro reviews e da decisão do aprovador.

- **O gate:** a app revisora comenta o que uma pessoa precisa conferir. Ela só aprova se a organização ligou `github_apps.reviewer_approves` e a política do time permite.
- **O PR Shepherd:** CI vermelho ou review pedindo mudança acionam o Shepherd, com até 3 ciclos.
- **O merge:** é sempre de uma pessoa. Ele fecha o achado e move a issue para Done.

### No modo local

Sem Linear, as mesmas decisões entram pela CLI. Todas gravam `decided_by human`, e o relatório em `reports/local` mostra o comando de cada ação possível para cada achado.

```bash
sentinela finding list                                   # achados abertos (--all inclui os decididos)
sentinela finding show 12                                # o detalhe de um
sentinela finding accept 14                              # é real; fica aberto, esperando a correção
sentinela finding reject 13 "o handler já valida antes"  # recusa: vira supressão, não volta
sentinela finding fix 12                                 # pede a correção (como a label de gatilho)
sentinela finding resolve 15 --note "corrigido no commit abc123"   # corrigido à mão
sentinela finding duplicate 16 --of 9                    # mesmo problema: o mais antigo absorve o mais novo
sentinela finding answer 17 not-intentional              # pergunta de produto: não é intencional, vira achado
sentinela finding answer 18 intentional --note "o link é anônimo de propósito"   # é intencional: recusa
sentinela worker --once                                  # roda os pedidos de correção
```

Quando um agente executa a pedido de quem decidiu, use `--executed-by agent --note "..."`: a decisão continua humana.

<!-- guia:include docs/modos.md#Modo local -->
