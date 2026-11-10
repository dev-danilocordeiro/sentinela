# 0047: Marcador de issue com o id da instalação

## Contexto

O marcador `sentinela-fingerprint: <fp>` no corpo da issue torna a publicação idempotente (ADR
0015): se o processo cai depois de criar a issue e antes de gravar no Postgres, a rodada seguinte
reencontra a issue em vez de duplicá-la. A busca ia com `includeArchived: true` e pegava a primeira
issue do time com o marcador.

Em 2026-10-12 um achado real (atualização do Go por vulnerabilidades) foi ligado à EX-20. A EX-20
tinha sido criada no dia anterior por uma instalação de avaliação do modo MCP (outro banco), com o
mesmo fingerprint, e depois **arquivada** num projeto de teste. A instalação real a reaproveitou: o
achado ficou "publicado" numa issue que ninguém vê.

## Decisão

- **Id da instalação:** a migration 00018 cria a tabela `installation` (uma linha) com um id de 8
  hexadecimais sorteado. O marcador das issues novas passa a ser
  `sentinela-fingerprint: <fp>@<id>` e `sentinela-related: <fp>/<time>@<id>`.
- **Busca:** sem `includeArchived`. A publicação lê as candidatas (até 50) e só reaproveita uma
  issue que:
  - não está arquivada nem na lixeira (`archivedAt`, `trashed`);
  - é do time e do **projeto** que o roteamento dá agora (sem projeto quando ele não dá nenhum);
  - tem o marcador desta instalação; ou o marcador antigo, sem id, numa issue criada até
    `installation.legacy_markers_until`.
- **Marcador antigo:** `legacy_markers_until` é o instante da migration quando a instalação já
  tinha publicado issues (a real), e vazio numa instalação nova. Assim as issues antigas da
  instalação real continuam reconhecidas, e uma instalação nova nunca adota issue de outra.
- **Descartes:** cada candidata recusada vira aviso no log, com o motivo.
- **Desligar um achado:** `sentinela finding unlink <id> --issue <ID> --reason "<motivo>"` apaga a
  ligação, grava `finding_unlinks` (issue, papel, motivo, quem pediu) e deixa o achado pendente. Não
  mexe no Linear, em outcomes nem em supressões. Só para achado `open`.
- **Modo MCP:** a mesma regra. O modelo devolve as candidatas com as linhas do marcador, a criação,
  `archived` e o nome do projeto (o `ProjectID` do MCP é o nome).

Campos conferidos por introspecção do tipo `Issue` na API GraphQL do Linear em 2026-10-14
(`archivedAt`, `trashed`, `createdAt`, `project`, `team`).

## Consequências

- Duas instalações no mesmo workspace (avaliação e real, ou uma por time) não reaproveitam as
  issues uma da outra.
- Uma issue que uma pessoa moveu de projeto, ou arquivou, deixa de ser reencontrada numa
  publicação retomada; nesse caso raro sai uma issue nova. A ligação já gravada no banco não muda:
  a busca só acontece para achado sem issue.
- Mudança no esquema depois da Fase 2 (`installation`, `finding_unlinks`), pedida pelo dono da
  instalação; as duas têm `down`.
