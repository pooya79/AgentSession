package web

import (
	"net/http"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// timeline renders one bounded, payload-aware timeline page.
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
	if limit > app.DefaultPageSize {
		limit = app.DefaultPageSize
	}
	sessionID := model.SessionID(r.PathValue("session"))
	page, err := h.services.Timeline(r.Context(), app.TimelineRequest{
		SessionID: sessionID, Cursor: cursor, Limit: limit, FocusedEvent: model.EventID(event), IncludePayloads: true,
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
	diagnostics := append([]model.Diagnostic(nil), page.Diagnostics.Diagnostics...)
	refs := h.resolveDiagnostics(r.Context(), sessionID, diagnostics)
	render(w, r, http.StatusOK, timelinePage(timelineView{
		CSRF: h.csrf, Notice: notice, SessionID: sessionID, Page: page,
		Projection: projections, ProjectionErr: projectionErr, DiagnosticRefs: refs,
	}))
}

// timelineFragment appends the next cursor-bound page of event summaries.
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
	if limit > app.DefaultPageSize {
		limit = app.DefaultPageSize
	}
	sessionID := model.SessionID(r.PathValue("session"))
	page, err := h.services.Timeline(r.Context(), app.TimelineRequest{
		SessionID: sessionID, Cursor: cursor, Limit: limit, IncludePayloads: true,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if page.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	render(w, r, http.StatusOK, eventRows(sessionID, page, nil, h.csrf))
}

// eventRedirect resolves an event through shared services before choosing its canonical URL.
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

// unknownEvidenceFragment performs the explicit, CSRF-protected retained
// evidence action. Raw content never appears in URLs or generic error bodies.
func (h *handler) unknownEvidenceFragment(w http.ResponseWriter, r *http.Request) {
	if !h.available(w) {
		return
	}
	if _, ok := h.validMutation(w, r); !ok {
		return
	}
	sessionID := model.SessionID(r.PathValue("session"))
	eventID := model.EventID(r.PathValue("event"))
	inspection, err := h.services.InspectUnknownEvidence(r.Context(), sessionID, eventID)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if inspection.State == app.EvidenceNotFound {
		writeError(w, http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	render(w, r, http.StatusOK, unknownEvidence(inspection))
}
