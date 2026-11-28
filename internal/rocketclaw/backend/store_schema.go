package backend

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/stdlib"
	migrate "github.com/rubenv/sql-migrate"
)

var errApplySchemaMigrations = errors.New("apply rocketclaw state schema migrations")

//go:embed migrations/*.sql
var sessionDBMigrations embed.FS

func initializeSessionDB(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	startedAt := time.Now()

	logger.Info("initializing rocketclaw state store schema")

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}
	defer func() { _ = conn.Close() }()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, query := range []string{
		`CREATE TABLE IF NOT EXISTS pg_migrations (id text PRIMARY KEY, applied_at timestamptz)`,
		lockMigrationsSQL,
	} {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}

	migrations, err := (migrate.EmbedFileSystemMigrationSource{
		FileSystem: sessionDBMigrations,
		Root:       "migrations",
	}).FindMigrations()
	if err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}

	rows, err := conn.QueryContext(ctx, `SELECT id, applied_at FROM pg_migrations`)
	if err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}
	defer func() { _ = rows.Close() }()

	applied := make(map[string]bool)

	for rows.Next() {
		var record migrate.MigrationRecord
		if err := rows.Scan(&record.Id, &record.AppliedAt); err != nil {
			return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
		}

		if !slices.ContainsFunc(migrations, func(m *migrate.Migration) bool { return m.Id == record.Id }) {
			return fmt.Errorf("%w: unknown migration in database: %s", errApplySchemaMigrations, record.Id)
		}

		applied[record.Id] = true
	}

	if err := rows.Err(); err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}

	n := 0

	for _, migration := range migrations {
		if applied[migration.Id] {
			continue
		}

		if err := applySessionMigration(ctx, conn, migration); err != nil {
			return fmt.Errorf("%w: %s: %w", errApplySchemaMigrations, migration.Id, err)
		}

		n++
	}

	logger.Info("initialized rocketclaw state store schema", "migrations", n, "elapsed", time.Since(startedAt))

	return nil
}

const lockMigrationsSQL = `LOCK TABLE pg_migrations IN EXCLUSIVE MODE`

func applySessionMigration(ctx context.Context, conn *sql.Conn, migration *migrate.Migration) error {
	err := conn.Raw(func(raw any) error {
		tx, err := raw.(*stdlib.Conn).Conn().Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration: %w", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()

		if _, err := tx.Exec(ctx, lockMigrationsSQL); err != nil {
			return fmt.Errorf("lock migrations: %w", err)
		}

		var applied bool
		if err := tx.QueryRow(ctx, `SELECT count(*) > 0 FROM pg_migrations WHERE id = $1`, migration.Id).Scan(&applied); err != nil {
			return fmt.Errorf("read migration ledger: %w", err)
		}

		if applied {
			return nil
		}

		for _, query := range migration.Up {
			if _, err := tx.Exec(ctx, query); err != nil {
				return fmt.Errorf("execute migration: %w", err)
			}
		}

		if _, err := tx.Exec(ctx, `INSERT INTO pg_migrations (id, applied_at) VALUES ($1, $2)`, migration.Id, time.Now()); err != nil {
			return fmt.Errorf("record migration: %w", err)
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("apply migration: %w", err)
	}

	return nil
}
