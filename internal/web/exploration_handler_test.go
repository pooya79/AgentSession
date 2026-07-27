package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

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

func TestUnknownEvidenceInspectionIsCSRFProtectedNoStoreAndEscaped(t *testing.T) {
	eventID := validEventID("c")
	sessionID := model.SessionID("session")
	services := servicesStub{
		timeline: func(_ context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
			return app.TimelinePage{State: app.EvidenceComplete, Events: []model.EventSummary{{
				ID: eventID, SessionID: sessionID, Kind: model.EventKindUnknown, Summary: "future",
			}}}, nil
		},
		detail: func(context.Context, app.EventDetailRequest) (app.EventDetail, error) {
			return app.EventDetail{
				State:   app.EvidenceComplete,
				Event:   model.EventSummary{ID: eventID, SessionID: sessionID, Kind: model.EventKindUnknown},
				Payload: model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
			}, nil
		},
		inspect: func(context.Context, model.SessionID, model.EventID) (app.UnknownEvidenceInspection, error) {
			return app.UnknownEvidenceInspection{
				State: app.EvidenceComplete, SessionID: sessionID, EventID: eventID,
				Text: "<script>alert(1)</script>", OriginalSize: 30, ReturnedSize: 25, RedactionCount: 1,
			}, nil
		},
	}
	handler := NewHandler(services)
	page := request(t, handler, http.MethodGet, focusedTimelineURL(sessionID, eventID), nil, nil)
	csrf := extractCSRF(t, page.Body.String())
	target := unknownEvidenceFragmentURL(sessionID, eventID)
	rejected := request(t, handler, http.MethodPost, target, strings.NewReader("csrf=wrong"), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if rejected.Code != http.StatusBadRequest {
		t.Fatalf("CSRF rejection status = %d", rejected.Code)
	}
	response := request(t, handler, http.MethodPost, target, strings.NewReader(url.Values{"csrf": {csrf}}.Encode()), map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" ||
		strings.Contains(response.Body.String(), "<script>alert") ||
		!strings.Contains(response.Body.String(), "&lt;script&gt;alert") {
		t.Fatalf("inspection response = %d headers=%v body=%q", response.Code, response.Header(), response.Body.String())
	}
}
