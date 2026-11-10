# 0051: Perfis de modelos, com qualidade como padrão

## Contexto

O princípio 3 escolhia o modelo pelo volume da etapa: Haiku na triagem, Sonnet na análise, na
arquitetura e no juiz. Na descoberta, um erro de julgamento não se recupera depois:

- um pacote que a triagem descarta nunca chega à análise;
- um achado que a análise não vê não chega ao Judge nem ao time.

Na execução é o contrário. A issue chega escopada, com critérios de aceite, e quatro revisores e
o approver conferem o builder.

## Decisão

- **Dois perfis em código** (`internal/config/profiles.go`):
  - **qualidade, o padrão:** Opus na triagem, na análise e na arquitetura. Sonnet no Judge, com
    `judge_votes: 3` perto do limiar. Spec writer e executor em Sonnet, sem escalada para Opus.
    Opus na revisão de segurança e no approver. Sonnet nas outras revisões, no curator, nas
    notas, no orquestrador e no shepherd. Na recusa, o fallback é o Sonnet.
  - **economia:** o conjunto anterior, para orçamento ou cota limitados. Haiku na triagem e no
    curator, Sonnet na análise, na arquitetura e no Judge, Opus no executor, na revisão de
    segurança e no approver. Na recusa, o fallback é o Opus.
- **O bloco `models` continua obrigatório e explícito no `sentinela.yaml`:**
  - o perfil é só o ponto de partida;
  - nada é preenchido por baixo dos panos;
  - uma instalação que já tem o bloco não muda de modelo.
- **`init` e `config bootstrap` aceitam `--models qualidade|economia|personalizado`**, com
  qualidade por padrão:
  - o yaml gerado traz o bloco completo, com o perfil de origem num comentário;
  - no `init`, personalizado escreve o bloco do qualidade para a pessoa editar;
  - no `bootstrap`, personalizado mantém os modelos da config base (`--config`).
- **`config models`:**
  - diz de que perfil é o bloco global: qualidade, economia ou personalizado;
  - mostra, por papel, a que perfil o modelo corresponde, ao lado de onde ele foi definido.
- **O exemplo do modo completo** (`sentinela.example.yaml`) segue o perfil qualidade.
- **Ajustes que andam com o perfil:** o perfil também escreve os tetos de contexto, a
  concorrência e o teto da rodada.

  | Chave | qualidade | economia |
  |---|---|---|
  | `discovery.triage_context_tokens` | 150000 | 90000 |
  | `discovery.analysis_context_tokens` | 300000 | 150000 |
  | `llm.claude_code.concurrency` | 3 | 2 |
  | `llm.claude_code.timeout` | 15m | 10m |
  | `budget.per_run_usd` (só `anthropic_api`) | 10 | 5 |

  - **Contexto:** o teto de 90k da triagem existia pela faixa de preço do Haiku acima de 100k
    tokens de prompt. O Opus 5.5 tem janela de 1M e preço único, então a triagem leva mais código
    dos vizinhos.
  - **Concorrência e tempo:** as chamadas ao Opus são mais longas. Uma a mais em paralelo segura o
    tempo da rodada sem mudar a cota consumida, e o teto por chamada sobe junto.
  - **Orçamento:** uma rodada completa no perfil qualidade (≈ US$ 4,20 num repo médio, mais os
    votos do Judge) ficaria perto de abortar com US$ 5.
- **A trava de 100k** (`config validate` e o aviso do scan) só vale quando a triagem roda em
  Haiku, no global, num repo ou num time. Os padrões em Go, para quando a chave falta no yaml,
  continuam os do economia, que valem para os dois perfis.

## Consequências

- **Custo:** na estimativa de três repos reais, a rodada completa na API sai cerca de 2,2× a do
  perfil economia (docs/custos.md). A conta de planejamento do `Sentinela.html`, com 15k tokens por
  pacote, dá 3,5×. A triagem passa a ser a maior parte desse custo. Na assinatura, a cota semanal
  acaba antes.
- **`judge_votes: 3`:** em 2026-10-10, com a mesma entrada, os votos saíram idênticos; a variação
  vinha da entrada. Os votos extras custam pouco (Sonnet, só perto do limiar), mas podem não
  mudar o veredito.
- **Testes:**
  - `TestModelProfiles` confere os dois perfis;
  - `TestInitConfigCarriesTheChosenModelsProfile` e `TestConfigBootstrapWritesTheChosenModelsProfile`
    conferem o yaml gerado;
  - `TestConfigModelsShowsTheProfileOfEachRole` confere a saída do `config models`;
  - `TestTriageContextLimitOnlyAppliesToHaiku` confere a trava de 100k só com Haiku.
