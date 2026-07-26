package web

import (
	"net/url"
	"strconv"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

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

type indexingView struct {
	CSRF           string
	Notice         string
	Status         app.ImportAllStatus
	DiagnosticRefs map[model.EventID]eventReference
}

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

type eventReference struct {
	Found          bool
	MatchesSession bool
	SessionID      model.SessionID
}

func sessionURL(sessionID model.SessionID) string {
	return "/sessions/" + url.PathEscape(string(sessionID))
}

func sessionPageURL(cursor string) string {
	if cursor == "" {
		return "/"
	}
	return "/?" + url.Values{"cursor": {cursor}}.Encode()
}

func timelinePageURL(sessionID model.SessionID, cursor string) string {
	return sessionURL(sessionID) + "?" + url.Values{"cursor": {cursor}}.Encode()
}

func timelineFragmentURL(sessionID model.SessionID, cursor string) string {
	return sessionURL(sessionID) + "/fragments/events?" + url.Values{"cursor": {cursor}}.Encode()
}

func focusedTimelineURL(sessionID model.SessionID, eventID model.EventID) string {
	return sessionURL(sessionID) + "?" + url.Values{"event": {string(eventID)}}.Encode()
}

func eventFragmentURL(sessionID model.SessionID, eventID model.EventID) string {
	return sessionURL(sessionID) + "/fragments/event/" + url.PathEscape(string(eventID))
}

func projectionsFragmentURL(sessionID model.SessionID) string {
	return sessionURL(sessionID) + "/fragments/projections"
}

func projectionActionURL(sessionID model.SessionID, action string) string {
	return sessionURL(sessionID) + "/projections/" + action
}

func timelineNoticeURL(sessionID model.SessionID, notice string) string {
	return sessionURL(sessionID) + "?" + url.Values{"notice": {notice}}.Encode()
}

func eventAnchorID(eventID model.EventID) string { return "event-" + string(eventID) }

func sessionTitle(session app.SessionSummary) string {
	if session.Title != "" {
		return session.Title
	}
	return string(session.ID)
}

func formatTime(value *time.Time) string {
	if value == nil {
		return "Unavailable"
	}
	return value.Local().Format("2006-01-02 15:04 MST")
}

func formatCount(count int64, noun string) string {
	suffix := "s"
	if count == 1 {
		suffix = ""
	}
	return strconv.FormatInt(count, 10) + " " + noun + suffix
}

func strconvFormatInt(value int64) string { return strconv.FormatInt(value, 10) }
