---
name: sentinela-setup
description: Configura o Sentinela do zero para quem acabou de clonar o repo (pré-requisitos, modo, perfil de modelos, repos, AGENTS.md, times, validação e primeiro scan em sombra) e atende os atalhos depois da instalação (adicionar repo, adicionar time, trocar de modo, trocar o perfil de modelos, entender por que um caminho foi para um time). Use quando alguém pedir para instalar, configurar, começar a usar ou ajustar o Sentinela neste repo.
---

# /sentinela-setup

Você guia uma pessoa, de júnior a sênior, a configurar o Sentinela neste repo e a começar a usar.
Converse em português, uma pergunta por vez, com a opção recomendada marcada. Rode os comandos
reais do binário (`./sentinela ...`) e mostre a saída que importa. Explique o porquê em uma frase
quando a pessoa for júnior; vá direto quando ela mostrar que já conhece.

Arquivos de apoio desta skill:

- [agents-md.md](agents-md.md): detectar linguagens e estrutura de um repo e rascunhar o
  `AGENTS.md`.
- [times.md](times.md): propor os times e gerar a config de cada um, em cada modo.

## Regras (valem o tempo todo)

1. **Nada sai da máquina sem confirmação explícita da pessoa.** Não publique no Linear, não poste
   no Slack, não crie label, repo nem PR antes de mostrar o que vai ser feito e ouvir um "sim"
   para aquela ação. Um "sim" vale só para a ação perguntada.
2. **Decisão humana é da pessoa.** Nunca cancele nem feche issue, nunca aplique a label de
   gatilho, nunca rode `sentinela finding fix`, `reject`, `accept`, `resolve` ou `duplicate` por
   conta própria, nunca aprove nem mergeie PR, nunca revogue supressão. Explique o comando e deixe
   a pessoa rodar, ou rode só se ela pedir aquela ação, com aquele achado.
3. **Segredos só no `.env`.** Nunca peça token ou chave no chat, nunca escreva segredo em arquivo
   versionado nem em log. Diga qual variável vai no `.env` (lista em `docs/configuracao.md`,
   seção de variáveis de ambiente) e deixe a pessoa preencher.
4. **O banco é só pelos comandos.** Nunca rode SQL à mão. Migrations: `./sentinela migrate up`.
   Decisões: os comandos `sentinela finding ...` (modo local) ou o Linear.
5. **Quando algo falhar,** mostre a mensagem exata, diga a correção e cite a seção de
   `docs/guia-configuracao.md` (ou `docs/avaliacao.md`, problemas comuns) que trata do caso. Não
   esconda erro nem tente de novo em silêncio.
6. **Clones e scripts dos repos:** clone para ler, sem rodar instalação, build ou teste do repo
   sem perguntar (eles executam código de fora). Se a pessoa já pediu explicitamente para conferir
   os comandos (por exemplo, "conferidos num clone limpo"), esse pedido é o OK para rodá-los, num
   clone temporário e com a infra do repo isolada (veja [agents-md.md](agents-md.md), seção 5).

## Passo 1: pré-requisitos

Rode e resuma em uma tabela (ok / falta / como resolver):

| O quê | Como conferir | Se faltar |
|---|---|---|
| Go na versão do `go.mod` | `go version` e a linha `go` do `go.mod` | instalar o Go dessa versão (ou mais nova) |
| Docker | `docker info` | instalar o Docker e pôr o usuário para usá-lo |
| gh logado, e com que usuário | `gh auth status` | `gh auth login` (no modo local e no MCP, os PRs em draft saem por essa conta) |
| Claude Code logado | `claude auth status --json` (campo `loggedIn`) | `claude auth login` (assinatura) ou `claude setup-token` |
| Outra instalação do Sentinela na máquina | `docker compose ls`, `systemctl --user list-units '*sentinela*'` e a porta 5432 (`ss -ltn`) | siga "Instalação separada", abaixo |

O Postgres do `init` sobe pelo `docker compose` deste diretório. Se a porta 5432 já estiver em uso,
não siga sem trocar a porta: o `init` recusa um banco de outra instalação, mas a pessoa precisa
entender o porquê.

### Instalação separada (segunda instalação na mesma máquina)

Quando a pessoa quer uma instalação à parte (outro cliente, um teste, um repo de avaliação) ao lado
de uma que já existe, cada uma precisa de diretório, banco, projeto do compose e units próprios
(ADR 0052 e ADR 0056):

1. **Diretório:** um clone do Sentinela em outro lugar. O `init` precisa do `docker-compose.yml`, e
   o worker precisa do plugin e dos prompts do repo, então copiar só o binário não basta:

   ```bash
   git clone <url do Sentinela ou o caminho da instalação atual> ../sentinela-<nome>
   cd ../sentinela-<nome> && go build -o sentinela ./cmd/sentinela
   ```

2. **Banco e compose:** `SENTINELA_PG_PORT=55432` (uma porta livre em `ss -ltn`) antes do `init`.
   Ele grava a porta e um `COMPOSE_PROJECT_NAME` próprio no `.env` e recusa o banco de outra
   instalação.
3. **Worktrees, artefatos e HOME do job:** já saem por padrão de um diretório desta instalação
   (`~/.cache/sentinela/<chave>/`); não precisa configurar nada.
4. **Serviços:** `deploy/install.sh --user --prefix <nome>-sentinela`. O `install.sh` sem
   `--prefix` usaria os nomes da outra instalação; ele recusa sobrescrevê-las, mas é melhor não
   chegar lá. O `./sentinela status` acha as units desta instalação pelo prefixo.

## Passo 2: escolhas (uma pergunta por vez)

1. **Modo** (veja `docs/modos.md`):
   - **local (recomendado para avaliar):** achados num relatório na máquina, decisões por
     `sentinela finding`, PR pela conta do `gh`. Não precisa de Linear nem Slack.
   - **MCP (recomendado para piloto com um time):** o time decide no Linear, pelos conectores do
     Claude Code, como a pessoa logada. Precisa do Linear conectado em `claude mcp list`.
   - **completo (uso contínuo, empresa):** app do Linear, GitHub Apps, bot do Slack e serviços.
     Precisa dos administradores (`docs/integracoes.md`).
2. **Perfil de modelos** (veja `docs/custos.md`, seção Perfis de modelos):
   - **qualidade (recomendado):** modelo forte onde há julgamento que não se recupera depois.
   - **economia:** para orçamento ou cota limitados.
   - **personalizado:** parte do qualidade e a pessoa ajusta papel por papel.
3. **Repos:** `owner/repo` de cada um. Confira com `gh repo view owner/repo --json name` que a
   conta enxerga o repo.

## Passo 3: compilar e preparar

```bash
go build -o sentinela ./cmd/sentinela
./sentinela init --mode local --repo acme/svc --models qualidade
```

- Modo MCP: `./sentinela init --mode mcp --repo acme/svc --models qualidade --linear-team ENG`
  (e `--slack-channel C0XXXXXXXXX` se houver canal). O `init` gera `publish.enabled: false`.
- Modo completo: o `init` não cobre. Siga `docs/instalacao.md` e `docs/integracoes.md`
  (`cp sentinela.example.yaml sentinela.yaml`, `cp .env.example .env`, `./sentinela migrate up`),
  e deixe `publish.enabled: false` até o fim do passo 6.
- Numa instalação separada, rode o `init` com `SENTINELA_PG_PORT` (passo 1, Instalação separada).
- Repita o `init` até ele terminar com `pronto.`; ele não sobrescreve o que já existe.
- Se o `init` disser que o banco é de outra instalação, pare e volte ao passo 1 (porta e projeto
  do compose).

## Passo 4: cada repo (linguagens, estrutura e AGENTS.md)

Siga [agents-md.md](agents-md.md). Em resumo:

1. Clone raso numa pasta temporária (`git clone --depth 1`), só para ler.
2. Detecte as linguagens e a estrutura (módulos Go, Gradle e Maven, workspaces npm, pnpm e yarn).
3. **Scanners por linguagem:** o `init` e o `config bootstrap` geram `discovery.scanners` pelas
   linguagens dos repos (do clone do scan ou da API de linguagens do GitHub): Go ganha
   `golangci-lint`, `gosec` e `govulncheck`; TypeScript e JavaScript, `eslint`, `tsc` e
   `osv-scanner`; Java e Kotlin, `osv-scanner`; o `semgrep` entra sempre, com os pacotes de regras
   das linguagens. Mostre a linha gerada e o comentário com as linguagens. Se o init avisar que não
   detectou as linguagens de um repo (privado sem `gh` logado), a lista sai completa: ajuste pela
   tabela em [agents-md.md](agents-md.md), seção 2. Depois do primeiro scan, o `config validate`
   avisa quando um repo tem uma linguagem cujo scanner está instalado e fora da lista.
4. Confira se há `AGENTS.md` ou `CLAUDE.md` na raiz e nos workspaces. O job usa o mais próximo do
   achado, com o da raiz como reserva; sem nenhum no caminho, a correção não roda.
5. Se faltar, escreva um rascunho com os comandos reais (tirados do `Makefile`, dos scripts do
   `package.json`, do wrapper do Maven ou do Gradle, do README e do CI do repo), mostre para a
   pessoa e pergunte. Só com o OK dela: branch, commit e `gh pr create --draft`. Nunca mergeie.
6. Se existir, confira os comandos que ele lista (com o OK da pessoa, num clone limpo). Um
   comando que falha (lint sem config, teste que depende de um Docker diferente) faz toda correção
   falhar no portão. Mostre o que passou e o que falhou e proponha um PR em draft corrigindo a
   seção de comandos, pelo mesmo fluxo do rascunho.

## Passo 5: times

Siga [times.md](times.md). Em resumo: proponha os times a partir do CODEOWNERS (no modo completo,
com `./sentinela config bootstrap`), pergunte por time o board ou time do Linear, o projeto, o
canal e o grupo do Slack (no modo local, só o nome), grave a config e mostre o
`./sentinela owners explain` de 3 a 5 caminhos reais de cada repo para a pessoa conferir.

## Passo 6: validação e primeiro scan em sombra

```bash
./sentinela config validate --offline
./sentinela config validate                       # MCP e completo: confere Linear, Slack e GitHub de verdade
./sentinela scan --repo acme/svc --estimate-only   # por repo: chamadas e custo esperados
./sentinela scan --repo acme/svc                   # primeiro scan, com publish.enabled: false
```

- Antes do primeiro scan no MCP ou no completo, confirme no `sentinela.yaml` que
  `publish.enabled` está `false`. No modo local não há publicação: o relatório sai em
  `reports/local`.
- **Estimativa:** se ela avisar que o pior caso passa do teto (`llm.claude_code.max_calls_per_run`,
  ou o orçamento em dólar com a API), explique: a rodada para no meio (`aborted_budget`) se chegar
  lá, e o resto fica para a próxima. Mostre o que o aviso sugere (subir o teto, diminuir
  `discovery.max_findings_per_package`, rodar por partes) e pergunte antes de mudar.
- Mostre o resumo do que saiu: rodada, chamadas, achados propostos e aprovados e scanners que
  rodaram ou foram pulados (e por quê). No modo local, cada achado aparece como `achado F-<id>`, e
  "publicados" no resumo quer dizer "no relatório local": nada sai da máquina. Diga onde ler:
  - modo local: `reports/local/index.md` e o `.html` de cada repo;
  - todos os modos: `./sentinela report --run <id>` gera o HTML da rodada em `reports/`.
- `./sentinela status` fecha o passo: modo, última rodada por repo e o que falta.

## Passo 7: o dia a dia

Explique o ciclo do modo escolhido, sem executar as decisões:

- **Onde ver os achados:**
  - local: `./sentinela finding list` e `reports/local`;
  - MCP e completo: o board do Linear de cada time.
- **Recusar:**
  - local: `./sentinela finding reject <id> "motivo"`;
  - Linear: cancelar a issue com o motivo num comentário.

  O motivo vira supressão e exemplo para o Judge.
- **Pedir a correção:**
  - local: `./sentinela finding fix <id>`;
  - Linear: aplicar a label de gatilho (`sentinela-fix`).

  Em seguida, `./sentinela worker --once` (ou o worker rodando sozinho, `docs/operacao.md`).
- **Revisar o PR:** o PR sai em draft, com a spec, o build, as quatro reviews e a decisão do
  approver no corpo. Quem aprova e mergeia é a pessoa.
- **Ligar a publicação (MCP e completo):** depois de revisar a rodada em sombra, ponha
  `publish.enabled: true` e rode `./sentinela scan --all`. Os pendentes saem no Linear, até
  `max_issues_per_team_per_run` por time. Peça a confirmação antes.
- **Rodar sozinho:** `deploy/install.sh --user` (`docs/operacao.md`, Rodando sozinho). Numa
  instalação separada, sempre com `--prefix <nome>-sentinela`.

## Atalhos depois da instalação

- **"adicionar um repo":** passo 4 para o repo novo. Depois, inclua-o em `repos` do
  `sentinela.yaml` (ou do arquivo do time), rode `./sentinela config validate --offline` e o
  `./sentinela scan --repo owner/repo --estimate-only`.
- **"adicionar um time":** passo 5 só para o time novo. Arquivo `config.d/teams/<time>.yaml` no
  modo completo, ou bloco em `teams` no principal. Depois, `./sentinela owners explain` de caminhos
  do time e `./sentinela config validate --offline`.
- **"trocar de modo":** siga `docs/modos.md` (migrar entre modos sem perder o histórico). O banco é
  o mesmo; mudam os `adapters` e as credenciais. Valide sem `--offline` antes do próximo scan.
  - Para o MCP, o bloco completo inclui `defaults.linear` (time, projeto, `triage_state`,
    `review_state`, `done_state`), as labels de pergunta e de intenção, `trigger.mode: polling` e
    `publish`. O `init` não sobrescreve o `sentinela.yaml` existente: gere um num diretório
    temporário e copie os blocos.
  - Os achados abertos sem issue saem no Linear na próxima rodada, até
    `publish.max_issues_per_team_per_run` por time. Sem commits novos, um `scan` incremental não
    chama modelos e só publica os pendentes. Antes, mostre o que sairia e peça a confirmação.
  - Achados gravados no modo local não têm time do Linear no roteamento: saem no time de
    `defaults.linear`. Se os times usam boards diferentes, avise antes de publicar.
- **"trocar o perfil de modelos":** mostre o atual com `./sentinela config models`. Troque o bloco
  `models` pelo do perfil novo (tabela em `docs/custos.md`). Para ver o bloco completo de um
  perfil, gere com `./sentinela init --mode local --models economia` num diretório temporário e
  copie de lá. Depois, `./sentinela config validate --offline`.
- **"por que este caminho foi para este time":**
  `./sentinela owners explain owner/repo caminho/do/arquivo` mostra a regra que decidiu e de onde
  veio cada valor (no modo local, só o time, a regra e a política: não há Linear nem Slack). Com mais caminhos (as outras evidências de um achado), mostra também os times
  secundários. Explique a ordem em `docs/guia-configuracao.md`.
