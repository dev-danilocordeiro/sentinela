# O que é o Sentinela

<p class="lead">O Sentinela lê os repositórios da empresa toda semana, acha problemas (bug, performance, segurança, melhoria), abre issues no board do time dono e, quando uma pessoa pede, implementa a correção e abre um PR em draft. Cada decisão humana volta para um Postgres e calibra as rodadas seguintes.</p>

<!-- guia:diagram ciclo -->

1. **Achado.** Os scanners (`golangci-lint`, `gosec`, `govulncheck`, `semgrep`) dão sinais. Os agentes fazem a triagem de cada pacote e a análise dos sinalizados, e um juiz decide o que vale publicar. Cada achado tem evidência `arquivo:linha`, a correção proposta e os critérios de aceite.
2. **Issue.** O achado vira issue no board e no projeto do time dono, decidido pelo roteamento: a config, o CODEOWNERS e o repo. O canal do time recebe o aviso. No modo local, em vez de issue, ele entra num relatório na sua máquina.
3. **Decisão humana.** Alguém do time recusa (cancela com o motivo), pede a correção (aplica a label de gatilho) ou responde a uma pergunta de produto.
4. **Correção.** Num worktree isolado, o Claude Code roda o plugin `sentinela-factory`: spec, builder, quatro revisores em paralelo e um aprovador.
5. **PR em draft.** O Policy Gate comenta o que uma pessoa precisa conferir. Aprovar e mergear é sempre de uma pessoa.
6. **Aprendizado.** Recusas viram supressões e exemplos para o juiz, e merges e reverts entram nas métricas. O Feedback Curator ajusta os limiares toda semana.

> **O Sentinela nunca toma uma decisão humana.** Ele não cancela nem fecha issue, não aplica a label de gatilho, não aprova nem mergeia PR e não revoga supressão. Ele só lê essas decisões e reage a elas. Uma pessoa pode pedir a um agente que execute uma decisão dela; o registro fica `decided_by human`, com `executed_by agent` e uma nota.

O que fica fixo em código, sem modelo, e é testado: os scanners, a publicação no Linear, a fila, a política de aprovação e o roteamento. O modelo entra só onde há julgamento: triagem, análise, juiz, correção e revisão. Toda chamada de modelo fica registrada em `llm_calls`, com tokens, custo e versão do prompt.

**Como ler este guia.** Se você só quer ver se o Sentinela presta, vá para [Escolha o seu cenário](#escolha-o-seu-cenario) e siga o modo local: são 10 minutos e nenhuma chave. Para configurar times, comece em [Configuração](#configuracao) e teste no [simulador de roteamento](#simulador-de-roteamento).
