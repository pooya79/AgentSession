package app

import (
	"errors"
	"fmt"
	"os"
)

// RemoveDatabase removes the private SQLite index and its WAL sidecars after
// proving that no running AgentSession instance has the database open.
func RemoveDatabase(paths RuntimePaths) (err error) {
	if _, err := os.Stat(paths.DataDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("remove database: inspect data directory: %w", err)
	}
	lock, err := acquireDatabaseLock(paths.DatabasePath, true)
	if err != nil {
		return fmt.Errorf("remove database: %w", err)
	}
	defer func() {
		if releaseErr := lock.release(); releaseErr != nil {
			err = errors.Join(err, releaseErr)
		}
	}()

	for _, path := range []string{paths.DatabasePath, paths.DatabasePath + "-wal", paths.DatabasePath + "-shm"} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove database file: %w", err)
		}
	}
	return nil
}
