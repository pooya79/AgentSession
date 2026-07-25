package sqlite

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
	storagecontract "github.com/pooya79/AgentSession/internal/storage"
)

func TestListSessionsOrdersVariablePrecisionTimestampsChronologicallyAcrossPages(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	ctx := context.Background()
	rows := []struct {
		id        model.SessionID
		startedAt any
	}{
		{"fractional-later", "2026-07-22T12:00:00.1Z"},
		{"exact-second", "2026-07-22T12:00:00Z"},
		{"previous-second", "2026-07-22T11:59:59.9Z"},
		{"unknown-time", nil},
	}
	for _, row := range rows {
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO sessions (
				id, title, summary, started_at, source_id, adapter_name,
				adapter_version, format_version, model_version, normalization_version
			) VALUES (?, '', '', ?, 'source', 'adapter', '1', '1', '1', '1')
		`, row.id, row.startedAt)
		if err != nil {
			t.Fatalf("insert session %q: %v", row.id, err)
		}
	}

	first, hasMore, err := store.ListSessions(ctx, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(first) != 2 || first[0].ID != "fractional-later" || first[1].ID != "exact-second" {
		t.Fatalf("first page = (%v, hasMore %v), want fractional-later then exact-second", sessionSummaryIDs(first), hasMore)
	}

	cursor := storagecontract.SessionCursor{LastActivityAt: first[1].LastActivityAt, ID: first[1].ID}
	second, hasMore, err := store.ListSessions(ctx, &cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(second) != 2 || second[0].ID != "previous-second" || second[1].ID != "unknown-time" {
		t.Fatalf("second page = (%v, hasMore %v), want previous-second then unknown-time", sessionSummaryIDs(second), hasMore)
	}
}

func TestListSessionsUsesLastActivityAndEarliestUserPreview(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	ctx := context.Background()
	insertExplorationSession(t, store, "ended", "codex", "2026-07-20T00:00:00Z", "2026-07-25T00:00:00Z")
	insertExplorationSession(t, store, "event-a", "claude", "2026-07-22T00:00:00Z", nil)
	insertExplorationSession(t, store, "event-b", "opencode", "2026-07-22T00:00:00Z", nil)
	insertExplorationSession(t, store, "started", "codex", "2026-07-23T00:00:00Z", nil)
	insertExplorationSession(t, store, "unknown", "codex", nil, nil)

	insertExplorationEvent(t, store, "event-a", 0, "2026-07-24T00:00:00Z", "message", "assistant text", `{"Role":"assistant","Text":"assistant text"}`)
	insertExplorationEvent(t, store, "event-a", 1, "2026-07-24T00:00:00Z", "message", "  first user request  ", `{"Role":"user","Text":"first user request"}`)
	insertExplorationEvent(t, store, "event-a", 2, nil, "message", "later user request", `{"Role":"user","Text":"later user request"}`)
	insertExplorationEvent(t, store, "event-b", 0, "2026-07-24T00:00:00Z", "summary", "system summary", `{"Text":"system summary"}`)

	var all []storagecontract.SessionSummary
	var cursor *storagecontract.SessionCursor
	for {
		page, more, err := store.ListSessions(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page...)
		if !more {
			break
		}
		last := page[len(page)-1]
		cursor = &storagecontract.SessionCursor{LastActivityAt: last.LastActivityAt, ID: last.ID}
	}
	want := []model.SessionID{"ended", "event-a", "event-b", "started", "unknown"}
	if got := sessionSummaryIDs(all); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("session order = %v, want %v", got, want)
	}
	if all[1].FirstUserMessage != "  first user request  " {
		t.Fatalf("first user preview = %q", all[1].FirstUserMessage)
	}
	if all[2].FirstUserMessage != "" {
		t.Fatalf("non-user preview = %q, want empty", all[2].FirstUserMessage)
	}
	if all[4].LastActivityAt != nil {
		t.Fatalf("unknown last activity = %v, want nil", all[4].LastActivityAt)
	}
}

func TestLibraryOverviewCountsExactDistinctAndDeduplicatedTotals(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	ctx := context.Background()
	insertExplorationSession(t, store, "one", "codex", nil, nil)
	insertExplorationSession(t, store, "two", "codex", nil, nil)
	insertExplorationSession(t, store, "three", "claude", nil, nil)
	insertExplorationEvent(t, store, "one", 0, nil, "message", "hello", `{"Role":"user","Text":"hello"}`)
	insertExplorationEvent(t, store, "two", 0, nil, "message", "hello", `{"Role":"user","Text":"hello"}`)

	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO session_diagnostics (session_id, position, code, severity, message, event_ids_json, raw_record_ids_json)
		VALUES ('one', 0, 'session.issue', 'warning', 'issue', '[]', '[]'),
		       ('two', 0, 'session.issue', 'warning', 'issue', '[]', '[]')
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `
		INSERT INTO record_diagnostics (session_id, raw_record_id, ordinal, code, severity, message, event_ids_json, raw_record_ids_json)
		SELECT 'one', id, 0, 'record.issue', 'warning', 'issue', '[]', '[]'
		FROM raw_records WHERE session_id = 'one' LIMIT 1
	`); err != nil {
		t.Fatal(err)
	}
	overview, err := store.LibraryOverview(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := storagecontract.LibraryOverview{Sessions: 3, Events: 2, Agents: 2, IssueSessions: 2}
	if overview != want {
		t.Fatalf("LibraryOverview() = %#v, want %#v", overview, want)
	}
}

func insertExplorationSession(t *testing.T, store *ImportStore, id, agent string, startedAt, endedAt any) {
	t.Helper()
	_, err := store.db.Exec(`
		INSERT INTO sessions (
			id, title, summary, started_at, ended_at, source_id, adapter_name,
			adapter_version, format_version, model_version, normalization_version
		) VALUES (?, '', '', ?, ?, 'source-' || ?, ?, '1', '1', '1', '1')
	`, id, startedAt, endedAt, id, agent)
	if err != nil {
		t.Fatalf("insert session %q: %v", id, err)
	}
}

func insertExplorationEvent(t *testing.T, store *ImportStore, sessionID string, sequence int64, timestamp any, kind, searchable, data string) {
	t.Helper()
	rawID := fmt.Sprintf("raw-%s-%d", sessionID, sequence)
	eventID := fmt.Sprintf("event-%s-%d", sessionID, sequence)
	summary := "Summary"
	if kind == "message" {
		summary = "Assistant message"
		if strings.Contains(data, `"Role":"user"`) {
			summary = "User message"
		}
	}
	if _, err := store.db.Exec(`
		INSERT INTO raw_records (
			id, session_id, source_id, record_sequence, content_hash,
			storage_encoding, original_size, content
		) VALUES (?, ?, 'source-' || ?, ?, 'hash', 'identity', 0, x'')
	`, rawID, sessionID, sessionID, sequence); err != nil {
		t.Fatalf("insert raw record %q: %v", rawID, err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO events (
			id, session_id, sequence, timestamp, kind, summary, searchable_text,
			data_json, raw_record_id, raw_source_id, raw_record_sequence, raw_content_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'source-' || ?, ?, 'hash')
	`, eventID, sessionID, sequence, timestamp, kind, summary, searchable, data, rawID, sessionID, sequence); err != nil {
		t.Fatalf("insert event %q: %v", eventID, err)
	}
}

func sessionSummaryIDs(summaries []storagecontract.SessionSummary) []model.SessionID {
	ids := make([]model.SessionID, len(summaries))
	for i, summary := range summaries {
		ids[i] = summary.ID
	}
	return ids
}
