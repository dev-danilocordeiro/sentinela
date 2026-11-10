# ADR 0036: Regenerar o texto de um achado de vulnerabilidade sem mexer na decisão

- Status: aceita
- Data: 2026-10-11

## Contexto

O achado 31 (acme/go-svc, rodada 25) foi gravado antes do último ajuste de texto da
ADR 0033. Por isso dizia "módulo ." e trazia o resumo das vulnerabilidades só importadas sem a
versão corrigida. Uma nova rodada não o corrige: ela vê o mesmo fingerprint e não grava nada. O
dono pediu para regenerá-lo antes de qualquer publicação, sem alterar o histórico de decisões.

## Decisão

- Novo comando `sentinela refresh-vuln <finding-id>`. Ele roda só o govulncheck, sem modelo e sem
  Linear, no clone e no mesmo commit (`head_sha`) da rodada que gravou o achado. Se o clone
  estiver em outro commit, recusa.
- Reescreve só o conteúdo:
  - título, contexto, evidências, sinais, ação sugerida, critérios de aceite e escopo;
  - o módulo;
  - o embedding.
- **Não muda** estado, roteamento, severidade, confiança, o veredito e o motivo do Judge, nem o
  fingerprint.
- **Recusa** quando:
  - o achado não é de vulnerabilidade;
  - não está `open`, ou foi mesclado;
  - já tem issue (o `UPDATE` confere isso na mesma instrução);
  - o govulncheck de agora gera outro fingerprint, isto é, outras versões alvo. Nesse caso cabe à
    próxima rodada gravar um achado novo, que passa pelo Judge.

## Consequências

- Os pontos de chamada citados podem mudar entre execuções: o govulncheck escolhe um trace
  representativo por vulnerabilidade. As vulnerabilidades e as versões alvo, que definem o
  fingerprint, não mudam.
- Aplicado ao achado 31 em 2026-10-11. Antes da mudança, a linha foi salva fora do git.
