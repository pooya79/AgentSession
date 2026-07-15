package sanitization

import (
	"fmt"
	"testing"
)

func TestTerminalEscapeSequences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain text", in: "plain 👋 text", want: "plain 👋 text"},
		{name: "newlines and tabs", in: "one\ttwo\nthree", want: "one\ttwo\nthree"},
		{name: "SGR styling", in: "\x1b[31mred\x1b[0m", want: "red"},
		{name: "CSI cursor movement", in: "before\x1b[2J\x1b[Hafter", want: "beforeafter"},
		{name: "8-bit CSI", in: "before\x9b2Jafter", want: "beforeafter"},
		{name: "terminal title with BEL", in: "before\x1b]2;deceptive title\x07after", want: "beforeafter"},
		{name: "terminal title with ST", in: "before\x1b]0;deceptive title\x1b\\after", want: "beforeafter"},
		{name: "8-bit OSC and ST", in: "before\x9d2;deceptive title\x9cafter", want: "beforeafter"},
		{name: "clipboard command", in: "before\x1b]52;c;c2VjcmV0\x07after", want: "beforeafter"},
		{name: "hyperlink", in: "\x1b]8;;https://attacker.invalid\x1b\\safe label\x1b]8;;\x1b\\", want: "safe label"},
		{name: "DCS payload", in: "before\x1bPmalicious payload\x1b\\after", want: "beforeafter"},
		{name: "unterminated sequence", in: "before\x1b]2;hidden", want: "before"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Terminal(tt.in); got != tt.want {
				t.Errorf("Terminal(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestTerminalUnicodeControls(t *testing.T) {
	tests := []struct {
		name string
		r    rune
	}{
		{name: "arabic letter mark", r: '\u061c'},
		{name: "left-to-right mark", r: '\u200e'},
		{name: "right-to-left mark", r: '\u200f'},
		{name: "left-to-right embedding", r: '\u202a'},
		{name: "right-to-left embedding", r: '\u202b'},
		{name: "pop directional formatting", r: '\u202c'},
		{name: "left-to-right override", r: '\u202d'},
		{name: "right-to-left override", r: '\u202e'},
		{name: "left-to-right isolate", r: '\u2066'},
		{name: "right-to-left isolate", r: '\u2067'},
		{name: "first strong isolate", r: '\u2068'},
		{name: "pop directional isolate", r: '\u2069'},
		{name: "NUL", r: '\u0000'},
		{name: "backspace", r: '\u0008'},
		{name: "carriage return", r: '\u000d'},
		{name: "delete", r: '\u007f'},
		{name: "C1 next line", r: '\u0085'},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := "before" + string(tt.r) + "after"
			want := fmt.Sprintf("before<U+%04X>after", tt.r)
			if got := Terminal(in); got != want {
				t.Errorf("Terminal(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestTerminalIsIdempotent(t *testing.T) {
	in := "\x1b[31mhello\x1b[0m\u202eworld\r"
	once := Terminal(in)
	if twice := Terminal(once); twice != once {
		t.Fatalf("Terminal() is not idempotent: once = %q, twice = %q", once, twice)
	}
}
