package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

func TestSessionCursorDirectionsAndFocusedTimeline(t *testing.T) {
	eventID := model.EventID("evt_" + strings.Repeat("b", 64))
	activity := time.Date(2026, 7, 26, 8, 0, 0, 0, time.UTC)
	stub := &explorationReaderStub{
		exists: true, sessionMore: true,
		sessions: []storage.SessionSummary{{ID: "one", LastActivityAt: &activity}, {ID: "two"}},
		events:   []model.EventSummary{{ID: eventID, SessionID: "one", Sequence: 9}},
		locations: map[model.EventID]storage.EventLocation{
			eventID: {EventID: eventID, SessionID: "one", Sequence: 9},
		},
	}
	explorer, _ := NewExplorer(stub)
	first, err := explorer.ListSessions(context.Background(), ListSessionsRequest{Limit: 2})
	if err != nil || first.PreviousCursor != "" || first.NextCursor == "" {
		t.Fatalf("first page = (%#v, %v)", first, err)
	}
	stub.sessionMore = false
	next, err := explorer.ListSessions(context.Background(), ListSessionsRequest{Cursor: first.NextCursor, Limit: 2})
	if err != nil || stub.lastCursor == nil || stub.lastCursor.Before || next.PreviousCursor == "" {
		t.Fatalf("next page/cursor = (%#v, %#v, %v)", next, stub.lastCursor, err)
	}
	previousToken := next.PreviousCursor
	stub.sessionMore = true
	_, err = explorer.ListSessions(context.Background(), ListSessionsRequest{Cursor: previousToken, Limit: 2})
	if err != nil || stub.lastCursor == nil || !stub.lastCursor.Before {
		t.Fatalf("previous cursor = (%#v, %v)", stub.lastCursor, err)
	}

	timeline, err := explorer.Timeline(context.Background(), TimelineRequest{SessionID: "one", FocusedEvent: eventID, Limit: 10})
	if err != nil || timeline.FocusedEvent != eventID || stub.windowReads != 1 || stub.windowEnd != 9 {
		t.Fatalf("focused timeline = (%#v, %v), window reads/end %d/%d", timeline, err, stub.windowReads, stub.windowEnd)
	}
	stub.windowErr = errors.New("window unavailable")
	if _, err := explorer.Timeline(context.Background(), TimelineRequest{SessionID: "one", FocusedEvent: eventID}); err == nil || !strings.Contains(err.Error(), `read timeline for "one"`) {
		t.Fatalf("focused timeline storage error = %v", err)
	}
	stub.windowErr = nil
	stub.locations[eventID] = storage.EventLocation{EventID: eventID, SessionID: "other", Sequence: 9}
	missing, err := explorer.Timeline(context.Background(), TimelineRequest{SessionID: "one", FocusedEvent: eventID})
	if err != nil || missing.State != EvidenceNotFound || stub.windowReads != 2 {
		t.Fatalf("wrong-session focus = (%#v, %v), reads %d", missing, err, stub.windowReads)
	}
	if _, err := explorer.Timeline(context.Background(), TimelineRequest{SessionID: "one", Cursor: "cursor", FocusedEvent: eventID}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("focus plus cursor error = %v", err)
	}
}

func TestEventLocationsAreBoundedAndPreserveMissing(t *testing.T) {
	id := model.EventID("evt_" + strings.Repeat("c", 64))
	explorer, _ := NewExplorer(&explorationReaderStub{locations: map[model.EventID]storage.EventLocation{
		id: {EventID: id, SessionID: "session", Sequence: 3},
	}})
	found, err := explorer.EventLocations(context.Background(), []model.EventID{id})
	if err != nil || found[id].SessionID != "session" || found[id].Sequence != 3 {
		t.Fatalf("EventLocations() = (%#v, %v)", found, err)
	}
	tooMany := make([]model.EventID, MaximumPageSize+1)
	for i := range tooMany {
		tooMany[i] = id
	}
	if _, err := explorer.EventLocations(context.Background(), tooMany); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized lookup error = %v", err)
	}
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
	stub.payload = model.SummaryData{Category: model.SummaryCategorySummary, Text: "payload"}
	detail, err := explorer.EventDetail(context.Background(), EventDetailRequest{SessionID: "session", EventID: eventID})
	if err != nil || detail.Payload != nil || stub.payloadReads != 0 {
		t.Fatalf("EventDetail(no payload) = (%#v, %v), reads=%d", detail, err, stub.payloadReads)
	}
	detail, err = explorer.EventDetail(context.Background(), EventDetailRequest{SessionID: "session", EventID: eventID, IncludePayload: true})
	if err != nil || detail.Payload == nil || stub.payloadReads != 1 {
		t.Fatalf("EventDetail(payload) = (%#v, %v), reads=%d", detail, err, stub.payloadReads)
	}
}

func TestTimelinePayloadsAreBoundedAndOptIn(t *testing.T) {
	eventID := model.EventID("evt_" + strings.Repeat("d", 64))
	stub := &explorationReaderStub{
		exists: true,
		events: []model.EventSummary{{
			ID: eventID, SessionID: "session", Sequence: 1, Kind: model.EventKindMessage, Summary: "hello",
		}},
		payloads: map[model.EventID]model.NormalizedData{
			eventID: model.MessageData{Role: model.MessageRoleUser, Text: "complete text"},
		},
	}
	explorer, _ := NewExplorer(stub)
	summaryOnly, err := explorer.Timeline(context.Background(), TimelineRequest{SessionID: "session"})
	if err != nil || summaryOnly.Payloads != nil || stub.batchReads != 0 {
		t.Fatalf("summary timeline = (%#v, %v), batch reads %d", summaryOnly, err, stub.batchReads)
	}
	withPayloads, err := explorer.Timeline(context.Background(), TimelineRequest{
		SessionID: "session", IncludePayloads: true,
	})
	if err != nil || withPayloads.Payloads[eventID] == nil || stub.batchReads != 1 {
		t.Fatalf("payload timeline = (%#v, %v), batch reads %d", withPayloads, err, stub.batchReads)
	}
}
