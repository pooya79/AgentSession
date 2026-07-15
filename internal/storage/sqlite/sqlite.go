// Package sqlite provides the SQLite database lifecycle and migration foundation.
package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

var migrationNamePattern = regexp.MustCompile(`^(\d{4})_([a-z0-9][a-z0-9_]*)\.sql$`)

type migration struct {
	version int
	name    string
	sql     string
}

// Open opens the database at path, verifies the connection, and applies all
// pending embedded migrations. The caller owns the returned database and must
// close it.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("sqlite: open database: path is empty")
	}

	dsn, err := dataSourceName(path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open database: %w", err)
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open database: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return nil, closeAfterError(db, fmt.Errorf("sqlite: initialize connection: %w", err))
	}

	migrationFS, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		return nil, closeAfterError(db, fmt.Errorf("sqlite: load embedded migrations: %w", err))
	}
	if err := runMigrations(ctx, db, migrationFS); err != nil {
		return nil, closeAfterError(db, fmt.Errorf("sqlite: migrate database: %w", err))
	}

	return db, nil
}

func dataSourceName(path string) (string, error) {
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve database path %q: %w", path, err)
	}

	u := url.URL{
		Scheme: "file",
		Path:   filepath.ToSlash(absolutePath),
	}
	query := u.Query()
	query.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func closeAfterError(db *sql.DB, cause error) error {
	if err := db.Close(); err != nil {
		return errors.Join(cause, fmt.Errorf("sqlite: close database after failure: %w", err))
	}
	return cause
}

func runMigrations(ctx context.Context, db *sql.DB, migrationFS fs.FS) error {
	migrations, err := loadMigrations(migrationFS)
	if err != nil {
		return err
	}

	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	if err := validateAppliedMigrations(applied, migrations); err != nil {
		return err
	}

	for _, migration := range migrations[len(applied):] {
		if err := applyMigration(ctx, db, migration); err != nil {
			return err
		}
	}
	return nil
}

func loadMigrations(migrationFS fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return nil, fmt.Errorf("migration directory contains subdirectory %q", entry.Name())
		}

		matches := migrationNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("invalid migration filename %q", entry.Name())
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("parse migration version from %q: %w", entry.Name(), err)
		}
		contents, err := fs.ReadFile(migrationFS, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			return nil, fmt.Errorf("migration %q is empty", entry.Name())
		}
		migrations = append(migrations, migration{
			version: version,
			name:    entry.Name(),
			sql:     string(contents),
		})
	}

	for i, migration := range migrations {
		expectedVersion := i + 1
		if migration.version != expectedVersion {
			return nil, fmt.Errorf("migration %q has version %d; expected contiguous version %d", migration.name, migration.version, expectedVersion)
		}
	}
	return migrations, nil
}

func appliedMigrations(ctx context.Context, db *sql.DB) ([]migration, error) {
	var tableCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM sqlite_schema
		WHERE type = 'table' AND name = 'schema_migrations'
	`).Scan(&tableCount); err != nil {
		return nil, fmt.Errorf("inspect migration history: %w", err)
	}
	if tableCount == 0 {
		return nil, nil
	}

	rows, err := db.QueryContext(ctx, `
		SELECT version, name
		FROM schema_migrations
		ORDER BY version
	`)
	if err != nil {
		return nil, fmt.Errorf("query migration history: %w", err)
	}
	defer rows.Close()

	var applied []migration
	for rows.Next() {
		var migration migration
		if err := rows.Scan(&migration.version, &migration.name); err != nil {
			return nil, fmt.Errorf("scan migration history: %w", err)
		}
		applied = append(applied, migration)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read migration history: %w", err)
	}
	return applied, nil
}

func validateAppliedMigrations(applied, available []migration) error {
	if len(applied) > len(available) {
		return fmt.Errorf("database migration version %d is newer than available version %d", applied[len(applied)-1].version, len(available))
	}

	for i, migration := range applied {
		expectedVersion := i + 1
		if migration.version != expectedVersion {
			return fmt.Errorf("migration history has version %d; expected contiguous version %d", migration.version, expectedVersion)
		}
		if migration.name != available[i].name {
			return fmt.Errorf("migration version %d is recorded as %q; expected %q", migration.version, migration.name, available[i].name)
		}
	}
	return nil
}

func applyMigration(ctx context.Context, db *sql.DB, migration migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %q: %w", migration.name, err)
	}

	if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("execute migration %q: %w", migration.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`,
		migration.version,
		migration.name,
	); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("record migration %q: %w", migration.name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %q: %w", migration.name, err)
	}
	return nil
}
