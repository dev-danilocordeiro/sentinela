# ADR 0032: Identidade própria no Linear (app OAuth com actor=app)

- Status: aceita
- Data: 2026-10-11

## Contexto

O Sentinela falava com o Linear pela chave pessoal do dono (`LINEAR_API_KEY`). Issues,
comentários, labels e mudanças de estado saíam com o nome dele. No histórico, uma ação do
Sentinela não se distinguia de uma ação humana. O prefixo `**Sentinela` nos comentários era o
único jeito de separar os dois, e não resolvia a autoria, as notificações nem o histórico de
labels. O dono criou um app OAuth "Sentinela" no Linear, com callback em
`http://localhost:8787/oauth/linear/callback`.

## Decisão

- **Identidade:** o app OAuth do Sentinela, autorizado com `actor=app`. Fontes, conferidas em
  2026-10-11:
  - https://linear.app/developers/oauth-actor-authorization: "add `actor=app` parameter to your
    OAuth authorization URL"; com ele, "all mutations such as creating issues, comments, and status
    changes will be performed by the app". `actor=application` foi descontinuado.
  - https://linear.app/developers/oauth-2-0-authentication:
    - authorize em `https://linear.app/oauth/authorize`;
    - token em `POST https://api.linear.app/oauth/token`;
    - PKCE `S256`;
    - access token de 24 h e refresh token que gira a cada uso (30 min de tolerância);
    - `client_credentials`, habilitado no app, com token de app de cerca de 30 dias e sem refresh
      token;
    - header `Authorization: Bearer <token>`.
- **Dois grants** (`linear_auth.grant`):
  - `authorization_code` (padrão): `sentinela linear auth` sobe um servidor temporário no
    `redirect_uri` (só localhost), mostra o link com `actor=app`, `state`, PKCE e
    `prompt=consent`, recebe o código e troca pelo token.
  - `client_credentials`: pede o token direto, sem navegador, e pede outro quando vence.
- **Token:** fica em `linear_auth.token_file` (padrão `~/.config/sentinela/linear-token.json`),
  em JSON, com permissão 0600 e diretório 0700, fora do git. A gravação é atômica (arquivo
  temporário e rename).
  - **Renovação:** começa 5 min antes de vencer. O refresh token novo é gravado antes de o access
    token ser usado.
  - **Vários processos (revisão 2026-10-11, C6):** serve, worker e scan têm cada um o token na
    memória. A renovação acontece sob uma trava entre processos (`flock` em
    `<token_file>.lock`). Depois de pegar a trava, o processo relê o arquivo: se outro já renovou,
    usa o token dele. Assim, o Linear vê uma renovação só.
  - **401:** o cliente descarta o token (ele não volta a valer, mesmo que o arquivo ainda o tenha)
    e tenta uma vez de novo.
  - **Segredos:** `LINEAR_OAUTH_CLIENT_ID` e `LINEAR_OAUTH_CLIENT_SECRET`, só no `.env`.
- **Escopos:** `read` e `write`. `write` cobre criar issue, comentar, aplicar label e mudar
  estado. `admin` não é pedido.
- **Fallback** (`linear_auth.fallback_api_key`, padrão false): se o token do app faltar ou não
  renovar, a chave pessoal assume. As ações voltam a sair com o nome da pessoa, e o processo
  avisa. `linear_auth.mode: api_key` força a chave pessoal. Com o modo vazio, vale `app` quando
  `LINEAR_OAUTH_CLIENT_ID` está definida.
- **Autor no lugar do prefixo:** os comentários são lidos com `user { name app isMe }`
  (`User.app` e `User.isMe` existem no schema público do SDK do Linear).
  - Com o app, um comentário é do Sentinela quando o autor é o usuário autenticado (`isMe`).
  - Um comentário de outro app (`user.app`) ou de integração (`botActor`) também não é humano.
  - O prefixo `**Sentinela` continua nos comentários, mas só como texto. Um humano que comece um
    comentário assim, depois da troca, é humano.
  - **Comentários antigos:** os da época da chave pessoal, com o prefixo e anteriores a
    `linear_auth.legacy_prefix_until`, continuam do Sentinela. O padrão desse corte é o
    `authorized_at` gravado no token na primeira autorização (reautorizar não muda a data).
  - **Com a chave pessoal,** o autor é a pessoa, e o prefixo volta a ser o que separa.
- **Conferência:** `config validate` lê o `viewer` e diz com que identidade as ações saem. Token de
  app que não autentica como app é erro, e a chave pessoal gera aviso. `sentinela linear whoami`
  mostra a identidade. `sentinela linear test-comment <ISSUE>` comenta numa issue e mostra o autor
  e como o sync o classifica.

## Consequências

- O histórico do Linear separa Sentinela e humano. Notificações de issue e comentário vêm do app.
- A label `sentinela-fix` continua sendo aplicada por uma pessoa. Com a identidade própria, a
  autoria das instruções da equipe (roadmap) pode filtrar pelo autor.
- Revogar o app ou girar o client secret derruba o Sentinela até `sentinela linear auth` rodar de
  novo, a menos que o fallback esteja ligado.
- `Workspace` não exige mais `LINEAR_API_KEY`: a credencial vem de `linear.Auth`.
