# 0056: Segunda instalação na mesma máquina: units com prefixo e diretórios por instalação

## Contexto

Num teste com uma segunda instalação ao lado da principal, cada uma com o próprio diretório,
banco e projeto do compose (ADR 0052), apareceram mais dois pontos divididos:

- **Units:** o `deploy/install.sh --user` gera sempre `sentinela-serve`, `sentinela-worker` e
  `sentinela-scan`. Na segunda instalação, ele sobrescreveria as units da primeira, que passariam
  a rodar o código e o banco da outra. A saída foi subir o serve e o worker à mão, com
  `systemd-run --user` e outro nome.
- **Diretórios:** `execution.worktrees`, `jobs` e `home` tinham padrão global em
  `~/.cache/sentinela`. As duas instalações dividiriam worktrees (o mesmo `<repo>/<issue>` em dois
  bancos), artefatos (`jobs/<id>`, com ids que se repetem entre bancos) e o HOME do job. A limpeza
  de uma removeria arquivos da outra.

## Decisão

- **`deploy/install.sh --prefix NOME`** (ou `SENTINELA_UNIT_PREFIX`), com padrão `sentinela`:
  - as units ficam `NOME-serve.service`, `NOME-worker.service`, `NOME-scan.service` e
    `NOME-scan.timer`, e o `SyslogIdentifier` acompanha;
  - o prefixo aceita minúsculas, dígitos e `-`;
  - antes de gravar ou remover, o script lê o `Environment=SENTINELA_HOME=` da unit que já existe
    com o mesmo nome. Se for outro diretório, para sem mexer em nada e pede outro `--prefix`. A
    própria instalação regrava as units dela normalmente.
- **`sentinela status`:** acha as units desta instalação entre as `*-worker.service` carregadas,
  pelo `SENTINELA_HOME`, qualquer que seja o prefixo (inclusive units transitórias de
  `systemd-run`). As de outra instalação aparecem como tal, com o prefixo.
- **Diretórios por instalação:**
  - sem `worktrees`, `jobs` nem `home` no arquivo, os três ficam em
    `~/.cache/sentinela/<chave>/{worktrees,jobs,job-home}`;
  - a chave são os 8 primeiros hex do sha256 do diretório da instalação, o mesmo esquema do
    `COMPOSE_PROJECT_NAME` (`config.InstallKey`; o `init` passa a usar a mesma função);
  - ficam fora de qualquer projeto, como antes: o Claude Code lê o CLAUDE.md dos diretórios pais.
- **Compatibilidade:** numa instalação cujo banco já tem execuções com worktree em
  `~/.cache/sentinela/worktrees/`, cada um dos três que não está no arquivo continua no diretório
  compartilhado de antes, com uma linha no log do worker. O que está no arquivo vale como está. A
  decisão é por diretório porque a instalação principal fixava `worktrees` e `jobs`, mas não
  `home`, e o HOME do job guarda as sessões que a retomada procura. Assim, jobs em andamento, sessões
  retomáveis e a limpeza dos worktrees continuam achando o que é deles. A escolha é refeita a
  cada releitura da config. Para migrar, basta fixar os diretórios novos no `sentinela.yaml`
  depois de a fila esvaziar.

## Consequências

- **Testes:**
  - `TestInstallPrefixAndRefusalToOverwriteAnotherInstallation`;
  - `TestStatusFindsPrefixedUnitsOfThisInstallation`;
  - `TestExecutionDirsDefaultToADirectoryOfThisInstallation`;
  - `TestLegacyExecutionDirsKeepAnInstallationThatAlreadyUsesThem`.
- **`llm.claude_code.work_dir`** (`~/.cache/sentinela/claude-cwd`) continua compartilhado. É só
  um diretório vazio de trabalho para o `claude -p`, sem estado.
- **Sem `shellcheck`:** a máquina do teste não tem o `shellcheck`; o script é coberto pelos
  testes que o executam com `--render`.
