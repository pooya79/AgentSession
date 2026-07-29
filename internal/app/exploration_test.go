package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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

func TestSessionPreviewPrecedenceNormalizationAndRuneBound(t *testing.T) {
	t.Parallel()

	if got := sessionPreview("  summary\n with\tspaces ", "user message"); got != "summary with spaces" {
		t.Fatalf("summary preview = %q", got)
	}
	if got := sessionPreview("", "  first\n user\tmessage "); got != "first user message" {
		t.Fatalf("user preview = %q", got)
	}
	long := strings.Repeat("界", SessionPreviewMaxRunes+20)
	got := sessionPreview("", long)
	if len([]rune(got)) != SessionPreviewMaxRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("bounded preview has %d runes and suffix %q", len([]rune(got)), got[len(got)-3:])
	}
}

func TestExplorerOverviewAndSessionFieldsRemainIndependent(t *testing.T) {
	t.Parallel()

	activity := time.Date(2026, 7, 25, 10, 0, 0, 0, time.UTC)
	stub := &explorationReaderStub{
		overview: storage.LibraryOverview{Sessions: 1, Events: 2, Agents: 1},
		sessions: []storage.SessionSummary{{
			ID: "session", AgentName: "codex", LastActivityAt: &activity,
			FirstUserMessage: " inspect   this ", EventCount: 2,
		}},
	}
	explorer, err := NewExplorer(stub)
	if err != nil {
		t.Fatal(err)
	}
	overview, err := explorer.LibraryOverview(context.Background())
	if err != nil || overview.Sessions != 1 || overview.Events != 2 {
		t.Fatalf("LibraryOverview() = (%#v, %v)", overview, err)
	}
	page, err := explorer.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("ListSessions() = (%#v, %v)", page, err)
	}
	if got := page.Sessions[0]; got.AgentName != "codex" || got.Preview != "inspect this" || got.LastActivityAt == nil || !got.LastActivityAt.Equal(activity) {
		t.Fatalf("session fields = %#v", got)
	}

	stub.overviewErr = errors.New("metrics unavailable")
	if _, err := explorer.LibraryOverview(context.Background()); err == nil {
		t.Fatal("LibraryOverview() error = nil")
	}
	page, err = explorer.ListSessions(context.Background(), ListSessionsRequest{})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("sessions were blocked by overview failure: (%#v, %v)", page, err)
	}
}

func TestExplorerOverviewHonorsCancellation(t *testing.T) {
	t.Parallel()

	explorer, _ := NewExplorer(&explorationReaderStub{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := explorer.LibraryOverview(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("LibraryOverview(canceled) error = %v", err)
	}
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

func TestInspectUnknownEvidenceIsExplicitBoundedRedactedAndSessionScoped(t *testing.T) {
	eventID := model.EventID("evt_" + strings.Repeat("b", 64))
	rawID := model.RawRecordID("raw_" + strings.Repeat("c", 64))
	content := append([]byte("Authorization: Bearer visible-secret\n"), 0xff)
	stub := &explorationReaderStub{
		envelope: storage.EventEnvelope{
			EventSummary: model.EventSummary{ID: eventID, SessionID: "session", Kind: model.EventKindUnknown, Summary: "unknown"},
			RawRecord:    model.RawRecordRef{ID: rawID, SourceID: "source", ContentHash: "sha256:test"},
		},
		rawPrefix: storage.RawRecordPrefix{Content: content, OriginalSize: int64(len(content) + 10)},
		rawFound:  true,
	}
	explorer, _ := NewExplorer(stub)
	if _, err := explorer.EventDetail(context.Background(), EventDetailRequest{SessionID: "session", EventID: eventID}); err != nil || stub.rawReads != 0 {
		t.Fatalf("ordinary detail read raw evidence: error=%v reads=%d", err, stub.rawReads)
	}
	inspection, err := explorer.InspectUnknownEvidence(context.Background(), "session", eventID)
	if err != nil || inspection.State != EvidenceComplete || !inspection.Truncated || inspection.RedactionCount != 1 ||
		inspection.ReturnedSize != int64(len(inspection.Text)) || !utf8.ValidString(inspection.Text) ||
		strings.Contains(inspection.Text, "visible-secret") || stub.rawReads != 1 {
		t.Fatalf("InspectUnknownEvidence() = (%#v, %v), reads=%d", inspection, err, stub.rawReads)
	}
	stub.envelope.Kind = model.EventKindMessage
	if _, err := explorer.InspectUnknownEvidence(context.Background(), "session", eventID); !errors.Is(err, ErrInvalidRequest) || stub.rawReads != 1 {
		t.Fatalf("typed inspection error=%v reads=%d", err, stub.rawReads)
	}
	stub.envelope.Kind = model.EventKindUnknown
	if result, err := explorer.InspectUnknownEvidence(context.Background(), "other", eventID); err != nil || result.State != EvidenceNotFound || stub.rawReads != 1 {
		t.Fatalf("cross-session inspection=(%#v, %v) reads=%d", result, err, stub.rawReads)
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
