package tui

import (
	"context"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type servicesStub struct {
	mu sync.Mutex

	start               app.ImportAllStart
	startErr            error
	status              app.ImportAllStatus
	statusErr           error
	sessionPage         app.SessionPage
	sessionErr          error
	overview            app.LibraryOverview
	overviewErr         error
	timeline            app.TimelinePage
	timelineErr         error
	detail              app.EventDetail
	detailErr           error
	inspection          app.UnknownEvidenceInspection
	inspectionErr       error
	projections         app.ProjectionStatus
	projectionErr       error
	projectionAction    app.ProjectionAction
	projectionActionErr error

	startCalls            int
	sessionCalls          []app.ListSessionsRequest
	timelineCalls         []app.TimelineRequest
	detailCalls           []app.EventDetailRequest
	statusCtx             context.Context
	projectionStatusCalls int
	retryCalls            int
	rebuildCalls          []string
	searchCalls           []app.SearchRequest
}

func (s *servicesStub) Search(_ context.Context, request app.SearchRequest) (app.SearchPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.searchCalls = append(s.searchCalls, request)
	return app.SearchPage{State: app.EvidenceComplete, Availability: app.SearchAvailability{State: app.EvidenceComplete}}, nil
}

func (s *servicesStub) DiscoverSources(context.Context) (app.SourceDiscovery, error) {
	return app.SourceDiscovery{}, nil
}

func (s *servicesStub) StartImport(context.Context, model.SourceID) (app.ImportStart, error) {
	return app.ImportStart{}, nil
}

func (s *servicesStub) StartImportAll(context.Context) (app.ImportAllStart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.startCalls++
	return s.start, s.startErr
}

func (s *servicesStub) ImportAllStatus(ctx context.Context) (app.ImportAllStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.statusCtx = ctx
	return s.status, s.statusErr
}

func (s *servicesStub) ListSessions(_ context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionCalls = append(s.sessionCalls, request)
	return s.sessionPage, s.sessionErr
}

func (s *servicesStub) LibraryOverview(context.Context) (app.LibraryOverview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.overview, s.overviewErr
}

func (s *servicesStub) EventLocations(context.Context, []model.EventID) (map[model.EventID]app.EventLocation, error) {
	return nil, nil
}

func (s *servicesStub) Timeline(_ context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timelineCalls = append(s.timelineCalls, request)
	return s.timeline, s.timelineErr
}

func (s *servicesStub) EventDetail(_ context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.detailCalls = append(s.detailCalls, request)
	return s.detail, s.detailErr
}

func (s *servicesStub) InspectUnknownEvidence(context.Context, model.SessionID, model.EventID) (app.UnknownEvidenceInspection, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inspection, s.inspectionErr
}

func (s *servicesStub) ProjectionStatus(context.Context, model.SessionID) (app.ProjectionStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.projectionStatusCalls++
	return s.projections, s.projectionErr
}

func (s *servicesStub) RetryProjections(context.Context, model.SessionID) (app.ProjectionAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.retryCalls++
	return s.projectionAction, s.projectionActionErr
}

func (s *servicesStub) RebuildProjections(_ context.Context, _ model.SessionID, kind string) (app.ProjectionAction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rebuildCalls = append(s.rebuildCalls, kind)
	return s.projectionAction, s.projectionActionErr
}

func testSession(id string) app.SessionSummary {
	return app.SessionSummary{
		ID: model.SessionID(id), Title: id, EventCount: 2, State: app.EvidenceComplete,
	}
}

func testEvent(id string, sequence int64) model.EventSummary {
	return model.EventSummary{
		ID: model.EventID(id), SessionID: "session-1", Sequence: sequence,
		Kind: model.EventKindMessage, Summary: "summary",
	}
}

func updateModel(t *testing.T, current Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	updated, cmd := current.Update(msg)
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("updated model type = %T, want Model", updated)
	}
	return result, cmd
}
