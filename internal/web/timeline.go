package web

import (
	"net/http"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// timeline renders bounded summaries and loads a payload only for an explicitly focused event.
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
	render(w, r, http.StatusOK, eventRows(sessionID, page, app.EventDetail{}, "", nil, h.csrf))
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

// eventFragment returns the selected normalized payload and its retained diagnostics.
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
	render(w, r, http.StatusOK, eventDetail(detail, payload, h.resolveDiagnostics(r.Context(), sessionID, detail.Diagnostics.Diagnostics), h.csrf))
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
