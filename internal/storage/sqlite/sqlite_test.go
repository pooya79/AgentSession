package sqlite

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

const migrationTableSQL = `
	CREATE TABLE schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL UNIQUE
	) STRICT;
`

func TestOpenMigratesFreshDatabaseAndReopenIsIdempotent(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agentsession.db")

	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() fresh database error = %v", err)
	}
	wantMigrations := []migration{
		{version: 1, name: "0001_foundation.sql"},
		{version: 2, name: "0002_import_store.sql"},
		{version: 3, name: "0003_full_retention.sql"},
		{version: 4, name: "0004_record_diagnostics.sql"},
		{version: 5, name: "0005_zero_record_checkpoints.sql"},
		{version: 6, name: "0006_adapter_checkpoints_and_reconciliation.sql"},
	}
	assertMigrationHistory(t, db, wantMigrations)
	if err := db.Close(); err != nil {
		t.Fatalf("Close() fresh database error = %v", err)
	}

	db, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() existing database error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() reopened database error = %v", err)
		}
	})

	assertMigrationHistory(t, db, wantMigrations)
	var tableCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND name = 'schema_migrations'
	`).Scan(&tableCount); err != nil {
		t.Fatalf("query schema_migrations table count: %v", err)
	}
	if tableCount != 1 {
		t.Fatalf("schema_migrations table count = %d, want 1", tableCount)
	}
}

func TestAdapterCheckpointMigrationPreservesLegacyState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agentsession.db")
	dsn, err := dataSourceName(path)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	all, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	legacy := fstest.MapFS{}
	entries, err := fs.ReadDir(all, ".")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() > "0005_zero_record_checkpoints.sql" {
			continue
		}
		content, err := fs.ReadFile(all, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		legacy[entry.Name()] = &fstest.MapFile{Data: content}
	}
	if err := runMigrations(ctx, db, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO import_checkpoints (
			source_id, byte_offset, record_sequence, prefix_hash, last_record_hash, source_size
		) VALUES ('source-legacy', 7, 2, 'prefix', 'record', 9)
	`); err != nil {
		t.Fatal(err)
	}
	if err := runMigrations(ctx, db, all); err != nil {
		t.Fatal(err)
	}
	var version string
	var cursor, fingerprint []byte
	if err := db.QueryRowContext(ctx, `
		SELECT state_version, cursor, fingerprint FROM import_checkpoints WHERE source_id = 'source-legacy'
	`).Scan(&version, &cursor, &fingerprint); err != nil {
		t.Fatal(err)
	}
	if version != "legacy-stream-v1" || string(cursor) != "offset=7;size=9" || string(fingerprint) != "prefix\x00record" {
		t.Fatalf("migrated checkpoint = (%q, %q, %q)", version, cursor, fingerprint)
	}
}

func TestOpenEnablesForeignKeysOnEveryConnection(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "agentsession.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	db.SetMaxOpenConns(4)
	connections := make([]*sql.Conn, 0, 4)
	for i := 0; i < 4; i++ {
		connection, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("Conn() %d error = %v", i, err)
		}
		connections = append(connections, connection)
	}
	defer func() {
		for _, connection := range connections {
			if err := connection.Close(); err != nil {
				t.Errorf("connection Close() error = %v", err)
			}
		}
	}()

	for i, connection := range connections {
		var enabled int
		if err := connection.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
			t.Fatalf("connection %d PRAGMA foreign_keys error = %v", i, err)
		}
		if enabled != 1 {
			t.Errorf("connection %d foreign_keys = %d, want 1", i, enabled)
		}
	}

	if _, err := connections[0].ExecContext(ctx, `
		CREATE TABLE parents (id INTEGER PRIMARY KEY);
		CREATE TABLE children (
			id INTEGER PRIMARY KEY,
			parent_id INTEGER NOT NULL REFERENCES parents(id)
		);
	`); err != nil {
		t.Fatalf("create foreign-key test tables: %v", err)
	}
	if _, err := connections[0].ExecContext(ctx, `INSERT INTO children (id, parent_id) VALUES (1, 99)`); err == nil {
		t.Fatal("invalid child insert succeeded; want foreign-key constraint error")
	}
}

func TestFailedMigrationRollsBackSchemaAndVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	db := openUnmigratedTestDatabase(t)
	migrations := fstest.MapFS{
		"0001_foundation.sql": {Data: []byte(migrationTableSQL)},
		"0002_failure.sql": {Data: []byte(`
			CREATE TABLE should_be_rolled_back (id INTEGER PRIMARY KEY);
			INSERT INTO table_that_does_not_exist (id) VALUES (1);
		`)},
	}

	err := runMigrations(ctx, db, migrations)
	if err == nil {
		t.Fatal("runMigrations() error = nil, want failed migration error")
	}
	if !strings.Contains(err.Error(), `execute migration "0002_failure.sql"`) {
		t.Fatalf("runMigrations() error = %q, want migration context", err)
	}

	assertMigrationHistory(t, db, []migration{{version: 1, name: "0001_foundation.sql"}})
	var tableCount int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sqlite_schema
		WHERE type = 'table' AND name = 'should_be_rolled_back'
	`).Scan(&tableCount); err != nil {
		t.Fatalf("query rolled-back table count: %v", err)
	}
	if tableCount != 0 {
		t.Fatalf("rolled-back table count = %d, want 0", tableCount)
	}
}

func TestLoadMigrationsRejectsInvalidOrdering(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		migrations fs.FS
		wantError  string
	}{
		{
			name: "malformed filename",
			migrations: fstest.MapFS{
				"first.sql": {Data: []byte("SELECT 1;")},
			},
			wantError: `invalid migration filename "first.sql"`,
		},
		{
			name: "version gap",
			migrations: fstest.MapFS{
				"0001_foundation.sql": {Data: []byte("SELECT 1;")},
				"0003_gap.sql":        {Data: []byte("SELECT 1;")},
			},
			wantError: "expected contiguous version 2",
		},
		{
			name: "empty migration",
			migrations: fstest.MapFS{
				"0001_empty.sql": {Data: []byte(" \n")},
			},
			wantError: `migration "0001_empty.sql" is empty`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadMigrations(tt.migrations)
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("loadMigrations() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestRunMigrationsRejectsIncompatibleHistory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		history   []migration
		wantError string
	}{
		{
			name: "newer database",
			history: []migration{
				{version: 1, name: "0001_foundation.sql"},
				{version: 2, name: "0002_future.sql"},
			},
			wantError: "newer than available version 1",
		},
		{
			name:      "renamed migration",
			history:   []migration{{version: 1, name: "0001_old_name.sql"}},
			wantError: `recorded as "0001_old_name.sql"; expected "0001_foundation.sql"`,
		},
		{
			name:      "history gap",
			history:   []migration{{version: 2, name: "0002_gap.sql"}},
			wantError: "expected contiguous version 1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			db := openUnmigratedTestDatabase(t)
			if _, err := db.ExecContext(context.Background(), migrationTableSQL); err != nil {
				t.Fatalf("create migration table: %v", err)
			}
			for _, migration := range tt.history {
				if _, err := db.ExecContext(context.Background(),
					`INSERT INTO schema_migrations (version, name) VALUES (?, ?)`,
					migration.version,
					migration.name,
				); err != nil {
					t.Fatalf("insert migration history: %v", err)
				}
			}

			err := runMigrations(context.Background(), db, fstest.MapFS{
				"0001_foundation.sql": {Data: []byte(migrationTableSQL)},
			})
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("runMigrations() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestDatabaseLifecycle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "agentsession.db")
	db, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := db.PingContext(ctx); err == nil {
		t.Fatal("PingContext() after Close() error = nil, want closed database error")
	}

	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open() after Close() error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatalf("Close() reopened database error = %v", err)
	}

	if _, err := Open(ctx, ""); err == nil || !strings.Contains(err.Error(), "path is empty") {
		t.Fatalf("Open() empty path error = %v, want path validation error", err)
	}
	if _, err := Open(ctx, filepath.Join(t.TempDir(), "missing", "agentsession.db")); err == nil {
		t.Fatal("Open() database in missing directory error = nil, want initialization error")
	}
}

func openUnmigratedTestDatabase(t *testing.T) *sql.DB {
	t.Helper()

	dsn, err := dataSourceName(filepath.Join(t.TempDir(), "agentsession.db"))
	if err != nil {
		t.Fatalf("dataSourceName() error = %v", err)
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	if err := db.PingContext(context.Background()); err != nil {
		t.Fatalf("PingContext() error = %v", err)
	}
	return db
}

func assertMigrationHistory(t *testing.T, db *sql.DB, want []migration) {
	t.Helper()

	got, err := appliedMigrations(context.Background(), db)
	if err != nil {
		t.Fatalf("appliedMigrations() error = %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("migration history length = %d, want %d; history = %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].version != want[i].version || got[i].name != want[i].name {
			t.Errorf("migration history[%d] = {%d %q}, want {%d %q}", i, got[i].version, got[i].name, want[i].version, want[i].name)
		}
	}
}
