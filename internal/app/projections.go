package app

import (
	"context"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

// ProjectionController is the consumer-owned boundary used by shared
// application workflows. Presentation layers do not manipulate storage state.
type ProjectionController interface {
	Status(context.Context, model.SessionID) ([]projection.State, error)
	Retry(context.Context, model.SessionID) error
	Rebuild(context.Context, model.SessionID, *projection.Kind) error
}

type ProjectionService struct {
	controller ProjectionController
}

func NewProjectionService(controller ProjectionController) *ProjectionService {
	return &ProjectionService{controller: controller}
}

func (s *ProjectionService) Status(ctx context.Context, sessionID model.SessionID) ([]projection.State, error) {
	return s.controller.Status(ctx, sessionID)
}

func (s *ProjectionService) Retry(ctx context.Context, sessionID model.SessionID) error {
	return s.controller.Retry(ctx, sessionID)
}

func (s *ProjectionService) Rebuild(ctx context.Context, sessionID model.SessionID, kind *projection.Kind) error {
	return s.controller.Rebuild(ctx, sessionID, kind)
}
