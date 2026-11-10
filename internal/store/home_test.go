package store

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// D-06 da revisão de prontidão: o banco guarda o diretório da instalação dona dele. Um banco novo
// é registrado no primeiro uso; um banco de outro diretório é recusado; um banco com histórico e
// sem registro só é adotado com adopt (o serve e o worker da instalação, ou init --adotar-banco).
func TestClaimHomeRefusesTheDatabaseOfAnotherInstallation(t *testing.T) {
	url := os.Getenv("SENTINELA_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("SENTINELA_TEST_DATABASE_URL não definida")
	}
	ctx := context.Background()
	m, err := NewMigrator(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.DownTo(ctx, 0)
		_ = m.Close()
	})
	if _, err := m.DownTo(ctx, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Up(ctx); err != nil {
		t.Fatal(err)
	}
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Banco novo: registra o primeiro diretório, mesmo sem adopt.
	if err := ClaimHome(ctx, s.Q, "/srv/a/sentinela", false); err != nil {
		t.Fatalf("banco novo: %v", err)
	}
	if err := ClaimHome(ctx, s.Q, "/srv/a/sentinela", false); err != nil {
		t.Errorf("mesmo diretório: %v", err)
	}
	err = ClaimHome(ctx, s.Q, "/tmp/clone/sentinela", true)
	if !errors.Is(err, ErrOtherInstallation) {
		t.Fatalf("outro diretório deveria ser recusado, até com adopt: %v", err)
	}
	t.Log(err)

	// Banco com histórico e sem registro (instalação anterior à migration 00019).
	if _, err := s.Pool.Exec(ctx, "UPDATE installation SET home_dir = NULL"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Q.CreateRun(ctx, db.CreateRunParams{Repo: "o/r", BudgetUsd: NanoUSDToNumeric(1), Mode: "full"}); err != nil {
		t.Fatal(err)
	}
	if err := ClaimHome(ctx, s.Q, "/tmp/clone/sentinela", false); !errors.Is(err, ErrUnclaimedHistory) {
		t.Fatalf("histórico sem registro deveria pedir adopt: %v", err)
	}
	if err := ClaimHome(ctx, s.Q, "/srv/a/sentinela", true); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if home, _ := s.Q.InstallationHome(ctx); home != "/srv/a/sentinela" {
		t.Errorf("home = %q", home)
	}
}
