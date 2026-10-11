package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

func wrappedField(label, value string, width int) []string {
	prefix := "  " + safeInline(label) + ": "
	safe := terminalText(value)
	if safe == "" {
		safe = "—"
	}
	var parts []string
	padding := strings.Repeat(" ", ansi.StringWidth(prefix))
	for _, line := range strings.Split(safe, "\n") {
		for _, wrapped := range wrapIndented(line, max(1, width-ansi.StringWidth(prefix)), true) {
			if len(parts) == 0 {
				parts = append(parts, prefix+wrapped)
			} else {
				parts = append(parts, padding+wrapped)
			}
		}
	}

	return parts
}

func safeInline(value string) string {
	safe := sanitization.Terminal(strings.ToValidUTF8(value, "\uFFFD"))
	return strings.Join(strings.Fields(safe), " ")
}

func optionalBool(value *bool) string {
	if value == nil {
		return "unreported"
	}
	return strconv.FormatBool(*value)
}

func optionalInt(value *int) string {
	if value == nil {
		return "unreported"
	}
	return strconv.Itoa(*value)
}

func optionalInt64(value *int64) string {
	if value == nil {
		return "unreported"
	}
	return strconv.FormatInt(*value, 10)
}

func reportedString(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unreported"
	}
	return value
}

func lineRange(start, end *int64) string {
	switch {
	case start == nil && end == nil:
		return "unreported"
	case start != nil && end != nil:
		return fmt.Sprintf("%d–%d", *start, *end)
	case start != nil:
		return fmt.Sprintf("from %d", *start)
	default:
		return fmt.Sprintf("through %d", *end)
	}
}
