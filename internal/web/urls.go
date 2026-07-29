package web

import (
	"net/url"

	"github.com/pooya79/AgentSession/internal/model"
)

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

// searchPageURL keeps a continuation cursor bound to its original query.
func searchPageURL(query, cursor string) string {
	values := url.Values{"q": {query}}
	if cursor != "" {
		values.Set("cursor", cursor)
	}
	return "/search?" + values.Encode()
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
