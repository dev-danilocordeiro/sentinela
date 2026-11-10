# ADR 0016: Evidência validada em código e sobreposição parcial entre achados

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## Contexto
Duas issues publicadas na Fase 3 mostraram problemas de qualidade que o Judge sozinho não barrou:

- **EX-9 (evidência inventada).** O System Architect citou `UrlShortenerService.java:1` e
  `docker-compose.yml:1` com uma descrição no lugar do código ("redis com persistência AOF
  habilitada"). O prompt dele mandava usar a linha 1 "se o problema é o arquivo como um todo", e
  ele só via resumos dos pacotes, nunca código. A validação existente só conferia arquivo e linha.
- **EX-5 e EX-7 (sobreposição parcial).** O critério 2 da EX-7 ("o número de ClickEvent por
  URL respeita um limite configurável") é a EX-5 inteira. Os achados não eram duplicados, então
  nem o fingerprint, nem o pgvector, nem o merge do Judge pegaram: a parte repetida foi publicada
  duas vezes.

## Decisão: evidência
- O código confere cada evidência no checkout do SHA analisado, antes do Judge
  (`code.EvidenceChecker`, usado ao montar o `Finding`):
  - o caminho é relativo e fica dentro do repositório;
  - o arquivo existe;
  - a linha está dentro do arquivo;
  - o snippet aparece no arquivo até 5 linhas da linha informada, ignorando espaços em branco.
    Reticências (`...`) dividem o snippet em partes, e cada parte precisa aparecer.
- Evidência inválida avulsa é removida do achado; o motivo fica em `run_items` (`evidence_dropped`).
- Achado sem nenhuma evidência válida é descartado com o motivo `evidência inválida: <detalhes>`.
- A validação acontece antes do roteamento, então o dono vem da primeira evidência válida.
- Os contadores `invalid_evidence` e `evidence_dropped` aparecem nas estatísticas, no resumo do
  Slack e no relatório.
- O Architect passa a receber `landmarks` por pacote: linhas reais (declarações primeiro, depois
  imports, até 40 por pacote e 1500 na rodada) com arquivo e linha. O prompt v2 exige que a
  evidência seja um landmark copiado literalmente. Sem isso, a validação descartaria todo achado
  dele.

## Decisão: sobreposição parcial
1. **Detecção (código, léxica).** Antes do Judge, o achado novo é comparado com os achados
   `open` e `executing` do repo:
   - um critério de aceite conta como coberto quando ao menos 3 radicais dele (5 primeiras letras,
     camelCase separado, sem stopwords) aparecem no título ou nos critérios do existente e somam
     ao menos 50% dos radicais do critério;
   - uma evidência conta como sobreposta quando aponta para o mesmo arquivo a até 3 linhas.
   O existente entra em `similar` com `issue`, `acceptance_criteria`, `evidence` e `overlap`,
   mesmo fora dos 3 mais próximos no pgvector. Isso é só pista.
2. **Julgamento (Judge, prompt v4).** Se o que sobra justifica uma issue, o Judge devolve
   `narrowed` (título, contexto, correção e critérios sem a parte coberta) e `related_to` (os
   fingerprints sobrepostos). Se não sobra nada, devolve `merge`.
3. **Trava (código).** Depois do Judge, um critério que ainda coincide de forma forte com um
   aberto é removido, e o aberto entra em `related_to`. Coincidência forte é 70% dos radicais, com
   no mínimo 3. Sem critério restante, o achado vira `merge` (duplicado).
   - A regra forte foi calibrada nas propostas reais do app-java-angular (rodadas 3 a 10). As 12
     coincidências fortes eram variantes das EX-5 a EX-9. Os falsos positivos da regra fraca,
     como "excluir uma URL remove seus eventos de clique" contra a EX-7, ficaram abaixo de 0,7.
   - A regra fraca nunca remove nada sozinha: não desfaz um julgamento com uma heurística léxica.
4. **Publicação.**
   - O `related_to` fica na decisão do Judge em `run_items`, então não houve mudança de esquema
     nem do contrato `Finding`.
   - O publisher lê o `related_to`, linka a issue nova às existentes como `related` e lista as
     existentes numa seção "Fora do escopo" do corpo.
   - A issue existente não é alterada.

## Consequências
- A EX-7 continua como está no Linear, por decisão do dono. A regra vale para os próximos achados.
- A detecção é léxica, como o `HashEmbedder`: paráfrases sem palavras em comum dependem do Judge.
  Os limiares (3 radicais e 50% para a pista, 70% para a trava) estão em `overlap.go`, com a
  calibração documentada.
- Um achado reduzido mantém o fingerprint original. Se a mesma proposta voltar, o dedup por
  fingerprint a barra.

## Revisão (2026-10-10, ADR 0019)
A regra forte errava com critérios curtos: "Delete com falha devolve erro" contava como coberto
por "Save com falha devolve erro", porque 3 dos 4 radicais (os genéricos) coincidiam. Critério com
menos de 5 radicais agora só é forte com todos os radicais no achado aberto. As 12 coincidências
fortes das propostas reais do app-java-angular continuam as mesmas.
