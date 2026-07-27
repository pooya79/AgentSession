package web

import (
	"net/url"
	"strconv"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// dashboardView keeps independent service failures visible without hiding usable sessions.
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

// indexingView carries the latest bounded import status and resolved evidence links.
type indexingView struct {
	CSRF           string
	Notice         string
	Status         app.ImportAllStatus
	DiagnosticRefs map[model.EventID]eventReference
}

// timelineView combines canonical timeline evidence with separately fallible projections.
type timelineView struct {
	CSRF           string
	Notice         string
	SessionID      model.SessionID
	Page           app.TimelinePage
	Projection     app.ProjectionStatus
	ProjectionErr  error
	Focused        app.EventDetail
	FocusedPayload string
	DiagnosticRefs map[model.EventID]eventReference
}

// eventReference records whether an evidence link was found in the expected session.
type eventReference struct {
	Found          bool
	MatchesSession bool
	SessionID      model.SessionID
}

// sessionURL path-escapes an opaque session identifier.
func sessionURL(sessionID model.SessionID) string {
	return "/sessions/" + url.PathEscape(string(sessionID))
}

// sessionPageURL builds a dashboard URL from an opaque cursor.
func sessionPageURL(cursor string) string {
	if cursor == "" {
		return "/"
	}
	return "/?" + url.Values{"cursor": {cursor}}.Encode()
}

// timelinePageURL builds the no-JavaScript continuation URL.
func timelinePageURL(sessionID model.SessionID, cursor string) string {
	return sessionURL(sessionID) + "?" + url.Values{"cursor": {cursor}}.Encode()
}

// timelineFragmentURL builds the HTMX continuation endpoint.
func timelineFragmentURL(sessionID model.SessionID, cursor string) string {
	return sessionURL(sessionID) + "/fragments/events?" + url.Values{"cursor": {cursor}}.Encode()
}

// focusedTimelineURL builds the canonical full-page URL for a selected event.
func focusedTimelineURL(sessionID model.SessionID, eventID model.EventID) string {
	return sessionURL(sessionID) + "?" + url.Values{"event": {string(eventID)}}.Encode()
}

// eventFragmentURL builds the payload-on-demand fragment endpoint.
func eventFragmentURL(sessionID model.SessionID, eventID model.EventID) string {
	return sessionURL(sessionID) + "/fragments/event/" + url.PathEscape(string(eventID))
}

// unknownEvidenceFragmentURL builds the explicit retained-evidence inspection
// endpoint for a session-owned Unknown event.
func unknownEvidenceFragmentURL(sessionID model.SessionID, eventID model.EventID) string {
	return sessionURL(sessionID) + "/fragments/event/" + url.PathEscape(string(eventID)) + "/unknown-evidence"
}

// projectionsFragmentURL builds the projection polling endpoint.
func projectionsFragmentURL(sessionID model.SessionID) string {
	return sessionURL(sessionID) + "/fragments/projections"
}

// projectionActionURL builds a session-scoped projection action endpoint.
func projectionActionURL(sessionID model.SessionID, action string) string {
	return sessionURL(sessionID) + "/projections/" + action
}

// timelineNoticeURL builds an allowlisted post-mutation redirect URL.
func timelineNoticeURL(sessionID model.SessionID, notice string) string {
	return sessionURL(sessionID) + "?" + url.Values{"notice": {notice}}.Encode()
}

// eventAnchorID gives each canonical event a stable in-document target.
func eventAnchorID(eventID model.EventID) string { return "event-" + string(eventID) }

// sessionTitle prefers retained title metadata and falls back to the stable ID.
func sessionTitle(session app.SessionSummary) string {
	if session.Title != "" {
		return session.Title
	}
	return string(session.ID)
}

// formatTime renders unavailable timestamps honestly and known values in local time.
func formatTime(value *time.Time) string {
	if value == nil {
		return "Unavailable"
	}
	return value.Local().Format("2006-01-02 15:04 MST")
}

// formatCount formats exact counts with simple English pluralization.
func formatCount(count int64, noun string) string {
	suffix := "s"
	if count == 1 {
		suffix = ""
	}
	return strconv.FormatInt(count, 10) + " " + noun + suffix
}

// strconvFormatInt exposes deterministic base-ten formatting to templates.
func strconvFormatInt(value int64) string { return strconv.FormatInt(value, 10) }
