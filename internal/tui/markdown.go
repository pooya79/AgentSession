package tui

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/pooya79/AgentSession/internal/sanitization"
)

// markdownLines deliberately supports only local text structure. Links and
// unsupported Markdown remain visible text; no HTML, images or URLs are loaded.
func markdownLines(text string, width int) []string {
	text = terminalText(text)
	var lines []string
	fence := ""
	fenceLength := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) <= 3 && (strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~")) {
			char := trimmed[:1]
			n := len(trimmed) - len(strings.TrimLeft(trimmed, char))
			if fence == "" {
				fence, fenceLength = char, n
				label := "Code"
				if language := strings.TrimSpace(trimmed[n:]); language != "" {
					label += " · " + language
				}
				lines = append(lines, wrapIndented(label, width, false)...)
				continue
			}
			if char == fence && n >= fenceLength && strings.TrimSpace(trimmed[n:]) == "" {
				fence = ""
				lines = append(lines, "")
				continue
			}
		}
		code := fence != "" || strings.HasPrefix(line, "    ")
		if !code {
			heading := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if heading >= 1 && heading <= 6 && len(trimmed) > heading && trimmed[heading] == ' ' {
				line = "§ " + strings.TrimSpace(trimmed[heading:])
			}
			if marker := listMarker(trimmed); marker != "" {
				indent := strings.Repeat(" ", len(line)-len(trimmed))
				parts := strings.Split(ansi.Wrap(trimmed[len(marker):], max(1, width-len(indent)-len(marker)), ""), "\n")
				for i, part := range parts {
					prefix := marker
					if i > 0 {
						prefix = strings.Repeat(" ", len(marker))
					}
					lines = append(lines, indent+prefix+part)
				}
				continue
			}
		}
		lines = append(lines, wrapIndented(line, width, code)...)
	}
	return lines
}

func listMarker(line string) string {
	for _, marker := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, marker) {
			return marker
		}
	}
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i <= 9 && len(line) > i+1 && (line[i] == '.' || line[i] == ')') && line[i+1] == ' ' {
		return line[:i+2]
	}
	return ""
}

// wrapIndented preserves code whitespace and indents continuation rows. Long
// indentation itself remains accessible rather than being silently discarded.
func wrapIndented(line string, width int, code bool) []string {
	width = max(1, width)
	indent := len(line) - len(strings.TrimLeft(line, " "))
	if indent >= width {
		return strings.Split(ansi.Hardwrap(line, width, true), "\n")
	}
	prefix := line[:indent]
	body := line[indent:]
	var wrapped string
	if code {
		wrapped = ansi.Hardwrap(body, width-indent, true)
	} else {
		wrapped = ansi.Wrap(body, width-indent, "")
	}
	parts := strings.Split(wrapped, "\n")
	for i := range parts {
		parts[i] = prefix + parts[i]
	}
	return parts
}

// terminalText sanitizes before layout and expands tabs to four-cell stops so
// terminal tab settings cannot change indentation or hide off-screen content.
func terminalText(text string) string {
	safe := sanitization.Terminal(strings.ToValidUTF8(text, "\uFFFD"))
	if !strings.ContainsRune(safe, '\t') {
		return safe
	}
	var out strings.Builder
	column := 0
	for _, r := range safe {
		switch r {
		case '\t':
			n := 4 - column%4
			out.WriteString(strings.Repeat(" ", n))
			column += n
		case '\n':
			out.WriteRune(r)
			column = 0
		default:
			out.WriteRune(r)
			if !unicode.IsControl(r) {
				column += ansi.StringWidth(string(r))
			}
		}
	}
	return out.String()
}
