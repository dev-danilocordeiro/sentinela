# Dois cenários completos

### Empresa pequena: um time, um board, um canal

Dez pessoas desenvolvedoras, um time de engenharia, um board no Linear (ENG), um canal no Slack (`#engenharia`) e dois repos. Não há `config.d`: um arquivo só, mantido por quem administra o Sentinela.

**Passo 1. Os admins instalam as integrações** (cerca de uma hora): o app OAuth do Linear, o app do Slack (convidado em `#engenharia`) e as duas GitHub Apps, só nos dois repos. A chave da API vem do Console da empresa. Os detalhes estão em [Uso em empresa](#uso-em-empresa).

**Passo 2. O arquivo inteiro**, com o porquê de cada linha. Ele está em `examples/empresa-pequena/sentinela.yaml`, e um teste automatizado (`TestSmallCompanyExampleValidates`) garante que ele valida.

<!-- guia:annotated examples/empresa-pequena/sentinela.yaml -->

**Passo 3. Confira**, primeiro sem chamar ninguém:

<!-- guia:run --config examples/empresa-pequena/sentinela.yaml config validate --offline -->

Depois, sem `--offline`, com as chaves no `.env`: o `validate` confere o time ENG, o projeto, os estados e as labels no Linear, o bot no canal e as apps nos repos. Crie as quatro labels no time ENG antes.

**Passo 4. Rode em modo sombra** uma semana (`publish.enabled: false`), revise com `sentinela report --run <id>` e, depois, ligue a publicação. Com no máximo 3 issues por rodada, o board não enche.

**Passo 5. O dia a dia** é o de [Dia a dia](#dia-a-dia): o time recusa o que não presta e pede a correção do resto com `sentinela-fix`. Só membros do time ENG podem pedir, por causa do `fix_trigger: team_members`.

Para gerar a sua versão deste arquivo, use o [gerador de config](#gerador-de-config).

### Empresa com 10 times: o monorepo acme

O cenário `examples/monorepo-acme` é o de referência para muitos times. Ele tem 4 times (pagamentos, catálogo, identidade e plataforma); 10 times é o mesmo formato, com mais arquivos. O que ele mostra:

- **Um monorepo**, `acme/plataforma`, com 10 módulos em `modules/<nome>` e CODEOWNERS por módulo:
  - pagamentos tem 4 módulos e `libs/dinheiro`;
  - catálogo e identidade têm 2 cada;
  - a plataforma tem 2, mais os Dockerfiles, o CI e a infra.
- **Repos exclusivos:** `acme/vitrine-web` (catálogo) e `acme/sdk-identidade` (identidade).
- **Canal e grupo próprios** para cada time no Slack.
- **Pagamentos com 5 projetos no Linear:** um por módulo e um "Pagamentos · Segurança" para os achados de segurança.
- **Arquivos próprios:** o principal é dos administradores, cada arquivo de time é do time dele, e as mudanças entram por PR.

Arquivo por arquivo, na ordem em que alguém montaria:

<!-- guia:tabs acme -->
### 1. sentinela.yaml
O principal, dos administradores: adapters, modelos, custos, padrões, labels, `fix_trigger`, `blocked_paths`, a auto-aprovação por time e o monorepo compartilhado.

<!-- guia:annotated examples/monorepo-acme/sentinela.yaml -->
### 2. pagamentos
O time com mais regras: um projeto por módulo, segurança num projeto à parte, uma label de gatilho própria e um limite de PR mais apertado.

<!-- guia:annotated examples/monorepo-acme/config.d/teams/pagamentos.yaml -->
### 3. catálogo
Não lista os módulos do monorepo: o CODEOWNERS já diz que são dele, e eles usam o projeto do time. Declara o repo exclusivo.

<!-- guia:annotated examples/monorepo-acme/config.d/teams/catalogo.yaml -->
### 4. identidade
Responsável em rodízio e uma label de falha própria.

<!-- guia:annotated examples/monorepo-acme/config.d/teams/identidade.yaml -->
### 5. plataforma
Dona do que é transversal. O Dockerfile de qualquer lugar é dela, com `override: true`.

<!-- guia:annotated examples/monorepo-acme/config.d/teams/plataforma.yaml -->
### 6. CODEOWNERS do monorepo
O passo 2 do roteamento. A última regra que casa vence; `/libs/comum/` tem dois donos, e `/docs/rascunhos/` não tem nenhum.

<!-- guia:annotated examples/monorepo-acme/repos/plataforma/.github/CODEOWNERS -->
### 7. CODEOWNERS do repo da config
Quem aprova cada arquivo da config: o time aprova o arquivo dele, e o principal é dos administradores.

<!-- guia:annotated examples/monorepo-acme/.github/CODEOWNERS -->
### 8. workflow do PR
Roda o `validate` offline em cada PR e comenta o roteamento antes e depois.

<!-- guia:file examples/monorepo-acme/.github/workflows/sentinela-config.yml yaml -->
<!-- guia:end -->

**Conferindo o cenário inteiro.** O `validate --offline` mostra os times de cada arquivo e o projeto, o canal, a menção e o gatilho efetivos de cada um, com a fonte:

<!-- guia:run --config examples/monorepo-acme/sentinela.yaml config validate --offline -->

**Um caminho de cada vez.** O caso mais rico, um módulo de pagamentos com projeto próprio e um projeto por categoria:

<!-- guia:run --config examples/monorepo-acme/sentinela.yaml owners explain acme/plataforma modules/cobranca/fatura.go --codeowners examples/monorepo-acme/repos/plataforma/.github/CODEOWNERS -->

Os outros casos (Dockerfile, arquivo compartilhado, caminho sem dono, repo exclusivo, achado que cruza times) estão no simulador logo abaixo.
