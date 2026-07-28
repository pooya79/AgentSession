package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type servicesStub struct {
	discover           func(context.Context) (app.SourceDiscovery, error)
	start              func(context.Context, model.SourceID) (app.ImportStart, error)
	startAll           func(context.Context) (app.ImportAllStart, error)
	statusAll          func(context.Context) (app.ImportAllStatus, error)
	overview           func(context.Context) (app.LibraryOverview, error)
	list               func(context.Context, app.ListSessionsRequest) (app.SessionPage, error)
	timeline           func(context.Context, app.TimelineRequest) (app.TimelinePage, error)
	detail             func(context.Context, app.EventDetailRequest) (app.EventDetail, error)
	locations          func(context.Context, []model.EventID) (map[model.EventID]app.EventLocation, error)
	inspect            func(context.Context, model.SessionID, model.EventID) (app.UnknownEvidenceInspection, error)
	projectionStatus   func(context.Context, model.SessionID) (app.ProjectionStatus, error)
	retryProjections   func(context.Context, model.SessionID) (app.ProjectionAction, error)
	rebuildProjections func(context.Context, model.SessionID, string) (app.ProjectionAction, error)
	search             func(context.Context, app.SearchRequest) (app.SearchPage, error)
}

func (s servicesStub) Search(ctx context.Context, request app.SearchRequest) (app.SearchPage, error) {
	if s.search != nil {
		return s.search(ctx, request)
	}
	return app.SearchPage{State: app.EvidenceComplete, Availability: app.SearchAvailability{State: app.EvidenceComplete}}, nil
}

func (s servicesStub) InspectUnknownEvidence(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (app.UnknownEvidenceInspection, error) {
	if s.inspect != nil {
		return s.inspect(ctx, sessionID, eventID)
	}
	return app.UnknownEvidenceInspection{State: app.EvidenceNotFound}, nil
}

func (s servicesStub) LibraryOverview(ctx context.Context) (app.LibraryOverview, error) {
	if s.overview != nil {
		return s.overview(ctx)
	}
	return app.LibraryOverview{}, nil
}
func (s servicesStub) ProjectionStatus(ctx context.Context, id model.SessionID) (app.ProjectionStatus, error) {
	if s.projectionStatus != nil {
		return s.projectionStatus(ctx, id)
	}
	return app.ProjectionStatus{State: app.EvidenceComplete, SessionID: id}, nil
}
func (s servicesStub) RetryProjections(ctx context.Context, id model.SessionID) (app.ProjectionAction, error) {
	if s.retryProjections != nil {
		return s.retryProjections(ctx, id)
	}
	return app.ProjectionAction{State: app.EvidenceComplete, Status: app.ProjectionStatus{State: app.EvidenceComplete, SessionID: id}}, nil
}
func (s servicesStub) RebuildProjections(ctx context.Context, id model.SessionID, kind string) (app.ProjectionAction, error) {
	if s.rebuildProjections != nil {
		return s.rebuildProjections(ctx, id, kind)
	}
	return app.ProjectionAction{State: app.EvidenceComplete, Status: app.ProjectionStatus{State: app.EvidenceComplete, SessionID: id}}, nil
}
func (s servicesStub) StartImportAll(ctx context.Context) (app.ImportAllStart, error) {
	if s.startAll != nil {
		return s.startAll(ctx)
	}
	return app.ImportAllStart{Status: app.ImportAllStatus{Phase: app.ImportAllUpToDate}}, nil
}
func (s servicesStub) ImportAllStatus(ctx context.Context) (app.ImportAllStatus, error) {
	if s.statusAll != nil {
		return s.statusAll(ctx)
	}
	return app.ImportAllStatus{Phase: app.ImportAllUpToDate}, nil
}
func (s servicesStub) DiscoverSources(ctx context.Context) (app.SourceDiscovery, error) {
	if s.discover != nil {
		return s.discover(ctx)
	}
	return app.SourceDiscovery{}, nil
}
func (s servicesStub) StartImport(ctx context.Context, id model.SourceID) (app.ImportStart, error) {
	if s.start != nil {
		return s.start(ctx, id)
	}
	return app.ImportStart{}, nil
}
func (s servicesStub) ListSessions(ctx context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
	if s.list != nil {
		return s.list(ctx, request)
	}
	return app.SessionPage{State: app.EvidenceComplete}, nil
}
func (s servicesStub) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	if s.timeline != nil {
		return s.timeline(ctx, request)
	}
	return app.TimelinePage{State: app.EvidenceComplete}, nil
}
func (s servicesStub) EventDetail(ctx context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
	if s.detail != nil {
		return s.detail(ctx, request)
	}
	return app.EventDetail{State: app.EvidenceNotFound}, nil
}
func (s servicesStub) EventLocations(ctx context.Context, ids []model.EventID) (map[model.EventID]app.EventLocation, error) {
	if s.locations != nil {
		return s.locations(ctx, ids)
	}
	return map[model.EventID]app.EventLocation{}, nil
}
func request(t *testing.T, handler http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, target, body)
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func extractCSRF(t *testing.T, body string) string {
	t.Helper()
	match := regexp.MustCompile(`name="csrf" value="([^"]+)"`).FindStringSubmatch(body)
	if len(match) != 2 {
		t.Fatalf("CSRF token not found in %q", body)
	}
	return match[1]
}

func validEventID(fill string) model.EventID {
	return model.EventID("evt_" + strings.Repeat(fill, 64))
}
