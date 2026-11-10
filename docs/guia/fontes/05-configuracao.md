# Configuração

A config é o `sentinela.yaml` e, com vários times, um arquivo por time em `config.d/teams/<time>.yaml`. Segredos nunca entram nela: ficam em variáveis de ambiente (o `.env`). Todos os serviços releem a config a cada ciclo.

### Glossário

| Termo | O que é | Exemplo |
|---|---|---|
| **Achado** | Um problema com evidência `arquivo:linha`, categoria, severidade, correção proposta e critérios de aceite; tem um fingerprint para não sair duas vezes | "Feche o body da resposta HTTP em `cliente.go:42`" (bug, high) |
| **Unidade** | O que a descoberta examina por vez: um pacote (um diretório de código) | `modules/cobranca/fatura` |
| **Time** | Grupo dono de código, com board no Linear, canal no Slack, grupo para menção e política | `pagamentos`: board PAG, canal `C0ACMEPAG01`, grupo `S0ACMEPAG01` |
| **Módulo** | Um glob de um repo com dono explícito na config, que pode ter projeto próprio | `{ repo: acme/plataforma, path: "modules/cobranca/**", linear_project: "Pagamentos · Cobrança" }` |
| **Projeto** | O projeto do Linear em que a issue nasce, dentro do board do time | "Pagamentos · Cobrança" |
| **Label de gatilho** | A label que uma pessoa aplica para pedir a correção; cada time pode ter a sua | `sentinela-fix`, ou `pagamentos-fix` só no time de pagamentos |
| **Supressão** | O registro de uma recusa (fingerprint e motivo): o achado não volta, e o motivo ensina o juiz | "o handler já valida antes" |
| **Gate** | O Policy Gate, em código: decide se o PR pode ser aprovado pela app revisora. Por padrão, só comenta | `policy: { auto_approve: false, max_lines: 50 }` |
| **Override** | Um módulo com `override: true`, que vence os módulos comuns: serve para arquivos transversais | `{ repo: acme/plataforma, path: "**/Dockerfile", override: true }` |
| **Também afeta** | A seção da issue com os outros times que as evidências tocam: eles são avisados sem ganhar issue | identidade, num achado de pagamentos que também toca `modules/contas` |

### O que é da organização e o que é do time

A organização (os administradores do Sentinela) decide o que custa dinheiro, o que muda o risco e o modo de operação. O time decide para onde as coisas dele vão. Num arquivo de time, uma chave da organização é recusada pelo `config validate`, que diz qual é a chave, em qual arquivo está e por quê.

<!-- guia:include docs/guia-configuracao.md#Customizar: o que é do time e o que é da organização -->

`fix_trigger` (quem pode pedir a correção) também é da organização: fica no principal, em `fix_trigger.allowed` ou, por time, em `teams.<time>.fix_trigger`. Com `team_members`, só os membros do time do Linear dono do achado disparam a correção, e só os comentários deles entram como instruções. Um gatilho recusado não mexe na issue nem na label: o Sentinela comenta uma vez e espera alguém autorizado aplicar a label de novo.

### Ordem de precedência: time, projeto, canal e menção

Para cada achado, o Sentinela resolve o dono pelo caminho do arquivo da evidência principal, nesta ordem. O resultado fica gravado no achado, então uma mudança futura no CODEOWNERS não reescreve o passado.

<!-- guia:diagram precedencia -->

- **Time:** módulo da config (`modules[].path` ou o arquivo do time) → CODEOWNERS do repo (a última regra que casa) → `repos[].team` → `fallback_team` → `defaults`. Com `owners_source: codeowners`, o passo da config é pulado; com `config`, o do CODEOWNERS.
- **Projeto:** do módulo (`linear_project`) → por categoria do time (`linear.category_projects`, aplicado na publicação) → do time (`linear.project`) → do repo (`repos[].linear_project`) → `defaults.linear.project`.
- **Canal:** `teams.<time>.notify.slack` → `defaults.notify.slack`. Um canal com erro (o bot não está nele, ou ele foi arquivado) nunca derruba a rodada: o canal padrão recebe o aviso do problema.
- **Menção:** `teams.<time>.notify.mention`, com o ID do grupo (`S...`) → ninguém. Um handle (`@grupo`) sai como texto, porque o Slack só notifica grupos por ID.
- **Dono genérico:** se o dono do arquivo principal vem de uma regra genérica (o `*` do CODEOWNERS, `repos[].team`, `fallback_team` ou `defaults`), vence um caminho mais específico do achado: `scope_paths`, depois as outras evidências, depois os pacotes citados.

`sentinela owners explain <repo> <caminho>` mostra cada passo e de onde veio cada valor. Para testar sem instalar nada, use o [simulador](#simulador-de-roteamento).

### Override explícito para arquivos transversais

Entre os módulos da config que casam com um caminho, vence o glob mais longo. Isso funciona bem para módulos, mas não para arquivos que um time cuida em qualquer lugar do repo (Dockerfile, CI, infra, `go.mod`). No cenário acme, a plataforma é dona de todo Dockerfile:

- `**/Dockerfile` tem 13 caracteres, e `modules/cobranca/**` (pagamentos) tem 19. Pelo tamanho, o Dockerfile de `modules/cobranca` seria de pagamentos.
- Um módulo novo de nome longo, como `modules/conciliacao-de-repasses/**`, também ganharia de qualquer glob transversal. A regra viraria uma corrida de nomes.

Por isso, não conte com o tamanho do glob. Declare o módulo transversal com `override: true`: ele vence os módulos comuns que também casam (entre overrides, vale o mais longo).

```yaml
# config.d/teams/plataforma.yaml
modules:
  - { repo: acme/plataforma, path: "**/Dockerfile", override: true }
```

O `owners explain` mostra quando o override decidiu (`casou (override: vence os módulos comuns)`). No [simulador](#simulador-de-roteamento), compare o Dockerfile e o código de `modules/conciliacao-de-repasses`.

### "Também afeta" ou issue relacionada

Um achado pode envolver mais de um time de dois jeitos diferentes, e os dois convivem:

| | "Também afeta" | Issue relacionada |
|---|---|---|
| De onde vem | As **evidências** do achado tocam módulos de outros times | O **System Architect**, que olha o repo inteiro, cita módulos afetados (`related_modules`) |
| O que cria | Nenhuma issue: só a seção "Também afeta" na issue do dono e a menção do grupo de cada time no aviso do canal do dono | Uma **issue no board do outro time**, linkada à principal, com aviso no canal dele |
| Quando faz sentido | O outro time precisa saber, mas a correção é do dono | O outro time tem trabalho próprio a fazer |
| Repetição | Um time que já ganhou issue relacionada não aparece em "Também afeta" | — |

O dono continua um só nos dois casos: o do arquivo da evidência principal. Com mais de um caminho, `owners explain` mostra os times secundários. O caso real está no simulador (`cobranca/fatura.go + contas/login.go`).
