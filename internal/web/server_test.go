package web

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
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

func TestImportProgressHandlerStreamsTerminalFailure(t *testing.T) {
	manager, err := app.NewImportManager(func(_ context.Context, source importer.Source, observe importer.ProgressObserver) ([]importer.ImportResult, error) {
		observe(importer.Progress{
			SourceID: source.ID, ActiveSourceID: source.ID, Phase: importer.PhaseImporting,
			DiagnosticsObserved: 1, Diagnostics: []model.Diagnostic{{Code: "unsafe", Severity: model.SeverityWarning, Message: "<script>"}},
		})
		return nil, errors.New("failed\ncleanly")
	}, app.ImportManagerOptions{})
	if err != nil {
		t.Fatal(err)
	}
	source := testImportSource("http-source")
	handler := NewImportProgressHandler(func(*http.Request) (*app.ImportSubscription, error) {
		subscription, _, err := manager.Request(source)
		return subscription, err
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/progress", nil))
	body := recorder.Body.String()
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), "text/event-stream") {
		t.Fatalf("response status/content-type = %d/%q", recorder.Code, recorder.Header().Get("Content-Type"))
	}
	for _, fragment := range []string{"event: failure", `"source":"http-source"`, `"phase":"failed"`, `failed\ncleanly`, `\u003cscript\u003e`} {
		if !strings.Contains(body, fragment) {
			t.Errorf("SSE body = %q, want %q", body, fragment)
		}
	}
	if strings.Contains(body, "\ndata: failed") {
		t.Fatalf("failure introduced an SSE data line: %q", body)
	}
}

func TestImportProgressJSONIncludesAggregateUnchangedCount(t *testing.T) {
	payload := importProgressJSON(app.ImportProgress{
		ImportResultsObserved: 100, UnchangedResultsObserved: 100, ImportResultsOmitted: 36,
	})
	if payload.ImportResultsObserved != 100 || payload.UnchangedResultsObserved != 100 || payload.ImportResultsOmitted != 36 {
		t.Fatalf("aggregate result counts = %#v", payload)
	}
}

func TestImportProgressHandlerDisconnectDoesNotCancelImport(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	runnerContext := make(chan context.Context, 1)
	manager, _ := app.NewImportManager(func(ctx context.Context, _ importer.Source, _ importer.ProgressObserver) ([]importer.ImportResult, error) {
		runnerContext <- ctx
		close(started)
		<-release
		return nil, nil
	}, app.ImportManagerOptions{})
	source := testImportSource("disconnect")
	primary, _, _ := manager.Request(source)
	defer primary.Close()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodGet, "/progress", nil).WithContext(ctx)
	done := make(chan struct{})
	go func() {
		NewImportProgressHandler(func(*http.Request) (*app.ImportSubscription, error) {
			subscription, _, err := manager.Request(source)
			return subscription, err
		}).ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSE handler did not stop after disconnect")
	}
	importCtx := <-runnerContext
	select {
	case <-importCtx.Done():
		t.Fatal("HTTP disconnect canceled shared import")
	default:
	}
	close(release)
	for range primary.Updates() {
	}
}

func testImportSource(id model.SourceID) importer.Source {
	return importer.Source{ID: id, Open: func(context.Context) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("")), nil
	}}
}
