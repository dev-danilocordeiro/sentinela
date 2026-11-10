# ADR 0014: Filtro de vulnerabilidades já corrigidas e premissa de produto no Judge

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-10

## govulncheck
- O adaptador descarta, antes da triagem, as vulnerabilidades cuja `fixed_version` já está coberta
  pelo que o módulo usa. Para a stdlib, vale a maior versão entre a diretiva `go`, a diretiva
  `toolchain` do `go.mod` e a toolchain que rodou o scan (`config.go_version` do stream JSON).
  Para dependências, vale a versão do `require`, ou a do `replace` quando ele troca a versão.
  Vulnerabilidade sem versão corrigida nunca é descartada.
- O `go.mod` é lido com `golang.org/x/mod/modfile`, e as versões são comparadas com `go/version`
  (stdlib, aceita `rc`) e `golang.org/x/mod/semver` (módulos).
- O número de sinais descartados vai em `scanners[].filtered` nas estatísticas da rodada.
- A chave de dedup dos sinais passou a incluir o módulo. A mesma OSV pode afetar a stdlib e um
  `golang.org/x` com correções em versões diferentes (ex.: `net/http` e `x/net/http2`). Com a
  chave antiga, os 5 sinais de `x/net` do lib-go-auth ficavam escondidos atrás dos da stdlib (54
  sinais em vez de 59).
- No lib-go-auth atual (`go 1.27.0`, toolchain local go1.27.0) nada é descartado. As 49
  vulnerabilidades da stdlib são corrigidas no Go 1.27.2, que nem o `go.mod` nem a toolchain usam,
  então continuam sendo sinais reais. O teste usa a saída real e varia o `go.mod` e a toolchain.

## Premissa de produto no Judge
- O Judge (prompt v2) devolve `premise`: a premissa de produto da qual o achado depende, ou "".
  Exemplo: exigir autenticação num app que não tem autenticação. Quando há premissa, o prompt
  manda reduzir a confiança.
- Quem grava o achado acrescenta a premissa ao `context` ("Premissa de produto: ..."), e ela chega
  à issue. O contrato `Finding` não muda; só a saída do Judge ganhou um campo.
