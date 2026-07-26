package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
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
)

//go:embed assets/*
var embeddedAssets embed.FS

type handler struct {
	services app.Services
	csrf     string
}

// NewHandler creates the local, server-rendered operations console.
func NewHandler(services app.Services) http.Handler {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		panic("web: generate CSRF token: " + err.Error())
	}
	h := &handler{services: services, csrf: base64.RawURLEncoding.EncodeToString(token)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.dashboard)
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /indexing", h.indexing)
	mux.HandleFunc("POST /indexing/rescan", h.rescan)
	mux.HandleFunc("GET /fragments/index-status", h.indexStatusFragment)
	mux.HandleFunc("GET /fragments/index-strip", h.indexStripFragment)
	mux.HandleFunc("GET /events/{event}", h.eventRedirect)
	mux.HandleFunc("GET /sessions/{session}", h.timeline)
	mux.HandleFunc("GET /sessions/{session}/fragments/events", h.timelineFragment)
	mux.HandleFunc("GET /sessions/{session}/fragments/event/{event}", h.eventFragment)
	mux.HandleFunc("GET /sessions/{session}/fragments/projections", h.projectionFragment)
	mux.HandleFunc("POST /sessions/{session}/projections/retry", h.retryProjections)
	mux.HandleFunc("POST /sessions/{session}/projections/rebuild", h.rebuildProjection)
	mux.HandleFunc("GET /sessions/{session}/projections/rebuild-all", h.confirmRebuildAll)
	mux.HandleFunc("POST /sessions/{session}/projections/rebuild-all", h.rebuildAll)

	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("web: embedded assets are unavailable: " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets)))
	return securityHeaders(mux)
}

func (h *handler) available(w http.ResponseWriter) bool {
	if h.services == nil {
		writeError(w, http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (h *handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" || !h.available(w) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
		}
		return
	}
	values, ok := strictQuery(w, r, "cursor", "limit", "notice")
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
	notice, ok := parseNotice(w, values)
	if !ok {
		return
	}
	vm := dashboardView{CSRF: h.csrf, Notice: notice}
	vm.Import, vm.ImportErr = h.services.ImportAllStatus(r.Context())
	vm.Overview, vm.OverviewErr = h.services.LibraryOverview(r.Context())
	vm.Sessions, vm.SessionsErr = h.services.ListSessions(r.Context(), app.ListSessionsRequest{Cursor: cursor, Limit: limit})
	if serviceRequestError(w, vm.SessionsErr) {
		return
	}
	render(w, r, http.StatusOK, dashboardPage(vm))
}

func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	if !requireNoQuery(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (h *handler) indexing(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	values, ok := strictQuery(w, r, "notice")
	if !ok {
		return
	}
	notice, ok := parseNotice(w, values)
	if !ok {
		return
	}
	status, err := h.services.ImportAllStatus(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	render(w, r, http.StatusOK, indexingPage(indexingView{
		CSRF: h.csrf, Notice: notice, Status: status, DiagnosticRefs: h.resolveImportDiagnostics(r.Context(), status.RecentDiagnostics),
	}))
}

func (h *handler) rescan(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	if _, ok := h.validMutation(w, r); !ok {
		return
	}
	started, err := h.services.StartImportAll(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if isHTMX(r) {
		w.Header().Set("HX-Push-Url", "/indexing?notice=rescan-started")
		render(w, r, http.StatusOK, indexStatus(started.Status, h.csrf, h.resolveImportDiagnostics(r.Context(), started.Status.RecentDiagnostics)))
		return
	}
	redirectSeeOther(w, r, "/indexing?notice=rescan-started")
}

func (h *handler) indexStatusFragment(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) || !requireNoQuery(w, r) {
		return
	}
	status, err := h.services.ImportAllStatus(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if !status.Active {
		w.Header().Set("HX-Refresh", "true")
	}
	render(w, r, http.StatusOK, indexStatus(status, h.csrf, h.resolveImportDiagnostics(r.Context(), status.RecentDiagnostics)))
}

func (h *handler) indexStripFragment(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) || !requireNoQuery(w, r) {
		return
	}
	status, err := h.services.ImportAllStatus(r.Context())
	if err != nil {
		render(w, r, http.StatusOK, indexStrip(app.ImportAllStatus{}, err))
		return
	}
	if !status.Active {
		w.Header().Set("HX-Refresh", "true")
	}
	render(w, r, http.StatusOK, indexStrip(status, nil))
}

func (h *handler) timeline(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	values, ok := strictQuery(w, r, "cursor", "limit", "event", "notice")
	if !ok {
		return
	}
	cursor, ok := optionalSingle(w, values, "cursor")
	if !ok {
		return
	}
	event, ok := optionalSingle(w, values, "event")
	if !ok {
		return
	}
	notice, ok := parseNotice(w, values)
	if !ok {
		return
	}
	limit, ok := parseLimit(w, values)
	if !ok {
		return
	}
	sessionID := model.SessionID(r.PathValue("session"))
	page, err := h.services.Timeline(r.Context(), app.TimelineRequest{
		SessionID: sessionID, Cursor: cursor, Limit: limit, FocusedEvent: model.EventID(event),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if page.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	projections, projectionErr := h.services.ProjectionStatus(r.Context(), sessionID)
	var focused app.EventDetail
	var payload string
	if event != "" {
		focused, err = h.services.EventDetail(r.Context(), app.EventDetailRequest{
			SessionID: sessionID, EventID: model.EventID(event), IncludePayload: true,
		})
		if err != nil {
			writeServiceError(w, err)
			return
		}
		if focused.State == app.EvidenceNotFound {
			writeError(w, http.StatusNotFound)
			return
		}
		payload, err = normalizedPayload(focused.Payload)
		if err != nil {
			writeError(w, http.StatusInternalServerError)
			return
		}
	}
	diagnostics := append([]model.Diagnostic(nil), page.Diagnostics.Diagnostics...)
	diagnostics = append(diagnostics, focused.Diagnostics.Diagnostics...)
	refs := h.resolveDiagnostics(r.Context(), sessionID, diagnostics)
	render(w, r, http.StatusOK, timelinePage(timelineView{
		CSRF: h.csrf, Notice: notice, SessionID: sessionID, Page: page,
		Projection: projections, ProjectionErr: projectionErr, Focused: focused,
		FocusedPayload: payload, DiagnosticRefs: refs,
	}))
}

func (h *handler) timelineFragment(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	values, ok := strictQuery(w, r, "cursor", "limit")
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
	sessionID := model.SessionID(r.PathValue("session"))
	page, err := h.services.Timeline(r.Context(), app.TimelineRequest{SessionID: sessionID, Cursor: cursor, Limit: limit})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if page.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	render(w, r, http.StatusOK, eventRows(sessionID, page, app.EventDetail{}, "", nil))
}

func (h *handler) eventRedirect(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) || !requireNoQuery(w, r) {
		return
	}
	eventID := model.EventID(r.PathValue("event"))
	locations, err := h.services.EventLocations(r.Context(), []model.EventID{eventID})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	location, found := locations[eventID]
	if !found {
		writeError(w, http.StatusNotFound)
		return
	}
	redirectSeeOther(w, r, focusedTimelineURL(location.SessionID, eventID))
}

func (h *handler) eventFragment(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) || !requireNoQuery(w, r) {
		return
	}
	sessionID := model.SessionID(r.PathValue("session"))
	eventID := model.EventID(r.PathValue("event"))
	detail, err := h.services.EventDetail(r.Context(), app.EventDetailRequest{
		SessionID: sessionID, EventID: eventID, IncludePayload: true,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if detail.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	payload, err := normalizedPayload(detail.Payload)
	if err != nil {
		writeError(w, http.StatusInternalServerError)
		return
	}
	render(w, r, http.StatusOK, eventDetail(detail, payload, h.resolveDiagnostics(r.Context(), sessionID, detail.Diagnostics.Diagnostics)))
}

func (h *handler) projectionFragment(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) || !requireNoQuery(w, r) {
		return
	}
	sessionID := model.SessionID(r.PathValue("session"))
	status, err := h.services.ProjectionStatus(r.Context(), sessionID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if status.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	render(w, r, http.StatusOK, projectionStatus(status, h.csrf, ""))
}

func (h *handler) retryProjections(w http.ResponseWriter, r *http.Request) {
	h.projectionMutation(w, r, "", func(ctx context.Context, session model.SessionID, _ string) (app.ProjectionAction, error) {
		return h.services.RetryProjections(ctx, session)
	})
}

func (h *handler) rebuildProjection(w http.ResponseWriter, r *http.Request) {
	h.projectionMutation(w, r, "kind", func(ctx context.Context, session model.SessionID, kind string) (app.ProjectionAction, error) {
		if kind == app.ProjectionKindAll {
			return app.ProjectionAction{}, app.ErrInvalidRequest
		}
		return h.services.RebuildProjections(ctx, session, kind)
	})
}

func (h *handler) projectionMutation(w http.ResponseWriter, r *http.Request, field string, action func(context.Context, model.SessionID, string) (app.ProjectionAction, error)) {
	if !h.available(w) {
		return
	}
	fields := []string{}
	if field != "" {
		fields = append(fields, field)
	}
	values, ok := h.validMutation(w, r, fields...)
	if !ok {
		return
	}
	value := ""
	if field != "" {
		value = values.Get(field)
	}
	sessionID := model.SessionID(r.PathValue("session"))
	result, err := action(r.Context(), sessionID, value)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if result.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	if isHTMX(r) {
		render(w, r, http.StatusOK, projectionStatus(result.Status, h.csrf, "Projection work started."))
		return
	}
	redirectSeeOther(w, r, timelineNoticeURL(sessionID, "projection-started"))
}

func (h *handler) confirmRebuildAll(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) || !requireNoQuery(w, r) {
		return
	}
	sessionID := model.SessionID(r.PathValue("session"))
	status, err := h.services.ProjectionStatus(r.Context(), sessionID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if status.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	if !allBuildersAvailable(status) {
		writeError(w, http.StatusConflict)
		return
	}
	render(w, r, http.StatusOK, rebuildAllPage(sessionID, status, h.csrf))
}

func (h *handler) rebuildAll(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	if _, ok := h.validMutation(w, r); !ok {
		return
	}
	sessionID := model.SessionID(r.PathValue("session"))
	status, err := h.services.ProjectionStatus(r.Context(), sessionID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if status.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	if !allBuildersAvailable(status) {
		writeError(w, http.StatusConflict)
		return
	}
	result, err := h.services.RebuildProjections(r.Context(), sessionID, app.ProjectionKindAll)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if isHTMX(r) {
		render(w, r, http.StatusOK, projectionStatus(result.Status, h.csrf, "All projections scheduled for rebuild."))
		return
	}
	redirectSeeOther(w, r, timelineNoticeURL(sessionID, "rebuild-all-started"))
}

func (h *handler) resolveDiagnostics(ctx context.Context, expected model.SessionID, diagnostics []model.Diagnostic) map[model.EventID]eventReference {
	ids := make([]model.EventID, 0)
	seen := make(map[model.EventID]struct{})
	for _, diagnostic := range diagnostics {
		for _, id := range diagnostic.EventIDs {
			if _, exists := seen[id]; !exists && len(ids) < app.MaximumPageSize {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	result := make(map[model.EventID]eventReference, len(ids))
	locations, err := h.services.EventLocations(ctx, ids)
	if err != nil {
		return result
	}
	for _, id := range ids {
		location, found := locations[id]
		result[id] = eventReference{Found: found, MatchesSession: found && (expected == "" || location.SessionID == expected), SessionID: location.SessionID}
	}
	return result
}

func (h *handler) resolveImportDiagnostics(ctx context.Context, diagnostics []app.ImportAllDiagnostic) map[model.EventID]eventReference {
	normalized := make([]model.Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		normalized = append(normalized, model.Diagnostic{EventIDs: diagnostic.EventIDs})
	}
	return h.resolveDiagnostics(ctx, "", normalized)
}

func (h *handler) validMutation(w http.ResponseWriter, r *http.Request, fields ...string) (url.Values, bool) {
	if r.URL.RawQuery != "" || !sameOrigin(r) {
		writeError(w, http.StatusBadRequest)
		return nil, false
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" || len(parameters) != 0 {
		writeError(w, http.StatusBadRequest)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maximumRequestBody)
	if err := r.ParseForm(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge)
		} else {
			writeError(w, http.StatusBadRequest)
		}
		return nil, false
	}
	allowed := make(map[string]struct{}, len(fields)+1)
	allowed["csrf"] = struct{}{}
	for _, field := range fields {
		allowed[field] = struct{}{}
	}
	for key, values := range r.PostForm {
		if _, exists := allowed[key]; !exists || len(values) != 1 || values[0] == "" {
			writeError(w, http.StatusBadRequest)
			return nil, false
		}
	}
	if len(r.PostForm) != len(allowed) || r.PostForm.Get("csrf") != h.csrf {
		writeError(w, http.StatusBadRequest)
		return nil, false
	}
	return r.PostForm, true
}

func normalizedPayload(payload model.NormalizedData) (string, error) {
	if payload == nil {
		return "", nil
	}
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

var notices = map[string]string{
	"rescan-started":      "Rescan started. Indexing continues if you leave this page.",
	"projection-started":  "Projection work started.",
	"rebuild-all-started": "All available projections were scheduled for rebuild.",
}

func parseNotice(w http.ResponseWriter, values url.Values) (string, bool) {
	code, ok := optionalSingle(w, values, "notice")
	if !ok {
		return "", false
	}
	if code == "" {
		return "", true
	}
	message, exists := notices[code]
	if !exists {
		writeError(w, http.StatusBadRequest)
		return "", false
	}
	return message, true
}

func sameOrigin(r *http.Request) bool {
	if r.Host == "" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return err == nil && parsed.Scheme == scheme && parsed.Host == r.Host &&
		parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

func serviceRequestError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, app.ErrInvalidRequest) {
		writeError(w, http.StatusBadRequest)
		return true
	}
	return false
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, app.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest)
	case errors.Is(err, app.ErrSourceNotFound):
		writeError(w, http.StatusNotFound)
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

func redirectSeeOther(w http.ResponseWriter, r *http.Request, target string) {
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

func allBuildersAvailable(status app.ProjectionStatus) bool {
	return len(status.Projections) > 0 && status.Summary.Unimplemented == 0
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
	if _, err := services.StartImportAll(context.Background()); err != nil {
		return fmt.Errorf("web: start automatic import: %w", err)
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
