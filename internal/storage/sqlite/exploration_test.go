package sqlite

import (
	"context"
	"errors"
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
		if err := refreshSessionExploration(ctx, store.db, row.id); err != nil {
			t.Fatalf("refresh session %q: %v", row.id, err)
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

func TestInterpretationCoverageCountsUnknownEventsAndDistinctMalformedRecords(t *testing.T) {
	t.Parallel()
	store := openImportStore(t)
	insertExplorationSession(t, store, "coverage", "codex", nil, nil)
	insertExplorationEvent(t, store, "coverage", 0, nil, "unknown", "future", `{"Reason":"unsupported_record_kind","OriginalKind":"future"}`)
	insertExplorationEvent(t, store, "coverage", 1, nil, "message", "known", `{"Role":"user","Text":"known"}`)
	_, err := store.db.Exec(`
		INSERT INTO record_diagnostics (
			session_id, raw_record_id, ordinal, code, severity, message,
			interpretation_reason, event_ids_json, raw_record_ids_json
		) VALUES
			('coverage', 'raw-coverage-1', 0, 'missing', 'warning', 'missing type',
			 'missing_discriminant', '[]', '["raw-coverage-1"]'),
			('coverage', 'raw-coverage-1', 1, 'invalid', 'warning', 'invalid body',
			 'structurally_invalid_known_record', '[]', '["raw-coverage-1"]')
	`)
	if err != nil {
		t.Fatal(err)
	}
	coverage, err := store.InterpretationCoverage(context.Background(), "coverage")
	if err != nil || coverage.UnknownEvents != 1 || coverage.MalformedRecords != 1 {
		t.Fatalf("InterpretationCoverage() = (%#v, %v)", coverage, err)
	}
}

func TestListSessionsSupportsBackwardKeysetPages(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	ctx := context.Background()
	for index, id := range []model.SessionID{"one", "two", "three", "four", "five"} {
		insertExplorationSession(t, store, string(id), "codex", fmt.Sprintf("2026-07-%02dT00:00:00Z", 25-index), nil)
	}
	first, more, err := store.ListSessions(ctx, nil, 2)
	if err != nil || !more {
		t.Fatalf("first page = (%v, %v, %v)", sessionSummaryIDs(first), more, err)
	}
	forward := storagecontract.SessionCursor{LastActivityAt: first[1].LastActivityAt, ID: first[1].ID}
	second, more, err := store.ListSessions(ctx, &forward, 2)
	if err != nil || !more || fmt.Sprint(sessionSummaryIDs(second)) != fmt.Sprint([]model.SessionID{"three", "four"}) {
		t.Fatalf("second page = (%v, %v, %v)", sessionSummaryIDs(second), more, err)
	}
	backward := storagecontract.SessionCursor{LastActivityAt: second[0].LastActivityAt, ID: second[0].ID, Before: true}
	previous, more, err := store.ListSessions(ctx, &backward, 2)
	if err != nil || more || fmt.Sprint(sessionSummaryIDs(previous)) != fmt.Sprint([]model.SessionID{"one", "two"}) {
		t.Fatalf("previous page = (%v, %v, %v)", sessionSummaryIDs(previous), more, err)
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
	if _, err := store.db.ExecContext(ctx, `UPDATE events SET summary = 'adapter wording changed' WHERE id = 'event-event-a-1'`); err != nil {
		t.Fatal(err)
	}

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

func TestEventWindowAndBatchLocationsStayBoundedAndLightweight(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	ctx := context.Background()
	insertExplorationSession(t, store, "window", "codex", nil, nil)
	for sequence := int64(0); sequence < 6; sequence++ {
		insertExplorationEvent(t, store, "window", sequence, nil, "message", strings.Repeat("payload", 100), `{"Role":"user","Text":"retained payload"}`)
	}
	window, later, err := store.EventSummaryWindow(ctx, "window", 4, 3)
	if err != nil || !later || len(window) != 3 || window[0].Sequence != 2 || window[2].Sequence != 4 {
		t.Fatalf("window = (%#v, later %v, %v)", window, later, err)
	}
	ids := []model.EventID{"event-window-1", "event-window-4", "missing"}
	locations, err := store.EventLocations(ctx, ids)
	if err != nil || len(locations) != 2 || locations["event-window-4"].Sequence != 4 {
		t.Fatalf("locations = (%#v, %v)", locations, err)
	}
	if _, exists := locations["missing"]; exists {
		t.Fatal("missing event received a location")
	}
}

func TestEventPayloadsLoadsInlineAndDetachedWithoutRawEvidence(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	insertExplorationSession(t, store, "payloads", "codex", nil, nil)
	insertExplorationSession(t, store, "other", "codex", nil, nil)
	insertExplorationEvent(t, store, "payloads", 0, nil, "message", "inline", `{"Role":"user","Text":"inline text"}`)
	insertExplorationEvent(t, store, "payloads", 1, nil, "summary", "detached", `{"Text":"placeholder"}`)
	insertExplorationEvent(t, store, "other", 0, nil, "summary", "wrong session", `{"Text":"wrong session"}`)

	detachedText := strings.Repeat("detached text ", 24000)
	detached := []byte(`{"Text":` + fmt.Sprintf("%q", detachedText) + `}`)
	encoded, err := storagecontract.EncodePayload(detached)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE events SET data_json = '', payload_storage = 'detached'
		WHERE id = 'event-payloads-1'
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		INSERT INTO event_payloads (
			event_id, retention_policy_version, storage_encoding, original_size, content
		) VALUES ('event-payloads-1', ?, ?, ?, ?)
	`, encoded.PolicyVersion, encoded.Encoding, encoded.OriginalSize, encoded.Content); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
		UPDATE raw_records SET content = ?, original_size = ?
		WHERE id = 'raw-payloads-0'
	`, []byte("raw-secret-must-not-cross"), len("raw-secret-must-not-cross")); err != nil {
		t.Fatal(err)
	}

	payloads, err := store.EventPayloads(context.Background(), "payloads", []model.EventID{
		"event-payloads-1", "missing", "event-other-0", "event-payloads-0",
	})
	if err != nil || len(payloads) != 2 {
		t.Fatalf("EventPayloads() = (%#v, %v)", payloads, err)
	}
	if got := payloads["event-payloads-0"].(model.MessageData).Text; got != "inline text" {
		t.Fatalf("inline text = %q", got)
	}
	if got := payloads["event-payloads-1"].(model.SummaryData).Text; got != detachedText {
		t.Fatalf("detached text length = %d, want %d", len(got), len(detachedText))
	}
	if strings.Contains(fmt.Sprint(payloads), "raw-secret-must-not-cross") || payloads["event-other-0"] != nil {
		t.Fatalf("batch leaked excluded evidence: %#v", payloads)
	}

	tooMany := make([]model.EventID, 201)
	if _, err := store.EventPayloads(context.Background(), "payloads", tooMany); err == nil {
		t.Fatal("oversized payload batch error = nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.EventPayloads(ctx, "payloads", []model.EventID{"event-payloads-0"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled payload batch error = %v", err)
	}
}

func TestEventForStoragePreservesMessageRoleWhenPayloadIsDetached(t *testing.T) {
	t.Parallel()

	stored, err := eventForStorage(model.Event{
		ID:             "event",
		SessionID:      "session",
		Sequence:       0,
		Kind:           model.EventKindMessage,
		Summary:        "adapter-specific wording",
		SearchableText: strings.Repeat("x", storagecontract.InlinePayloadThresholdBytes+1),
		Data: model.MessageData{
			Role: model.MessageRoleUser,
			Text: strings.Repeat("x", storagecontract.InlinePayloadThresholdBytes+1),
		},
		RawRecord: model.RawRecordRef{ID: "raw", SourceID: "source", ContentHash: "hash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if stored.Payload == nil || stored.DataJSON != "" || stored.MessageRole != string(model.MessageRoleUser) {
		t.Fatalf("detached event = payload %v, inline %d bytes, role %q", stored.Payload != nil, len(stored.DataJSON), stored.MessageRole)
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
	if err := refreshSessionExploration(context.Background(), store.db, model.SessionID(id)); err != nil {
		t.Fatalf("refresh session %q: %v", id, err)
	}
}

func insertExplorationEvent(t *testing.T, store *ImportStore, sessionID string, sequence int64, timestamp any, kind, searchable, data string) {
	t.Helper()
	rawID := fmt.Sprintf("raw-%s-%d", sessionID, sequence)
	eventID := fmt.Sprintf("event-%s-%d", sessionID, sequence)
	summary := "Summary"
	messageRole := ""
	if kind == "message" {
		summary = "Assistant message"
		if strings.Contains(data, `"Role":"user"`) {
			summary = "User message"
			messageRole = "user"
		} else {
			messageRole = "assistant"
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
			data_json, message_role, raw_record_id, raw_source_id, raw_record_sequence, raw_content_hash
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'source-' || ?, ?, 'hash')
	`, eventID, sessionID, sequence, timestamp, kind, summary, searchable, data, messageRole, rawID, sessionID, sequence); err != nil {
		t.Fatalf("insert event %q: %v", eventID, err)
	}
	if err := refreshSessionExploration(context.Background(), store.db, model.SessionID(sessionID)); err != nil {
		t.Fatalf("refresh session %q: %v", sessionID, err)
	}
}

func sessionSummaryIDs(summaries []storagecontract.SessionSummary) []model.SessionID {
	ids := make([]model.SessionID, len(summaries))
	for i, summary := range summaries {
		ids[i] = summary.ID
	}
	return ids
}
