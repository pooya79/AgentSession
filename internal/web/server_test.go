package web

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSourceTextEscapesHTML(t *testing.T) {
	const source = `<script data-value="'&">alert(1)</script>`
	var rendered bytes.Buffer
	if err := sourceText(source).Render(context.Background(), &rendered); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	body := rendered.String()
	if strings.Contains(body, "<script") || strings.Contains(body, "</script>") {
		t.Fatalf("sourceText() rendered trusted HTML: %q", body)
	}
	for _, escaped := range []string{"&lt;script", "&#34;", "&#39;", "&amp;", "&lt;/script&gt;"} {
		if !strings.Contains(body, escaped) {
			t.Errorf("sourceText() = %q, want escaped fragment %q", body, escaped)
		}
	}
}

func TestHandler(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		status      int
		contentType string
		body        string
	}{
		{name: "index", method: http.MethodGet, path: "/", status: http.StatusOK, contentType: "text/html", body: "AgentSession"},
		{name: "health", method: http.MethodGet, path: "/healthz", status: http.StatusOK, contentType: "text/plain", body: "ok\n"},
		{name: "asset", method: http.MethodGet, path: "/assets/styles.css", status: http.StatusOK, contentType: "text/css", body: "color-scheme"},
		{name: "missing", method: http.MethodGet, path: "/missing", status: http.StatusNotFound, contentType: "text/plain", body: "404 page not found"},
		{name: "method", method: http.MethodPost, path: "/", status: http.StatusMethodNotAllowed, contentType: "text/plain", body: "Method Not Allowed"},
	}

	handler := NewHandler()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(tt.method, tt.path, nil))
			response := recorder.Result()
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if response.StatusCode != tt.status {
				t.Errorf("status = %d, want %d", response.StatusCode, tt.status)
			}
			if got := response.Header.Get("Content-Type"); !strings.Contains(got, tt.contentType) {
				t.Errorf("Content-Type = %q, want it to contain %q", got, tt.contentType)
			}
			if !strings.Contains(string(body), tt.body) {
				t.Errorf("body = %q, want it to contain %q", body, tt.body)
			}
		})
	}
}
