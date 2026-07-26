package web

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

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
	projectionStatus   func(context.Context, model.SessionID) (app.ProjectionStatus, error)
	retryProjections   func(context.Context, model.SessionID) (app.ProjectionAction, error)
	rebuildProjections func(context.Context, model.SessionID, string) (app.ProjectionAction, error)
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

func TestNoJavaScriptDashboardIndexingAndFocusedTimeline(t *testing.T) {
	eventID := validEventID("a")
	sessionID := model.SessionID("session/encoded")
	activity := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	var listRequest app.ListSessionsRequest
	var timelineRequest app.TimelineRequest
	services := servicesStub{
		statusAll: func(context.Context) (app.ImportAllStatus, error) {
			return app.ImportAllStatus{Phase: app.ImportAllIssues, SourcesDiscovered: 2, DiagnosticsTotal: 1}, nil
		},
		overview: func(context.Context) (app.LibraryOverview, error) {
			return app.LibraryOverview{Sessions: 1, Events: 7, Agents: 1, IssueSessions: 1}, nil
		},
		list: func(_ context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
			listRequest = request
			return app.SessionPage{State: app.EvidencePartial, PreviousCursor: "previous", NextCursor: "next", Sessions: []app.SessionSummary{{
				ID: sessionID, AgentName: "codex", Preview: `<script>unsafe</script>`, LastActivityAt: &activity,
				EventCount: 7, State: app.EvidencePartial, Diagnostics: app.DiagnosticSynopsis{Total: 1},
			}}}, nil
		},
		timeline: func(_ context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
			timelineRequest = request
			return app.TimelinePage{State: app.EvidencePartial, FocusedEvent: request.FocusedEvent, Events: []model.EventSummary{{
				ID: eventID, SessionID: request.SessionID, Sequence: 7, Kind: model.EventKindMessage, Summary: `<b>escaped</b>`,
			}}, NextCursor: "more"}, nil
		},
		detail: func(_ context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
			return app.EventDetail{State: app.EvidenceComplete, Event: model.EventSummary{ID: request.EventID}, Payload: model.MessageData{Role: model.MessageRoleUser, Text: "payload"}}, nil
		},
		projectionStatus: func(context.Context, model.SessionID) (app.ProjectionStatus, error) {
			return app.ProjectionStatus{State: app.EvidenceComplete, SessionID: sessionID}, nil
		},
	}
	handler := NewHandler(services)
	dashboard := request(t, handler, http.MethodGet, "/?cursor=opaque&limit=17", nil, nil)
	body := dashboard.Body.String()
	for _, want := range []string{"Sessions", ">1</strong><span>Sessions", ">7</strong><span>Events", "Previous", "Next", "Indexing details", "&lt;script&gt;unsafe"} {
		if dashboard.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("dashboard status/body missing %q: %d %q", want, dashboard.Code, body)
		}
	}
	if strings.Contains(body, "<script>unsafe") || listRequest.Cursor != "opaque" || listRequest.Limit != 17 {
		t.Fatalf("dashboard escaped/request = %q / %#v", body, listRequest)
	}

	target := sessionURL(sessionID) + "?event=" + url.QueryEscape(string(eventID))
	timeline := request(t, handler, http.MethodGet, target, nil, nil)
	if timeline.Code != http.StatusOK || !strings.Contains(timeline.Body.String(), "Normalized payload") ||
		!strings.Contains(timeline.Body.String(), "&lt;b&gt;escaped&lt;/b&gt;") ||
		!strings.Contains(timeline.Body.String(), "Load more events") {
		t.Fatalf("timeline = %d %q", timeline.Code, timeline.Body.String())
	}
	if timelineRequest.SessionID != sessionID || timelineRequest.FocusedEvent != eventID {
		t.Fatalf("Timeline request = %#v", timelineRequest)
	}
}

func TestEventRedirectUsesCanonicalLocation(t *testing.T) {
	eventID := validEventID("b")
	handler := NewHandler(servicesStub{locations: func(context.Context, []model.EventID) (map[model.EventID]app.EventLocation, error) {
		return map[model.EventID]app.EventLocation{eventID: {EventID: eventID, SessionID: "s/1", Sequence: 4}}, nil
	}})
	response := request(t, handler, http.MethodGet, "/events/"+string(eventID), nil, nil)
	if response.Code != http.StatusSeeOther || response.Header().Get("Location") != focusedTimelineURL("s/1", eventID) {
		t.Fatalf("redirect = %d %q", response.Code, response.Header().Get("Location"))
	}
}

func TestMutationCSRFOriginFieldsAndRedirects(t *testing.T) {
	starts := 0
	handler := NewHandler(servicesStub{startAll: func(context.Context) (app.ImportAllStart, error) {
		starts++
		return app.ImportAllStart{Status: app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}}, nil
	}})
	page := request(t, handler, http.MethodGet, "/indexing", nil, nil)
	csrf := extractCSRF(t, page.Body.String())
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "http://example.com"}
	valid := request(t, handler, http.MethodPost, "/indexing/rescan", strings.NewReader(url.Values{"csrf": {csrf}}.Encode()), headers)
	if valid.Code != http.StatusSeeOther || valid.Header().Get("Location") != "/indexing?notice=rescan-started" || starts != 1 {
		t.Fatalf("valid mutation = %d %q calls=%d", valid.Code, valid.Header().Get("Location"), starts)
	}
	tests := []struct {
		name, body string
		headers    map[string]string
		status     int
	}{
		{"missing csrf", "", headers, http.StatusBadRequest},
		{"wrong csrf", "csrf=wrong", headers, http.StatusBadRequest},
		{"duplicate", "csrf=" + csrf + "&csrf=" + csrf, headers, http.StatusBadRequest},
		{"unexpected", "csrf=" + csrf + "&extra=1", headers, http.StatusBadRequest},
		{"cross origin", "csrf=" + csrf, map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Origin": "http://evil.example"}, http.StatusBadRequest},
		{"wrong type", "csrf=" + csrf, map[string]string{"Content-Type": "text/plain"}, http.StatusBadRequest},
		{"oversized", "csrf=" + csrf + strings.Repeat("x", maximumRequestBody), headers, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := request(t, handler, http.MethodPost, "/indexing/rescan", strings.NewReader(tt.body), tt.headers)
			if got.Code != tt.status {
				t.Fatalf("status = %d, want %d: %q", got.Code, tt.status, got.Body.String())
			}
		})
	}
}

func TestConditionalPollingAndRebuildAllConfirmation(t *testing.T) {
	active := app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}
	handler := NewHandler(servicesStub{
		statusAll: func(context.Context) (app.ImportAllStatus, error) { return active, nil },
		projectionStatus: func(_ context.Context, id model.SessionID) (app.ProjectionStatus, error) {
			return app.ProjectionStatus{State: app.EvidenceComplete, SessionID: id, Projections: []app.ProjectionState{{Kind: "search", BuildAvailable: true}}}, nil
		},
	})
	indexing := request(t, handler, http.MethodGet, "/indexing", nil, nil)
	if !strings.Contains(indexing.Body.String(), `hx-get="/fragments/index-status"`) {
		t.Fatalf("active indexing has no conditional polling: %q", indexing.Body.String())
	}
	active.Active = false
	indexing = request(t, handler, http.MethodGet, "/indexing", nil, nil)
	if strings.Contains(indexing.Body.String(), `hx-get="/fragments/index-status"`) {
		t.Fatalf("terminal indexing still polls: %q", indexing.Body.String())
	}
	confirm := request(t, handler, http.MethodGet, "/sessions/s1/projections/rebuild-all", nil, nil)
	if confirm.Code != http.StatusOK || !strings.Contains(confirm.Body.String(), "Confirm rebuild all") {
		t.Fatalf("confirmation = %d %q", confirm.Code, confirm.Body.String())
	}
}

func TestDocumentsExposeAccessibleStructureAndControls(t *testing.T) {
	handler := NewHandler(servicesStub{})
	for _, target := range []string{"/", "/indexing", "/sessions/session"} {
		response := request(t, handler, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", target, response.Code)
		}
		body := response.Body.String()
		for _, want := range []string{
			`<title>`, `class="skip-link" href="#main"`, `<main id="main"`,
			`aria-label="Primary"`, `aria-label="Breadcrumb"`, `:focus-visible`,
		} {
			if want == `:focus-visible` {
				continue // asserted in the embedded stylesheet below
			}
			if !strings.Contains(body, want) {
				t.Errorf("%s missing accessible structure %q", target, want)
			}
		}
	}
	styles := request(t, handler, http.MethodGet, "/assets/styles.css", nil, nil).Body.String()
	for _, want := range []string{":focus-visible", "prefers-reduced-motion", "[role=cell]::before"} {
		if !strings.Contains(styles, want) {
			t.Errorf("stylesheet missing %q", want)
		}
	}
}

func TestRejectsOldRoutesAndMalformedQueries(t *testing.T) {
	handler := NewHandler(servicesStub{})
	for _, target := range []string{"/timeline?session=s", "/imports", "/projections/retry", "/?unknown=1", "/?cursor=a&cursor=b", "/indexing?notice=unknown"} {
		response := request(t, handler, http.MethodGet, target, nil, nil)
		if target == "/imports" || target == "/projections/retry" || strings.HasPrefix(target, "/timeline") {
			if response.Code != http.StatusNotFound {
				t.Fatalf("%s status = %d, want 404", target, response.Code)
			}
		} else if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", target, response.Code)
		}
	}
}

func request(t *testing.T, handler http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
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
