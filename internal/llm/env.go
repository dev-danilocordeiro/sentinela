package llm

import "strings"

// Ambiente dos processos claude -p: a descoberta, as chamadas ao MCP e o Claude Code do job
// (revisão de 2026-10-11, C2; D-17 da revisão de prontidão). Eles recebem só uma lista fechada de
// variáveis, nunca o ambiente do Sentinela, que tem os tokens do GitHub, do Slack e do Linear, as
// chaves das GitHub Apps e o DATABASE_URL (docs/seguranca.md).
var envAllowList = map[string]bool{
	// Básico para rodar comandos.
	"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TERM": true, "TMPDIR": true, "TZ": true,
	// Idioma.
	"LANG": true, "LANGUAGE": true,
	// Onde o Claude Code guarda o login da assinatura (em HOME, ou nestes diretórios) e o login
	// por token de assinatura (`claude setup-token`). Nunca ANTHROPIC_*: com ela, a sessão usaria
	// crédito da API.
	"CLAUDE_CONFIG_DIR": true, "CLAUDE_CODE_OAUTH_TOKEN": true,
	"XDG_CONFIG_HOME": true, "XDG_CACHE_HOME": true, "XDG_DATA_HOME": true, "XDG_STATE_HOME": true, "XDG_RUNTIME_DIR": true,
}

// EnvAllowed diz se a variável passa para um processo claude -p (LC_* entram como idioma).
func EnvAllowed(name string) bool {
	return envAllowList[name] || strings.HasPrefix(name, "LC_")
}

// ClosedEnv filtra o ambiente pela lista fechada.
func ClosedEnv(base []string) []string {
	out := make([]string, 0, len(envAllowList))
	for _, kv := range base {
		if k, _, ok := strings.Cut(kv, "="); ok && EnvAllowed(k) {
			out = append(out, kv)
		}
	}
	return out
}
