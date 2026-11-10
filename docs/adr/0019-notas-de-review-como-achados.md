# ADR 0019: Notas fora do escopo dos reviewers viram achados candidatos

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## Contexto
No PR #3 (EX-12), os reviewers aprovaram a correção e deixaram observações reais fora do escopo,
como corpo de requisição sem `MaxBytesReader`, `err.Error()` vazando para o cliente e falta de
shutdown gracioso. Elas ficaram só no corpo do PR, sem passar pelo Judge e sem chance de virar
issue.

## Decisão
- **Contrato (autorizado pelo dono):**
  - `Finding.source` ganha `review_note`, e `SignalRef.kind` também.
  - `findings.origin_execution_id` liga o achado à execução de origem.
  - `analysis_runs.mode` ganha `review_note`.
  - Migration `00011`, com `down`.
- **Notas:** os itens `(não bloqueia)` e `(fora do escopo)` dos reviews da execução, gravados em
  `reviews.findings`. O prompt dos reviewers passou a pedir `(fora do escopo)`, com arquivo:linha,
  para problemas reais fora do PR.
- **Review Note Interpreter (`internal/agents/notes`):**
  - Modelo: `models.analysis`, porque o volume é baixo e o julgamento importa.
  - Recebe a issue resolvida, as notas e trechos reais do código (±12 linhas em volta de cada
    arquivo:linha citado, no checkout da branch padrão, já com o merge).
  - Devolve `Finding[]` no formato do Analyst, com `signal_refs` apontando as notas.
  - Descarta confirmação, comentário sobre o próprio PR e hipótese condicional.
  - Achado que não cita nota existente é removido em código.
- **Mesmo caminho dos achados de código:**
  - Evidência conferida (`EvidenceChecker`), roteamento por achado (ADR 0017) e fingerprint, via
    `code.Source.FromModel`.
  - Depois, supressões, fingerprint, similaridade, decisões anteriores, sobreposição parcial e Judge
    (`judgeAndStore`).
- **Modos:**
  - **Sombra:** `sentinela notes --execution N` registra a decisão do Judge em `run_items` e não
    grava em `findings`.
  - **Publicação:** `--publish` grava os aprovados com `origin_execution_id`; eles saem na próxima
    publicação.
  - **Worker:** roda automaticamente ao fim de um job com PR, no modo de `execution.review_notes`
    (padrão `shadow`).
- **Rodada `review_note`:** não serve de base para o diff incremental do `scan`.

## Consequências
- Uma nota vira, no máximo, um achado julgado como qualquer outro. Uma nota recusada pelo time vira
  supressão pelo fingerprint, como os achados de código.
- O intérprete custa uma chamada por execução com notas, mais uma chamada do Judge por achado
  candidato.
