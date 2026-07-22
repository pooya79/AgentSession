package web

import (
	"context"
	"testing"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/app/apptest"
)

type contractConsumer struct{ services app.Services }

func (c contractConsumer) ListSessions(ctx context.Context, request app.ListSessionsRequest) (app.SessionPage, error) {
	return c.services.ListSessions(ctx, request)
}
func (c contractConsumer) Timeline(ctx context.Context, request app.TimelineRequest) (app.TimelinePage, error) {
	return c.services.Timeline(ctx, request)
}
func (c contractConsumer) EventDetail(ctx context.Context, request app.EventDetailRequest) (app.EventDetail, error) {
	return c.services.EventDetail(ctx, request)
}

func TestSharedServiceContract(t *testing.T) {
	fixture := apptest.NewFixture(t)
	apptest.RunConsumerContract(t, contractConsumer{services: fixture.Services})
}
