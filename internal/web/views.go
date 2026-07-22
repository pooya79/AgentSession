package web

import (
	"net/url"
	"strconv"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

func timelineURL(sessionID model.SessionID) string {
	return "/timeline?" + url.Values{"session": {string(sessionID)}}.Encode()
}

func sessionsFragmentURL(cursor string) string {
	return "/fragments/sessions?" + url.Values{"cursor": {cursor}}.Encode()
}

func timelineFragmentURL(sessionID model.SessionID, cursor string) string {
	return "/fragments/timeline?" + url.Values{"session": {string(sessionID)}, "cursor": {cursor}}.Encode()
}

func eventFragmentURL(sessionID model.SessionID, eventID model.EventID) string {
	return "/fragments/event?" + url.Values{"session": {string(sessionID)}, "event": {string(eventID)}}.Encode()
}

func sessionTitle(session app.SessionSummary) string {
	if session.Title != "" {
		return session.Title
	}
	return string(session.ID)
}

func formatTime(value *time.Time) string {
	if value == nil {
		return "time unavailable"
	}
	return value.Local().Format("2006-01-02 15:04:05 MST")
}

func formatCount(count int64, noun string) string {
	suffix := "s"
	if count == 1 {
		suffix = ""
	}
	return strconv.FormatInt(count, 10) + " " + noun + suffix
}

func strconvFormatInt(value int64) string { return strconv.FormatInt(value, 10) }
