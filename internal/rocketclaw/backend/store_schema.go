package backend

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	migrate "github.com/rubenv/sql-migrate"
)

var errApplySchemaMigrations = errors.New("apply rocketclaw state schema migrations")

//go:embed migrations/*.sql
var sessionDBMigrations embed.FS

func initializeSessionDB(ctx context.Context, db *sql.DB, logger *slog.Logger) (errResult error) {
	startedAt := time.Now()

	logger.Info("initializing rocketclaw state store schema")

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}
	defer func() {
		// Never return a session lock to the pool after cancellation or an
		// uncertain unlock response. ErrBadConn closes the physical connection.
		if errResult != nil {
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}

		_ = conn.Close()
	}()

	for _, query := range []string{
		`SELECT pg_advisory_lock(hashtext('rocketclaw schema migrations'), hashtext(current_schema()))`,
		`ALTER TABLE IF EXISTS gorp_migrations RENAME TO pg_migrations`,
		`CREATE TABLE IF NOT EXISTS pg_migrations (id text PRIMARY KEY, applied_at timestamptz)`,
	} {
		if _, err := conn.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
		}
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

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext('rocketclaw schema migrations'), hashtext(current_schema()))`); err != nil {
		return fmt.Errorf("%w: %w", errApplySchemaMigrations, err)
	}

	logger.Info("initialized rocketclaw state store schema", "migrations", n, "elapsed", time.Since(startedAt))

	return nil
}

// applySessionMigration keeps each migration's schema and ledger row atomic.
func applySessionMigration(ctx context.Context, conn *sql.Conn, migration *migrate.Migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, query := range migration.Up {
		if _, err := tx.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("execute migration: %w", err)
		}
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO pg_migrations (id, applied_at) VALUES ($1, $2)`, migration.Id, time.Now()); err != nil {
		return fmt.Errorf("record migration: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	return nil
}
