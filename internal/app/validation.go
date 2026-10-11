package app

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/model"
)

// validateIdentifier rejects empty, oversized, malformed, or control-bearing identifiers.
func validateIdentifier(kind, value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > maximumIdentifierSize || !utf8.ValidString(value) {
		return fmt.Errorf("%w: %s ID is malformed", ErrInvalidRequest, kind)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %s ID is malformed", ErrInvalidRequest, kind)
		}
	}
	return nil
}

// validateEventID enforces the stable hexadecimal canonical event ID shape.
func validateEventID(id model.EventID) error {
	value := string(id)
	if len(value) != 68 || !strings.HasPrefix(value, "evt_") {
		return fmt.Errorf("%w: event ID is malformed", ErrInvalidRequest)
	}
	for _, r := range value[4:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return fmt.Errorf("%w: event ID is malformed", ErrInvalidRequest)
		}
	}
	return nil
}
