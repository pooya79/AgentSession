package web

import (
	"net/http"

	"github.com/pooya79/AgentSession/internal/app"
)

// indexing renders the latest import-all status without starting discovery.
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

// rescan starts application-owned discovery and import after mutation validation.
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

// indexStatusFragment returns the detailed polling fragment and refreshes terminal pages.
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

// indexStripFragment returns the compact dashboard polling fragment.
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
