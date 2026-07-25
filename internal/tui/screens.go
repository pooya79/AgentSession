package tui

import (
	"context"

	"charm.land/bubbles/v2/viewport"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// Each screen owns its page, selection, loading, error, and viewport state.
// The root Model coordinates navigation and asynchronous lifetimes between
// these units rather than making each screen an independent tea.Model.
type sessionsState struct {
	page       app.SessionPage
	loading    bool
	err        error
	cursor     int
	cursors    []string
	pageNumber int
	selected   model.SessionID
}

type timelineState struct {
	page       app.TimelinePage
	loading    bool
	err        error
	cursor     int
	cursors    []string
	pageNumber int
}

type detailState struct {
	detail   app.EventDetail
	loading  bool
	err      error
	viewport viewport.Model
}

type indexingState struct {
	status   app.ImportAllStatus
	err      error
	viewport viewport.Model
}

type projectionsState struct {
	generation   uint64
	ctx          context.Context
	cancel       context.CancelFunc
	status       app.ProjectionStatus
	err          error
	loading      bool
	cursor       int
	confirmAll   bool
	actionNotice string
}
