package web

import (
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// dashboardView keeps independent service failures visible without hiding
// sessions that remain usable.
type dashboardView struct {
	CSRF        string
	Notice      string
	Import      app.ImportAllStatus
	ImportErr   error
	Overview    app.LibraryOverview
	OverviewErr error
	Sessions    app.SessionPage
	SessionsErr error
}

// indexingView carries the latest bounded import status and resolved evidence
// links.
type indexingView struct {
	CSRF           string
	Notice         string
	Status         app.ImportAllStatus
	DiagnosticRefs map[model.EventID]eventReference
}

type searchView struct {
	Query string
	Page  app.SearchPage
	Err   error
}

// timelineView combines canonical timeline evidence with separately fallible
// projections.
type timelineView struct {
	CSRF           string
	Notice         string
	SessionID      model.SessionID
	Page           app.TimelinePage
	Projection     app.ProjectionStatus
	ProjectionErr  error
	DiagnosticRefs map[model.EventID]eventReference
}

// eventReference records whether an evidence link was found in the expected
// session. Missing and cross-session references must remain plain text.
type eventReference struct {
	Found          bool
	MatchesSession bool
	SessionID      model.SessionID
}
