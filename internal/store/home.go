package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

// ErrOtherInstallation: o banco pertence a uma instalação de outro diretório (D-06 da revisão de
// prontidão, ADR 0052).
var ErrOtherInstallation = errors.New("o banco pertence a outra instalação")

// ErrUnclaimedHistory: o banco tem histórico e nenhum diretório registrado; só quem é dele pode
// adotá-lo.
var ErrUnclaimedHistory = errors.New("o banco tem histórico de uma instalação sem diretório registrado")

// HomeDir é o diretório desta instalação: o atual, absoluto e sem links simbólicos.
func HomeDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(wd); err == nil {
		wd = real
	}
	return filepath.Abs(wd)
}

// ClaimHome confere se o banco é desta instalação (dir) e, se nenhum diretório foi registrado,
// registra este. Com histórico e sem registro, só registra com adopt (o serve e o worker da
// instalação, ou init --adotar-banco); um banco novo é sempre registrado.
func ClaimHome(ctx context.Context, q *db.Queries, dir string, adopt bool) error {
	home, err := q.InstallationHome(ctx)
	if err != nil {
		return fmt.Errorf("diretório da instalação (migration 00019): %w", err)
	}
	switch {
	case home == dir:
		return nil
	case home != "":
		return fmt.Errorf("%w: este banco é da instalação em %s, e o Sentinela está rodando em %s. Use outro banco (SENTINELA_PG_PORT, COMPOSE_PROJECT_NAME e DATABASE_URL no .env) ou rode o comando no diretório %s", ErrOtherInstallation, home, dir, home)
	}
	if !adopt {
		hist, err := q.HasHistory(ctx)
		if err != nil {
			return err
		}
		if hist.Bool {
			return fmt.Errorf("%w: confira se este é o banco certo (DATABASE_URL). Se for o banco desta instalação, rode o init com --adotar-banco, ou suba o serve ou o worker aqui, que o registram", ErrUnclaimedHistory)
		}
	}
	n, err := q.ClaimInstallationHome(ctx, pgtype.Text{String: dir, Valid: true})
	if err != nil {
		return err
	}
	if n == 0 {
		// Outro processo registrou no meio do caminho: confere de novo.
		return ClaimHome(ctx, q, dir, false)
	}
	return nil
}
