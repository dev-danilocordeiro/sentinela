# ADR 0010: Adaptadores de scanner e mapa de pacotes independentes de linguagem

> Nomes de repositórios, times e issues neutralizados em 2026-10-10 (ex.: `app-java-angular`, `EX-9`). Medições, números, datas e conclusões são de execuções reais e não mudaram.

- Status: aceita
- Data: 2026-10-09

## Decisão
- Pacote = diretório com arquivos de código ou configuração (Go, Java, Kotlin, Python, TS/JS,
  SQL, YAML, Dockerfile...). Isso vale para qualquer linguagem, e o primeiro repo real (app-java-angular) é
  Java mais Angular. Testes, `node_modules`, `vendor`, `target`, gerados, lockfiles e arquivos acima de
  200 KB ficam fora da análise.
- Grafo de dependências léxico por linguagem: imports Go com o prefixo do `go.mod`, imports JVM
  casados pelo sufixo do diretório depois de `/java/` ou `/kotlin/`, imports relativos de TS/JS e
  imports Python. Não compila nada.
- Scanners com flags e formato conferidos rodando cada ferramenta: golangci-lint 2.14
  (`--output.json.path stdout`), gosec v2 (`-fmt json -no-fail`), govulncheck v1.8
  (`-format json`, stream de mensagens `osv`/`finding`), `go test -json`. As fixtures em
  `internal/scan/testdata` são saídas reais. O semgrep 1.180 (instalado num venv em `~/.local`)
  foi conferido do mesmo jeito: `semgrep scan --json --quiet --metrics off --config p/default`
  (`--config auto` exige métricas ligadas).
- Scanner ausente, não aplicável ou que falha não derruba a rodada; o status vai para `stats` e
  para o relatório.
- `go-test` executa código do repo-alvo e fica fora da lista padrão.

## Consequências
No app-java-angular (Java + Angular) só o semgrep se aplica; no lib-go-auth rodam os quatro scanners.
