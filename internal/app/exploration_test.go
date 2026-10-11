package app

import (
	"context"
	"errors"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

type explorationReaderStub struct {
	exists       bool
	events       []model.EventSummary
	diagnostics  storage.DiagnosticPage
	envelope     storage.EventEnvelope
	payload      model.NormalizedData
	payloadReads int
	payloads     map[model.EventID]model.NormalizedData
	batchReads   int
	sessions     []storage.SessionSummary
	sessionMore  bool
	lastCursor   *storage.SessionCursor
	overview     storage.LibraryOverview
	overviewErr  error
	locations    map[model.EventID]storage.EventLocation
	windowEnd    int64
	windowReads  int
	windowErr    error
	coverage     storage.InterpretationCoverage
	rawPrefix    storage.RawRecordPrefix
	rawFound     bool
	rawReads     int
}

func (s *explorationReaderStub) InterpretationCoverage(context.Context, model.SessionID) (storage.InterpretationCoverage, error) {
	return s.coverage, nil
}

func (s *explorationReaderStub) RawRecordPrefix(context.Context, model.SessionID, model.RawRecordID, int64) (storage.RawRecordPrefix, bool, error) {
	s.rawReads++
	return s.rawPrefix, s.rawFound, nil
}

func (s *explorationReaderStub) LibraryOverview(context.Context) (storage.LibraryOverview, error) {
	return s.overview, s.overviewErr
}

func (s *explorationReaderStub) ListSessions(_ context.Context, cursor *storage.SessionCursor, _ int) ([]storage.SessionSummary, bool, error) {
	s.lastCursor = cursor
	return s.sessions, s.sessionMore, nil
}

func (s *explorationReaderStub) SessionExists(context.Context, model.SessionID) (bool, error) {
	return s.exists, nil
}
func (s *explorationReaderStub) EventSummaryPage(context.Context, model.SessionID, *int64, int) ([]model.EventSummary, bool, error) {
	return s.events, false, nil
}
func (s *explorationReaderStub) EventSummaryWindow(_ context.Context, _ model.SessionID, endingAt int64, _ int) ([]model.EventSummary, bool, error) {
	s.windowEnd = endingAt
	s.windowReads++
	return s.events, false, s.windowErr
}
func (s *explorationReaderStub) EventLocations(context.Context, []model.EventID) (map[model.EventID]storage.EventLocation, error) {
	if s.locations != nil {
		return s.locations, nil
	}
	result := make(map[model.EventID]storage.EventLocation)
	for _, event := range s.events {
		result[event.ID] = storage.EventLocation{EventID: event.ID, SessionID: event.SessionID, Sequence: event.Sequence}
	}
	return result, nil
}

func (s *explorationReaderStub) EventEnvelope(context.Context, model.SessionID, model.EventID) (storage.EventEnvelope, bool, error) {
	return s.envelope, s.envelope.ID != "", nil
}
func (s *explorationReaderStub) EventPayload(context.Context, model.SessionID, model.EventID) (model.NormalizedData, bool, error) {
	s.payloadReads++
	return s.payload, s.payload != nil, nil
}
func (s *explorationReaderStub) EventPayloads(context.Context, model.SessionID, []model.EventID) (map[model.EventID]model.NormalizedData, error) {
	s.batchReads++
	return s.payloads, nil
}
func (s *explorationReaderStub) Diagnostics(context.Context, model.SessionID, *model.EventID, int) (storage.DiagnosticPage, error) {
	return s.diagnostics, nil
}

func TestExplorerRejectsInvalidRequestsBeforeStorage(t *testing.T) {
	explorer, _ := NewExplorer(&explorationReaderStub{})
	if _, err := explorer.ListSessions(context.Background(), ListSessionsRequest{Limit: MaximumPageSize + 1}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized page error = %v", err)
	}
	if _, err := explorer.Timeline(context.Background(), TimelineRequest{SessionID: " session"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("malformed session error = %v", err)
	}
	if _, err := explorer.ListSessions(context.Background(), ListSessionsRequest{Cursor: "not-a-cursor"}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("malformed cursor error = %v", err)
	}
}
