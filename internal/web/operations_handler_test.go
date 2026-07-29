package web

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

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

func TestRejectsMalformedQueries(t *testing.T) {
	handler := NewHandler(servicesStub{})
	for _, target := range []string{"/?unknown=1", "/?cursor=a&cursor=b", "/indexing?notice=unknown"} {
		response := request(t, handler, http.MethodGet, target, nil, nil)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", target, response.Code)
		}
	}
}
