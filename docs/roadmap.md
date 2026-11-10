# Roadmap

O que falta, em ordem de prioridade: a Fase 5 da especificação e o resto do
necessário para uma equipe ou empresa adotar o Sentinela no dia a dia. Fora os itens marcados como
feitos, nada aqui está implementado.

A recusa que volta com outras palavras (EX-9) foi tratada sem embeddings: antes do Judge, o código
busca os achados existentes no mesmo lugar do código (arquivo, símbolo ou linhas próximas, no
mesmo módulo) e os mostra a ele (ADR 0025). Embeddings semânticos ficaram como item opcional, no
fim desta página.

## Ideias aprovadas da linha de execução (inspiradas no sandcastle)

Lista aprovada pelo dono, inspirada em github.com/mattpocock/sandcastle. Itens feitos:
- item 1, retomar a sessão no retry (ADR 0030);
- item 2, fila consciente de conflitos (ADR 0031): jobs do mesmo repo com arquivos de escopo ou de
  evidência em comum rodam em sequência, e o seguinte só começa depois que o PR do anterior for
  mergeado ou fechado, a partir da branch padrão atualizada. O motivo da espera aparece em
  `executions.wait_reason`, no `job list` e no `job show`;
- item 3, dois timeouts (ADR 0034): `idle_timeout` encerra a sessão parada, e `completion_timeout`
  encerra com aviso o processo que não sai depois do resultado. Uma nova tentativa que recomeça do
  zero guarda o worktree da anterior em vez de apagá-lo.

### Só no roadmap: builder e reviewers em contêiner

Para uso em equipe, rodar o builder e os reviewers num contêiner Docker que monta só o worktree,
sem acesso a `.env`, `secrets/` e `~/.ssh` do host. Não implementar por enquanto.

## Fase 5: Sentry e Datadog

Sinais de produção como novas fontes de achados, pelo mesmo contrato `Finding` e pelo mesmo Judge.

- **Fontes:** implementar a interface `SignalSource` para Sentry e Datadog. O agente de cada fonte
  usa o MCP server oficial da ferramenta para buscar detalhes:
  - no Sentry: stack trace, release, tags e frequência;
  - no Datadog: monitores, traces APM, logs e métricas.

  Se o MCP estiver indisponível, usar a API REST com o mesmo contrato.
- **Repo e módulo:** cada intérprete mapeia o sinal pelas tags de serviço do `sentinela.yaml`
  (`signals.sentry.service_map`, `signals.datadog.service_map`).
- **Correlação:** se um achado de código e um sinal de produção apontam para o mesmo arquivo ou
  função, o Judge mescla os dois e aumenta a severidade.

## Feito: modos de operação e configuração por time

Os três modos (local, MCP e completo; ADRs 0038 e 0039), o projeto por módulo e por categoria
(ADR 0040) e a configuração mantida pelos times via PR em `config.d/teams` (ADR 0041) estão
implementados. Os guias são [modos.md](modos.md), [guia-configuracao.md](guia-configuracao.md) e
[../CONTRIBUTING.md](../CONTRIBUTING.md).

## Outros itens para adoção em equipes e empresas

- **Imagem de contêiner completa para `scan` e `worker`:** a imagem do `Dockerfile` é mínima (só
  o binário) e serve para `migrate` e `serve`. Falta uma com git, os scanners, o Claude Code e o
  plugin ([empresa.md](empresa.md#onde-rodar)).

- **Migração de board:** times diferentes podem compartilhar um time do Linear e se separar por
  projeto, ou ter cada um o seu board (`config.d/teams/<time>.yaml`). Falta mover as issues já
  abertas quando um time troca de board: hoje elas ficam onde foram criadas.
- **Notificações por app do Slack em várias workspaces**, para empresas com mais de um workspace.
- **Auto-merge por categoria:** previsto na especificação, deliberadamente não implementado. Exige
  `github_apps.reviewer_approves` ligado, política por categoria e histórico de reverts por módulo.
- **"Bloqueio confirmado pelo humano" mais preciso:** hoje conta review humana pedindo mudança ou PR
  fechado sem merge; falta cruzar com o conteúdo da review.
- **Rebase do PR Shepherd:** quando a branch padrão anda, o conflito aparece como CI vermelho ou
  review humana.
- **Referências de pacote em outras linguagens no roteamento:** hoje só Go.
- **Exportador de telemetria validado** num coletor OTLP real.
- **Autoria das Instruções da equipe:** hoje entra na spec, com prioridade sobre a correção proposta,
  todo comentário humano da janela (ADR 0026). Num time, um comentário qualquer ("acho que dava pra
  fazer X") sobrescreveria a correção. Para uso em equipe:
  - aceitar só os comentários de quem aplicou a `sentinela-fix` (autor da entrada de histórico que
    adicionou a label) ou de membros do time dono resolvido pelo roteamento do achado;
  - mapear usuário do Linear para time, pelo e-mail em `teams.<time>.members` ou `lead`, ou pelos
    membros do time no Linear;
  - mostrar na spec e no PR os comentários ignorados por autoria, para quem comentou saber por que
    não valeu.

  A identidade própria no Linear (ADR 0032) já separa o Sentinela das pessoas pelo autor, o que
  este item pressupõe.

## Opcional: embeddings semânticos

Não é prioridade. A ADR 0025 trocou esta proposta pela busca de candidatos por localização no
código. Vale retomar se a medição mostrar paráfrases do mesmo problema em **lugares diferentes** do
código chegando ao Judge sem o similar: a localização não pega esse caso, e a similaridade léxica
só o pega quando o vocabulário é parecido.

- **Ideia:** gerar o embedding localmente, sem custo de API (por exemplo, `nomic-embed-text` pelo
  Ollama), com um `Embedder` escolhido em config e `hash` como padrão e fallback se o serviço cair.
  Conferir na documentação atual o endpoint e os prefixos de tarefa do modelo antes de codar.
- **Banco:** o `nomic-embed-text` gera 768 dimensões e a coluna é `vector(1024)`. Completar com
  zeros ou migrar a coluna, guardando o modelo de cada vetor e recalculando os antigos, muda o
  esquema e precisa de decisão do dono.
- **Decisão em faixas:** acima de um limiar alto, duplicado em código; numa faixa intermediária,
  similar para o Judge; abaixo, achado novo. Os limiares seriam calibrados com os achados já
  decididos.
- **Aceite sugerido:** `sentinela rejudge` antes e depois mostra quantas decisões mudam, e a
  rodada continua funcionando sem o serviço de embeddings.
