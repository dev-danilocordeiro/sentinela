# ADR 0017: Dono do achado pelo caminho mais específico

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## Contexto
Na rodada 12 do monorepo-poliglota, as EX-14 ("Valida capacidade positiva ao abrir turma") e
EX-15 ("Distingue erros internos de não encontrado ao matricular") foram para a plataforma. A
evidência das duas está em `go/cmd/api/main.go`, que só casa com o `*` do CODEOWNERS (L7). Mas
o problema é do acadêmico:
- a EX-14 tem `go/internal/turmas` em `scope_paths`;
- a EX-15 tem só `go/cmd/api` no escopo e na evidência, mas a correção proposta pede
  `turmas.ErrNaoEncontrada`, e `turmas` é o import `.../go/internal/turmas` do próprio arquivo.

A regra anterior olhava só o arquivo da evidência principal.

## Decisão
- `repo.Resolver.ResolveFinding` decide o dono de um achado a partir de quatro fontes:
  - o arquivo da evidência principal;
  - `scope_paths`;
  - as demais evidências;
  - as referências: diretórios dos pacotes Go do próprio repo citados como `pkg.Ident` na
    correção proposta ou nos snippets, resolvidos pelos imports dos arquivos `.go` da evidência e
    pelo `go.mod` mais próximo. A biblioteca padrão e as dependências externas ficam de fora.
- A evidência principal vale, a menos que tenha caído em regra genérica:
  - `*`, `**`, `/*`, `/**` ou `**/*` do CODEOWNERS;
  - um `modules[].path` desses;
  - `repos[].team`, `fallback_team` ou `defaults`.
- Nesse caso, vence a regra específica mais específica (padrão mais longo), procurando primeiro
  em `scope_paths`, depois nas demais evidências e depois nas referências. Caminhos sem `/` final
  são testados também como diretório, porque `scope_paths` traz diretórios e regras de diretório do
  CODEOWNERS só casam com o conteúdo deles.
- A regra gravada em `routing.rule` diz de onde veio, por exemplo
  `codeowners:L11 via scope:go/internal/turmas`. O rastro fica em `owners explain`.
- Sem nenhum caminho específico, fica a regra genérica. A EX-12 (timeouts, só `go/cmd/api` e a
  biblioteca padrão) continua na plataforma.
- As referências são uma terceira fonte além de `scope_paths` e evidências. Sem ela, a EX-15 não
  teria nenhum caminho do acadêmico. Por enquanto só existem para Go; outras linguagens usam só
  escopo e evidência.
- Override no `sentinela.yaml` do monorepo-poliglota: `**/Dockerfile → plataforma`. É um
  `modules[].path` (passo 1, vence o CODEOWNERS). Com ele, a EX-13 (Dockerfile do
  `java/servico-turmas`) iria para a plataforma.

## Consequências
- As issues já publicadas (EX-13, EX-14 e EX-15) não foram movidas no Linear. O `routing`
  gravado no achado não é reescrito, como manda a spec. A regra vale para os próximos achados.
- Um achado de um handler genérico que toca dois times vai para o mais específico de `scope_paths`.
  Se o escopo trouxer só caminhos genéricos, o dono continua a plataforma.
- Os testes de tabela usam o CODEOWNERS e o `sentinela.yaml` reais, com os casos das EX-12 a
  EX-15. O `goRefs` é testado com o `main.go` e o `go.mod` reais do monorepo-poliglota.
