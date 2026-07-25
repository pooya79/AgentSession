package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/app/apptest"
	"github.com/pooya79/AgentSession/internal/model"
)

type servicesStub struct {
	discover           func(context.Context) (app.SourceDiscovery, error)
	start              func(context.Context, model.SourceID) (app.ImportStart, error)
	startAll           func(context.Context) (app.ImportAllStart, error)
	statusAll          func(context.Context) (app.ImportAllStatus, error)
	list               func(context.Context, app.ListSessionsRequest) (app.SessionPage, error)
	timeline           func(context.Context, app.TimelineRequest) (app.TimelinePage, error)
	detail             func(context.Context, app.EventDetailRequest) (app.EventDetail, error)
	projectionStatus   func(context.Context, model.SessionID) (app.ProjectionStatus, error)
	retryProjections   func(context.Context, model.SessionID) (app.ProjectionAction, error)
	rebuildProjections func(context.Context, model.SessionID, string) (app.ProjectionAction, error)
}

func (s servicesStub) ProjectionStatus(ctx context.Context, sessionID model.SessionID) (app.ProjectionStatus, error) {
	if s.projectionStatus != nil {
		return s.projectionStatus(ctx, sessionID)
	}
	return app.ProjectionStatus{State: app.EvidenceNotFound, SessionID: sessionID}, nil
}

func (s servicesStub) RetryProjections(ctx context.Context, sessionID model.SessionID) (app.ProjectionAction, error) {
	if s.retryProjections != nil {
		return s.retryProjections(ctx, sessionID)
	}
	return app.ProjectionAction{State: app.EvidenceNotFound}, nil
}

func (s servicesStub) RebuildProjections(ctx context.Context, sessionID model.SessionID, kind string) (app.ProjectionAction, error) {
	if s.rebuildProjections != nil {
		return s.rebuildProjections(ctx, sessionID, kind)
	}
	return app.ProjectionAction{State: app.EvidenceNotFound}, nil
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
	return app.SourceDiscovery{State: app.EvidenceComplete}, nil
}

func (s servicesStub) StartImport(ctx context.Context, sourceID model.SourceID) (app.ImportStart, error) {
	if s.start != nil {
		return s.start(ctx, sourceID)
	}
	return app.ImportStart{State: app.EvidenceNotFound}, nil
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
	return app.TimelinePage{State: app.EvidenceNotFound}, nil
}

func (s servicesStub) EventDetail(ctx context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
	if s.detail != nil {
		return s.detail(ctx, request)
	}
	return app.EventDetail{State: app.EvidenceNotFound}, nil
}

func TestSourceFragmentStatesAndEscaping(t *testing.T) {
	hostile := `<img src=x onerror="alert(1)">`
	tests := []struct {
		name      string
		discovery app.SourceDiscovery
		want      []string
	}{
		{name: "empty", discovery: app.SourceDiscovery{State: app.EvidenceComplete}, want: []string{"No supported session sources"}},
		{name: "unavailable", discovery: app.SourceDiscovery{State: app.EvidenceUnavailable, Diagnostics: []app.DiscoveryDiagnostic{{Code: hostile, Message: hostile, Path: hostile}}}, want: []string{"Source discovery is unavailable", "&lt;img"}},
		{name: "partial", discovery: app.SourceDiscovery{State: app.EvidencePartial, Sources: []app.SourceSummary{{ID: "source-1", Kind: hostile, Path: hostile, Origin: hostile}}, Diagnostics: []app.DiscoveryDiagnostic{{Code: "warning", Message: hostile}}}, want: []string{"1 source discovered", "&lt;img"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewHandler(servicesStub{discover: func(context.Context) (app.SourceDiscovery, error) { return tt.discovery, nil }})
			response := request(t, handler, http.MethodGet, "/fragments/sources", nil, nil)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
			body := response.Body.String()
			for _, want := range tt.want {
				if !strings.Contains(body, want) {
					t.Errorf("body does not contain %q: %q", want, body)
				}
			}
			if strings.Contains(body, "<img") || strings.Contains(body, "onerror=\"alert") {
				t.Fatalf("untrusted source content rendered as markup: %q", body)
			}
		})
	}
}

func TestSessionAndTimelineFragmentsEscapeContentAndStayLightweight(t *testing.T) {
	hostile := `<script>alert("x")</script>`
	eventID := validEventID("a")
	var mu sync.Mutex
	var listRequest app.ListSessionsRequest
	var timelineRequests []app.TimelineRequest
	detailCalls := 0
	services := servicesStub{
		list: func(_ context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
			mu.Lock()
			listRequest = request
			mu.Unlock()
			return app.SessionPage{State: app.EvidencePartial, Sessions: []app.SessionSummary{{
				ID: "session&one", Title: hostile, Summary: hostile, EventCount: 1, State: app.EvidencePartial,
			}}, NextCursor: "opaque+/cursor"}, nil
		},
		timeline: func(_ context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
			mu.Lock()
			timelineRequests = append(timelineRequests, request)
			mu.Unlock()
			return app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{{
				ID: eventID, SessionID: request.SessionID, Sequence: 3, Kind: model.EventKindMessage, Summary: hostile,
			}}, NextCursor: "next opaque"}, nil
		},
		detail: func(context.Context, app.EventDetailRequest) (app.EventDetail, error) {
			detailCalls++
			return app.EventDetail{}, nil
		},
	}
	handler := NewHandler(services)
	sessions := request(t, handler, http.MethodGet, "/fragments/sessions?cursor=opaque%2B%2Fcursor&limit=17", nil, nil)
	if sessions.Code != http.StatusOK || strings.Contains(sessions.Body.String(), "<script>") || !strings.Contains(sessions.Body.String(), "&lt;script") {
		t.Fatalf("sessions response = %d %q", sessions.Code, sessions.Body.String())
	}
	if listRequest.Cursor != "opaque+/cursor" || listRequest.Limit != 17 {
		t.Fatalf("ListSessions request = %#v", listRequest)
	}

	timeline := request(t, handler, http.MethodGet, "/timeline?session=session%26one&limit=9", nil, nil)
	if timeline.Code != http.StatusOK || strings.Contains(timeline.Body.String(), "<script>") || !strings.Contains(timeline.Body.String(), "&lt;script") {
		t.Fatalf("timeline response = %d %q", timeline.Code, timeline.Body.String())
	}
	if detailCalls != 0 {
		t.Fatalf("timeline called EventDetail %d time(s)", detailCalls)
	}
	if len(timelineRequests) != 1 || timelineRequests[0].Cursor != "" || timelineRequests[0].Limit != 9 {
		t.Fatalf("Timeline requests = %#v", timelineRequests)
	}

	next := request(t, handler, http.MethodGet, "/fragments/timeline?session=session%26one&cursor=next+opaque&limit=4", nil, nil)
	if next.Code != http.StatusOK || len(timelineRequests) != 2 || timelineRequests[1].Cursor != "next opaque" || timelineRequests[1].Limit != 4 {
		t.Fatalf("next timeline = %d, requests %#v", next.Code, timelineRequests)
	}
}

func TestEventDetailRequestsPayloadAndEscapesIt(t *testing.T) {
	eventID := validEventID("b")
	hostile := `</pre><script>alert(1)</script>`
	var got app.EventDetailRequest
	handler := NewHandler(servicesStub{detail: func(_ context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
		got = request
		return app.EventDetail{State: app.EvidencePartial, Event: model.EventSummary{ID: eventID}, Payload: model.MessageData{Role: model.MessageRoleUser, Text: hostile}, Diagnostics: app.DiagnosticSynopsis{Total: 1, Diagnostics: []model.Diagnostic{{Code: hostile, Message: hostile}}}}, nil
	}})
	response := request(t, handler, http.MethodGet, "/fragments/event?session=s1&event="+url.QueryEscape(string(eventID)), nil, nil)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
	}
	if !got.IncludePayload || got.SessionID != "s1" || got.EventID != eventID {
		t.Fatalf("EventDetail request = %#v", got)
	}
	body := response.Body.String()
	if strings.Contains(body, "</pre><script>") || !strings.Contains(body, "&lt;/pre&gt;&lt;script&gt;") {
		t.Fatalf("payload was not escaped text: %q", body)
	}
}

func TestEventDetailUnavailableIsHonest(t *testing.T) {
	handler := NewHandler(servicesStub{detail: func(context.Context, app.EventDetailRequest) (app.EventDetail, error) {
		return app.EventDetail{State: app.EvidenceUnavailable}, nil
	}})
	response := request(t, handler, http.MethodGet, "/fragments/event?session=s1&event="+string(validEventID("c")), nil, nil)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "not evidence of success") {
		t.Fatalf("response = %d %q", response.Code, response.Body.String())
	}
}

func TestHandlerRejectsMalformedAndDuplicateInputs(t *testing.T) {
	services := servicesStub{
		list: func(context.Context, app.ListSessionsRequest) (app.SessionPage, error) { return app.SessionPage{}, nil },
		timeline: func(context.Context, app.TimelineRequest) (app.TimelinePage, error) {
			return app.TimelinePage{}, app.ErrInvalidRequest
		},
		detail: func(context.Context, app.EventDetailRequest) (app.EventDetail, error) {
			return app.EventDetail{}, app.ErrInvalidRequest
		},
	}
	handler := NewHandler(services)
	tests := []string{
		"/?unexpected=1",
		"/fragments/sessions?limit=0",
		"/fragments/sessions?limit=201",
		"/fragments/sessions?limit=1&limit=2",
		"/fragments/sessions?cursor=",
		"/fragments/sessions?unknown=x",
		"/timeline?session=s&session=t",
		"/timeline?session=%20bad",
		"/fragments/timeline?session=s&cursor=wrong",
		"/fragments/event?session=s&event=bad",
	}
	for _, target := range tests {
		t.Run(target, func(t *testing.T) {
			response := request(t, handler, http.MethodGet, target, nil, nil)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body = %q", response.Code, response.Body.String())
			}
		})
	}
}

func TestImportRequestValidationAndStatusMapping(t *testing.T) {
	calls := 0
	handler := NewHandler(servicesStub{startAll: func(context.Context) (app.ImportAllStart, error) {
		calls++
		return app.ImportAllStart{Status: app.ImportAllStatus{RunID: 7, Active: true, Phase: app.ImportAllIndexing}, Joined: true}, nil
	}})

	validHeaders := map[string]string{"Content-Type": "application/x-www-form-urlencoded", importHeader: "import", "Origin": "http://example.com"}
	valid := request(t, handler, http.MethodPost, "/imports", strings.NewReader(""), validHeaders)
	if valid.Code != http.StatusAccepted || valid.Header().Get("X-AgentSession-Import-Joined") != "true" || !strings.Contains(valid.Body.String(), "Indexing local evidence") || calls != 1 {
		t.Fatalf("valid import = %d %q", valid.Code, valid.Body.String())
	}

	tests := []struct {
		name    string
		body    string
		headers map[string]string
		status  int
	}{
		{name: "missing header", headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded"}, status: http.StatusBadRequest},
		{name: "cross origin", headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded", importHeader: "import", "Origin": "http://evil.example"}, status: http.StatusBadRequest},
		{name: "wrong media", headers: map[string]string{"Content-Type": "text/plain", importHeader: "import"}, status: http.StatusBadRequest},
		{name: "source selector rejected", body: "source=a", headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded", importHeader: "import"}, status: http.StatusBadRequest},
		{name: "extra", body: "other=b", headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded", importHeader: "import"}, status: http.StatusBadRequest},
		{name: "oversized", body: "extra=" + strings.Repeat("x", maximumRequestBody+1), headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded", importHeader: "import"}, status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, handler, http.MethodPost, "/imports", strings.NewReader(tt.body), tt.headers)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, tt.status, response.Body.String())
			}
		})
	}
}

func TestProjectionStatusActionsPollingEscapingAndValidation(t *testing.T) {
	hostile := `<img src=x onerror="alert(1)">`
	status := app.ProjectionStatus{
		State: app.EvidenceComplete, SessionID: "session-1", Active: true,
		Summary: app.ProjectionSummary{Running: 1, Stale: 1},
		Projections: []app.ProjectionState{{
			Kind: hostile, Status: "running", TargetVersion: hostile, TargetRevision: 2, Stale: true,
			Diagnostic: &app.ProjectionDiagnostic{Code: hostile, Summary: hostile},
		}},
	}
	var retries int
	var rebuilds []string
	handler := NewHandler(servicesStub{
		projectionStatus: func(context.Context, model.SessionID) (app.ProjectionStatus, error) {
			return status, nil
		},
		retryProjections: func(context.Context, model.SessionID) (app.ProjectionAction, error) {
			retries++
			return app.ProjectionAction{State: app.EvidenceComplete, Active: true, Joined: true, Status: status}, nil
		},
		rebuildProjections: func(_ context.Context, _ model.SessionID, kind string) (app.ProjectionAction, error) {
			if kind == "invalid" {
				return app.ProjectionAction{}, app.ErrInvalidRequest
			}
			rebuilds = append(rebuilds, kind)
			return app.ProjectionAction{State: app.EvidenceComplete, Active: true, Status: status}, nil
		},
	})

	got := request(t, handler, http.MethodGet, "/fragments/projections?session=session-1", nil, nil)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "delay:500ms") ||
		!strings.Contains(got.Body.String(), "&lt;img") || strings.Contains(got.Body.String(), "<img") {
		t.Fatalf("projection fragment = %d %q", got.Code, got.Body.String())
	}

	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
		importHeader:   "projection",
		"Origin":       "http://example.com",
	}
	retry := request(t, handler, http.MethodPost, "/projections/retry", strings.NewReader("session=session-1"), headers)
	if retry.Code != http.StatusAccepted || retry.Header().Get("X-AgentSession-Projection-Joined") != "true" || retries != 1 {
		t.Fatalf("retry response = %d %q calls=%d", retry.Code, retry.Body.String(), retries)
	}
	rebuild := request(t, handler, http.MethodPost, "/projections/rebuild", strings.NewReader("session=session-1&kind=all"), headers)
	if rebuild.Code != http.StatusAccepted || len(rebuilds) != 1 || rebuilds[0] != app.ProjectionKindAll {
		t.Fatalf("rebuild response = %d %q calls=%v", rebuild.Code, rebuild.Body.String(), rebuilds)
	}

	tests := []struct {
		name, body string
		headers    map[string]string
		status     int
	}{
		{name: "duplicate", body: "session=a&session=b", headers: headers, status: http.StatusBadRequest},
		{name: "extra", body: "session=a&extra=b", headers: headers, status: http.StatusBadRequest},
		{name: "wrong media", body: "session=a", headers: map[string]string{importHeader: "projection", "Content-Type": "text/plain"}, status: http.StatusBadRequest},
		{name: "cross origin", body: "session=a", headers: map[string]string{importHeader: "projection", "Content-Type": "application/x-www-form-urlencoded", "Origin": "http://evil.example"}, status: http.StatusBadRequest},
		{name: "invalid kind", body: "session=a&kind=invalid", headers: headers, status: http.StatusBadRequest},
		{name: "oversized", body: "session=" + strings.Repeat("x", maximumRequestBody+1), headers: headers, status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := "/projections/retry"
			if tt.name == "invalid kind" {
				path = "/projections/rebuild"
			}
			response := request(t, handler, http.MethodPost, path, strings.NewReader(tt.body), tt.headers)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d; body = %q", response.Code, tt.status, response.Body.String())
			}
		})
	}
}

func TestImportResponseReportsCoalescedWork(t *testing.T) {
	handler := NewHandler(servicesStub{startAll: func(context.Context) (app.ImportAllStart, error) {
		return app.ImportAllStart{Joined: true, Status: app.ImportAllStatus{RunID: 3, Active: true, Phase: app.ImportAllIndexing}}, nil
	}})
	response := request(t, handler, http.MethodPost, "/imports", strings.NewReader(""), map[string]string{
		"Content-Type": "application/x-www-form-urlencoded", importHeader: "import",
	})
	if response.Header().Get("X-AgentSession-Import-Joined") != "true" || !strings.Contains(response.Body.String(), "Scanning") {
		t.Fatalf("coalesced response headers/body = %#v %q", response.Header(), response.Body.String())
	}
}

func TestImportStatusTerminalTriggersSessionRefreshAndEscapesDetails(t *testing.T) {
	hostile := `<script>alert(1)</script>`
	handler := NewHandler(servicesStub{statusAll: func(context.Context) (app.ImportAllStatus, error) {
		return app.ImportAllStatus{
			Phase: app.ImportAllIssues, SourcesDiscovered: 1, SourcesCompleted: 1, SourcesFailed: 1,
			DiagnosticsTotal: 2, DiagnosticsOmitted: 1,
			Sources:           []app.ImportAllSourceStatus{{ID: "source", Kind: "codex", Path: hostile, Origin: "default", Phase: app.ImportFailed, Failure: hostile}},
			RecentDiagnostics: []app.ImportAllDiagnostic{{Code: hostile, Message: hostile, SourcePath: hostile, EventIDs: []model.EventID{validEventID("status")}}},
		}, nil
	}})
	response := request(t, handler, http.MethodGet, "/fragments/import-status", nil, nil)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("HX-Trigger") != "sessionsRefresh" || !strings.Contains(body, "completed with issues") || !strings.Contains(body, "earlier diagnostic") {
		t.Fatalf("terminal status = %d %#v %q", response.Code, response.Header(), body)
	}
	if strings.Contains(body, "<script>") || !strings.Contains(body, "&lt;script") || !strings.Contains(body, "Source details") {
		t.Fatalf("status details were not safely rendered: %q", body)
	}
}

func TestServiceErrorsAndMissingResourcesMapToHonestStatuses(t *testing.T) {
	tests := []struct {
		name     string
		services app.Services
		target   string
		status   int
	}{
		{name: "nil services", services: nil, target: "/fragments/sources", status: http.StatusServiceUnavailable},
		{name: "shutdown", services: servicesStub{discover: func(context.Context) (app.SourceDiscovery, error) { return app.SourceDiscovery{}, app.ErrShuttingDown }}, target: "/fragments/sources", status: http.StatusServiceUnavailable},
		{name: "storage", services: servicesStub{list: func(context.Context, app.ListSessionsRequest) (app.SessionPage, error) {
			return app.SessionPage{}, errors.New("secret storage detail")
		}}, target: "/fragments/sessions", status: http.StatusInternalServerError},
		{name: "missing session", services: servicesStub{timeline: func(context.Context, app.TimelineRequest) (app.TimelinePage, error) {
			return app.TimelinePage{State: app.EvidenceNotFound}, nil
		}}, target: "/timeline?session=missing", status: http.StatusNotFound},
		{name: "missing event", services: servicesStub{detail: func(context.Context, app.EventDetailRequest) (app.EventDetail, error) {
			return app.EventDetail{State: app.EvidenceNotFound}, nil
		}}, target: "/fragments/event?session=s&event=" + string(validEventID("d")), status: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := request(t, NewHandler(tt.services), http.MethodGet, tt.target, nil, nil)
			if response.Code != tt.status {
				t.Fatalf("status = %d, want %d", response.Code, tt.status)
			}
			if strings.Contains(response.Body.String(), "secret storage detail") {
				t.Fatalf("internal service detail leaked: %q", response.Body.String())
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	response := request(t, NewHandler(nil), http.MethodGet, "/", nil, nil)
	for _, header := range []string{"Content-Security-Policy", "X-Content-Type-Options", "X-Frame-Options", "Referrer-Policy"} {
		if response.Header().Get(header) == "" {
			t.Errorf("%s header is missing", header)
		}
	}
}

func TestEndToEndLocalWebWorkflow(t *testing.T) {
	fixture := apptest.NewFixture(t)
	handler := NewHandler(fixture.Services)
	sources := request(t, handler, http.MethodGet, "/fragments/sources", nil, nil)
	if sources.Code != http.StatusOK || !strings.Contains(sources.Body.String(), "source discovered") || strings.Contains(sources.Body.String(), "checkbox") {
		t.Fatalf("sources = %d %q", sources.Code, sources.Body.String())
	}
	discovery, err := fixture.Services.DiscoverSources(context.Background())
	if err != nil || len(discovery.Sources) == 0 {
		t.Fatalf("DiscoverSources() = (%#v, %v)", discovery, err)
	}
	headers := map[string]string{"Content-Type": "application/x-www-form-urlencoded", importHeader: "import"}
	imported := request(t, handler, http.MethodPost, "/imports", strings.NewReader(""), headers)
	if imported.Code != http.StatusAccepted || !strings.Contains(imported.Body.String(), "Indexing local evidence") {
		t.Fatalf("import = %d %q", imported.Code, imported.Body.String())
	}
	sessions := request(t, handler, http.MethodGet, "/fragments/sessions?limit=1", nil, nil)
	if sessions.Code != http.StatusOK || !strings.Contains(sessions.Body.String(), "/timeline?") {
		t.Fatalf("sessions = %d %q", sessions.Code, sessions.Body.String())
	}
	page, err := fixture.Services.ListSessions(context.Background(), app.ListSessionsRequest{Limit: 1})
	if err != nil || len(page.Sessions) != 1 {
		t.Fatalf("ListSessions() = (%#v, %v)", page, err)
	}
	sessionID := page.Sessions[0].ID
	timeline := request(t, handler, http.MethodGet, "/timeline?session="+url.QueryEscape(string(sessionID))+"&limit=1", nil, nil)
	if timeline.Code != http.StatusOK || !strings.Contains(timeline.Body.String(), "Inspect event detail") {
		t.Fatalf("timeline = %d %q", timeline.Code, timeline.Body.String())
	}
	timelinePage, err := fixture.Services.Timeline(context.Background(), app.TimelineRequest{SessionID: sessionID, Limit: 1})
	if err != nil || len(timelinePage.Events) != 1 {
		t.Fatalf("Timeline() = (%#v, %v)", timelinePage, err)
	}
	if timelinePage.NextCursor != "" {
		nextURL := "/fragments/timeline?" + url.Values{"session": {string(sessionID)}, "cursor": {timelinePage.NextCursor}, "limit": {"1"}}.Encode()
		next := request(t, handler, http.MethodGet, nextURL, nil, nil)
		if next.Code != http.StatusOK {
			t.Fatalf("next timeline = %d %q", next.Code, next.Body.String())
		}
	}
	detailURL := "/fragments/event?" + url.Values{"session": {string(sessionID)}, "event": {string(timelinePage.Events[0].ID)}}.Encode()
	detail := request(t, handler, http.MethodGet, detailURL, nil, nil)
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), "Normalized payload") {
		t.Fatalf("detail = %d %q", detail.Code, detail.Body.String())
	}
}

func request(t *testing.T, handler http.Handler, method, target string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, body)
	req.Host = "example.com"
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	return recorder
}

func validEventID(character string) model.EventID {
	return model.EventID("evt_" + strings.Repeat(character, 64))
}

func TestImportBodyLimitConstant(t *testing.T) {
	if maximumRequestBody < 1024 || maximumRequestBody > 64*1024 {
		t.Fatalf("maximumRequestBody = %s", strconv.Itoa(maximumRequestBody))
	}
}
