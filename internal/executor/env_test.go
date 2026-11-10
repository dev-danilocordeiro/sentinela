package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// C2 (revisão 2026-10-11): o Claude Code do job recebe só uma lista fechada de variáveis. Os
// segredos do Sentinela (tokens do GitHub, do Slack e do Linear, chaves, DATABASE_URL) não chegam
// ao processo filho, que roda Bash no worktree com conteúdo que vem de fora.
func TestJobProcessGetsOnlyTheAllowedEnvironment(t *testing.T) {
	secrets := map[string]string{
		"GITHUB_TOKEN": "ghp_segredo", "GH_TOKEN": "ghp_segredo2", "SLACK_BOT_TOKEN": "xoxb-segredo",
		"LINEAR_OAUTH_CLIENT_SECRET": "lin-segredo", "LINEAR_API_KEY": "lin_api_segredo", "LINEAR_WEBHOOK_SECRET": "w",
		"GITHUB_WEBHOOK_SECRET": "w2", "GITHUB_BOT_PRIVATE_KEY_PATH": "/x/bot.pem", "GITHUB_REVIEWER_APP_ID": "1",
		"DATABASE_URL": "postgres://u:senha@h/db", "SENTINELA_TEST_DATABASE_URL": "postgres://u:senha@h/t",
		"ANTHROPIC_API_KEY": "sk-ant", "DD_API_KEY": "dd", "SENTRY_AUTH_TOKEN": "sentry", "AWS_SECRET_ACCESS_KEY": "aws",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	t.Setenv("LANG", "pt_BR.UTF-8")
	t.Setenv("LC_ALL", "pt_BR.UTF-8")
	t.Setenv("CLAUDE_CONFIG_DIR", "/tmp/claude-config")
	dump := filepath.Join(t.TempDir(), "env.txt")
	bin := scriptClaude(t, "env > "+dump+"\n"+initLine+"\n"+resultLine)
	if _, err := (ClaudeRunner{Binary: bin}).Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			got[k] = v
		}
	}
	for k := range secrets {
		if _, ok := got[k]; ok {
			t.Errorf("%s chegou ao Claude Code do job", k)
		}
	}
	for _, k := range []string{"PATH", "HOME", "LANG", "LC_ALL", "CLAUDE_CONFIG_DIR"} {
		if got[k] == "" {
			t.Errorf("%s não chegou ao Claude Code do job (precisa para rodar e para o login)", k)
		}
	}
	for k := range got {
		// JAVA_HOME, GRADLE_OPTS (fixo, sem daemon) e o corepack sem pergunta (fixo) são do ambiente
		// de build do job (ADR 0057).
		if !envAllowed(k) && k != "PWD" && k != "SHLVL" && k != "_" && k != "OLDPWD" && k != "JAVA_HOME" && k != "GRADLE_OPTS" && k != "COREPACK_ENABLE_DOWNLOAD_PROMPT" {
			t.Errorf("variável fora da lista chegou ao job: %s", k)
		}
	}
}

// M6 (revisão 2026-10-11): sem usuário separado, o Claude Code do job roda com HOME próprio do
// Sentinela, só com a credencial do Claude Code, e com o git e o gh sem as credenciais da
// máquina. De dentro do job, git push e gh falham, e ~/.ssh não existe; o commit continua
// funcionando, com a identidade do usuário do worker. O push é do Sentinela, fora do job.
func TestJobCannotReachTheMachinesGitAndGHCredentials(t *testing.T) {
	real := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ssh-usado")
	sshCmd := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(sshCmd, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0o700); err != nil { //nolint:gosec // script de teste executável
		t.Fatal(err)
	}
	creds := `{"claudeAiOauth":{"accessToken":"sk-ant-oat-x","refreshToken":"sk-ant-ort-x","expiresAt":4102444800000}}`
	for rel, content := range map[string]string{
		".ssh/id_ed25519": "chave sem passphrase",
		".gitconfig": "[user]\n\tname = Pessoa Dona\n\temail = pessoa@exemplo.com\n" +
			"[credential]\n\thelper = \"!f() { echo username=pessoa; echo password=segredo-do-helper; }; f\"\n" +
			"[core]\n\tsshCommand = " + sshCmd + "\n",
		".config/gh/hosts.yml":      "github.com:\n    oauth_token: gho_segredo\n    user: pessoa\n    git_protocol: https\n",
		".config/sentinela/token":   "segredo",
		".claude/.credentials.json": creds,
		".claude/settings.json":     `{"hooks":{}}`,
	} {
		p := filepath.Join(real, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", real)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(real, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")

	wt := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "init"}, {"remote", "add", "origin", "ssh://git@github.invalid/acme/svc.git"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = wt
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out := filepath.Join(t.TempDir(), "out.txt")
	home := filepath.Join(t.TempDir(), "home")
	bin := scriptClaude(t, `{
echo "HOME=$HOME"
test -e "$HOME/.ssh" && echo SSH_EXISTE
for f in .gitconfig .config/gh .config/sentinela .claude/settings.json; do test -e "$HOME/$f" && echo "EXISTE $f"; done
stat -c 'CRED %a' "$HOME/.claude/.credentials.json"
git push origin HEAD:refs/heads/x >/dev/null 2>&1 && echo PUSH_OK
git push https://github.invalid/acme/svc.git HEAD:refs/heads/x >/dev/null 2>&1 && echo PUSH_HTTPS_OK
printf 'protocol=https\nhost=github.com\n\n' | git credential fill 2>/dev/null | grep -q password && echo CREDENCIAL
if command -v gh >/dev/null; then
  gh auth status >/dev/null 2>&1 && echo GH_STATUS_OK
  gh auth token 2>/dev/null | grep -q gho_ && echo GH_TOKEN
else
  echo SEM_GH
fi
git commit -q --allow-empty -m job && git log -1 --format='AUTOR %an <%ae>'
} > `+out+` 2>&1
`+initLine+"\n"+resultLine)
	runner := ClaudeRunner{Binary: bin, Home: JobHome{Dir: home, Source: filepath.Join(real, ".claude")}}
	if _, err := runner.Run(context.Background(), Invocation{Prompt: "/x", Dir: wt, Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "HOME="+home+"\n") {
		t.Errorf("HOME do job não é o do Sentinela:\n%s", got)
	}
	for _, bad := range []string{"SSH_EXISTE", "EXISTE ", "PUSH_OK", "PUSH_HTTPS_OK", "CREDENCIAL", "GH_STATUS_OK", "GH_TOKEN"} {
		if strings.Contains(got, bad) {
			t.Errorf("de dentro do job: %s\n%s", bad, got)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("o push usou o core.sshCommand do ~/.gitconfig da máquina")
	}
	if !strings.Contains(got, "CRED 600") || !strings.Contains(got, "AUTOR Pessoa Dona <pessoa@exemplo.com>") {
		t.Errorf("o job precisa da credencial do Claude Code (0600) e de commitar com a identidade do worker:\n%s", got)
	}
	if c, _ := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json")); string(c) != creds {
		t.Errorf("credencial no HOME do job = %q", c)
	}
	if strings.Contains(got, "SEM_GH") {
		t.Log("gh fora do PATH: a parte do gh não foi conferida")
	}
}

// Se o Claude Code renovar o login dentro do job, a credencial nova volta para a máquina: com
// refresh token rotativo, a cópia antiga deixaria de valer. Sem login nenhum, o job nem começa.
func TestJobHomeReturnsTheRenewedCredential(t *testing.T) {
	src := t.TempDir()
	old := `{"claudeAiOauth":{"accessToken":"a1","refreshToken":"r1","expiresAt":1000}}`
	renewed := `{"claudeAiOauth":{"accessToken":"a2","refreshToken":"r2","expiresAt":2000}}`
	if err := os.WriteFile(filepath.Join(src, ".credentials.json"), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	home := filepath.Join(t.TempDir(), "home")
	bin := scriptClaude(t, "printf '%s' '"+renewed+"' > \"$HOME/.claude/.credentials.json\"\n"+initLine+"\n"+resultLine)
	runner := ClaudeRunner{Binary: bin, Home: JobHome{Dir: home, Source: src}}
	if _, err := runner.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(src, ".credentials.json"))
	if string(got) != renewed {
		t.Errorf("credencial da máquina = %s, quer a renovada", got)
	}
	if fi, err := os.Stat(filepath.Join(src, ".credentials.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("credencial da máquina sem 0600: %v %v", fi, err)
	}

	empty := ClaudeRunner{Binary: bin, Home: JobHome{Dir: filepath.Join(t.TempDir(), "h"), Source: t.TempDir()}}
	if _, err := empty.Run(context.Background(), Invocation{Prompt: "/x", Dir: t.TempDir(), Timeout: time.Minute}); err == nil || !strings.Contains(err.Error(), "sem login do Claude Code") {
		t.Errorf("sem credencial: erro = %v", err)
	}
}

// Com o HOME do job, o mise (e o asdf) continuam achando as ferramentas instaladas na máquina:
// os shims no PATH do worker leem o diretório de dados do HOME.
func TestJobHomeKeepsTheMachinesToolManagers(t *testing.T) {
	real := t.TempDir()
	for _, d := range []string{".local/share/mise", ".config/mise", ".asdf"} {
		_ = os.MkdirAll(filepath.Join(real, d), 0o700)
	}
	src := filepath.Join(real, ".claude")
	_ = os.MkdirAll(src, 0o700)
	_ = os.WriteFile(filepath.Join(src, ".credentials.json"), []byte(`{"claudeAiOauth":{"expiresAt":1}}`), 0o600)
	env, err := JobHome{Dir: filepath.Join(t.TempDir(), "home"), Source: src}.prepare(context.Background(),
		[]string{"HOME=" + real, "PATH=/usr/bin", "MISE_STATE_DIR=/srv/mise-state"}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"MISE_DATA_DIR": filepath.Join(real, ".local/share/mise"), "MISE_CONFIG_DIR": filepath.Join(real, ".config/mise"),
		"MISE_STATE_DIR": "/srv/mise-state", "ASDF_DATA_DIR": filepath.Join(real, ".asdf"),
	}
	for k, v := range want {
		if got := envValue(env, k); got != v {
			t.Errorf("%s = %q, quer %q", k, got, v)
		}
	}
	if got := envValue(env, "MISE_CACHE_DIR"); got != "" {
		t.Errorf("MISE_CACHE_DIR sem diretório na máquina = %q", got)
	}
}
