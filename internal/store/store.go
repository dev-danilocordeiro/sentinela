// Package store concentra o acesso ao Postgres: pool pgx, migrations goose
// embutidas no binário e as queries geradas pelo sqlc em internal/store/db.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"math/big"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registra o driver "pgx" em database/sql
	"github.com/pressly/goose/v3"

	"github.com/dev-danilocordeiro/sentinela/internal/store/db"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store agrupa o pool e as queries tipadas.
type Store struct {
	Pool *pgxpool.Pool
	Q    *db.Queries
}

// Open conecta ao Postgres e confere a conexão.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL não definida")
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("abrindo pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("conectando ao Postgres: %w", err)
	}
	return &Store{Pool: pool, Q: db.New(pool)}, nil
}

func (s *Store) Close() { s.Pool.Close() }

// Migrator executa as migrations embutidas.
type Migrator struct {
	provider *goose.Provider
	sqlDB    *sql.DB
}

// NewMigrator usa o driver database/sql do pgx sobre o mesmo DATABASE_URL.
func NewMigrator(databaseURL string) (*Migrator, error) {
	if databaseURL == "" {
		return nil, errors.New("DATABASE_URL não definida")
	}
	sqlDB, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("abrindo conexão: %w", err)
	}
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("lendo migrations embutidas: %w", err)
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub)
	if err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("criando provider do goose: %w", err)
	}
	return &Migrator{provider: p, sqlDB: sqlDB}, nil
}

func (m *Migrator) Close() error { return m.sqlDB.Close() }

// Up aplica todas as migrations pendentes.
func (m *Migrator) Up(ctx context.Context) ([]*goose.MigrationResult, error) {
	return m.provider.Up(ctx)
}

// Down desfaz a última migration aplicada.
func (m *Migrator) Down(ctx context.Context) (*goose.MigrationResult, error) {
	return m.provider.Down(ctx)
}

// DownTo desfaz migrations até a versão informada (0 desfaz todas).
func (m *Migrator) DownTo(ctx context.Context, version int64) ([]*goose.MigrationResult, error) {
	return m.provider.DownTo(ctx, version)
}

// Status lista o estado de cada migration.
func (m *Migrator) Status(ctx context.Context) ([]*goose.MigrationStatus, error) {
	return m.provider.Status(ctx)
}

// NanoUSDToNumeric converte nanodólares para o NUMERIC(18, 9) do banco, sem perda.
func NanoUSDToNumeric(nano int64) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(nano), Exp: -9, Valid: true}
}

// NumericToNanoUSD converte um NUMERIC do banco em nanodólares.
func NumericToNanoUSD(n pgtype.Numeric) (int64, error) {
	if !n.Valid {
		return 0, nil
	}
	v := new(big.Int).Set(n.Int)
	exp := n.Exp + 9
	ten := big.NewInt(10)
	for ; exp > 0; exp-- {
		v.Mul(v, ten)
	}
	for ; exp < 0; exp++ {
		v.Quo(v, ten)
	}
	if !v.IsInt64() {
		return 0, fmt.Errorf("valor fora do intervalo: %s", v)
	}
	return v.Int64(), nil
}
