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

// timelineState accumulates bounded chunks into one continuously scrollable
// timeline. Expansion and retained-evidence inspection are keyed by stable
// event identity so appends and resizes do not disturb them.
type timelineState struct {
	page              app.TimelinePage
	loading           bool
	err               error
	cursor            int
	selected          model.EventID
	pendingCursor     string
	requestedCursors  map[string]bool
	expanded          map[model.EventID]bool
	inspections       map[model.EventID]app.UnknownEvidenceInspection
	inspectionErrors  map[model.EventID]error
	inspectionLoading map[model.EventID]bool
	viewport          viewport.Model
	renderRevision    uint64
	cachedRevision    uint64
	cachedWidth       int
	cachedLines       []string
	cachedRanges      map[model.EventID]timelineCardRange
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

// searchState keeps query editing separate from bounded result navigation.
type searchState struct {
	query   string
	editing bool
	page    app.SearchPage
	loading bool
	err     error
	cursor  int
}
