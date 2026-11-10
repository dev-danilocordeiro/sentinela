package executor

import (
	"os"
	"strings"

	"github.com/dev-danilocordeiro/sentinela/internal/llm"
)

// O Claude Code do job recebe só a lista fechada de variáveis de llm.ClosedEnv (C2 da revisão de
// 2026-10-11): ele roda Bash no worktree, com conteúdo que vem de fora.

// envAllowed diz se a variável passa para o Claude Code do job.
func envAllowed(name string) bool { return llm.EnvAllowed(name) }

// jobEnv filtra o ambiente pela lista fechada.
func jobEnv(base []string) []string { return llm.ClosedEnv(base) }

// JobEnv é o ambiente que o Claude Code do job recebe (para o status e os testes).
func JobEnv() []string { return jobEnv(os.Environ()) }

// corepackNoPrompt desliga a pergunta do corepack antes de baixar o pnpm ou o yarn do
// packageManager: o job não tem terminal (https://github.com/nodejs/corepack#environment-variables).
const corepackNoPrompt = "COREPACK_ENABLE_DOWNLOAD_PROMPT=0"

// withToolEnv acrescenta ao ambiente do job no host o que os builds de JVM precisam (ADR 0057):
// o JAVA_HOME do worker (o mise e o sentinela-run o definem) e o Gradle sem daemon, que
// sobreviveria ao fim do job fora do grupo de processos (D-21). O GRADLE_OPTS do worker não
// entra: pode levar proxy com senha.
func withToolEnv(env, base []string) []string {
	out := make([]string, 0, len(env)+2)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); k != "JAVA_HOME" && k != "GRADLE_OPTS" && k != "COREPACK_ENABLE_DOWNLOAD_PROMPT" {
			out = append(out, kv)
		}
	}
	if v := envValue(base, "JAVA_HOME"); v != "" {
		out = append(out, "JAVA_HOME="+v)
	}
	return append(out, "GRADLE_OPTS=-Dorg.gradle.daemon=false", corepackNoPrompt)
}
