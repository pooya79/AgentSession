package app

import (
	"context"
	"errors"

	"github.com/pooya79/AgentSession/internal/model"
)

// Search delegates to the shared lifecycle-aware search service.
func (r *Runtime) Search(ctx context.Context, request SearchRequest) (SearchPage, error) {
	return r.searcher.Search(ctx, request)
}

// LibraryOverview reports aggregate counts over committed canonical evidence.
func (r *Runtime) LibraryOverview(ctx context.Context) (LibraryOverview, error) {
	return r.explorer.LibraryOverview(ctx)
}

// ListSessions delegates bounded session exploration to the shared service.
func (r *Runtime) ListSessions(ctx context.Context, request ListSessionsRequest) (SessionPage, error) {
	page, err := r.explorer.ListSessions(ctx, request)
	if err != nil {
		return page, err
	}
	for index := range page.Sessions {
		status, statusErr := r.projections.ProjectionStatus(ctx, page.Sessions[index].ID)
		if statusErr != nil {
			if errors.Is(statusErr, context.Canceled) || errors.Is(statusErr, context.DeadlineExceeded) {
				return page, statusErr
			}
			continue
		}
		page.Sessions[index].Projections = status.Summary
	}
	return page, nil
}

// Timeline delegates bounded canonical timeline reads to the shared service.
func (r *Runtime) Timeline(ctx context.Context, request TimelineRequest) (TimelinePage, error) {
	return r.explorer.Timeline(ctx, request)
}

// EventDetail delegates explicit event payload reads to the shared service.
func (r *Runtime) EventDetail(ctx context.Context, request EventDetailRequest) (EventDetail, error) {
	return r.explorer.EventDetail(ctx, request)
}

// EventLocations resolves bounded diagnostic references without loading payloads.
func (r *Runtime) EventLocations(ctx context.Context, eventIDs []model.EventID) (map[model.EventID]EventLocation, error) {
	return r.explorer.EventLocations(ctx, eventIDs)
}

// InspectUnknownEvidence delegates the explicit retained-evidence action.
func (r *Runtime) InspectUnknownEvidence(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (UnknownEvidenceInspection, error) {
	return r.explorer.InspectUnknownEvidence(ctx, sessionID, eventID)
}

// ProjectionStatus exposes the runtime-owned projection lifecycle service.
func (r *Runtime) ProjectionStatus(ctx context.Context, sessionID model.SessionID) (ProjectionStatus, error) {
	return r.projections.ProjectionStatus(ctx, sessionID)
}

// RetryProjections admits retry work only while the runtime accepts new work.
func (r *Runtime) RetryProjections(ctx context.Context, sessionID model.SessionID) (ProjectionAction, error) {
	if err := r.accepting(); err != nil {
		return ProjectionAction{}, err
	}
	return r.projections.RetryProjections(ctx, sessionID)
}

// RebuildProjections admits rebuild work only while the runtime accepts new
// work.
func (r *Runtime) RebuildProjections(ctx context.Context, sessionID model.SessionID, kind string) (ProjectionAction, error) {
	if err := r.accepting(); err != nil {
		return ProjectionAction{}, err
	}
	return r.projections.RebuildProjections(ctx, sessionID, kind)
}
