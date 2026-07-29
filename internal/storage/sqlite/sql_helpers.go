package sqlite

import (
	"context"
	"database/sql"
	"time"
)

// These small interfaces let persistence helpers work with both a database
// handle and an active transaction without hiding transaction ownership.
type sqlExecer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowScanner interface {
	Scan(...any) error
}

func encodeTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func timeString(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func decodeTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	return decodeTimeString(value.String)
}

func decodeTimeString(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nullableInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

func nullIfEmpty(value string) any {
	if value == "" {
		return nil
	}
	return value
}
