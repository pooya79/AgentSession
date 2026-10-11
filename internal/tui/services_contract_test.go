package tui

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/app/apptest"
	"github.com/pooya79/AgentSession/internal/model"
)

type contractConsumer struct{ services app.Services }

func (c contractConsumer) LibraryOverview(ctx context.Context) (app.LibraryOverview, error) {
	return c.services.LibraryOverview(ctx)
}
func (c contractConsumer) ListSessions(ctx context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
	return c.services.ListSessions(ctx, request)
}
func (c contractConsumer) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	return c.services.Timeline(ctx, request)
}
func (c contractConsumer) EventDetail(ctx context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
	return c.services.EventDetail(ctx, request)
}
func (c contractConsumer) EventLocations(ctx context.Context, ids []model.EventID) (map[model.EventID]app.EventLocation, error) {
	return c.services.EventLocations(ctx, ids)
}
func (c contractConsumer) InspectUnknownEvidence(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (app.UnknownEvidenceInspection, error) {
	return c.services.InspectUnknownEvidence(ctx, sessionID, eventID)
}
func (c contractConsumer) ProjectionStatus(ctx context.Context, sessionID model.SessionID) (app.ProjectionStatus, error) {
	return c.services.ProjectionStatus(ctx, sessionID)
}
func (c contractConsumer) RetryProjections(ctx context.Context, sessionID model.SessionID) (app.ProjectionAction, error) {
	return c.services.RetryProjections(ctx, sessionID)
}
func (c contractConsumer) RebuildProjections(ctx context.Context, sessionID model.SessionID, kind string) (app.ProjectionAction, error) {
	return c.services.RebuildProjections(ctx, sessionID, kind)
}
func (c contractConsumer) Search(ctx context.Context, request app.SearchRequest) (app.SearchPage, error) {
	return c.services.Search(ctx, request)
}

func TestSharedServiceContract(t *testing.T) {
	fixture := apptest.NewFixture(t, "streamed_commands.jsonl")
	apptest.RunConsumerContract(t, contractConsumer{services: fixture.Services})
}

func (c contractConsumer) SessionFindings(ctx context.Context, id model.SessionID) (app.FindingsPage, error) {
	return c.services.SessionFindings(ctx, id)
}

func TestVerificationServiceContract(t *testing.T) {
	fixture := apptest.NewFixture(t, "go_verification.jsonl")
	apptest.RunVerificationContract(t, contractConsumer{services: fixture.Services})
}

func TestChangeVerificationServiceContract(t *testing.T) {
	fixture := apptest.NewFixture(t, "changes.jsonl")
	apptest.RunChangeVerificationContract(t, contractConsumer{services: fixture.Services})
}

func TestClaudeEvidenceServiceContract(t *testing.T) {
	fixture := apptest.NewClaudeFixture(t, "ordinary_tools.jsonl")
	apptest.RunClaudeEvidenceContract(t, contractConsumer{services: fixture.Services})
}

func TestOpenCodeEvidenceServiceContract(t *testing.T) {
	fixture := apptest.NewOpenCodeFixture(t, "findings_evidence_v1.sql")
	apptest.RunOpenCodeEvidenceContract(t, contractConsumer{services: fixture.Services})
}

func TestMoveConfigurationServiceContract(t *testing.T) {
	fixture := apptest.NewFixture(t, "moves_configuration.jsonl")
	apptest.RunMoveConfigurationContract(t, contractConsumer{services: fixture.Services})
}

func TestTimelineDisplaysSharedSummaryCategoriesWithoutDisclosure(t *testing.T) {
	for _, test := range []struct {
		category model.SummaryCategory
		title    string
	}{
		{model.SummaryCategoryReasoning, "Reasoning"},
		{model.SummaryCategoryContext, "Conversation context"},
		{model.SummaryCategoryPlan, "Plan update"},
		{model.SummaryCategorySummary, "Session summary"},
	} {
		t.Run(string(test.category), func(t *testing.T) {
			event := testEvent("summary", 7)
			event.Kind = model.EventKindSummary
			services := &servicesStub{timeline: app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{event}, Payloads: map[model.EventID]model.NormalizedData{event.ID: model.SummaryData{Category: test.category, Text: "RETAINED SUMMARY TEXT"}}}}
			m := New(t.Context(), services)
			m.sessionsState.loading = false
			m.sessionsState.page = app.SessionPage{Sessions: []app.SessionSummary{testSession("session-1")}}
			m, cmd := updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			m, _ = updateModel(t, m, cmd())
			if got := m.View().Content; !strings.Contains(got, test.title) || strings.Contains(got, "RETAINED SUMMARY TEXT") {
				t.Fatalf("category/disclosure mismatch:\n%s", got)
			}
			m, _ = updateModel(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
			if got := m.View().Content; !strings.Contains(got, "RETAINED SUMMARY TEXT") {
				t.Fatalf("summary not accessible:\n%s", got)
			}
			if len(services.timelineCalls) != 1 || !services.timelineCalls[0].IncludePayloads {
				t.Fatalf("unexpected shared service request: %+v", services.timelineCalls)
			}
		})
	}
}
