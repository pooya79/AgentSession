// Package search owns the safe, source-neutral search language and projection.
package search

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/model"
)

const (
	// MaxQueryBytes bounds the UTF-8 encoded query text.
	MaxQueryBytes = 4 * 1024
	// MaxClauses bounds the total number of text and filter clauses.
	MaxClauses = 32
	// MaxFilterBytes bounds each UTF-8 encoded filter value.
	MaxFilterBytes = 1024
)

// ValidationError is safe to return to a presentation. It never contains SQL
// or a generated FTS expression.
type ValidationError struct {
	Code    string
	Message string
}

func (e *ValidationError) Error() string { return e.Message }

// TextClause is a literal full-text term or quoted phrase.
type TextClause struct {
	Value  string
	Phrase bool
}

// Query is the parsed form. Values within a filter category are alternatives;
// categories themselves and every text clause are conjunctive.
type Query struct {
	Text     []TextClause
	Sessions []model.SessionID
	Kinds    []model.EventKind
	After    []time.Time
	Before   []time.Time
	Files    []string
	Tools    []string
	Commands []string
}

// HasText reports whether the query includes a full-text term or phrase.
func (q Query) HasText() bool { return len(q.Text) != 0 }

// Parse validates and converts the source-neutral search language into a Query.
func Parse(raw string) (Query, error) {
	if len(raw) > MaxQueryBytes {
		return Query{}, invalid("query_too_long", "Search query must be at most 4 KiB.")
	}
	if !utf8.ValidString(raw) {
		return Query{}, invalid("invalid_utf8", "Search query must be valid UTF-8.")
	}
	tokens, err := lex(raw)
	if err != nil {
		return Query{}, err
	}
	if len(tokens) > MaxClauses {
		return Query{}, invalid("too_many_clauses", "Search query must contain at most 32 clauses.")
	}
	var query Query
	for _, token := range tokens {
		if token.quoted {
			query.Text = append(query.Text, TextClause{Value: token.value, Phrase: true})
			continue
		}
		name, value, filtered := strings.Cut(token.value, ":")
		if !filtered {
			value = name
			if isOperator(value) {
				return Query{}, invalid("raw_operator", "Raw full-text operators are not supported.")
			}
			if strings.ContainsAny(value, "*^(){}[]") {
				return Query{}, invalid("raw_operator", "Raw full-text operators are not supported.")
			}
			query.Text = append(query.Text, TextClause{Value: value})
			continue
		}
		name = strings.ToLower(name)
		if value == "" {
			return Query{}, invalid("empty_filter", "Search filter values must not be empty.")
		}
		if len(value) > MaxFilterBytes {
			return Query{}, invalid("filter_too_long", "Search filter values must be at most 1 KiB.")
		}
		switch name {
		case "session":
			if len(value) > 512 || strings.TrimSpace(value) != value || hasControl(value) {
				return Query{}, invalid("invalid_session", "Session filter value is invalid.")
			}
			query.Sessions = append(query.Sessions, model.SessionID(value))
		case "kind":
			kind := model.EventKind(value)
			if !validKind(kind) {
				return Query{}, invalid("invalid_kind", "Kind filter value is not a canonical event kind.")
			}
			query.Kinds = append(query.Kinds, kind)
		case "after", "before":
			value, parseErr := parseBound(value)
			if parseErr != nil {
				return Query{}, invalid("invalid_date", "Date filters require RFC3339 or YYYY-MM-DD in UTC.")
			}
			if name == "after" {
				query.After = append(query.After, value)
			} else {
				query.Before = append(query.Before, value)
			}
		case "file":
			normalized := normalizePath(value)
			if normalized == "" || normalized == "." {
				return Query{}, invalid("invalid_file", "File filter value is invalid.")
			}
			query.Files = append(query.Files, normalized)
		case "tool":
			if strings.TrimSpace(value) == "" || hasControl(value) {
				return Query{}, invalid("invalid_tool", "Tool filter value is invalid.")
			}
			query.Tools = append(query.Tools, strings.ToLower(value))
		case "command":
			if strings.TrimSpace(value) == "" || strings.IndexByte(value, 0) >= 0 {
				return Query{}, invalid("invalid_command", "Command filter value is invalid.")
			}
			query.Commands = append(query.Commands, strings.ToLower(value))
		default:
			return Query{}, invalid("unknown_filter", fmt.Sprintf("Unknown search filter %q.", name))
		}
	}
	return query, nil
}

type lexicalToken struct {
	value  string
	quoted bool
}

func lex(raw string) ([]lexicalToken, error) {
	var result []lexicalToken
	for offset := 0; offset < len(raw); {
		for offset < len(raw) {
			r, size := utf8.DecodeRuneInString(raw[offset:])
			if !unicode.IsSpace(r) {
				break
			}
			offset += size
		}
		if offset == len(raw) {
			break
		}
		start := offset
		quoteAt := -1
		for offset < len(raw) && !isSpaceAt(raw, offset) {
			if raw[offset] == '\\' {
				return nil, invalid("invalid_escape", "Backslash escapes are allowed only inside quoted values.")
			}
			if raw[offset] == '"' {
				quoteAt = offset
				break
			}
			_, size := utf8.DecodeRuneInString(raw[offset:])
			offset += size
		}
		if quoteAt >= 0 {
			prefix := raw[start:quoteAt]
			if prefix != "" && !strings.HasSuffix(prefix, ":") {
				return nil, invalid("invalid_quote", "A quote may start only a phrase or a filter value.")
			}
			offset++
			var value strings.Builder
			for offset < len(raw) {
				if raw[offset] == '"' {
					offset++
					if offset < len(raw) && !isSpaceAt(raw, offset) {
						return nil, invalid("invalid_quote", "Quoted values must end at a clause boundary.")
					}
					if value.Len() == 0 {
						return nil, invalid("empty_clause", "Search clauses must not be empty.")
					}
					result = append(result, lexicalToken{value: prefix + value.String(), quoted: prefix == ""})
					break
				}
				if raw[offset] == '\\' {
					offset++
					if offset == len(raw) || (raw[offset] != '\\' && raw[offset] != '"') {
						return nil, invalid("invalid_escape", `Only \" and \\ escapes are supported.`)
					}
				}
				r, size := utf8.DecodeRuneInString(raw[offset:])
				value.WriteRune(r)
				offset += size
			}
			if offset > len(raw) || (offset == len(raw) && (len(raw) == 0 || raw[len(raw)-1] != '"')) {
				return nil, invalid("unterminated_quote", "Search query contains an unterminated quote.")
			}
			if offset == len(raw) && raw[len(raw)-1] != '"' {
				return nil, invalid("unterminated_quote", "Search query contains an unterminated quote.")
			}
			continue
		}
		value := raw[start:offset]
		if value == "" {
			return nil, invalid("empty_clause", "Search clauses must not be empty.")
		}
		result = append(result, lexicalToken{value: value})
	}
	return result, nil
}

func isSpaceAt(value string, offset int) bool {
	r, _ := utf8.DecodeRuneInString(value[offset:])
	return unicode.IsSpace(r)
}

func parseBound(value string) (time.Time, error) {
	if len(value) == len("2006-01-02") {
		parsed, err := time.Parse("2006-01-02", value)
		return parsed.UTC(), err
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return parsed.UTC(), err
}

func normalizePath(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), "\\", "/")
	value = path.Clean(value)
	value = strings.TrimPrefix(value, "./")
	return strings.ToLower(value)
}

func isOperator(value string) bool {
	switch strings.ToUpper(value) {
	case "AND", "OR", "NOT", "NEAR":
		return true
	default:
		return false
	}
}

func validKind(kind model.EventKind) bool {
	for _, candidate := range model.EventKinds() {
		if kind == candidate {
			return true
		}
	}
	return false
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func invalid(code, message string) error {
	return &ValidationError{Code: code, Message: message}
}
