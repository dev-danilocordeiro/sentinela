# Como pedir uma mudança no Sentinela

Com vários times, a config mora num repo. O `sentinela.yaml` principal é dos administradores, e cada `config.d/teams/<time>.yaml` é do seu time, pelo CODEOWNERS desse repo. Um time muda a própria configuração por PR, sem pedir a ninguém, desde que fique no arquivo dele.

<!-- guia:diagram pr-do-time -->

<!-- guia:include CONTRIBUTING.md#O que você pode mudar sozinho (no arquivo do seu time) -->

### O PR, passo a passo

<!-- guia:include CONTRIBUTING.md#PR de exemplo: o time catálogo ganha um módulo com projeto próprio -->

### O que o CI mostra

O workflow do repo da config (`.github/workflows/sentinela-config.yml` no exemplo acme) roda `config validate --offline --changed` com os arquivos do PR. Ele comenta no PR o roteamento antes e depois de cada caminho afetado. Se o arquivo quebrar uma regra (uma chave da organização, um módulo de outro time, uma categoria que não existe), o job falha com a mensagem exata, que está em [Erros comuns](#erros-comuns).

### O que o revisor olha

<!-- guia:include CONTRIBUTING.md#O que o revisor deve olhar -->
