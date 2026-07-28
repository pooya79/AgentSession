package search

import (
	"context"

	"github.com/pooya79/AgentSession/internal/model"
)

// Availability describes the readiness of search projections and carries the
// generation used to invalidate stale cursors.
type Availability struct {
	Sessions      int64
	Usable        int64
	Pending       int64
	Running       int64
	Failed        int64
	Stale         int64
	Unimplemented int64
	Generation    string
}

// Cursor identifies a stable position and direction within a search result
// generation.
type Cursor struct {
	Rank       float64
	Timestamp  string
	SessionID  model.SessionID
	Sequence   int64
	EventID    model.EventID
	Before     bool
	Generation string
	Ranked     bool
}

// Row is one repository-level search match before application mapping.
type Row struct {
	SessionID model.SessionID
	EventID   model.EventID
	Sequence  int64
	Timestamp string
	Kind      model.EventKind
	Summary   string
	Snippet   string
	Rank      float64
}

// Rows contains a bounded repository result set and its availability snapshot.
type Rows struct {
	Items        []Row
	More         bool
	Availability Availability
}

// Repository executes only generated, parameterized search expressions.
type Repository interface {
	// Search returns matches from current ready projections.
	Search(context.Context, Query, *Cursor, int) (Rows, error)
}
