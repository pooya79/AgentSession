package opencode

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type snapshot struct {
	db   *sql.DB
	conn *sql.Conn
	tx   *sql.Tx
}

func openSnapshot(ctx context.Context, path string) (*snapshot, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve database path: %w", err)
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(absolute)}
	q := u.Query()
	q.Set("mode", "ro")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(ctx)
	if err != nil {
		db.Close()
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA query_only = ON`); err != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("enforce query-only connection: %w", err)
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("begin consistent read transaction: %w", err)
	}
	var schemaVersion int64
	if err := tx.QueryRowContext(ctx, `PRAGMA schema_version`).Scan(&schemaVersion); err != nil {
		tx.Rollback()
		conn.Close()
		db.Close()
		return nil, fmt.Errorf("establish read snapshot: %w", err)
	}
	return &snapshot{db: db, conn: conn, tx: tx}, nil
}

func (s *snapshot) close() error {
	return errors.Join(s.tx.Rollback(), s.conn.Close(), s.db.Close())
}
