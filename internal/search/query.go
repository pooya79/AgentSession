package search

import (
	"context"

	"github.com/pooya79/AgentSession/internal/model"
)

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

type Rows struct {
	Items        []Row
	More         bool
	Availability Availability
}

// Repository executes only generated, parameterized search expressions.
type Repository interface {
	Search(context.Context, Query, *Cursor, int) (Rows, error)
}
