package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all embedded migrations. Idempotent, and
// replica-safe: the run holds a Postgres session-level advisory lock
// (goose's session locker) for its duration, so two replicas booting
// together do not race each other's DDL — the second waits for the
// first, then finds nothing left to do. The lock is per database, held
// on a dedicated connection, and released with it; a replica that dies
// mid-migration releases it with its session.
func Migrate(ctx context.Context, dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return err
	}
	sqlDB := stdlib.OpenDB(*cfg)
	defer func() { _ = sqlDB.Close() }()

	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migration locker: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, sub, goose.WithSessionLocker(locker))
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}
	defer func() { _ = provider.Close() }()
	results, err := provider.Up(ctx)
	if err != nil {
		return err
	}
	// Say which of the two things happened — applied, or nothing to do —
	// so a replica's log shows whether it was the one that migrated.
	if len(results) == 0 {
		slog.Info("migrations: nothing to apply (schema current under the migration lock)")
		return nil
	}
	for _, r := range results {
		slog.Info("migrations: applied", "version", r.Source.Version, "path", r.Source.Path, "took", r.Duration)
	}
	return nil
}
