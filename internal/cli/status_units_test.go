package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeSystemctl responde como o systemctl para units de usuário (--user) ou de sistema.
func fakeSystemctl(scope, home string) func(context.Context, ...string) (string, error) {
	return func(_ context.Context, args ...string) (string, error) {
		user := len(args) > 0 && args[0] == "--user"
		if user {
			args = args[1:]
		}
		if (scope == "user") != user || scope == "" {
			if len(args) > 0 && args[0] == "show" {
				return "not-found", nil
			}
			return "inactive", errors.New("exit 3")
		}
		switch {
		case args[0] == "show" && strings.Contains(strings.Join(args, " "), "LoadState"):
			return "loaded", nil
		case args[0] == "show":
			return "SENTINELA_HOME=" + home, nil
		case args[0] == "is-active":
			return "active", nil
		case args[0] == "list-timers":
			return "Mon 2026-10-19 07:01:38 -03 2 days left - - sentinela-scan.timer sentinela-scan.service", nil
		}
		return "", nil
	}
}

// D-19 da revisão de prontidão: o status só mostra as units do systemd como desta instalação se o
// SENTINELA_HOME delas é o diretório atual; senão, diz de qual instalação elas são.
func TestStatusShowsOnlyTheUnitsOfThisInstallation(t *testing.T) {
	cases := []struct {
		name, scope, home string
		want, notWant     []string
	}{
		{"units de usuário desta instalação", "user", "/srv/a", []string{"serviços (systemd --user):", "sentinela-worker.service", "active", "próximo scan: Mon 2026-10-19 07:01:38"}, []string{"outra instalação"}},
		{"units de sistema desta instalação", "system", "/srv/a", []string{"serviços (systemd):", "active"}, nil},
		{"units de outra instalação", "user", "/srv/outra", []string{"as units sentinela-* são da instalação em /srv/outra, não desta (/srv/a)"}, []string{"active", "próximo scan"}},
		{"sem units", "", "", []string{"nenhuma unit do systemd desta instalação"}, []string{"active"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			printUnits(context.Background(), &b, "/srv/a", fakeSystemctl(tc.scope, tc.home))
			out := b.String()
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("saída sem %q:\n%s", w, out)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(out, w) {
					t.Errorf("saída não deveria ter %q:\n%s", w, out)
				}
			}
		})
	}
}

// No modo local não há issue: os achados abertos estão no relatório local.
func TestPendingLineDependsOnTheMode(t *testing.T) {
	if got := pendingLine(true, "reports/local", 3); got != "achados abertos no relatório local (reports/local), à espera de decisão: 3" {
		t.Errorf("local: %q", got)
	}
	if got := pendingLine(false, "", 2); !strings.Contains(got, "sem issue") {
		t.Errorf("linear: %q", got)
	}
}

// fakeSystemctlUnits responde com várias units *-worker.service, cada uma de uma instalação.
func fakeSystemctlUnits(homes map[string]string) func(context.Context, ...string) (string, error) {
	return func(_ context.Context, args ...string) (string, error) {
		if len(args) == 0 || args[0] != "--user" {
			if len(args) > 0 && args[0] == "show" {
				return "not-found", nil
			}
			return "", errors.New("exit 1")
		}
		args = args[1:]
		joined := strings.Join(args, " ")
		switch {
		case args[0] == "list-units":
			var names []string
			for u := range homes {
				names = append(names, u+" loaded active running x")
			}
			return strings.Join(names, "\n"), nil
		case args[0] == "show" && strings.Contains(joined, "LoadState"):
			if _, ok := homes[args[1]]; ok {
				return "loaded", nil
			}
			return "not-found", nil
		case args[0] == "show":
			return "SENTINELA_HOME=" + homes[args[1]], nil
		case args[0] == "is-active":
			return "active", nil
		}
		return "", nil
	}
}

// Units com prefixo (deploy/install.sh --prefix, ADR 0056): o status acha as desta instalação
// pelo SENTINELA_HOME, qualquer que seja o nome.
func TestStatusFindsPrefixedUnitsOfThisInstallation(t *testing.T) {
	var b bytes.Buffer
	printUnits(context.Background(), &b, "/srv/ceres", fakeSystemctlUnits(map[string]string{
		"sentinela-worker.service":       "/srv/principal",
		"ceres-sentinela-worker.service": "/srv/ceres",
	}))
	out := b.String()
	for _, w := range []string{"serviços (systemd --user):", "ceres-sentinela-serve.service", "ceres-sentinela-worker.service", "ceres-sentinela-scan.timer"} {
		if !strings.Contains(out, w) {
			t.Errorf("saída sem %q:\n%s", w, out)
		}
	}
	if strings.Contains(out, "não desta") {
		t.Errorf("achou as units desta instalação; não deveria dizer que são de outra:\n%s", out)
	}
}
