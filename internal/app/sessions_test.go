package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/storage"
)

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
