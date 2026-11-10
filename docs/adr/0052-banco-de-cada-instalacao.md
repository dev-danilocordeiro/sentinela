# 0052: Cada instalação usa o próprio banco

## Contexto

D-06 da revisão de prontidão. O `init` conectava sempre em `localhost:5432`, mesmo com
`SENTINELA_PG_PORT`. O docker compose usava o nome do diretório como projeto. Um clone novo numa
pasta `sentinela/`, na mesma máquina de outra instalação, caía no mesmo projeto do compose (mesmo
contêiner e volume) e no mesmo banco. O `init` aplicaria migrations nele, e o primeiro scan
gravaria no histórico da outra instalação.

## Decisão

- **URL do banco:** o `init` monta a URL com `SENTINELA_PG_PORT` (padrão 5432), a mesma porta
  que o `docker-compose.yml` publica.
- **Projeto do compose:**
  - num `.env` novo, o `init` grava `COMPOSE_PROJECT_NAME=sentinela-<8 hex do sha256 do
    diretório>`, `SENTINELA_PG_PORT` e `DATABASE_URL`, antes de subir o compose, que lê o projeto
    dali;
  - sem `.env` (com `DATABASE_URL` no ambiente), o projeto vai por `docker compose -p`;
  - um `.env` que já existia não muda: a instalação continua no projeto em que estava.
- **Diretório no banco:** coluna `installation.home_dir` (migration 00019, com `down`).
  `store.ClaimHome`:
  - num banco novo, registra o diretório;
  - num banco registrado por outro diretório, recusa, com a mensagem de qual é;
  - num banco com histórico (rodadas, execuções ou issues) e sem registro, o `init` recusa, a menos
    que receba `--adotar-banco`.
- **Quem registra e confere:** o `init`, o `serve` e o `worker`. O `serve` e o `worker` rodam no
  diretório da instalação e registram o diretório num banco sem registro: é assim que uma
  instalação anterior à migration ganha o registro na primeira subida.
- **Diretório:** o atual, absoluto e sem links simbólicos (`store.HomeDir`). As units rodam no
  `SENTINELA_HOME` (`deploy/bin/sentinela-run` entra nele).

## Consequências

- Esta é uma mudança de esquema depois da Fase 2, feita a pedido, com `down`.
- **Testes:**
  - `TestClaimHomeRefusesTheDatabaseOfAnotherInstallation`, no banco de teste;
  - `TestInitUsesItsOwnPortProjectAndDatabase`: porta, `.env` antes do compose, `-p` sem `.env` e
    recusa.
- **Mover a instalação de diretório:** é preciso atualizar o registro
  (`UPDATE installation SET home_dir = '<novo>'`) ou rodar o `serve` uma vez depois de apagar o
  registro. Fica documentado em `docs/operacao.md`.
- **O que não é conferido:** os outros comandos (`scan`, `finding`, `job`) só usam o banco que o
  `.env` aponta. A proteção está no `init` e nos processos de longa duração.
