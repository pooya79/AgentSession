package app

import (
	"context"
	"errors"
	"strings"
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
}

func (s *explorationReaderStub) ListSessions(context.Context, *storage.SessionCursor, int) ([]storage.SessionSummary, bool, error) {
	return nil, false, nil
}
func (s *explorationReaderStub) SessionExists(context.Context, model.SessionID) (bool, error) {
	return s.exists, nil
}
func (s *explorationReaderStub) EventSummaryPage(context.Context, model.SessionID, *int64, int) ([]model.EventSummary, bool, error) {
	return s.events, false, nil
}
func (s *explorationReaderStub) EventEnvelope(context.Context, model.SessionID, model.EventID) (storage.EventEnvelope, bool, error) {
	return s.envelope, s.envelope.ID != "", nil
}
func (s *explorationReaderStub) EventPayload(context.Context, model.SessionID, model.EventID) (model.NormalizedData, bool, error) {
	s.payloadReads++
	return s.payload, s.payload != nil, nil
}
func (s *explorationReaderStub) Diagnostics(context.Context, model.SessionID, *model.EventID, int) (storage.DiagnosticPage, error) {
	return s.diagnostics, nil
}

func TestExplorerEvidenceStatesAndExplicitPayload(t *testing.T) {
	eventID := model.EventID("evt_" + strings.Repeat("a", 64))
	stub := &explorationReaderStub{
		exists: true,
		diagnostics: storage.DiagnosticPage{Total: 1, Diagnostics: []model.Diagnostic{{
			Code: "record.malformed", Severity: model.SeverityWarning, Message: "record was retained without an event",
		}}},
	}
	explorer, err := NewExplorer(stub)
	if err != nil {
		t.Fatal(err)
	}
	timeline, err := explorer.Timeline(context.Background(), TimelineRequest{SessionID: "session"})
	if err != nil || timeline.State != EvidenceUnavailable || timeline.Diagnostics.Total != 1 {
		t.Fatalf("Timeline() = (%#v, %v), want unavailable with diagnostics", timeline, err)
	}
	stub.envelope = storage.EventEnvelope{EventSummary: model.EventSummary{ID: eventID, SessionID: "session", Kind: model.EventKindSummary, Summary: "summary"}}
	stub.payload = model.SummaryData{Text: "payload"}
	detail, err := explorer.EventDetail(context.Background(), EventDetailRequest{SessionID: "session", EventID: eventID})
	if err != nil || detail.Payload != nil || stub.payloadReads != 0 {
		t.Fatalf("EventDetail(no payload) = (%#v, %v), reads=%d", detail, err, stub.payloadReads)
	}
	detail, err = explorer.EventDetail(context.Background(), EventDetailRequest{SessionID: "session", EventID: eventID, IncludePayload: true})
	if err != nil || detail.Payload == nil || stub.payloadReads != 1 {
		t.Fatalf("EventDetail(payload) = (%#v, %v), reads=%d", detail, err, stub.payloadReads)
	}
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
