package web

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// eventAnchorID gives each canonical event a stable in-document target.
func eventAnchorID(eventID model.EventID) string { return "event-" + string(eventID) }

func eventCardClass(event model.EventSummary, focused model.EventID) string {
	class := "event event-" + string(event.Kind)
	if event.ID == focused {
		class += " event-focused"
	}
	return class
}

func messageRoleLabel(role model.MessageRole) string {
	switch role {
	case model.MessageRoleUser:
		return "You"
	case model.MessageRoleAssistant:
		return "Assistant"
	case model.MessageRoleSystem:
		return "System"
	case model.MessageRoleOther:
		return "Participant"
	default:
		return "Message · role unreported"
	}
}

func formattedJSON(value string) (string, bool) {
	var decoded any
	if json.Unmarshal([]byte(value), &decoded) != nil {
		return value, false
	}
	encoded, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return value, false
	}
	return string(encoded), true
}

func toolResultState(value *bool) string {
	if value == nil {
		return "status unreported"
	}
	if *value {
		return "failed"
	}
	return "completed"
}

func commandExitState(value *int) string {
	if value == nil {
		return "exit status unreported"
	}
	if *value == 0 {
		return "exited 0"
	}
	return fmt.Sprintf("exited %d", *value)
}

func lineRangeLabel(start, end *int64) string {
	switch {
	case start != nil && end != nil:
		return fmt.Sprintf("lines %d–%d", *start, *end)
	case start != nil:
		return fmt.Sprintf("from line %d", *start)
	case end != nil:
		return fmt.Sprintf("through line %d", *end)
	default:
		return "range unreported"
	}
}

func tokenCount(value *int64) string {
	if value == nil {
		return "unreported"
	}
	return strconv.FormatInt(*value, 10)
}

type patchLine struct {
	Class string
	Text  string
}

// patchLines classifies display lines without interpreting or modifying the
// retained patch text.
func patchLines(value string) []patchLine {
	lines := strings.Split(value, "\n")
	result := make([]patchLine, 0, len(lines))
	for _, line := range lines {
		class := "patch-neutral"
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			class = "patch-file"
		case strings.HasPrefix(line, "@@"):
			class = "patch-hunk"
		case strings.HasPrefix(line, "+"):
			class = "patch-add"
		case strings.HasPrefix(line, "-"):
			class = "patch-delete"
		}
		result = append(result, patchLine{Class: class, Text: line})
	}
	return result
}

func pathSummary(paths []string) string {
	if len(paths) == 0 {
		return "affected paths unreported"
	}
	if len(paths) == 1 {
		return paths[0]
	}
	return fmt.Sprintf("%s and %d more", paths[0], len(paths)-1)
}

// sessionTitle prefers retained title metadata and falls back to the stable ID.
func sessionTitle(session app.SessionSummary) string {
	if session.Title != "" {
		return session.Title
	}
	return string(session.ID)
}

// formatTime renders unavailable timestamps honestly and known values in local
// time.
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
