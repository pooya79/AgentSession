package web

import (
	"net/http"

	"github.com/pooya79/AgentSession/internal/app"
)

// dashboard renders one bounded session page and independent index/library summaries.
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

// health reports process availability without requiring application services.
func (h *handler) health(w http.ResponseWriter, r *http.Request) {
	if !requireNoQuery(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
