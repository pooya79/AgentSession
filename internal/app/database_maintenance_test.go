package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRemoveDatabaseRefusesActiveRuntimeLock verifies that cleanup is atomic
// with respect to active AgentSession owners.
func TestRemoveDatabaseRefusesActiveRuntimeLock(t *testing.T) {
	dataDir := t.TempDir()
	databasePath := filepath.Join(dataDir, databaseFilename)
	paths := RuntimePaths{DataDir: dataDir, DatabasePath: databasePath}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	active, err := acquireDatabaseLock(databasePath, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := RemoveDatabase(paths); !errors.Is(err, ErrDatabaseInUse) {
		t.Fatalf("RemoveDatabase() error = %v, want ErrDatabaseInUse", err)
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("active database file %q changed: %v", path, err)
		}
	}

	if err := active.release(); err != nil {
		t.Fatal(err)
	}
	if err := RemoveDatabase(paths); err != nil {
		t.Fatalf("RemoveDatabase() after release error = %v", err)
	}
	for _, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("database file %q still exists: %v", path, err)
		}
	}
}

// TestRemoveDatabaseDoesNotCreateMissingDataDirectory verifies that cleanup of
// an absent index remains a read-only no-op.
func TestRemoveDatabaseDoesNotCreateMissingDataDirectory(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "missing")
	if err := RemoveDatabase(RuntimePaths{
		DataDir: dataDir, DatabasePath: filepath.Join(dataDir, databaseFilename),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dataDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing data directory was created: %v", err)
	}
}
