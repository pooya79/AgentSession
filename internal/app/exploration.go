package app

import (
	"context"
	"errors"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

const (
	// DefaultPageSize is used when an exploration request does not specify a
	// limit.
	DefaultPageSize = 50
	// MaximumPageSize bounds exploration reads and presentation memory use.
	MaximumPageSize = 200
	// DiagnosticSynopsisMax bounds the diagnostic evidence returned inline.
	DiagnosticSynopsisMax = 10
	// SessionPreviewMaxRunes bounds human-readable session previews without
	// splitting a UTF-8 code point.
	SessionPreviewMaxRunes = 240
	// maximumIdentifierSize bounds untrusted route and cursor identifiers.
	maximumIdentifierSize = 512
	// UnknownInspectionMaxBytes is the fixed decoded-content cap for explicit
	// retained Unknown-evidence inspection.
	UnknownInspectionMaxBytes = 64 * 1024
)

// ErrInvalidRequest marks exploration input rejected before a storage read.
var ErrInvalidRequest = errors.New("invalid exploration request")

// EvidenceState describes how completely retained canonical evidence supports a view.
type EvidenceState string

const (
	// EvidenceComplete means the requested canonical evidence is available without diagnostics.
	EvidenceComplete EvidenceState = "complete"
	// EvidencePartial means usable evidence is accompanied by retained diagnostics.
	EvidencePartial EvidenceState = "partial"
	// EvidenceUnavailable means a known session or event lacks the requested normalized evidence.
	EvidenceUnavailable EvidenceState = "unavailable"
	// EvidenceNotFound means the requested canonical identifier is not retained.
	EvidenceNotFound EvidenceState = "not_found"
)

// DiagnosticSynopsis is a bounded diagnostic sample with an exact total.
type DiagnosticSynopsis struct {
	Diagnostics []model.Diagnostic
	Total       int64
	Omitted     int64
}

// InterpretationCoverageState describes interpretation independently of
// canonical evidence integrity and session outcome.
type InterpretationCoverageState string

const (
	// InterpretationFullyInterpreted means every committed record was either
	// mapped to typed canonical evidence or intentionally ignored as metadata.
	InterpretationFullyInterpreted InterpretationCoverageState = "fully_interpreted"
	// InterpretationPartiallyInterpreted means committed evidence contains at
	// least one Unknown event or categorized malformed record.
	InterpretationPartiallyInterpreted InterpretationCoverageState = "partially_interpreted"
)

// InterpretationCoverage contains exact committed coverage counts. It is
// independent of EvidenceState and any derived session outcome.
type InterpretationCoverage struct {
	State            InterpretationCoverageState
	UnknownEvents    int64
	MalformedRecords int64
}

// Explorer exposes bounded, validated reads over retained canonical evidence.
type Explorer interface {
	// LibraryOverview returns exact committed library counts.
	LibraryOverview(context.Context) (LibraryOverview, error)
	// ListSessions returns one activity-ordered page.
	ListSessions(context.Context, ListSessionsRequest) (SessionPage, error)
	// Timeline returns ordered summaries or a window containing a focused event.
	Timeline(context.Context, TimelineRequest) (TimelinePage, error)
	// EventDetail returns an event envelope and optionally its normalized payload.
	EventDetail(context.Context, EventDetailRequest) (EventDetail, error)
	// EventLocations resolves a bounded set of event IDs without loading payloads.
	EventLocations(context.Context, []model.EventID) (map[model.EventID]EventLocation, error)
	// InspectUnknownEvidence returns a bounded, redacted view of the retained
	// record for one session-owned Unknown event.
	InspectUnknownEvidence(context.Context, model.SessionID, model.EventID) (UnknownEvidenceInspection, error)
}

// explorationService validates presentation requests before delegating to storage.
type explorationService struct{ reader storage.ExplorationReader }

// NewExplorer creates the shared read service used by both presentation layers.
func NewExplorer(reader storage.ExplorationReader) (Explorer, error) {
	if reader == nil {
		return nil, errors.New("application explorer: reader is required")
	}
	return &explorationService{reader: reader}, nil
}

// interpretationCoverage maps exact storage counts to the source-neutral
// application state shared by both presentation layers.
func (s *explorationService) interpretationCoverage(ctx context.Context, sessionID model.SessionID) (InterpretationCoverage, error) {
	value, err := s.reader.InterpretationCoverage(ctx, sessionID)
	if err != nil {
		return InterpretationCoverage{}, err
	}
	state := InterpretationFullyInterpreted
	if value.UnknownEvents > 0 || value.MalformedRecords > 0 {
		state = InterpretationPartiallyInterpreted
	}
	return InterpretationCoverage{State: state, UnknownEvents: value.UnknownEvents, MalformedRecords: value.MalformedRecords}, nil
}

// diagnosticSynopsis preserves exact totals while returning only the bounded sample.
func diagnosticSynopsis(page storage.DiagnosticPage) DiagnosticSynopsis {
	omitted := page.Total - int64(len(page.Diagnostics))
	if omitted < 0 {
		omitted = 0
	}
	return DiagnosticSynopsis{Diagnostics: page.Diagnostics, Total: page.Total, Omitted: omitted}
}
