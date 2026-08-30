package backend

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	migrate "github.com/rubenv/sql-migrate"
)

const sessionDBSchemaVersion = 9

var errApplySchemaMigrations = errors.New("apply rocketclaw state schema migrations")

//go:embed migrations/*.sql
var sessionDBMigrations embed.FS

func initializeSessionDB(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	startedAt := time.Now()

	logger.Info("initializing rocketclaw state store schema")

	if _, err := db.ExecContext(ctx, `ALTER TABLE IF EXISTS gorp_migrations RENAME TO pg_migrations`); err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}

	n, err := migrate.MigrationSet{TableName: "pg_migrations"}.ExecContext(ctx, db, "postgres", migrate.EmbedFileSystemMigrationSource{
		FileSystem: sessionDBMigrations,
		Root:       "migrations",
	}, migrate.Up)
	if err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}

	logger.Info("initialized rocketclaw state store schema", "migrations", n, "elapsed", time.Since(startedAt))

	return nil
}
