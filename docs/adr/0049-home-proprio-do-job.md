# 0049: HOME próprio do Claude Code do job

## Contexto

M6 da revisão de 2026-10-11. Desde a C2, o job não recebe `GITHUB_TOKEN` nem outro segredo do
Sentinela. Mesmo assim, ele roda com o usuário do worker e com o HOME dele. Um `git push` de dentro
do job ainda autenticaria:

- pelo credential helper do `gh` no `~/.gitconfig` (o origin dos clones é https);
- pela chave SSH sem passphrase em `~/.ssh`;
- pelo `gh` logado, que guarda o token no chaveiro do sistema.

`execution.disallowed_tools` nega `git push`, mas não `git -C . push` nem um push dentro de um
script. A defesa completa é um usuário de serviço (ou contêiner) e um ruleset na branch padrão.
As duas mudam a máquina e os repos, e ficam fora desta ADR.

## Decisão

Sem usuário separado, o Claude Code do job roda com um HOME do Sentinela
(`execution.home`, padrão `~/.cache/sentinela/job-home`; `off` volta ao HOME do worker):

- **Só o login do Claude Code entra:** `.credentials.json` do `~/.claude` da máquina (ou de
  `CLAUDE_CONFIG_DIR`), com 0600. `settings.json`, hooks, `CLAUDE.md` pessoal, `~/.claude.json`
  e MCP da máquina não entram.
- **Credencial renovada volta:** antes e depois de cada sessão, vale nos dois lados a credencial
  com `expiresAt` maior. No empate com conteúdo diferente, vale a da máquina. Assim, um refresh
  token rotativo renovado no job não derruba o login da máquina.
- **git sem credencial:** sem configuração global (outro HOME e `XDG_CONFIG_HOME` dentro dele) nem
  de sistema (`GIT_CONFIG_NOSYSTEM=1`). Também sem prompt (`GIT_TERMINAL_PROMPT=0`) e sem ssh
  (`GIT_SSH_COMMAND=false`). A identidade do commit é a do worker no worktree, lida antes e passada
  em `GIT_AUTHOR_*` e `GIT_COMMITTER_*`.
- **gh sem login:** a configuração fica no HOME novo, e `DBUS_SESSION_BUS_ADDRESS=disabled:` corta
  o chaveiro. Sem isso, o `gh auth token` achava o token pelo `/run/user/<uid>/bus` mesmo sem
  configuração e sem `XDG_RUNTIME_DIR`. Conferido nesta máquina.
- **Sessões no HOME do job:** retomada (ADR 0030) e log da sessão procuram em
  `<home>/.claude/projects`.
- **Gerenciadores de versão continuam:** o PATH do worker tem os shims do mise, que procuram as
  ferramentas no HOME. Com o HOME novo, `go`, `node`, `java` e `mvn` tentavam se instalar de novo
  (conferido nesta máquina). O job recebe `MISE_DATA_DIR`, `MISE_CONFIG_DIR`, `MISE_CACHE_DIR`,
  `MISE_STATE_DIR` e `ASDF_DATA_DIR`. Vale o valor do worker; sem ele, o padrão na máquina, quando
  o diretório existe. São só ferramentas e versões, sem credencial.
- **Caches** (módulos do Go, npm e afins) ficam no HOME do job e duram entre os jobs.

## Consequências

- Teste `TestJobCannotReachTheMachinesGitAndGHCredentials`: de dentro do job, falham o push por
  ssh e por https e o `git credential fill`. Falham também o `gh auth status` e o `gh auth token`,
  e o `core.sshCommand` da máquina não roda. O commit sai com a identidade do worker.
- `TestRealClaudeRunsInTheJobHome` (com `SENTINELA_REAL_CLAUDE=1`): o Claude Code real faz login
  só com a credencial copiada.
- As sessões das execuções de antes da mudança ficam no `~/.claude` da máquina. Um retry delas
  recomeça do zero, com o motivo gravado (ADR 0030).
- O primeiro job de cada repo baixa de novo os módulos e as dependências para o cache do HOME
  novo.
- **`GOTOOLCHAIN` (item da M10, resolvido junto):** fica no padrão `auto` (2026-10-16). Com `local`, um repo que subisse a diretiva `go` quebraria os scanners e os testes
  do job até alguém atualizar o Go da máquina. O motivo completo está em `docs/seguranca.md`. Item
  fechado.
- **Não fecha:** é o mesmo usuário. Um comando que leia o `.ssh/id_ed25519` do diretório do usuário pelo
  caminho absoluto, ou que defina `GIT_SSH_COMMAND` de novo, ainda alcança a chave. Isso só fecha
  com usuário de serviço ou contêiner, e com o ruleset na branch padrão (`docs/seguranca.md`).
