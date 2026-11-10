# Simulador de roteamento

Escolha um caminho do exemplo acme. O resultado é a saída real do `sentinela owners explain`, gerada no build deste guia com o binário atual, a config do exemplo e o CODEOWNERS de cada repo. A regra não é reimplementada aqui: se o código ou o exemplo mudarem, o guia é gerado de novo, e um teste falha enquanto isso não acontecer.

<!-- guia:simulator -->

Como ler o resultado:

- **time e regra:** quem é o dono e qual regra decidiu. `config:<glob>` é um módulo da config; `codeowners:L<n>` é uma linha do CODEOWNERS; `fallback` é o `fallback_team` do repo.
- **projeto, canal e menção:** o valor efetivo e de onde veio. Esses são os campos que o time mais muda.
- **por categoria:** o projeto que um achado daquela categoria usaria, quando o projeto não vier de um módulo.
- **inscritos:** os outros donos da mesma regra do CODEOWNERS. Eles entram como inscritos na issue.
- **secundário:** os times que as outras evidências tocam. Eles vão para a seção "Também afeta" e são mencionados no aviso do dono.

Na sua instalação, rode o mesmo comando. Sem `--codeowners`, ele lê o CODEOWNERS do GitHub:

```bash
sentinela owners explain sua-org/seu-repo caminho/do/arquivo.go
```
