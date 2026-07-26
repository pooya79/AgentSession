package web

import (
	"context"
	"net/http"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// projectionFragment returns current secondary derived-data state for polling.
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

// retryProjections schedules retryable projection work through the application service.
func (h *handler) retryProjections(w http.ResponseWriter, r *http.Request) {
	h.projectionMutation(w, r, "", func(ctx context.Context, session model.SessionID, _ string) (app.ProjectionAction, error) {
		return h.services.RetryProjections(ctx, session)
	})
}

// rebuildProjection validates a single kind and rejects the all-kinds shortcut.
func (h *handler) rebuildProjection(w http.ResponseWriter, r *http.Request) {
	h.projectionMutation(w, r, "kind", func(ctx context.Context, session model.SessionID, kind string) (app.ProjectionAction, error) {
		if kind == app.ProjectionKindAll {
			return app.ProjectionAction{}, app.ErrInvalidRequest
		}
		return h.services.RebuildProjections(ctx, session, kind)
	})
}

// projectionMutation centralizes validation and full-page versus HTMX responses.
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

// confirmRebuildAll renders an explicit confirmation only when every builder is available.
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

// rebuildAll rechecks builder availability before scheduling every projection.
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
