package scan

import (
	"context"
	"strings"
	"testing"
	"time"
)

// M10 (revisão 2026-10-11): os scanners não recebem os segredos do Sentinela; o resto do ambiente
// passa.
func TestScannersDoNotGetTheSecrets(t *testing.T) {
	secrets := []string{"GITHUB_TOKEN", "GH_TOKEN", "SLACK_BOT_TOKEN", "LINEAR_OAUTH_CLIENT_SECRET", "LINEAR_WEBHOOK_SECRET",
		"GITHUB_BOT_PRIVATE_KEY_PATH", "GITHUB_WEBHOOK_SECRET", "DATABASE_URL", "ANTHROPIC_API_KEY", "SENTRY_AUTH_TOKEN",
		"DD_API_KEY", "DD_APP_KEY", "SENTINELA_TEST_DATABASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "OUTRO_PASSWORD"}
	for _, s := range secrets {
		t.Setenv(s, "segredo-"+s)
	}
	t.Setenv("GOFLAGS", "-mod=mod")
	t.Setenv("GOPROXY", "https://proxy.golang.org")
	out, err := runTool(context.Background(), 10*time.Second, t.TempDir(), "env", nil)
	if err != nil {
		t.Fatal(err)
	}
	env := string(out)
	for _, s := range secrets {
		if strings.Contains(env, "segredo-"+s) {
			t.Errorf("%s chegou ao scanner", s)
		}
	}
	for _, want := range []string{"PATH=", "GOFLAGS=-mod=mod", "GOPROXY=https://proxy.golang.org"} {
		if !strings.Contains(env, want) {
			t.Errorf("%s não chegou ao scanner", want)
		}
	}
}
