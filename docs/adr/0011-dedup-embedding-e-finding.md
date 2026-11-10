# ADR 0011: Deduplicação, embedding local e mudanças de contrato da Fase 2

- Status: aceita
- Data: 2026-10-09

## Decisão
- Rodada incremental: o diff desde o `head_sha` da última rodada concluída define os pacotes.
  Sem commits novos, a rodada é gravada como `no_changes`, sem custo. Esse é o principal
  mecanismo do critério "duas rodadas seguidas não geram achados novos". `--full` força a análise
  completa, e um `head_sha` que sumiu (force push) cai para completa com aviso.
- Antes do Judge: duplicado na própria rodada, supressão (fingerprint ou glob de caminho),
  fingerprint já gravado e similaridade de cosseno ≥ `discovery.similarity_threshold` com achados do
  repo (pgvector, índice HNSW) são pulados com motivo em `run_items`. Os 3 mais similares e até 6
  exemplos decididos por humanos vão para o Judge, que pode mesclar.
- Fingerprint = `sha1(repo|category|arquivo|problem_key normalizado)[:12]`. O Analyst devolve um
  `problem_key` curto e estável; números e pontuação saem na normalização.
- Embedding: a Anthropic não oferece embeddings. Para não exigir outra chave, o `HashEmbedder`
  faz feature hashing de palavras e bigramas (título, `problem_key`, contexto, arquivo, categoria)
  em 1024 dimensões. Pega reformulações com vocabulário parecido; paráfrases distantes ficam para o
  Judge. A interface `Embedder` permite trocar por Voyage depois, sem migration.
- Evidência é verificada no checkout: arquivo do pacote (ou do repo, para o Architect) e linha
  dentro do arquivo. Evidência inválida é descartada; achado sem nenhuma evidência válida é pulado.
- Mudanças de contrato e de esquema, feitas dentro da Fase 2:
  - `Finding.related_modules` para achados entre módulos;
  - `findings.judge_confidence`, `findings.judge_reason` e `findings.related_modules`;
  - tabela `run_items` (desfecho de cada item por agente);
  - `analysis_runs.mode`, `analysis_runs.stats` e o estado `no_changes`.
  Só achados com `publish` e confiança ≥ `min_confidence` entram em `findings` (estado `open`);
  descartes e mesclas ficam em `run_items` com a decisão do juiz.
- A orquestração mora em `internal/discovery`, e a fonte `code_analysis` em `internal/signals/code`,
  que implementa `SignalSource` e o opcional `BatchInterpreter` (o caminho em lote).

## Consequências
Depois da Fase 2, qualquer mudança no `Finding` ou no esquema exige perguntar antes (CLAUDE.md).
