package executor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readEnv roda um Claude Code falso que grava o ambiente e devolve as variáveis.
func readEnv(t *testing.T, r ClaudeRunner) map[string]string {
	t.Helper()
	dump := filepath.Join(t.TempDir(), "env.txt")
	r.Binary = scriptClaude(t, "env > "+dump+"\n"+initLine+"\n"+resultLine)
	if _, err := r.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dump) //nolint:gosec // arquivo do teste
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	return got
}

// D-09 e D-21 (ADR 0057): o job no host recebe o JAVA_HOME do worker e o Gradle sem daemon, que
// sobreviveria ao fim do job fora do grupo de processos; o GRADLE_OPTS do worker não entra (pode
// ter proxy com senha).
func TestJobEnvHasJavaHomeAndGradleWithoutDaemon(t *testing.T) {
	t.Setenv("JAVA_HOME", "/opt/jdk-25")
	t.Setenv("GRADLE_OPTS", "-Dhttp.proxyPassword=segredo")
	t.Setenv("COREPACK_ENABLE_DOWNLOAD_PROMPT", "1")
	real := t.TempDir()
	src := filepath.Join(real, ".claude")
	_ = os.MkdirAll(src, 0o700)
	_ = os.WriteFile(filepath.Join(src, ".credentials.json"), []byte(`{"claudeAiOauth":{"expiresAt":1}}`), 0o600)
	t.Setenv("HOME", real)
	for name, r := range map[string]ClaudeRunner{
		"sem HOME próprio": {},
		"com HOME próprio": {Home: JobHome{Dir: filepath.Join(t.TempDir(), "home"), Source: src}},
	} {
		got := readEnv(t, r)
		if got["JAVA_HOME"] != "/opt/jdk-25" || got["GRADLE_OPTS"] != "-Dorg.gradle.daemon=false" {
			t.Errorf("%s: JAVA_HOME = %q, GRADLE_OPTS = %q", name, got["JAVA_HOME"], got["GRADLE_OPTS"])
		}
		// O corepack não pergunta antes de baixar o pnpm ou o yarn: o job não tem terminal.
		if got["COREPACK_ENABLE_DOWNLOAD_PROMPT"] != "0" {
			t.Errorf("%s: COREPACK_ENABLE_DOWNLOAD_PROMPT = %q", name, got["COREPACK_ENABLE_DOWNLOAD_PROMPT"])
		}
	}
}

// Arquivos opcionais no HOME do job (ADR 0057): copiados da máquina com 0600 antes de cada job;
// os que saem da lista somem do HOME do job; o que falta na máquina é pulado.
func TestJobHomeCopiesTheConfiguredFilesAndRemovesTheOnesTakenOut(t *testing.T) {
	real := t.TempDir()
	for rel, content := range map[string]string{
		".npmrc":                    "//registry.empresa/:_authToken=leitura\n",
		".gradle/gradle.properties": "artifactoryUser=leitura\n",
		".claude/.credentials.json": `{"claudeAiOauth":{"expiresAt":1}}`,
	} {
		p := filepath.Join(real, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil { //nolint:gosec // teste: o arquivo da máquina pode ser legível
			t.Fatal(err)
		}
	}
	dir := filepath.Join(t.TempDir(), "home")
	h := JobHome{Dir: dir, Source: filepath.Join(real, ".claude"), Files: []string{".npmrc", ".gradle/gradle.properties", ".m2/settings.xml"}}
	if _, err := h.prepare(context.Background(), []string{"HOME=" + real, "PATH=/usr/bin"}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".npmrc", ".gradle/gradle.properties"} {
		fi, err := os.Stat(filepath.Join(dir, rel))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s no HOME do job: %v, modo %v", rel, err, fi)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, ".m2/settings.xml")); err == nil {
		t.Error("arquivo que não existe na máquina não aparece no HOME do job")
	}
	h.Files = []string{".gradle/gradle.properties"}
	if _, err := h.prepare(context.Background(), []string{"HOME=" + real, "PATH=/usr/bin"}, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".npmrc")); err == nil {
		t.Error(".npmrc saiu da lista e continua no HOME do job")
	}
	if _, err := os.Stat(filepath.Join(dir, ".gradle/gradle.properties")); err != nil {
		t.Errorf("gradle.properties continua: %v", err)
	}
}
