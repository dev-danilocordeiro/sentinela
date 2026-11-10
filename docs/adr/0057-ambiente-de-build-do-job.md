# 0057: Ambiente de build do job para Gradle e Node, e arquivos opcionais no HOME do job

## Contexto

D-09 e D-21 da revisão de prontidão.

- **JVM:** o job no host recebia só a lista fechada de variáveis (ADR 0049). Faltavam o
  `JAVA_HOME`, que o Gradle e o Maven usam para achar o JDK, e o `GRADLE_OPTS`. Sem
  `-Dorg.gradle.daemon=false`, o daemon do Gradle sai do grupo de processos do job, sobrevive ao
  fim dele e acumula memória a cada job.
- **Registro privado:** repos de empresa costumam baixar dependências de um registro privado
  (Artifactory, Nexus, npm privado). A credencial fica no `~/.npmrc`, no
  `~/.gradle/gradle.properties` ou no `~/.m2/settings.xml` da máquina. O HOME do job tem só o
  login do Claude Code, então esses builds falham no job.

## Decisão

- **Variáveis:**
  - no runtime `host`, o job recebe o `JAVA_HOME` do worker (definido pelo `sentinela-run`, pelo
    mise ou pelo ambiente);
  - recebe também `GRADLE_OPTS=-Dorg.gradle.daemon=false`, fixo. O `GRADLE_OPTS` do worker não
    entra, porque pode levar usuário e senha de proxy;
  - no runtime `container`, já era assim (JDK e `GRADLE_OPTS` da imagem; ADR 0053).
- **`execution.home_files`:** lista de caminhos relativos ao HOME da máquina, vazia por padrão.
  - **Cópia:** antes de cada job, cada arquivo que existe é copiado para o mesmo caminho no HOME do
    job, com 0600. O que não existe é pulado.
  - **Manifesto:** um arquivo no HOME do job (`.sentinela-home-files`) lista o que foi copiado, e o
    que sai da lista é removido no job seguinte.
  - **Contêiner:** o HOME do job é montado como HOME do contêiner, então os arquivos valem nos dois
    runtimes.
  - **Recusas da config:** caminho absoluto, `~`, `..`, vazio, e as credenciais que o HOME do job
    existe para não ter: `.ssh`, `.gnupg`, `.gitconfig`, `.git-credentials`, `.config/git`,
    `.config/gh`, `.config/sentinela`, `.claude`, `.claude.json`, `.docker`, `.kube`, `.aws` e
    `.netrc`.

## Risco

- **O job lê esses arquivos.** Ele roda Bash guiado por conteúdo de fora (código, issue,
  review). O token do registro pode sair pela rede (no runtime `host`, sem restrição de saída) ou
  ir parar num commit. Por isso:
  - só token de leitura do registro, nunca senha pessoal nem token com escrita ou publicação;
  - só quando o build do repo precisar;
  - de preferência no runtime `container` (ADR 0053): a saída de rede fica restrita à lista
    permitida, que precisa ter o registro.
- **A lista de recusas não é completa.** Ela protege dos erros comuns, mas não substitui revisar
  o que se põe na lista.

## Consequências

- **Testes:**
  - `TestJobEnvHasJavaHomeAndGradleWithoutDaemon`, com e sem HOME próprio;
  - `TestJobHomeCopiesTheConfiguredFilesAndRemovesTheOnesTakenOut`;
  - `TestHomeFilesRefuseCredentialsOfTheMachine`.
- **`TestJobProcessGetsOnlyTheAllowedEnvironment`** aceita as duas variáveis novas, e só elas.
- **Docs:** `docs/seguranca.md`, `docs/configuracao.md` e `sentinela.example.yaml`.
