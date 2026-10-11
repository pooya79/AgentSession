package app

import (
	"context"
	"fmt"
)

// accepting rejects new work once runtime shutdown begins.
func (r *Runtime) accepting() error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closing || r.closed {
		return ErrShuttingDown
	}
	return nil
}

// Shutdown rejects new work, cancels and settles imports, closes SQLite, and
// releases the maintenance lock. A context timeout leaves database closure
// retryable.
func (r *Runtime) Shutdown(ctx context.Context) error {
	r.shutdown.Lock()
	defer r.shutdown.Unlock()
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closing = true
	r.mu.Unlock()
	if err := r.importAll.Shutdown(ctx); err != nil {
		return err
	}
	if err := r.imports.Shutdown(ctx); err != nil {
		return err
	}
	if err := r.projections.Shutdown(ctx); err != nil {
		return err
	}
	if err := r.db.Close(); err != nil {
		return fmt.Errorf("shutdown runtime: close database: %w", err)
	}
	if err := r.databaseLock.release(); err != nil {
		return fmt.Errorf("shutdown runtime: %w", err)
	}
	r.mu.Lock()
	r.closed = true
	r.mu.Unlock()
	return nil
}
