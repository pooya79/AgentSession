package tui

import (
	"context"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/app/apptest"
	"github.com/pooya79/AgentSession/internal/model"
)

type contractConsumer struct{ services app.Services }

func (c contractConsumer) LibraryOverview(ctx context.Context) (app.LibraryOverview, error) {
	return c.services.LibraryOverview(ctx)
}
func (c contractConsumer) ListSessions(ctx context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
	return c.services.ListSessions(ctx, request)
}
func (c contractConsumer) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	return c.services.Timeline(ctx, request)
}
func (c contractConsumer) EventDetail(ctx context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
	return c.services.EventDetail(ctx, request)
}
func (c contractConsumer) EventLocations(ctx context.Context, ids []model.EventID) (map[model.EventID]app.EventLocation, error) {
	return c.services.EventLocations(ctx, ids)
}
func (c contractConsumer) InspectUnknownEvidence(ctx context.Context, sessionID model.SessionID, eventID model.EventID) (app.UnknownEvidenceInspection, error) {
	return c.services.InspectUnknownEvidence(ctx, sessionID, eventID)
}
func (c contractConsumer) ProjectionStatus(ctx context.Context, sessionID model.SessionID) (app.ProjectionStatus, error) {
	return c.services.ProjectionStatus(ctx, sessionID)
}
func (c contractConsumer) RetryProjections(ctx context.Context, sessionID model.SessionID) (app.ProjectionAction, error) {
	return c.services.RetryProjections(ctx, sessionID)
}
func (c contractConsumer) RebuildProjections(ctx context.Context, sessionID model.SessionID, kind string) (app.ProjectionAction, error) {
	return c.services.RebuildProjections(ctx, sessionID, kind)
}
func (c contractConsumer) Search(ctx context.Context, request app.SearchRequest) (app.SearchPage, error) {
	return c.services.Search(ctx, request)
}

func TestSharedServiceContract(t *testing.T) {
	fixture := apptest.NewFixture(t)
	apptest.RunConsumerContract(t, contractConsumer{services: fixture.Services})
}
