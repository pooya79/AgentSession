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
	showDetails     bool
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

// timelineState keeps lightweight visited summaries separately from a bounded
// contiguous content window. Disclosure and content positions use stable IDs.
type timelineState struct {
	page                       app.TimelinePage
	residentStart, residentEnd int
	payloadMetadata            map[model.EventID]timelinePayloadMetadata
	cards                      map[model.EventID]timelineCachedCard
	pendingTarget              model.EventID
	earlierExhausted           bool
	edgeStep                   int
	selectionRequested         bool
	scrollDirection            int
	loading                    bool
	err                        error
	cursor                     int
	selected                   model.EventID
	pendingCursor              string
	requestedCursors           map[string]bool
	expanded                   map[model.EventID]bool
	disclosureAnchors          map[model.EventID]timelineReadingAnchor
	inspections                map[model.EventID]app.UnknownEvidenceInspection
	inspectionErrors           map[model.EventID]error
	inspectionLoading          map[model.EventID]bool
	viewport                   viewport.Model
	renderRevision             uint64
	cachedRevision             uint64
	cachedWidth                int
	cachedLines                []string
	cachedRanges               map[model.EventID]timelineCardRange
	anchor                     timelineReadingAnchor
	notice                     string
}

// detailState retains the last usable detail while a refresh is in flight or
// fails, allowing the UI to represent partial availability honestly.
type inspectionPosition struct{ line, column int }

type detailState struct {
	unwrapped         bool
	showMetadata      bool
	renderedUnwrapped bool
	renderedPositions []inspectionPosition
	anchor            inspectionPosition
	anchorSet         bool
	lastTop, lastX    int

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
	showDetails bool
	status      app.ImportAllStatus
	err         error
	viewport    viewport.Model
}

// projectionsState owns panel observation state. generation rejects delayed
// replies after refresh or navigation; cancel stops observation, not work.
type projectionsState struct {
	showDetails  bool
	generation   uint64
	cancel       context.CancelFunc
	status       app.ProjectionStatus
	err          error
	loading      bool
	cursor       int
	confirmAll   bool
	actionNotice string
	viewport     viewport.Model
	revealTarget bool
}

// searchState keeps query editing separate from bounded result navigation.
type searchState struct {
	showDetails bool
	query       string
	queryCursor int
	dirty       bool
	notice      string
	refreshing  bool
	editing     bool
	page        app.SearchPage
	loading     bool
	err         error
	cursor      int
}
