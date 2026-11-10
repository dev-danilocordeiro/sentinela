# ADR 0026: Instruções da equipe na spec

- Status: aceita
- Data: 2026-10-10

## Contexto

A spec de um achado que veio do Judge com critérios de aceite é montada por código, a partir do
achado gravado (`SpecFromFinding`), e o spec writer é pulado. Até aqui, uma pessoa não tinha como
ajustar a correção antes da primeira rodada. Editar a issue não muda a spec, e o
`sentinela job rework` só existe depois de um PR.

Caso real, a EX-14 ("Valida capacidade positiva ao abrir turma"): a correção proposta pelo achado
pedia "rejeitar capacidade <= 0 (e um teto razoável)". O time decidiu validar só `> 0`, sem teto. O
builder teria implementado um teto que ninguém pediu.

A mesma issue mostrou outro problema. Uma automação antiga (scheduled tasks, hoje pausadas)
comentava pela conta pessoal da pessoa: "🤖 Claude começou…" e "❌ Não consegui abrir o PR…", este
com um diff que usava um teto de 1000. Para a API do Linear, esses comentários são humanos (têm
`user`, sem `botActor`).

## Decisão

- **O que entra:** os comentários humanos da issue viram a seção `## Instruções da equipe` da spec.
  - Humano: tem `user`, não tem `botActor`, não é vazio e não começa com `**Sentinela`.
  - Ficam de fora também os que começam com um prefixo de `execution.ignore_comment_prefixes`.
- **Janela:** do início do job anterior do mesmo achado (ou da criação da issue, no primeiro job)
  até o momento em que o worker monta a spec deste job.
  - O fim é o início do job, não a aplicação da label. Uma pessoa pode aplicar `sentinela-fix` e
    comentar logo depois, como na EX-14.
  - A janela vai escrita na spec.
- **Prioridade:**
  - A seção vem antes dos `## Requisitos` e diz que as instruções prevalecem sobre a correção
    proposta e os requisitos do achado.
  - Entre instruções, vale a mais recente.
  - Contra um critério de aceite, o builder segue a instrução e diz isso no `build.md`.
  - Os prompts do builder e do spec writer reforçam a regra. O spec writer copia a seção quando a
    spec não vem do código.
- **O achado gravado não muda:** a correção proposta continua em `## Requisitos`, marcada como
  "correção proposta pelo achado".
- **Onde aparece:**
  - na spec e no `issue.md`;
  - no `job.json` (`instructions` e `instruction_window`);
  - no corpo do PR (seção "Instruções da equipe", com autor, data e texto).
- **Registro de qual comentário entrou em qual spec:** fica em `executions.artifacts`, chave
  `team-instructions`, sem mudança de esquema.
  - Itens no formato `id do comentário · autor · data · primeira linha`.
  - O veredito `INSTRUCTIONS: n` e a janela no resumo.
  - É gravado também quando nenhum comentário entrou, para mostrar a janela considerada.
- **Leitura dos comentários:** a consulta `SentinelaDetail` passa a trazer `comments(first: 100)`
  com `id`, `body`, `createdAt`, `user` e `botActor`. Os campos foram conferidos por introspecção
  na API do Linear em 2026-10-10.

## Consequências

- Para ajustar a correção antes da primeira rodada, basta comentar na issue antes de o worker pegar
  o job.
- Um comentário de conversa ("vou ver isso amanhã") também entra. O texto da seção diz que são
  comentários, e o builder pondera. Quem quiser evitar isso comenta depois de o job começar.
- Outra automação que comente pela conta de uma pessoa precisa entrar em
  `execution.ignore_comment_prefixes`. A identidade própria no Linear (roadmap) resolve isso pelo
  autor.
- Mais de 100 comentários numa issue: só os 100 primeiros que a API devolve são lidos.

## Verificação

- `internal/executor/instructions_test.go` reproduz a EX-14 com a linha do tempo real (nomes
  neutralizados).
  - Só "Validar apenas capacidade > 0, sem teto superior." entra, antes dos `## Requisitos`. A
    correção proposta com "um teto razoável" continua lá, marcada como do achado.
  - Os comentários da automação antiga ficam fora pela lista de prefixos; sem ela, entrariam.
- `TestTeamCommentsBecomeInstructionsInSpecPRAndRecord` roda o worker com banco, Linear e GitHub
  falsos. A instrução entra na spec, no `issue.md`, no `job.json`, no PR e no registro, e no rework
  só entra o comentário novo.
