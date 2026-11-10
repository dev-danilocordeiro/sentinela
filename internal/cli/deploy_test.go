package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// As units do repo são modelos sem caminho de máquina; o install.sh gera as de sistema (usuário
// de serviço, /opt/sentinela) e as de usuário (diretório configurável) (D-01 da revisão de
// prontidão).
func TestInstallRendersUnitsWithoutMachinePaths(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, u := range []string{"sentinela-serve.service", "sentinela-worker.service", "sentinela-scan.service"} {
		b, err := os.ReadFile(filepath.Join(root, "deploy", "systemd", u))
		if err != nil {
			t.Fatal(err)
		}
		if s := string(b); strings.Contains(s, "%h") || strings.Contains(s, "Documents") || !strings.Contains(s, "@SENTINELA_HOME@") {
			t.Errorf("%s não é um modelo neutro:\n%s", u, s)
		}
	}
	render := func(t *testing.T, env []string, args ...string) string {
		t.Helper()
		dir := t.TempDir()
		cmd := exec.Command("sh", append([]string{filepath.Join(root, "deploy", "install.sh"), "--render", dir}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("install.sh %v: %v\n%s", args, err, out)
		}
		b, err := os.ReadFile(filepath.Join(dir, "sentinela-worker.service"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "@") {
			t.Errorf("sobrou marcador na unit gerada:\n%s", b)
		}
		return string(b)
	}
	sys := render(t, []string{"SENTINELA_HOME=", "SENTINELA_USER="})
	for _, want := range []string{"User=sentinela\n", "Environment=SENTINELA_HOME=/opt/sentinela\n", "ExecStart=/opt/sentinela/deploy/bin/sentinela-run worker\n", "WantedBy=multi-user.target\n"} {
		if !strings.Contains(sys, want) {
			t.Errorf("unit de sistema sem %q:\n%s", want, sys)
		}
	}
	custom := render(t, []string{"SENTINELA_HOME=/srv/sentinela", "SENTINELA_USER=svc-sentinela"})
	if !strings.Contains(custom, "User=svc-sentinela\n") || !strings.Contains(custom, "ExecStart=/srv/sentinela/deploy/bin/sentinela-run worker\n") {
		t.Errorf("unit de sistema configurada:\n%s", custom)
	}
	user := render(t, []string{"SENTINELA_HOME=/srv/u/sentinela"}, "--user")
	if strings.Contains(user, "User=") || !strings.Contains(user, "ExecStart=/srv/u/sentinela/deploy/bin/sentinela-run worker\n") || !strings.Contains(user, "WantedBy=default.target\n") {
		t.Errorf("unit de usuário:\n%s", user)
	}
	cmd := exec.Command("sh", filepath.Join(root, "deploy", "install.sh"), "--render", t.TempDir())
	cmd.Env = append(os.Environ(), "SENTINELA_HOME=relativo")
	if err := cmd.Run(); err == nil {
		t.Error("SENTINELA_HOME relativo deveria falhar")
	}
}

// Segunda instalação na mesma máquina (ADR 0056): --prefix dá outro nome às units (e ao
// SyslogIdentifier), e o install.sh recusa sobrescrever uma unit cujo SENTINELA_HOME é de outra
// instalação; a da mesma instalação é regravada.
func TestInstallPrefixAndRefusalToOverwriteAnotherInstallation(t *testing.T) {
	root := filepath.Join("..", "..")
	install := func(dir string, env []string, args ...string) (string, error) {
		cmd := exec.Command("sh", append([]string{filepath.Join(root, "deploy", "install.sh"), "--render", dir}, args...)...)
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	dir := t.TempDir()
	if out, err := install(dir, []string{"SENTINELA_HOME=/srv/ceres"}, "--user", "--prefix", "ceres-sentinela"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	for _, u := range []string{"ceres-sentinela-serve.service", "ceres-sentinela-worker.service", "ceres-sentinela-scan.service", "ceres-sentinela-scan.timer"} {
		if _, err := os.Stat(filepath.Join(dir, u)); err != nil {
			t.Errorf("sem %s: %v", u, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "sentinela-worker.service")); err == nil {
		t.Error("com --prefix, não gera as units sentinela-*")
	}
	b, _ := os.ReadFile(filepath.Join(dir, "ceres-sentinela-worker.service"))
	if !strings.Contains(string(b), "SyslogIdentifier=ceres-sentinela-worker\n") || !strings.Contains(string(b), "Environment=SENTINELA_HOME=/srv/ceres\n") {
		t.Errorf("unit com prefixo:\n%s", b)
	}

	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "sentinela-worker.service"), []byte("[Service]\nEnvironment=SENTINELA_HOME=/srv/principal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := install(other, []string{"SENTINELA_HOME=/srv/ceres"}, "--user")
	if err == nil || !strings.Contains(out, "é da instalação em /srv/principal") || !strings.Contains(out, "--prefix") {
		t.Errorf("deveria recusar sobrescrever a unit de outra instalação: %v\n%s", err, out)
	}
	if b, _ := os.ReadFile(filepath.Join(other, "sentinela-worker.service")); !strings.Contains(string(b), "/srv/principal") {
		t.Error("a unit da outra instalação não pode mudar")
	}
	if out, err := install(other, []string{"SENTINELA_HOME=/srv/principal"}, "--user"); err != nil {
		t.Errorf("a mesma instalação regrava as próprias units: %v\n%s", err, out)
	}
	if out, err := install(t.TempDir(), nil, "--user", "--prefix", "Ceres Sentinela"); err == nil {
		t.Errorf("prefixo inválido deveria falhar:\n%s", out)
	}
}
