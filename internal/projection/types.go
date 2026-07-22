// Package projection owns the durable lifecycle contracts for rebuildable data.
package projection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

type Kind string

const (
	KindSearch         Kind = "search"
	KindGitCorrelation Kind = "git_correlation"
	KindFindings       Kind = "findings"
	KindOutcomes       Kind = "outcomes"
	KindAggregates     Kind = "aggregates"
)

func Kinds() []Kind {
	return []Kind{KindSearch, KindGitCorrelation, KindFindings, KindOutcomes, KindAggregates}
}

func (k Kind) Valid() bool {
	for _, candidate := range Kinds() {
		if k == candidate {
			return true
		}
	}
	return false
}

type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusFailed  Status = "failed"
	StatusReady   Status = "ready"
)

type Definition struct {
	Kind    Kind
	Version string
}

func DefaultDefinitions() []Definition {
	definitions := make([]Definition, 0, len(Kinds()))
	for _, kind := range Kinds() {
		definitions = append(definitions, Definition{Kind: kind, Version: "1"})
	}
	return definitions
}

func (d Definition) Validate() error {
	if !d.Kind.Valid() {
		return fmt.Errorf("invalid projection kind %q", d.Kind)
	}
	if strings.TrimSpace(d.Version) == "" {
		return errors.New("projection version is required")
	}
	return nil
}

type Diagnostic struct {
	Kind           Kind
	TargetVersion  string
	TargetRevision int64
	Code           string
	Summary        string
	Attempt        int64
	At             time.Time
}

type State struct {
	SessionID      model.SessionID
	Kind           Kind
	Status         Status
	TargetVersion  string
	TargetRevision int64
	ReadyVersion   string
	ReadyRevision  *int64
	AttemptCount   int64
	StartedAt      *time.Time
	UpdatedAt      time.Time
	Diagnostic     *Diagnostic
}

func (s State) Usable() bool {
	return s.Status == StatusReady && s.ReadyRevision != nil &&
		s.ReadyVersion == s.TargetVersion && *s.ReadyRevision == s.TargetRevision
}

type Claim struct {
	SessionID model.SessionID
	Kind      Kind
	Version   string
	Revision  int64
	Attempt   int64
	RunToken  string
}

// Reader exposes authoritative database content to builders. It deliberately
// has no source-opening or adapter capability.
type Reader interface {
	Session(context.Context, model.SessionID) (model.Session, bool, error)
	Events(context.Context, model.SessionID) ([]model.Event, error)
}

type BuildRequest struct {
	SessionID         model.SessionID
	CanonicalRevision int64
	Reader            Reader
}

type Builder interface {
	Build(context.Context, BuildRequest) error
}

type BuilderFunc func(context.Context, BuildRequest) error

func (f BuilderFunc) Build(ctx context.Context, request BuildRequest) error { return f(ctx, request) }

// SafeError allows a builder to opt in a bounded, non-sensitive diagnostic.
// Ordinary error text is never persisted or returned by lifecycle services.
type SafeError struct {
	Code    string
	Summary string
}

func (e SafeError) Error() string { return "projection build failed" }

type Store interface {
	Register(context.Context, []Definition) error
	States(context.Context, model.SessionID) ([]State, error)
	CanonicalRevision(context.Context, model.SessionID) (int64, bool, error)
	Claim(context.Context, model.SessionID, Kind) (Claim, bool, error)
	Renew(context.Context, Claim) (bool, error)
	Complete(context.Context, Claim) (bool, error)
	Fail(context.Context, Claim, Diagnostic) (bool, error)
	Release(context.Context, Claim) error
	Invalidate(context.Context, model.SessionID, *Kind) error
}
