# ADR 0033: Vulnerabilidades de dependência viram um achado por go.mod

- Status: aceita
- Data: 2026-10-11

## Contexto

Na rodada 24 do acme/go-svc, o govulncheck deu 43 sinais e nenhum virou achado. O filtro
de vulnerabilidades já corrigidas estava certo: o go.mod declarava `go 1.27.0`, e as correções da
stdlib saíram na 1.27.2. Os sinais chegaram aos Triagers:
- os sem posição no código foram para o pacote `.` (go.mod);
- os outros, para os pacotes das chamadas.

Os Triagers procuram bug no código do pacote e descartaram todos. A correção de uma
vulnerabilidade de dependência é subir uma versão no go.mod, e não mudar o pacote. Com isso, a
triagem por pacote é a lente errada para esses sinais.

## Decisão

- **Mais detalhe no sinal:** o adaptador do govulncheck guarda, em `scan.Signal.Vuln`:
  - o OSV e o resumo;
  - o módulo (`stdlib` ou o caminho), a versão em uso e a versão corrigida;
  - o go.mod de origem (monorepo com vários módulos funciona);
  - o nível, lido do primeiro quadro do trace: `called` quando há função (o código chega no
    símbolo vulnerável), `imported` quando há só pacote e `required` quando há só módulo.
- **Fora da triagem:** os sinais com `Vuln` saem da entrada do Triager.
- **Um achado por go.mod, em código e sem LLM** (`code.VulnFindings`), depois do Architect:
  - Entram só as vulnerabilidades com alguma chamada, agrupadas por OSV e módulo. As só
    importadas ou exigidas aparecem como resumo no corpo (módulo, versão, maior versão corrigida e
    IDs), sem gerar ação nem pesar na severidade.
  - Sem nenhuma chamada, não há achado. O `run_item` do `govulncheck` registra o motivo.
  - O alvo de cada módulo é a maior versão corrigida entre as vulnerabilidades chamadas. As ações
    sugeridas são subir a diretiva `go` (e `toolchain`, e a imagem de build) para a stdlib, e
    `go get <módulo>@<versão> && go mod tidy` para dependências. Vulnerabilidade sem correção
    publicada pede mitigação.
  - **Formato:**
    - categoria `security`, severidade `high`, confiança 0,9 (alcançabilidade vista por análise
      estática);
    - evidência principal na linha do go.mod que muda, mais até 5 pontos de chamada no código;
    - `scope_paths` com go.mod e go.sum;
    - critérios de aceite: o govulncheck sem nenhuma das chamadas, e a versão mínima.
  - **Fingerprint:** sha1 de repo, categoria, go.mod e `govulncheck <módulo>@<versão alvo>...`,
    sem a normalização que descarta números. Uma correção que passe a exigir outra versão vira um
    achado novo, e o Judge vê o anterior pela localização (go.mod). Esse é o único achado com
    fingerprint fora da normalização comum.
  - O roteamento é o normal (`ResolveFinding` com o go.mod como evidência principal e os pontos de
    chamada como evidências).
- O achado passa pelo Judge, pela deduplicação e pela publicação como qualquer outro.

## Consequências

- Na rodada 25 do acme/go-svc (modo sombra) saiu um achado: "Atualiza Go para 1.27.2 por
  8 vulnerabilidade(s) chamada(s)". O Judge aprovou com 0,8, e o resumo traz as 13 só
  importadas ou exigidas.
- A triagem por pacote fica mais barata nos repos Go: os sinais do govulncheck saem do contexto.
- O nível `called` depende da análise de alcançabilidade do govulncheck. Um caminho que só existe
  por reflexão não aparece.
