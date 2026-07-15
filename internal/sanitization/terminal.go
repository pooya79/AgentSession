// Package sanitization provides output-context safety transformations for
// untrusted session content. It deliberately does not detect or redact secrets.
package sanitization

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
)

// Terminal neutralizes terminal control sequences and makes otherwise
// invisible control characters visible. Newlines and tabs are retained so
// multiline session evidence remains readable.
func Terminal(text string) string {
	text = ansi.Strip(text)

	var sanitized strings.Builder
	sanitized.Grow(len(text))
	for _, r := range text {
		switch {
		case r == '\n' || r == '\t':
			sanitized.WriteRune(r)
		case unicode.IsControl(r) || isBidiControl(r):
			_, _ = fmt.Fprintf(&sanitized, "<U+%04X>", r)
		default:
			sanitized.WriteRune(r)
		}
	}
	return sanitized.String()
}

func isBidiControl(r rune) bool {
	switch r {
	case '\u061c', '\u200e', '\u200f',
		'\u202a', '\u202b', '\u202c', '\u202d', '\u202e',
		'\u2066', '\u2067', '\u2068', '\u2069':
		return true
	default:
		return false
	}
}
