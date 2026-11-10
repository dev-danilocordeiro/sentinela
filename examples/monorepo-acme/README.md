# Cenário de referência: acme

Uma organização fictícia, "acme", com um monorepo e dois repos simples, configurada no formato em
que cada time mantém o próprio arquivo por PR (ADR 0041). O passo a passo que monta este cenário
do zero, com as saídas reais do `owners explain`, está em
[docs/guia-configuracao.md](../../docs/guia-configuracao.md).

| Time | No monorepo `acme/plataforma` | Repo próprio | Linear | Slack | Grupo |
|---|---|---|---|---|---|
| pagamentos | `modules/cobranca`, `carteira`, `conciliacao-de-repasses`, `fraude`, mais `libs/dinheiro` | — | PAG; um projeto por módulo, mais "Pagamentos · Segurança" | `C0ACMEPAG01` | `S0ACMEPAG01` (@pagamentos-team) |
| catalogo | `modules/produtos`, `busca` | `acme/vitrine-web` | CAT | `C0ACMECAT01` | `S0ACMECAT01` (@catalogo-team) |
| identidade | `modules/contas`, `sessoes` | `acme/sdk-identidade` | IDN | `C0ACMEIDN01` | `S0ACMEIDN01` (@identidade-team) |
| plataforma | `modules/observabilidade`, `fila`, Dockerfiles, CI, infra e o compartilhado | — | PLAT | `C0ACMEPLAT1` | `S0ACMEPLAT1` (@plataforma-team) |

```
examples/monorepo-acme/
├── sentinela.yaml                  # principal: política da organização (administradores)
├── config.d/teams/*.yaml           # um arquivo por time (cada time é dono do seu)
├── .github/CODEOWNERS              # quem aprova cada arquivo da config
├── .github/workflows/sentinela-config.yml   # validate --offline no PR, com comentário
├── .env.example                    # segredos do modo completo (nomes reais das variáveis)
└── repos/                          # árvore falsa mínima dos repos analisados
    ├── plataforma/                 # o monorepo, com o CODEOWNERS dele em .github/
    ├── vitrine-web/
    └── sdk-identidade/
```

Conferir (sem Linear, Slack nem GitHub):

```bash
sentinela --config examples/monorepo-acme/sentinela.yaml config validate --offline
sentinela --config examples/monorepo-acme/sentinela.yaml owners explain acme/plataforma modules/cobranca/fatura.go \
  --codeowners examples/monorepo-acme/repos/plataforma/.github/CODEOWNERS
```

O teste `TestAcmeExampleValidates` (internal/cli) valida este cenário com Linear, Slack e GitHub
falsos e confere o roteamento dos caminhos do guia. Se o código mudar e o cenário ficar
desatualizado, o teste falha.
