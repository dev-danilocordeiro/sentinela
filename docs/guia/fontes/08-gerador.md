# Gerador de config

Preencha o formulário: o yaml sai ao lado, conferido no navegador contra o JSON Schema embutido (o mesmo de `schema/`, gerado do código). Nada sai da sua máquina.

- **Empresa pequena:** gera um `sentinela.yaml` completo. Ele parte do `examples/empresa-pequena`, com os modelos, os preços e os caminhos bloqueados do exemplo.
- **Time novo:** gera o `config.d/teams/<time>.yaml` que um time adiciona por PR no repo da config, no formato do cenário acme.

<!-- guia:generator -->

O JSON Schema confere o formato: chaves que existem, tipos e valores aceitos. As regras que dependem do conjunto ficam com o `config validate --offline`, que roda na sua máquina e no CI do PR:

- o mesmo módulo em dois times;
- uma chave da organização num arquivo de time;
- um repo que ninguém declarou;
- o projeto do time igual ao de um módulo.

O `config validate` sem `--offline` confere também se o Linear e o Slack têm o que o arquivo cita.

```bash
sentinela config validate --offline --changed config.d/teams/financeiro.yaml --base origin/main
```
