# ADR 0006: Semântica do resolvedor de ownership

- Status: aceita
- Data: 2026-10-09

## Decisão
Ordem: `modules[].path` → CODEOWNERS → `repos[].team` → `repos[].fallback_team` → `defaults`.
`owners_source` pode ser definido por repo (padrão: `defaults.owners_source`).

- Módulos: globs doublestar; vence o padrão mais longo; empate vence o declarado primeiro.
  `ignore: true` marca o caminho como fora da análise e encerra a resolução.
- CODEOWNERS: última regra que casa vence. Padrões estilo gitignore: `/` inicial ou barra no meio
  ancora na raiz; barra final restringe a diretório; último segmento sem curinga também casa como
  diretório (`**/logs`), mas `docs/*` não casa `docs/a/b`, como na documentação do GitHub.
  Regra sem donos remove o dono.
- Owner → time: `@org/time` via `teams[].github`. Usuário individual primeiro pelo time em que
  é `members`, depois por `teams[].github`. Assim um usuário individual (`@fulano`) resolve para `plataforma`.
- Vários donos: o primeiro owner **mapeado** é o dono; os demais times viram inscritos.
- Owner sem time: vira aviso na resolução e no `config validate`. Se nenhum owner da regra
  tem time, a resolução segue para o passo 3, porque o CODEOWNERS não decidiu. Ex.: `/python/tests/`
  (`@exemplo/qa`) cai no `fallback_team` com aviso, e não na regra `*` de cima.
- Projeto do Linear: `teams[].linear.project`, senão `repos[].linear_project`, senão `defaults`.
  Estado inicial: time, senão `defaults` (`Backlog` no workspace de teste, que não tem Triage).
- Assignee: `lead` resolve para o lead; `round_robin` fica com o publisher (Fase 3).
- O resultado tem o mesmo formato de `Finding.routing` e um trace usado por `owners explain`.

## Consequências
Times do GitHub que não existem (`@exemplo/*`, fictícios) geram só aviso no `config validate`: o roteamento usa
o `sentinela.yaml`, não a API do GitHub.
