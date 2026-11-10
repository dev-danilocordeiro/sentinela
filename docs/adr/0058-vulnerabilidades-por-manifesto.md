# 0058: Vulnerabilidades de dependência por lockfile ou manifesto, com o osv-scanner

## Contexto

A ADR 0033 transformou as vulnerabilidades do govulncheck num achado por `go.mod`. Fora de Go,
não havia nada. No teste num monorepo Java e TypeScript:

- o `npm audit --omit=dev` acusava 4 pacotes de produção vulneráveis (`next`, `postcss`, `sharp`,
  `source-map-js`) e o Sentinela não deu nenhum sinal;
- as dependências Maven não eram conferidas por ninguém.

O osv-scanner lê os lockfiles e manifestos de vários ecossistemas (npm, pnpm, yarn, Maven, Gradle
com lockfile, Python, Rust e outros) com um formato só. Ao contrário do govulncheck, ele não diz se
o código chama o trecho vulnerável.

## Decisão

- **Scanner `osv-scanner`** (v2.6.0, fixa na doc de instalação), em `discovery.scanners`:
  - comando: `osv-scanner scan source -r --no-ignore --experimental-exclude node_modules
    --experimental-exclude .git --all-packages --format json .`;
  - `--no-ignore` porque, na v2.6.0, sem ela a varredura de um clone git para no primeiro
    diretório ("No package sources found");
  - `--all-packages` para a cobertura contar os manifestos lidos, inclusive os sem vulnerabilidade;
  - cada vulnerabilidade de cada pacote vira um sinal com `Vuln` (manifesto, ecossistema, versão,
    a menor versão corrigida acima da atual, severidade e se é só de desenvolvimento ou teste);
  - pacotes de Go ficam fora: o govulncheck cobre, com o alcance da chamada.
- **`scan.Vuln.GoMod` vira `Manifest`**, com `Ecosystem`, `Severity` e `Dev`. O achado de Go
  continua igual (ADR 0033).
- **Checagem prévia, sem rodar:** o scanner fica `pulado`, com o motivo, sem lockfile nem
  manifesto, ou com Gradle sem `gradle.lockfile`. Sem o lockfile, as versões só sairiam rodando o
  Gradle, que executa código do repo; o motivo pede para ativar o dependency locking.
- **Nota no resultado:** o resumo do scanner diz quantos pacotes saíram sem versão resolvida. No
  Maven, é a versão gerenciada pelo parent ou por um BOM que o osv-scanner não resolveu. Esses
  pacotes não são conferidos.
- **Um achado por lockfile ou manifesto** (`signals/code/vulns_osv.go`):
  - **entram:** as vulnerabilidades de produção com versão corrigida e severidade alta ou crítica;
  - **vão como resumo no corpo:** as outras (moderadas e baixas, só de desenvolvimento ou teste, ou
    sem correção);
  - **sem nenhuma que entre:** não há achado, e o motivo fica em `run_items`;
  - **título:** "Atualiza X para A, Y para B por N vulnerabilidade(s) alta(s) ou crítica(s) em
    <dir>";
  - **severidade:** high (critical se alguma for crítica);
  - **evidência:** a linha de cada dependência no lockfile ou manifesto;
  - **correção:** por ecossistema (npm: `npm install`, `npm update` ou `overrides`; Maven: subir no
    `pom.xml` ou no parent/BOM);
  - **critérios:** o osv-scanner não lista os IDs, e o lockfile resolve as versões novas.
- **Fingerprint:** inclui as versões alvo, como no Go.
- **`refresh-vuln`:** regenera os achados do osv-scanner com ele.

## Consequências

- **Dados que saem:** o osv-scanner manda nomes e versões das dependências para a API do OSV e
  consulta o Maven Central para os `pom.xml`. Fica em `docs/seguranca.md`.
- **Maven sem versão resolvida não é conferido.** No monorepo do teste, os 26 pacotes dos
  `pom.xml` vieram sem versão (Spring Boot gerencia pelo parent). O resumo do scanner avisa;
  cobrir isso pede um lockfile ou SBOM gerado pelo build, fora da descoberta.
- **Testes:** `TestOSVScannerParsesLockfilesAndManifests`, `TestOSVScannerPreflight` e
  `TestOneFindingPerLockfileWithHighProductionVulnerabilities`, com uma fixture neutra tirada de
  uma saída real.
