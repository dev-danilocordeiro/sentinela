# ADR 0025: Candidatos por localização no código, antes de embeddings

- Status: aceita
- Data: 2026-10-10

## Contexto

A similaridade entre achados é léxica: o `HashEmbedder` faz feature hashing de 1024 posições
(ADR 0011). Duas descrições do mesmo problema com palavras diferentes ficam longe uma da outra.
Nas rodadas 7 e 8 do `app-java-angular`, o Judge descartou "Unifique a fonte de verdade das URLs
entre memória e Redis" e, uma rodada depois, publicou "Persista as URLs curtas no Redis em vez da
memória do processo" (EX-9, ADR 0015). O mesmo problema voltou ainda na rodada 3 e na 11, cada vez
com outras palavras.

O roadmap propunha embeddings semânticos locais (`nomic-embed-text` via Ollama) e a migração da
coluna para `vector(768)`. O dono do projeto decidiu não adotar embeddings por enquanto, sem
mudança no esquema do vetor.

Os achados reescritos mudam as palavras, mas não mudam o lugar. As quatro versões do problema da
EX-9 apontam para o mesmo `UrlShortenerService.java` e o mesmo `docker-compose.yml`.

## Decisão

- **Busca de candidatos por localização**, em código determinístico, antes do Judge. Ela
  complementa a léxica, que continua igual: o pgvector com o `HashEmbedder`, as decisões anteriores
  do Judge e a sobreposição com abertos (ADR 0016).
- **Universo de candidatos**, só do mesmo repo e do mesmo módulo do achado novo:
  - achados abertos e em execução;
  - recusados (`rejected`);
  - suprimidos: achados com supressão ativa ligada a eles ou ao fingerprint deles;
  - mesclados: duplicatas marcadas por humano (`merged_into`) e mesclas do Judge em `run_items`;
  - descartados pelo Judge em rodadas anteriores (`run_items`).
- **Localização de cada evidência:**
  - arquivo e linha, com a linha relocada pelo trecho (`snippet`) quando o arquivo mudou desde o
    achado antigo e o trecho ainda está nele;
  - símbolo: a função ou o método que envolve a linha no checkout. A heurística é por indentação
    e declaração, e cobre Go, Java, Kotlin, C#, TypeScript, JavaScript e Python;
  - evidência na linha 0 ou 1 vale pelo arquivo inteiro: casa por arquivo, nunca por linha ou
    símbolo.
- **Pontuação de cada par de evidências** (achado novo × candidato); vale o maior tipo:
  - 4, mesmo símbolo no mesmo arquivo;
  - 3, linhas a até `discovery.location_line_window` (padrão 10) no mesmo arquivo;
  - 2, mesmo símbolo em outro arquivo do módulo, exceto nomes genéricos (`main`, `init`, `run`,
    `get`, `set`, `handle`, `execute`, `toString`, `equals`, `hashCode`, `String`, `Error`);
  - 1, mesmo arquivo.

  A nota do candidato é a soma, por evidência do achado novo, do melhor par.
- **O que vai ao Judge:**
  - os `discovery.location_candidates` (padrão 5) de maior nota; o empate é decidido pelo estado
    (recusado, suprimido, descartado, mesclado e só depois aberto) e pelo fingerprint;
  - um candidato que já está em `similar` ganha o campo `location` com os motivos ("mesmo
    símbolo deleteUrl", "linhas próximas", "mesmo arquivo");
  - os demais entram em `similar` com similaridade 0 e `location` preenchido;
  - `location_candidates: 0` desliga a busca.
- **O código não decide por localização.** Mesmo lugar não é mesmo problema: as EX-5 e EX-7 se
  sobrepõem no mesmo service sem ser o mesmo achado (ADR 0016). A localização só garante que o
  Judge veja a recusa anterior; descartar ou mesclar continua com ele.
- **Prompt do Judge v7:** explica `location`, o estado `suppressed`, e manda descartar de novo o
  achado que é o mesmo problema de um candidato recusado, suprimido ou descartado, mesmo com
  palavras diferentes. O schema de entrada ganha `location` (lista de texto) em cada `similar`.
  O contrato `Finding` e o esquema do banco não mudam.
- **Mesclado no próprio achado não é similar:** em nenhuma das vias (localização, pgvector e
  decisões anteriores do Judge) entra em `similar` um achado mesclado no achado em julgamento,
  direta ou indiretamente.
  - O grafo de mesclas do repo junta as duplicatas humanas (`merged_into`, que valem primeiro) e
    as mesclas do Judge. Na mescla do Judge vale o absorvedor da direção determinística ou, sem
    direção gravada, o `merge_with`.
  - Um achado A → B → C é parte de C.
  - Mostrado como outro achado, ele é uma parte do próprio achado, e o Judge conclui que o achado
    já está coberto. Visto no rejudge v7 da EX-6 e da EX-7, abaixo.
- **Auditoria:** o `run_items` do Judge grava os fingerprints e os motivos dos candidatos por
  localização, e `Stats.LocationCandidates` conta quantos achados receberam ao menos um.
- **`rejudge`:** usa o checkout em `discovery.workdir` quando existe. Sem checkout, os símbolos
  ficam vazios e vale só arquivo e linha.
- **Embeddings** (Ollama e `nomic-embed-text`, ou outro provedor) passam a item opcional futuro
  no roadmap.

## Consequências

- Nenhuma chamada de API e nenhuma dependência nova. O custo é ler os arquivos das evidências do
  checkout, uma vez por arquivo por rodada.
- O contexto do Judge cresce até 5 similares por achado, só quando há candidatos no mesmo lugar.
- Paráfrases em lugares diferentes do código continuam dependendo da similaridade léxica. É o caso
  que um embedding semântico pegaria, e fica para o item opcional.
- Em arquivos grandes, muitos achados compartilham o arquivo, e o "mesmo arquivo" vira ruído. A
  nota baixa dele e o teto de candidatos limitam isso; símbolo e linhas próximas pesam mais.

## Verificação

Os testes de `internal/discovery/location_test.go` usam os textos reais da EX-9 e o checkout real
do `app-java-angular` no SHA das rodadas 7 a 10, com os nomes neutralizados.

- **Rodada 8:** o descarte da rodada 7 entra em `similar` como `discarded`, com "mesmo arquivo" no
  service e no `docker-compose.yml`, em primeiro lugar entre os candidatos. A similaridade léxica
  do par é 0,447, abaixo do limiar de duplicata (0,92).
- **Rodada 11:** a terceira versão tem outro fingerprint, então a supressão da EX-9 não a barra. A
  EX-9 recusada entra como `suppressed`. A similaridade léxica com a EX-9 é 0,559 e com o descarte
  da rodada 7 é 0,455.
- **Símbolos:** as variantes de "link excluído servido pelo cache" casam com a EX-6 por
  `getOriginalUrl` e `deleteUrl` em linhas diferentes, e ficam à frente de um achado que só
  compartilha o arquivo.

Esses pares passam de 0,3, então hoje já chegariam ao Judge também pela memória de descartes da
ADR 0015, que não existia na rodada 8. A localização acrescenta um caminho que não depende das
palavras: uma reescrita com vocabulário diferente o bastante para ficar abaixo de 0,3 continua
chegando ao Judge, desde que aponte para o mesmo código.

No banco de desenvolvimento, a query de candidatos devolve a EX-9 como recusada e suprimida.
