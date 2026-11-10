# Referência

### Chaves de config

Todas as chaves do `sentinela.yaml` e do arquivo de time, tiradas do JSON Schema (`schema/sentinela.schema.json` e `schema/team.schema.json`, gerados do código). A descrição e o padrão vêm de `docs/configuracao.md`. Quando uma chave não tem linha própria lá, aparece a descrição da chave de cima. "Só no principal" quer dizer que um arquivo de `config.d/teams` não pode tê-la.

Dois detalhes:

- num time que mora em `config.d`, o principal pode declarar só `teams.<time>.policy`, `models` e `fix_trigger`;
- `policy.auto_approve` nunca vale num arquivo de time.

<!-- guia:reference -->

### Comandos e flags

Todos os comandos do binário, com as flags, lidos da árvore de comandos do próprio `sentinela`. A flag `--config` (padrão `sentinela.yaml`) vale em todos.

<!-- guia:commands -->
