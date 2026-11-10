# Sentinela

O Sentinela analisa os repositórios de uma equipe e encontra problemas de código (bugs potenciais,
performance, segurança, melhorias). Os que valem a atenção do time viram issues no Linear, no board
do time dono do código. Quando uma pessoa pede a correção, ele implementa, revisa e abre um PR em
draft. Cada decisão humana (aceitar, recusar, marcar como duplicada, mergear) volta para um Postgres
e calibra as próximas rodadas.

É código Go determinístico onde não há julgamento (scanners, roteamento, publicação, fila, política
de aprovação) e agentes de LLM onde há (triagem, análise, julgamento, implementação, revisão). Roda
local, com um binário só.

## Como funciona

![Fluxo do Sentinela: descoberta até a issue no Linear, execução sob demanda até o PR em draft, e retroalimentação no Postgres](docs/diagram_sentinela.png)

A linha de cima roda na descoberta e termina numa issue pronta para executar. A de baixo só começa
quando uma pessoa aplica a label. O Postgres no meio guarda o histórico que as duas consultam. A
fonte editável do desenho é [docs/diagram_sentinela.svg](docs/diagram_sentinela.svg), e o fluxo
detalhado está em [docs/arquitetura.md](docs/arquitetura.md).

1. **Descoberta** (`sentinela scan`): clona o repo e roda os scanners. O Triager decide quais
   pacotes aprofundar, o Analyst e o Architect propõem achados com evidência `arquivo:linha`, e o
   código confere cada evidência. O Judge publica, descarta ou mescla, comparando com o que já
   existe.
2. **Publicação**: cada achado aprovado vira issue no time resolvido pelo roteamento (CODEOWNERS e
   `sentinela.yaml`), com aviso no canal do Slack do time. Achado que depende de decisão de produto
   vira pergunta ao time.
3. **Execução** (`sentinela worker`): uma pessoa aplica a label `sentinela-fix`. O worker cria um git
   worktree e roda o plugin do Claude Code em modo headless. O plugin escreve a spec, implementa,
   passa por quatro revisões em paralelo e pelo approver. O worker abre o PR em draft, e uma GitHub
   App revisora comenta a decisão do approver e do Policy Gate.
4. **Feedback** (`sentinela serve`): o sync lê o que as pessoas decidiram no Linear e no GitHub,
   grava supressões e desfechos, e o Feedback Curator ajusta os exemplos do Judge.

O Sentinela nunca toma uma decisão humana por você. Ele não cancela issue, não aplica a label de
correção, não aprova nem faz merge de PR. Veja [docs/seguranca.md](docs/seguranca.md).

## Modos de operação

O Postgres é o mesmo em todos os modos; só mudam as pontas
([docs/modos.md](docs/modos.md)):

- **Local:** achados num relatório em `reports/`, decisões pela linha de comando
  (`sentinela finding`), PR em draft pela sua conta do `gh`. Sem nenhuma chave. Para avaliar em 10
  minutos: [docs/avaliacao.md](docs/avaliacao.md).
- **MCP:** issues e decisões no Linear pelo MCP do Linear do seu Claude Code, sem criar apps. As
  ações saem com a sua conta.
- **Completo:** app OAuth no Linear, bot no Slack e duas GitHub Apps, com identidade própria. É o
  modo de uso real.

Para uso numa empresa (provider, onde rodar, identidade, permissões e adoção), veja
[docs/empresa.md](docs/empresa.md). Com vários times, cada um mantém o próprio arquivo de config por PR (`config.d/teams/<time>.yaml`):
veja o [guia de configuração](docs/guia-configuracao.md), o [CONTRIBUTING.md](CONTRIBUTING.md) e o
cenário [examples/monorepo-acme](examples/monorepo-acme).

## Começar: `/sentinela-setup` (recomendado)

Depois de clonar, abra o [Claude Code](https://docs.claude.com/en/docs/claude-code) na pasta do
repo e digite `/sentinela-setup`. A skill (em `.claude/skills/sentinela-setup`) conversa com você,
uma pergunta por vez:

1. confere os pré-requisitos;
2. ajuda a escolher o modo, o perfil de modelos e os repos;
3. roda o `init`;
4. confere ou rascunha o `AGENTS.md` de cada repo;
5. monta os times;
6. valida e faz o primeiro scan em modo sombra;
7. explica o dia a dia.

Ela não publica no Linear, não posta no Slack e não abre PR sem a sua confirmação, e nunca toma
uma decisão por você. Depois da instalação, ela também atende "adicionar um repo", "adicionar um
time", "trocar de modo", "trocar o perfil de modelos" e "por que este caminho foi para este time".

## Quickstart manual (5 minutos, modo sombra)

Pré-requisitos: Go 1.27+, Docker, `git`, e o [Claude Code](https://docs.claude.com/en/docs/claude-code)
logado com uma assinatura (ou uma chave da API da Anthropic). Para só ver achados, não é preciso
Linear, Slack nem GitHub App.

```bash
git clone <este repositório> && cd sentinela
cp .env.example .env                        # preencha só DATABASE_URL e GITHUB_TOKEN por enquanto
cp sentinela.example.yaml sentinela.yaml    # troque os repos de exemplo por um repo seu
docker compose up -d                        # Postgres 16 + pgvector
go build -o sentinela ./cmd/sentinela
./sentinela migrate up
./sentinela config validate --offline       # confere o sentinela.yaml sem chamar serviços
./sentinela scan --repo sua-org/seu-repo --estimate-only   # quantas chamadas a rodada faria
./sentinela scan --repo sua-org/seu-repo    # roda; com publish.enabled: false nada sai do Postgres
./sentinela report --run 1                  # abre reports/run-1.html com achados e decisões
```

Com `publish.enabled: false` (padrão do exemplo), os achados ficam só no Postgres. Revise o
relatório antes de ligar o Linear.

Para olhar o banco, `docker compose --profile tools up -d` sobe um pgAdmin já conectado em
http://127.0.0.1:5050 (veja [docs/operacao.md](docs/operacao.md#pgadmin)).

## Documentação

| Documento | Conteúdo |
|---|---|
| [docs/avaliacao.md](docs/avaliacao.md) | Avaliar em 10 minutos no modo local, sem chaves |
| [docs/modos.md](docs/modos.md) | Os três modos (local, MCP e completo), o que cada um suporta, labels, limitações e como migrar |
| [docs/guia-configuracao.md](docs/guia-configuracao.md) | Guia do zero ao cenário de uma organização com vários times: glossário, instalação por modo, como o Sentinela decide time, projeto, canal e menção, e erros comuns |
| [docs/guia/index.html](docs/guia/index.html) | **Guia completo e interativo** (abra no navegador, funciona offline): cenários, os três modos, instalação, configuração, simulador de roteamento, gerador de config, erros comuns e referência |
| [docs/empresa.md](docs/empresa.md) | Requisitos para uso em empresa: provider e custo por time, onde rodar, identidade e escopos, permissões, caminho de adoção e dois cenários |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Como um time adiciona o seu time, módulo ou projeto por PR (config.d/teams), e como mexer no código |
| [examples/monorepo-acme](examples/monorepo-acme) | Cenário de referência: monorepo com 4 times, config por time e workflow de PR |
| [docs/instalacao.md](docs/instalacao.md) | Instalação completa, banco, binário, Docker |
| [docs/integracoes.md](docs/integracoes.md) | Linear, Slack, GitHub Apps, Claude por assinatura ou API |
| [docs/configuracao.md](docs/configuracao.md) | Referência do `sentinela.yaml`, com exemplo de repo simples e de monorepo |
| [docs/arquitetura.md](docs/arquitetura.md) | Agentes, modelos, contratos, banco e fluxo |
| [docs/operacao.md](docs/operacao.md) | Comandos do dia a dia, serviços, decisões humanas, diagnóstico |
| [docs/seguranca.md](docs/seguranca.md) | Segredos, permissões mínimas, decidir e executar |
| [docs/custos.md](docs/custos.md) | Quanto custa e como limitar |
| [docs/roadmap.md](docs/roadmap.md) | Fase 5 e o que falta para adoção em equipes e empresas |
| [docs/adr/](docs/adr/) | Decisões de arquitetura, uma por arquivo |
| [SENTINELA_BUILD_PROMPT.md](SENTINELA_BUILD_PROMPT.md) | Especificação original, por fases |

## Desenvolvimento

```bash
go test ./...
golangci-lint run
sqlc generate                               # depois de mudar migrations ou internal/store/queries
docker compose exec postgres createdb -U sentinela sentinela_test   # uma vez
SENTINELA_TEST_DATABASE_URL=postgres://sentinela:sentinela@localhost:5432/sentinela_test?sslmode=disable \
  go test -p 1 ./...                        # integração: recria o schema do banco de teste
```

Os testes não chamam nenhum modelo: usam respostas gravadas e servidores falsos. O teste opcional
`SENTINELA_REAL_CLAUDE=1` roda o plugin com o Claude Code de verdade, num repo local.
