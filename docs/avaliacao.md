# Avaliar o Sentinela em 10 minutos (modo local)

Sem Linear, sem Slack e sem GitHub App. Os achados saem num relatório na sua máquina, você decide
pela linha de comando e, se pedir uma correção, o PR em draft sai pela sua conta do `gh`. O
Postgres guarda tudo, então, se depois você migrar para o modo MCP ou o completo, o histórico vem
junto ([modos.md](modos.md)).

**Jeito recomendado:** abra o Claude Code na pasta do repo e digite `/sentinela-setup`. A skill faz
os passos abaixo com você, um de cada vez, e para antes de qualquer ação que saia da máquina. Este
documento é o mesmo roteiro, para fazer à mão.

## Antes de começar

- Docker (o Postgres com pgvector roda no `docker compose`).
- Go 1.27 ou mais novo, para compilar o binário.
- O [Claude Code](https://docs.claude.com/en/docs/claude-code) logado com uma assinatura: a
  descoberta e a correção usam `claude -p`.
- O [GitHub CLI](https://cli.github.com) logado (`gh auth login`), com acesso de escrita ao repo
  que você quer analisar. Ele só é necessário para abrir PR.
- Opcional: `golangci-lint`, `gosec`, `govulncheck` e `semgrep` no PATH. Sem eles, a descoberta
  roda com menos sinais.

## Passo a passo

**1. Compile** (1 min):

```bash
git clone <este repositório> && cd sentinela
go build -o sentinela ./cmd/sentinela
```

**2. Prepare** (1 min):

```bash
./sentinela init --mode local --repo sua-org/seu-repo
```

O `init`:

- cria o `.env`, se ele não existir, com o `DATABASE_URL` (na porta `SENTINELA_PG_PORT`, padrão
  5432) e um `COMPOSE_PROJECT_NAME` próprio deste diretório: dois clones na mesma máquina não
  dividem o Postgres;
- sobe o Postgres;
- aplica as migrations e confere a conexão;
- gera um `sentinela.yaml` mínimo (`tracker: local`, `notifier: none`, `git_host: gh_cli`);
- confere o `gh` e o `claude`.

No fim, ele lista o que falta. Rode de novo até sair `pronto.`; nada que já existe é sobrescrito.

**3. Descubra** (3 a 5 min num repo médio):

```bash
./sentinela scan --repo sua-org/seu-repo --estimate-only   # quantas chamadas a rodada faria
./sentinela scan --all
```

A primeira rodada analisa o repo inteiro; as seguintes, só o que mudou.

**4. Leia** `reports/local/index.md`, ou o `.html` de cada repo no navegador. Cada achado traz:

- o id (`F-12`);
- a evidência (`arquivo:linha`);
- o porquê e a correção proposta;
- os critérios de aceite;
- o comando de cada ação possível.

**5. Decida** (1 min por achado):

```bash
./sentinela finding list
./sentinela finding show 12
./sentinela finding reject 13 "o handler já valida antes"   # vira supressão: não volta
./sentinela finding accept 14                                # é real; fica aberto
./sentinela finding fix 12                                   # pede a correção
./sentinela finding resolve 15 --note "corrigido no commit abc123"   # você corrigiu à mão
```

**6. Corrija** (5 a 20 min por job, em segundo plano):

```bash
./sentinela worker --once
```

O worker:

1. cria um worktree só para o job;
2. roda a spec, o builder, os quatro reviewers e o approver;
3. abre o PR em draft pela sua conta.

O Policy Gate comenta no PR o que um humano precisa conferir. Aprovar e mergear é com você. Rodar
o worker de novo acompanha o PR: CI vermelho volta para o builder, até 3 vezes, e o merge fecha o
achado.

## O que observar

- **Recusas:** os motivos viram supressões e exemplos para o Judge. Uma segunda rodada no mesmo repo
  não deve repetir o que você recusou (`sentinela finding list --all`).
- **Custo:** `./sentinela report --run <id>` abre um HTML com os achados, as decisões do Judge e as
  chamadas por agente.
- **Situação geral:** `./sentinela status` mostra o modo, a última rodada por repo, os achados
  pendentes, a fila e os PRs.

## Problemas comuns

| Sintoma | O que fazer |
|---|---|
| `falta: conexão com o banco` | Confira `docker compose ps`; a porta é `SENTINELA_PG_PORT` (padrão 5432), a mesma no `.env` e no compose |
| `o banco pertence a outra instalação` | O `DATABASE_URL` aponta para o banco de outra instalação (outro diretório). Use outra porta (`SENTINELA_PG_PORT`) e outro `COMPOSE_PROJECT_NAME` no `.env`, ou rode o comando no diretório que a mensagem cita (ADR 0052) |
| Push com 403 | O `gh` precisa de escrita no repo (`gh auth status` mostra o escopo `repo`). O Sentinela usa a conta logada no `gh`, nunca o `GITHUB_TOKEN` do `.env` |
| O job falha dizendo que falta `AGENTS.md` | O repo precisa dizer como roda os testes, o build e o lint (`AGENTS.md` ou `CLAUDE.md`) |
| Nenhum achado | Pode ser verdade. Rode em outro repo ou veja os descartados no `sentinela report` |

## Depois

Para pôr o time para decidir no Linear sem criar apps, use o modo MCP
(`sentinela init --mode mcp`). Para uso real, use o modo completo. Os dois estão em
[modos.md](modos.md).
