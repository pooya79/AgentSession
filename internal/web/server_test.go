package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
)

func TestSourceTextEscapesHTML(t *testing.T) {
	const source = `<script data-value="'&">alert(1)</script>`
	var rendered bytes.Buffer
	if err := sourceText(source).Render(context.Background(), &rendered); err != nil {
		t.Fatal(err)
	}
	if body := rendered.String(); strings.Contains(body, "<script") || !strings.Contains(body, "&lt;script") {
		t.Fatalf("sourceText rendered unsafe content: %q", body)
	}
}

func TestHandlerHealthAssetsSecurityAndAvailability(t *testing.T) {
	handler := NewHandler(nil)
	tests := []struct {
		path, content string
		status        int
	}{
		{"/", "Service Unavailable", http.StatusServiceUnavailable},
		{"/healthz", "ok\n", http.StatusOK},
		{"/assets/styles.css", "color-scheme", http.StatusOK},
		{"/missing", "404 page not found", http.StatusNotFound},
	}
	for _, tt := range tests {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, tt.path, nil))
		body, _ := io.ReadAll(recorder.Result().Body)
		if recorder.Code != tt.status || !strings.Contains(string(body), tt.content) {
			t.Fatalf("%s = %d %q", tt.path, recorder.Code, body)
		}
		if recorder.Header().Get("Content-Security-Policy") == "" {
			t.Fatalf("%s has no CSP", tt.path)
		}
	}
}

func TestServeStartsAutomaticImportBeforeListening(t *testing.T) {
	calls := 0
	services := servicesStub{startAll: func(context.Context) (app.ImportAllStart, error) {
		calls++
		return app.ImportAllStart{Status: app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}}, nil
	}}
	if err := Serve(context.Background(), "127.0.0.1:-1", services); err == nil {
		t.Fatal("Serve() with invalid address returned no error")
	}
	if calls != 1 {
		t.Fatalf("automatic imports = %d, want 1", calls)
	}
}
