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

type explorationReaderStub struct {
	exists       bool
	events       []model.EventSummary
	diagnostics  storage.DiagnosticPage
	envelope     storage.EventEnvelope
	payload      model.NormalizedData
	payloadReads int
	sessions     []storage.SessionSummary
	overview     storage.LibraryOverview
	overviewErr  error
}

func (s *explorationReaderStub) LibraryOverview(context.Context) (storage.LibraryOverview, error) {
	return s.overview, s.overviewErr
}

func (s *explorationReaderStub) ListSessions(context.Context, *storage.SessionCursor, int) ([]storage.SessionSummary, bool, error) {
	return s.sessions, false, nil
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

func TestExplorerOverviewHonorsCancellationAndRejectsObsoleteSessionCursor(t *testing.T) {
	t.Parallel()

	explorer, _ := NewExplorer(&explorationReaderStub{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := explorer.LibraryOverview(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("LibraryOverview(canceled) error = %v", err)
	}
	obsolete, err := encodeCursor(cursorEnvelope{Kind: "sessions", SessionID: "session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := explorer.ListSessions(context.Background(), ListSessionsRequest{Cursor: obsolete}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("obsolete cursor error = %v", err)
	}
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
