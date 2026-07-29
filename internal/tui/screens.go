package tui

import (
	"context"

	"charm.land/bubbles/v2/viewport"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// sessionsState owns the bounded session page, stable selection, and separately
// loaded library overview. The root Model coordinates navigation and
// asynchronous lifetimes rather than making each screen an independent model.
type sessionsState struct {
	page            app.SessionPage
	loading         bool
	err             error
	cursor          int
	cursors         []string
	pageNumber      int
	selected        model.SessionID
	overview        app.LibraryOverview
	overviewLoading bool
	overviewErr     error
}

// timelineState owns one bounded event page and its page-local selection.
type timelineState struct {
	page       app.TimelinePage
	loading    bool
	err        error
	cursor     int
	cursors    []string
	pageNumber int
}

// detailState retains the last usable detail while a refresh is in flight or
// fails, allowing the UI to represent partial availability honestly.
type detailState struct {
	detail            app.EventDetail
	loading           bool
	err               error
	inspection        app.UnknownEvidenceInspection
	inspectionLoading bool
	inspectionErr     error
	viewport          viewport.Model
}

// indexingState contains the latest observation of application-owned import
// work and the viewport used to inspect its evidence.
type indexingState struct {
	status   app.ImportAllStatus
	err      error
	viewport viewport.Model
}

// projectionsState owns panel observation state. generation rejects delayed
// replies after refresh or navigation; cancel stops observation, not work.
type projectionsState struct {
	generation   uint64
	cancel       context.CancelFunc
	status       app.ProjectionStatus
	err          error
	loading      bool
	cursor       int
	confirmAll   bool
	actionNotice string
}

type searchState struct {
	query   string
	editing bool
	page    app.SearchPage
	loading bool
	err     error
	cursor  int
}
