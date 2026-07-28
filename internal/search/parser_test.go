package search

import (
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		raw     string
		wantErr string
		check   func(*testing.T, Query)
	}{
		{name: "terms phrase and filters", raw: `alpha "two words" kind:error kind:message file:./SRC/Main.go tool:Go after:2025-01-01 command:"go test"`, check: func(t *testing.T, q Query) {
			if len(q.Text) != 2 || !q.Text[1].Phrase || len(q.Kinds) != 2 || q.Files[0] != "src/main.go" || q.Tools[0] != "go" || q.After[0] != (time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("unexpected query: %#v", q)
			}
		}},
		{name: "escaped phrase", raw: `"say \"hi\" and \\ leave"`, check: func(t *testing.T, q Query) {
			if got := q.Text[0].Value; got != `say "hi" and \ leave` {
				t.Fatalf("value = %q", got)
			}
		}},
		{name: "phrase containing colon", raw: `"http://example.com"`, check: func(t *testing.T, q Query) {
			if len(q.Text) != 1 || q.Text[0] != (TextClause{Value: "http://example.com", Phrase: true}) {
				t.Fatalf("unexpected query: %#v", q)
			}
		}},
		{name: "filter only", raw: `session:abc before:2025-01-02T03:04:05+03:30`, check: func(t *testing.T, q Query) {
			if q.HasText() || len(q.Sessions) != 1 || len(q.Before) != 1 {
				t.Fatalf("unexpected query: %#v", q)
			}
		}},
		{name: "unknown filter", raw: "repo:x", wantErr: "unknown_filter"},
		{name: "operator", raw: "hello OR world", wantErr: "raw_operator"},
		{name: "fts punctuation", raw: "prefix*", wantErr: "raw_operator"},
		{name: "bad kind", raw: "kind:nope", wantErr: "invalid_kind"},
		{name: "bad date", raw: "after:2025-1-1", wantErr: "invalid_date"},
		{name: "unterminated", raw: `"hello`, wantErr: "unterminated_quote"},
		{name: "bad escape", raw: `"hello\q"`, wantErr: "invalid_escape"},
		{name: "outside escape", raw: `hello\ world`, wantErr: "invalid_escape"},
		{name: "empty is valid", raw: "   ", check: func(t *testing.T, q Query) {}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.raw)
			if tt.wantErr != "" {
				var validation *ValidationError
				if err == nil || !strings.Contains(err.Error(), "") {
					t.Fatalf("Parse() error = %v", err)
				}
				if !asValidation(err, &validation) || validation.Code != tt.wantErr {
					t.Fatalf("Parse() error = %#v, want code %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			tt.check(t, got)
		})
	}
}

func asValidation(err error, target **ValidationError) bool {
	value, ok := err.(*ValidationError)
	if ok {
		*target = value
	}
	return ok
}

func TestParseLimits(t *testing.T) {
	if _, err := Parse(strings.Repeat("x", MaxQueryBytes+1)); err == nil {
		t.Fatal("oversized query accepted")
	}
	if _, err := Parse(strings.Repeat("x ", MaxClauses+1)); err == nil {
		t.Fatal("too many clauses accepted")
	}
	if _, err := Parse("file:" + strings.Repeat("x", MaxFilterBytes+1)); err == nil {
		t.Fatal("oversized filter accepted")
	}
}
