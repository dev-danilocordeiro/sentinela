package scan

import (
	"os"
	"strings"
)

// secretPrefixes e secretSuffixes marcam as variáveis que não vão para os scanners (M10 da revisão
// de 2026-10-11): os segredos do Sentinela que o main carrega do .env (Linear, Slack, GitHub e as
// apps, Anthropic, banco, Sentry, Datadog) e qualquer token, segredo, chave ou senha. Os scanners
// rodam no clone do repo-alvo e não precisam de nenhuma delas; o resto do ambiente (PATH, HOME,
// GO*, proxy) continua, para as ferramentas funcionarem como antes. GOTOOLCHAIN não é mexido.
var (
	secretPrefixes = []string{"GITHUB_", "GH_", "SLACK_", "LINEAR_", "ANTHROPIC_", "SENTRY_", "DD_", "DATABASE_URL", "SENTINELA_", "CLAUDE_CODE_OAUTH"}
	secretSuffixes = []string{"_TOKEN", "_SECRET", "_KEY", "_PASSWORD", "_PRIVATE_KEY_PATH"}
)

// scannerEnv é o ambiente do processo sem as variáveis de segredo.
func scannerEnv(environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if secretName(strings.ToUpper(name)) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

func secretName(name string) bool {
	for _, p := range secretPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

func processEnv() []string { return scannerEnv(os.Environ()) }
