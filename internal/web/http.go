package web

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"

	"github.com/a-h/templ"

	"github.com/pooya79/AgentSession/internal/app"
)

// available fails closed when the shared application service graph is absent.
func (h *handler) available(w http.ResponseWriter) bool {
	if h.services == nil {
		writeError(w, http.StatusServiceUnavailable)
		return false
	}
	return true
}

// validMutation enforces same-origin, media type, body size, exact fields, and CSRF.
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
	if len(r.PostForm) != len(allowed) ||
		subtle.ConstantTimeCompare([]byte(r.PostForm.Get("csrf")), []byte(h.csrf)) != 1 {
		writeError(w, http.StatusBadRequest)
		return nil, false
	}
	return r.PostForm, true
}

// render buffers a component so template failures cannot produce partial successful responses.
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

// strictQuery rejects malformed or unrecognized query keys at the HTTP boundary.
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

// requireNoQuery enforces query-free fragment and health endpoints.
func requireNoQuery(w http.ResponseWriter, r *http.Request) bool {
	_, ok := strictQuery(w, r)
	return ok
}

// requiredSingle accepts exactly one non-empty value for a required key.
func requiredSingle(w http.ResponseWriter, values url.Values, key string) (string, bool) {
	items, exists := values[key]
	if !exists || len(items) != 1 || items[0] == "" {
		writeError(w, http.StatusBadRequest)
		return "", false
	}
	return items[0], true
}

// optionalSingle accepts an absent key or exactly one non-empty value.
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

// optionalSingleAllowEmpty accepts an absent key or exactly one value,
// including an empty value. Search uses this because an empty query is valid.
func optionalSingleAllowEmpty(w http.ResponseWriter, values url.Values, key string) (string, bool) {
	items, exists := values[key]
	if !exists {
		return "", true
	}
	if len(items) != 1 {
		writeError(w, http.StatusBadRequest)
		return "", false
	}
	return items[0], true
}

// parseLimit applies the web default while preserving the application maximum.
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

// notices allowlists redirect status messages instead of reflecting query text.
var notices = map[string]string{
	"rescan-started":      "Rescan started. Indexing continues if you leave this page.",
	"projection-started":  "Projection work started.",
	"rebuild-all-started": "All available projections were scheduled for rebuild.",
}

// parseNotice resolves only allowlisted user-facing redirect notices.
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

// sameOrigin rejects cross-origin mutations while allowing non-browser clients without Origin.
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

// serviceRequestError handles validation failures while allowing partial view errors to render.
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

// writeServiceError maps application failure classes without exposing sensitive causes.
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

// writeError emits only standard status text to avoid reflecting untrusted details.
func writeError(w http.ResponseWriter, status int) {
	message := http.StatusText(status)
	if message == "" {
		message = "Error"
	}
	http.Error(w, message, status)
}

// redirectSeeOther uses POST-redirect-GET semantics for non-HTMX mutations.
func redirectSeeOther(w http.ResponseWriter, r *http.Request, target string) {
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// isHTMX identifies fragment requests using htmx's conventional header.
func isHTMX(r *http.Request) bool { return r.Header.Get("HX-Request") == "true" }

// allBuildersAvailable permits rebuild-all only when every listed projection can run.
func allBuildersAvailable(status app.ProjectionStatus) bool {
	return len(status.Projections) > 0 && status.Summary.Unimplemented == 0
}

// securityHeaders applies a restrictive same-origin browser policy to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}
