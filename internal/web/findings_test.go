package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/app/apptest"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestImportedStreamedAssessmentAndEvidenceNavigation(t *testing.T) {
	fixture := apptest.NewFixture(t)
	ctx := context.Background()
	sessions, err := fixture.Services.ListSessions(ctx, app.ListSessionsRequest{})
	if err != nil || len(sessions.Sessions) != 1 {
		t.Fatalf("sessions = %#v, %v", sessions, err)
	}
	session := sessions.Sessions[0].ID
	page, err := fixture.Services.SessionFindings(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	h := NewHandler(fixture.Services)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, sessionURL(session), nil))
	for _, want := range []string{"triggered", "Invocations: 4", "failures: 2", "recovered: 1", "uncertainties: 1", "Missing, conflicting, unsupported, or oversized evidence"} {
		if !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %q in imported assessment", want)
		}
	}
	for _, id := range page.Assessments[0].EventIDs {
		url := focusedTimelineURL(session, id)
		if !strings.Contains(w.Body.String(), url) {
			t.Fatalf("missing evidence link %s", id)
		}
		focused := httptest.NewRecorder()
		h.ServeHTTP(focused, httptest.NewRequest(http.MethodGet, url, nil))
		if focused.Code != http.StatusOK || !strings.Contains(focused.Body.String(), string(id)) {
			t.Fatalf("evidence navigation %s: status %d", id, focused.Code)
		}
	}
}

func TestAssessmentRenderingAndEvidenceContext(t *testing.T) {
	id := model.EventID("evt_" + strings.Repeat("a", 64))
	services := servicesStub{
		timeline: func(context.Context, app.TimelineRequest) (app.TimelinePage, error) {
			return app.TimelinePage{State: app.EvidenceComplete}, nil
		},
		findings: func(context.Context, model.SessionID) (app.FindingsPage, error) {
			return app.FindingsPage{State: app.EvidenceComplete, Assessments: []model.Finding{{RuleID: "repeated_command_failures", RuleVersion: "1", State: model.FindingTriggered, Explanation: "later recovery <script>alert(1)</script>", EventIDs: []model.EventID{id}, Metadata: map[string]string{"sequences": "1", "recovered": "1"}}}}, nil
		},
	}
	h := NewHandler(services)
	r := httptest.NewRequest(http.MethodGet, "/sessions/session", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	body := w.Body.String()
	for _, want := range []string{"Rule assessments", "triggered", "repeated_command_failures", "later recovery &lt;script", focusedTimelineURL("session", id)} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatal("unsafe markup")
	}
	services.findings = func(context.Context, model.SessionID) (app.FindingsPage, error) {
		return app.FindingsPage{State: app.EvidenceUnavailable, Status: "pending", Stale: true}, nil
	}
	w = httptest.NewRecorder()
	NewHandler(services).ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), "stale") || strings.Contains(w.Body.String(), "later recovery") {
		t.Fatal("stale assessment exposed")
	}
}
