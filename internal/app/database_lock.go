package app

import (
	"errors"
	"fmt"
	"os"
)

// databaseLockSuffix names the stable lock file adjacent to the SQLite index.
const databaseLockSuffix = ".lock"

// ErrDatabaseInUse means a running AgentSession instance still owns the
// database selected for a maintenance operation.
var ErrDatabaseInUse = errors.New("AgentSession database is in use")

// databaseLock owns an advisory lock for the lifetime of its open file.
type databaseLock struct {
	file *os.File
}

// acquireDatabaseLock obtains a shared runtime lock or an exclusive
// maintenance lock without waiting for incompatible owners.
func acquireDatabaseLock(databasePath string, exclusive bool) (*databaseLock, error) {
	path := databasePath + databaseLockSuffix
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open database lock: %w", err)
	}
	locked, err := tryPlatformFileLock(file, exclusive)
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire database lock: %w", err)
	}
	if !locked {
		_ = file.Close()
		return nil, ErrDatabaseInUse
	}
	return &databaseLock{file: file}, nil
}

// release unlocks and closes the underlying file and is safe to call again.
func (l *databaseLock) release() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := unlockPlatformFile(file)
	closeErr := file.Close()
	if err := errors.Join(unlockErr, closeErr); err != nil {
		return fmt.Errorf("release database lock: %w", err)
	}
	return nil
}
