# 0046: Guia interativo gerado das fontes reais

## Contexto

A documentação estava espalhada em vários markdown, com saídas de comando copiadas à mão que
envelheciam. Faltava um ponto de entrada para quem vai avaliar, instalar e configurar sozinho,
com exemplos que se possa testar.

## Decisão

- **Formato:** `docs/guia/index.html` é um arquivo único, autocontido. CSS, JS e dados ficam
  embutidos, sem CDN, e ele funciona aberto do disco, com tema claro e escuro.
- **Geração:** `sentinela docs guide` (ou `go generate ./internal/guide`) monta o arquivo a partir
  de:
  - `docs/guia/fontes/*.md`: o texto próprio do guia, com diretivas `<!-- guia:... -->`;
  - os docs (`include`), os exemplos (`annotated`, `file`) e o JSON Schema;
  - a saída do binário, rodado em processo: `run`, e `init` com as pontas simuladas.
- **Simulador de roteamento:** mostra a saída real do `owners explain`, gerada no build, sem
  reimplementar a regra em JS.
- **Erros comuns:** cada caso muda uma cópia do cenário acme e roda a validação de verdade
  (`--offline`, ou com Linear, Slack e GitHub falsos). A mensagem é a do binário.
- **Gerador:** monta o yaml e o valida no navegador contra o JSON Schema embutido. As regras do
  conjunto ficam com o `config validate`.
- **Diagramas:** SVG gerado em Go no build, sem mermaid em tempo de execução.
- **Garantia:** `TestGuideIsUpToDate` falha se o arquivo commitado diferir do gerado. As fontes
  entram nos testes de consistência dos docs (comandos, flags e chaves).

## Consequências

- Mudar um doc, um exemplo, o schema ou um comando pede regenerar o guia.
- O goldmark entra como dependência, só para o guia.
