package cli

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dev-danilocordeiro/sentinela/internal/config"
	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// fakeAppAPI responde como a API de GitHub Apps para duas instalações (1 = bot, 2 = revisora),
// conferindo que cada chamada vem com o JWT (rotas /app) ou com o token de instalação.
func fakeAppAPI(t *testing.T, perms map[int]map[string]string, repos map[int][]string, slugs map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		jwt := strings.Count(auth, ".") == 2
		inst := 1
		if strings.Contains(r.URL.Path, "/installations/2") || auth == "tok-2" {
			inst = 2
		}
		switch {
		case r.URL.Path == "/app" && jwt:
			// o iss do JWT diz qual app é; os testes usam app id 11 (bot) e 22 (revisora)
			payload, _ := jsonPart(auth)
			_ = json.NewEncoder(w).Encode(map[string]any{"slug": slugs[payload["iss"].(string)]})
		case strings.HasSuffix(r.URL.Path, "/access_tokens") && r.Method == http.MethodPost && jwt:
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok-" + string(rune('0'+inst)), "expires_at": time.Now().Add(time.Hour), "permissions": perms[inst], "repository_selection": "selected"})
		case strings.HasPrefix(r.URL.Path, "/app/installations/") && jwt:
			_ = json.NewEncoder(w).Encode(map[string]any{"id": inst, "account": map[string]any{"login": "acme"}, "permissions": perms[inst]})
		case r.URL.Path == "/installation/repositories" && strings.HasPrefix(auth, "tok-"):
			var rs []map[string]string
			for _, n := range repos[inst] {
				rs = append(rs, map[string]string{"full_name": n})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": len(rs), "repositories": rs})
		default:
			http.Error(w, "inesperado "+r.Method+" "+r.URL.Path, http.StatusUnauthorized)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func jsonPart(jwt string) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	b, err := base64Raw(parts[1])
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, json.Unmarshal(b, &m)
}

func writeKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "app.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigValidateChecksBothGitHubAppsTokensPermissionsAndRepos(t *testing.T) {
	cfg, err := config.Load(rootConfig)
	if err != nil {
		t.Fatal(err)
	}
	var all []string
	for _, r := range cfg.Repos {
		all = append(all, r.Name)
	}
	apps := fakeAppAPI(t,
		map[int]map[string]string{
			1: {"contents": "write", "pull_requests": "write", "metadata": "read", "checks": "read", "statuses": "read"},
			2: {"contents": "write", "pull_requests": "write", "metadata": "read"}, // revisora com contents:write
		},
		map[int][]string{1: all[1:], 2: all}, // o bot não está no primeiro repo
		map[string]string{"11": "sentinela-bot-exemplo", "22": "outra-app"},
	)
	env := envFor(mockLinear(t, allProjects(t)), mockGitHub(t))
	env["GITHUB_APPS_API_URL"] = apps.URL
	key := writeKey(t)
	env["GITHUB_BOT_APP_ID"], env["GITHUB_BOT_INSTALLATION_ID"], env["GITHUB_BOT_PRIVATE_KEY_PATH"] = "11", "1", key
	env["GITHUB_REVIEWER_APP_ID"], env["GITHUB_REVIEWER_INSTALLATION_ID"], env["GITHUB_REVIEWER_PRIVATE_KEY_PATH"] = "22", "2", key

	out, err := run(t, env, "config", "validate")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("esperava ErrValidation, veio %v\n%s", err, out)
	}
	for _, want := range []string{
		"ok: bot sentinela-bot-exemplo: permissões efetivas checks:read, contents:write, metadata:read, pull_requests:write, statuses:read",
		"github_apps.bot (sentinela-bot-exemplo): sem acesso ao repo " + all[0],
		`github_apps.reviewer: a chave é da app "outra-app", a config espera "sentinela-reviewer-exemplo"`,
		"github_apps.reviewer (outra-app): permissão demais contents:write",
		"3 erro(s)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("saída sem %q:\n%s", want, out)
		}
	}
}

func TestConfigValidateWarnsWhenAppsAreNotConfigured(t *testing.T) {
	out, err := run(t, envFor(mockLinear(t, allProjects(t)), mockGitHub(t)), "config", "validate")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(out, "github_apps.bot (sentinela-bot-exemplo): GITHUB_BOT_APP_ID") {
		t.Errorf("faltou o aviso das apps:\n%s", out)
	}
}

func base64Raw(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

type fakeDecisions []db.AgentDecisionsRow

func (f fakeDecisions) AgentDecisions(context.Context) ([]db.AgentDecisionsRow, error) { return f, nil }

// Desfecho decidido por agente viola o princípio 6: o validate avisa (ADR 0022).
func TestValidateWarnsAboutAgentDecisions(t *testing.T) {
	var rep Report
	validateDecisions(context.Background(), fakeDecisions{{ID: 7, Kind: "rejected", Repo: "r/x", Title: "Achado"}}, &rep)
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "outcome 7") || !strings.Contains(rep.Warnings[0], "princípio 6") {
		t.Errorf("avisos = %v", rep.Warnings)
	}
	rep = Report{}
	validateDecisions(context.Background(), fakeDecisions{}, &rep)
	if len(rep.Warnings) != 0 {
		t.Errorf("sem decisões de agente, sem aviso: %v", rep.Warnings)
	}
}
