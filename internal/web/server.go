package web

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/a-h/templ"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

const (
	DefaultAddress     = "127.0.0.1:8080"
	defaultPageLimit   = 50
	maximumRequestBody = 8 << 10
	importHeader       = "X-AgentSession-Request"
)

//go:embed assets/*
var embeddedAssets embed.FS

// ImportSubscriptionProvider resolves an application-owned subscription for
// one HTTP request. It remains public so alternate local presentation shells
// can reuse the observer-only SSE boundary.
type ImportSubscriptionProvider func(*http.Request) (*app.ImportSubscription, error)

// NewImportProgressHandler renders a shared import subscription as SSE. A
// disconnected HTTP client detaches only its observer, never the import job.
func NewImportProgressHandler(provider ImportSubscriptionProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if provider == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		subscription, err := provider(r)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if subscription == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		streamImport(w, r, subscription)
	})
}

func streamImport(w http.ResponseWriter, r *http.Request, subscription *app.ImportSubscription) {
	defer subscription.Close()
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")

	for {
		select {
		case <-r.Context().Done():
			return
		case progress, open := <-subscription.Updates():
			if !open {
				return
			}
			payload, err := json.Marshal(importProgressJSON(progress))
			if err != nil {
				return
			}
			event := "progress"
			if progress.Failure != nil {
				event = "failure"
			} else if progress.Complete {
				event = "completion"
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

type importProgressPayload struct {
	RunID                    uint64                   `json:"run_id"`
	SourceID                 model.SourceID           `json:"source"`
	ActiveSourceID           model.SourceID           `json:"active_source"`
	Phase                    app.ImportPhase          `json:"phase"`
	RecordsProcessed         int64                    `json:"records_processed"`
	EventsProcessed          int64                    `json:"events_processed"`
	RecordsCommitted         int64                    `json:"records_committed"`
	BatchesCommitted         int64                    `json:"batches_committed"`
	DiagnosticsObserved      int64                    `json:"diagnostics_observed"`
	DiagnosticsOmitted       int64                    `json:"diagnostics_omitted"`
	RecentDiagnostics        []diagnosticPayload      `json:"recent_diagnostics,omitempty"`
	ImportedSessions         []importedSessionPayload `json:"imported_sessions,omitempty"`
	ImportResultsObserved    int64                    `json:"import_results_observed"`
	UnchangedResultsObserved int64                    `json:"unchanged_results_observed"`
	ImportResultsOmitted     int64                    `json:"import_results_omitted"`
	Complete                 bool                     `json:"complete"`
	Failure                  string                   `json:"failure,omitempty"`
}

type importedSessionPayload struct {
	SourceID          model.SourceID  `json:"source"`
	SessionID         model.SessionID `json:"session"`
	Change            string          `json:"change"`
	RecordsCommitted  int64           `json:"records_committed"`
	BatchesCommitted  int64           `json:"batches_committed"`
	CanonicalChanged  bool            `json:"canonical_changed"`
	Reconciled        bool            `json:"reconciled"`
	ProjectionWarning bool            `json:"projection_warning"`
}

type diagnosticPayload struct {
	Code         string              `json:"code"`
	Severity     model.Severity      `json:"severity"`
	Message      string              `json:"message"`
	EventIDs     []model.EventID     `json:"event_ids,omitempty"`
	RawRecordIDs []model.RawRecordID `json:"raw_record_ids,omitempty"`
}

func importProgressJSON(progress app.ImportProgress) importProgressPayload {
	payload := importProgressPayload{
		RunID: progress.RunID, SourceID: progress.SourceID, ActiveSourceID: progress.ActiveSourceID,
		Phase: progress.Phase, RecordsProcessed: progress.RecordsProcessed, EventsProcessed: progress.EventsProcessed,
		RecordsCommitted: progress.RecordsCommitted, BatchesCommitted: progress.BatchesCommitted,
		DiagnosticsObserved: progress.DiagnosticsObserved, DiagnosticsOmitted: progress.DiagnosticsOmitted,
		ImportResultsObserved: progress.ImportResultsObserved, UnchangedResultsObserved: progress.UnchangedResultsObserved,
		ImportResultsOmitted: progress.ImportResultsOmitted, Complete: progress.Complete,
	}
	for _, diagnostic := range progress.RecentDiagnostics {
		payload.RecentDiagnostics = append(payload.RecentDiagnostics, diagnosticPayload{
			Code: diagnostic.Code, Severity: diagnostic.Severity, Message: diagnostic.Message,
			EventIDs: diagnostic.EventIDs, RawRecordIDs: diagnostic.RawRecordIDs,
		})
	}
	for _, summary := range progress.ImportedSessions {
		payload.ImportedSessions = append(payload.ImportedSessions, importedSessionPayload{
			SourceID: summary.SourceID, SessionID: summary.SessionID, Change: string(summary.Change),
			RecordsCommitted: summary.RecordsCommitted, BatchesCommitted: summary.BatchesCommitted,
			CanonicalChanged: summary.CanonicalChanged, Reconciled: summary.Reconciled,
			ProjectionWarning: summary.ProjectionWarning,
		})
	}
	if progress.Failure != nil {
		payload.Failure = progress.Failure.Error()
	}
	return payload
}

// NewHandler creates the local web interface over the shared application services.
func NewHandler(services app.Services) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !requireNoQuery(w, r) {
			return
		}
		render(w, r, http.StatusOK, indexPage())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if !requireNoQuery(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /fragments/sources", func(w http.ResponseWriter, r *http.Request) {
		if !requireNoQuery(w, r) {
			return
		}
		if services == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		discovery, err := services.DiscoverSources(r.Context())
		if err != nil {
			writeServiceError(w, err)
			return
		}
		render(w, r, http.StatusOK, sourcesFragment(discovery))
	})
	mux.HandleFunc("POST /imports", func(w http.ResponseWriter, r *http.Request) {
		if services == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		if !validImportRequest(w, r) {
			return
		}
		sourceID := model.SourceID(r.PostForm["source"][0])
		started, err := services.StartImport(r.Context(), sourceID)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if started.State == app.EvidenceNotFound {
			writeError(w, http.StatusNotFound)
			return
		}
		if started.Subscription == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("X-AgentSession-Import-Joined", strconv.FormatBool(started.Joined))
		streamImport(w, r, started.Subscription)
	})
	mux.HandleFunc("GET /fragments/sessions", func(w http.ResponseWriter, r *http.Request) {
		if services == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		values, ok := strictQuery(w, r, "cursor", "limit")
		if !ok {
			return
		}
		cursor, ok := optionalSingle(w, values, "cursor")
		if !ok {
			return
		}
		limit, ok := parseLimit(w, values)
		if !ok {
			return
		}
		page, err := services.ListSessions(r.Context(), app.ListSessionsRequest{Cursor: cursor, Limit: limit})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		render(w, r, http.StatusOK, sessionsFragment(page, cursor == ""))
	})
	mux.HandleFunc("GET /timeline", func(w http.ResponseWriter, r *http.Request) {
		if services == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		values, ok := strictQuery(w, r, "session", "limit")
		if !ok {
			return
		}
		session, ok := requiredSingle(w, values, "session")
		if !ok {
			return
		}
		limit, ok := parseLimit(w, values)
		if !ok {
			return
		}
		page, err := services.Timeline(r.Context(), app.TimelineRequest{SessionID: model.SessionID(session), Limit: limit})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if page.State == app.EvidenceNotFound {
			writeError(w, http.StatusNotFound)
			return
		}
		render(w, r, http.StatusOK, timelinePage(model.SessionID(session), page))
	})
	mux.HandleFunc("GET /fragments/timeline", func(w http.ResponseWriter, r *http.Request) {
		if services == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		values, ok := strictQuery(w, r, "session", "cursor", "limit")
		if !ok {
			return
		}
		session, ok := requiredSingle(w, values, "session")
		if !ok {
			return
		}
		cursor, ok := requiredSingle(w, values, "cursor")
		if !ok {
			return
		}
		limit, ok := parseLimit(w, values)
		if !ok {
			return
		}
		page, err := services.Timeline(r.Context(), app.TimelineRequest{SessionID: model.SessionID(session), Cursor: cursor, Limit: limit})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if page.State == app.EvidenceNotFound {
			writeError(w, http.StatusNotFound)
			return
		}
		render(w, r, http.StatusOK, timelineFragment(model.SessionID(session), page))
	})
	mux.HandleFunc("GET /fragments/event", func(w http.ResponseWriter, r *http.Request) {
		if services == nil {
			writeError(w, http.StatusServiceUnavailable)
			return
		}
		values, ok := strictQuery(w, r, "session", "event")
		if !ok {
			return
		}
		session, ok := requiredSingle(w, values, "session")
		if !ok {
			return
		}
		event, ok := requiredSingle(w, values, "event")
		if !ok {
			return
		}
		detail, err := services.EventDetail(r.Context(), app.EventDetailRequest{
			SessionID: model.SessionID(session), EventID: model.EventID(event), IncludePayload: true,
		})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if detail.State == app.EvidenceNotFound {
			writeError(w, http.StatusNotFound)
			return
		}
		payload := ""
		if detail.Payload != nil {
			payload, err = normalizedPayload(detail.Payload)
			if err != nil {
				writeError(w, http.StatusInternalServerError)
				return
			}
		}
		render(w, r, http.StatusOK, eventFragment(detail, payload))
	})

	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("web: embedded assets are unavailable: " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets)))
	return securityHeaders(mux)
}

func normalizedPayload(payload model.NormalizedData) (string, error) {
	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(payload); err != nil {
		return "", err
	}
	return strings.TrimSuffix(output.String(), "\n"), nil
}

func render(w http.ResponseWriter, r *http.Request, status int, component templ.Component) {
	var output bytes.Buffer
	if err := component.Render(r.Context(), &output); err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.Copy(w, &output)
}

func strictQuery(w http.ResponseWriter, r *http.Request, allowed ...string) (url.Values, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest)
		return nil, false
	}
	valid := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		valid[key] = struct{}{}
	}
	for key := range values {
		if _, exists := valid[key]; !exists {
			writeError(w, http.StatusBadRequest)
			return nil, false
		}
	}
	return values, true
}

func requireNoQuery(w http.ResponseWriter, r *http.Request) bool {
	_, ok := strictQuery(w, r)
	return ok
}

func requiredSingle(w http.ResponseWriter, values url.Values, key string) (string, bool) {
	items, exists := values[key]
	if !exists || len(items) != 1 || items[0] == "" {
		writeError(w, http.StatusBadRequest)
		return "", false
	}
	return items[0], true
}

func optionalSingle(w http.ResponseWriter, values url.Values, key string) (string, bool) {
	items, exists := values[key]
	if !exists {
		return "", true
	}
	if len(items) != 1 || items[0] == "" {
		writeError(w, http.StatusBadRequest)
		return "", false
	}
	return items[0], true
}

func parseLimit(w http.ResponseWriter, values url.Values) (int, bool) {
	raw, ok := optionalSingle(w, values, "limit")
	if !ok {
		return 0, false
	}
	if raw == "" {
		return defaultPageLimit, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > app.MaximumPageSize {
		writeError(w, http.StatusBadRequest)
		return 0, false
	}
	return limit, true
}

func validImportRequest(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" || r.Header.Get(importHeader) != "import" || !sameOrigin(r) {
		writeError(w, http.StatusBadRequest)
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		writeError(w, http.StatusBadRequest)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumRequestBody)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge)
		} else {
			writeError(w, http.StatusBadRequest)
		}
		return false
	}
	if len(r.PostForm) != 1 {
		writeError(w, http.StatusBadRequest)
		return false
	}
	_, ok := requiredSingle(w, r.PostForm, "source")
	return ok
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && parsed.Scheme == scheme && parsed.Host == r.Host && parsed.Path == "" && parsed.RawQuery == ""
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest)
	case errors.Is(err, app.ErrShuttingDown), errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusServiceUnavailable)
	default:
		writeError(w, http.StatusInternalServerError)
	}
}

func writeError(w http.ResponseWriter, status int) {
	message := http.StatusText(status)
	if message == "" {
		message = "Error"
	}
	http.Error(w, message, status)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// Serve starts the local web interface and gracefully stops when ctx is done.
func Serve(ctx context.Context, addr string, services app.Services) error {
	if services == nil {
		return errors.New("web: application services are required")
	}
	server := &http.Server{
		Addr: addr, Handler: NewHandler(services), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
