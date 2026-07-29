package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	claudeAdapter "github.com/pooya79/AgentSession/internal/adapter/claude"
	codexAdapter "github.com/pooya79/AgentSession/internal/adapter/codex"
	"github.com/pooya79/AgentSession/internal/adaptertest"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	storageSQLite "github.com/pooya79/AgentSession/internal/storage/sqlite"
	_ "modernc.org/sqlite"
)

func createFixture(t *testing.T) string {
	return createNamedFixture(t, "valid_multi_session.sql")
}

func createNamedFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := adaptertest.LoadSanitizedFixture(t, filepath.Join("testdata", name), "private-project")
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatalf("execute fixture: %v", err)
	}
	return path
}

func TestGenerationSelectionUsesOneAuthoritativeTimeline(t *testing.T) {
	ctx := context.Background()
	view, err := New().PrepareContainer(ctx, sourceFor(createNamedFixture(t, "coexisting_generations.sql")))
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	children, err := view.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("children = %d, want 2", len(children))
	}
	want := []generation{generationEvent, generationMessage}
	for i, child := range children {
		prepared := child.Prepared.(*prepared)
		if prepared.selected.generation != want[i] {
			t.Fatalf("child %d generation = %q, want %q", i, prepared.selected.generation, want[i])
		}
		var tables []string
		if err := prepared.eachRecord(ctx, func(record logicalRecord) error {
			tables = append(tables, record.table)
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if len(tables) < 2 {
			t.Fatalf("child %d tables = %v, want session and selected-generation records", i, tables)
		}
		if tables[0] != "session" {
			t.Fatalf("child %d first table = %q, want session: %v", i, tables[0], tables)
		}
		for _, table := range tables[1:] {
			if i == 0 && table != "event" {
				t.Fatalf("durable child retained cross-generation table %q: %v", table, tables)
			}
			if i == 1 && table != "session_message" {
				t.Fatalf("fallback child retained cross-generation table %q: %v", table, tables)
			}
		}
	}
}

func TestGenerationSpecificFormatsAndMappings(t *testing.T) {
	tests := []struct {
		fixture string
		format  model.Version
		kinds   []model.EventKind
	}{
		{
			fixture: "legacy_variants_v1.sql", format: LegacyFormatVersion,
			kinds: []model.EventKind{model.EventKindMessage, model.EventKindFileRead, model.EventKindUsage, model.EventKindUnknown, model.EventKindUnknown, model.EventKindError, model.EventKindUnknown, model.EventKindUsage},
		},
		{
			fixture: "session_message_v1.sql", format: MessageFormatVersion,
			kinds: []model.EventKind{model.EventKindMessage, model.EventKindCommand, model.EventKindMessage, model.EventKindUnknown, model.EventKindToolCall, model.EventKindToolResult, model.EventKindUsage, model.EventKindSummary, model.EventKindUnknown, model.EventKindMessage},
		},
		{
			fixture: "durable_events_v1.sql", format: EventFormatVersion,
			kinds: []model.EventKind{model.EventKindMessage, model.EventKindToolCall, model.EventKindToolResult, model.EventKindToolCall, model.EventKindToolResult, model.EventKindUsage, model.EventKindUnknown, model.EventKindSummary, model.EventKindUnknown},
		},
	}
	for _, test := range tests {
		t.Run(test.fixture, func(t *testing.T) {
			ctx := context.Background()
			indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer indexDB.Close()
			store, _ := storageSQLite.NewImportStore(indexDB)
			coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
			results, err := coordinator.ImportAll(ctx, sourceFor(createNamedFixture(t, test.fixture)))
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 2 {
				t.Fatalf("results = %d, want 2", len(results))
			}
			session, found, err := store.Session(ctx, results[0].SessionID)
			if err != nil || !found {
				t.Fatalf("session = %#v, %v, %v", session, found, err)
			}
			if session.Import.FormatVersion != test.format {
				t.Fatalf("format = %q, want %q", session.Import.FormatVersion, test.format)
			}
			summaries, err := store.EventSummaries(ctx, results[0].SessionID)
			if err != nil {
				t.Fatal(err)
			}
			var kinds []model.EventKind
			for _, summary := range summaries {
				kinds = append(kinds, summary.Kind)
			}
			if !equalKinds(kinds, test.kinds) {
				t.Fatalf("kinds = %v, want %v", kinds, test.kinds)
			}
		})
	}
}

func TestMalformedVariantsAreDiagnosedRetainedAndDoNotStopImport(t *testing.T) {
	ctx := context.Background()
	indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	store, err := storageSQLite.NewImportStore(indexDB)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	results, err := coordinator.ImportAll(ctx, sourceFor(createNamedFixture(t, "malformed_variants.sql")))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d, want 1", len(results))
	}

	diagnostics, err := store.RecordDiagnostics(ctx, results[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	wantDiagnostics := map[string]bool{
		"opencode.record.data.malformed":         false,
		"opencode.session_message.type.conflict": false,
		"opencode.session_message.user.invalid":  false,
	}
	for _, diagnostic := range diagnostics {
		if _, wanted := wantDiagnostics[diagnostic.Diagnostic.Code]; wanted {
			wantDiagnostics[diagnostic.Diagnostic.Code] = true
		}
	}
	for code, found := range wantDiagnostics {
		if !found {
			t.Errorf("diagnostic %q missing from %#v", code, diagnostics)
		}
	}

	wantRawIDs := map[string]bool{
		`"value":"null_data"`:  false,
		`"value":"empty_data"`: false,
		`"value":"array_data"`: false,
		`"value":"known_bad"`:  false,
	}
	rows, err := indexDB.Query(`SELECT id FROM raw_records WHERE session_id = ? ORDER BY record_sequence`, results[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	rawCount := 0
	for rows.Next() {
		rawCount++
		var id model.RawRecordID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		record, found, err := store.RawRecord(ctx, id)
		if err != nil || !found {
			rows.Close()
			t.Fatalf("raw record %q = %v, %v", id, found, err)
		}
		for marker := range wantRawIDs {
			if strings.Contains(string(record.Content), marker) {
				wantRawIDs[marker] = true
			}
		}
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	if rawCount != 8 {
		t.Fatalf("raw record count = %d, want 8", rawCount)
	}
	for marker, found := range wantRawIDs {
		if !found {
			t.Errorf("malformed raw record marker %s was not retained", marker)
		}
	}

	summaries, err := store.EventSummaries(ctx, results[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundAfter := false
	for _, summary := range summaries {
		event, found, err := store.Event(ctx, summary.ID)
		if err != nil || !found {
			t.Fatalf("event %q = %v, %v", summary.ID, found, err)
		}
		if message, ok := event.Data.(model.MessageData); ok && message.Text == "valid after malformed" {
			foundAfter = true
		}
	}
	if !foundAfter {
		t.Fatalf("valid row after malformed variants was not normalized: %#v", summaries)
	}
}

func TestIncompleteDurableWithoutFallbackIsSelectedAndDiagnosed(t *testing.T) {
	ctx := context.Background()
	path := createNamedFixture(t, "durable_events_v1.sql")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE event_sequence SET seq = 99 WHERE aggregate_id = 'ev_main'`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	view, err := New().PrepareContainer(ctx, sourceFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	children, err := view.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prepared := children[0].Prepared.(*prepared)
	if prepared.selected.generation != generationEvent || !prepared.selected.durableIncomplete {
		t.Fatalf("selection = %#v", prepared.selected)
	}
	session := prepared.session()
	if len(session.Diagnostics) != 1 || session.Diagnostics[0].Code != "opencode.generation.durable.incomplete" {
		t.Fatalf("session diagnostics = %#v", session.Diagnostics)
	}
}

func TestNormalizationV1StateIsReplaced(t *testing.T) {
	ctx := context.Background()
	view, err := New().PrepareContainer(ctx, sourceFor(createFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	children, err := view.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}
	prepared := children[0].Prepared.(*prepared)
	change, err := prepared.Verify(ctx, importer.SourceState{Import: model.ImportMetadata{
		SourceID: prepared.source.ID, AdapterName: "opencode", AdapterVersion: AdapterVersion,
		FormatVersion: LegacyFormatVersion, ModelVersion: ModelVersion, NormalizationVersion: "1",
	}})
	if err != nil || change != importer.SourceReplaced {
		t.Fatalf("verify v1 = %q, %v", change, err)
	}
}

func TestParseErrorSupportsNestedOpenCodeErrorData(t *testing.T) {
	raw := json.RawMessage(`{"name":"APIError","data":{"message":"request failed","statusCode":500}}`)
	got, ok := parseError(raw, "assistant_error")
	if !ok {
		t.Fatal("parseError() rejected nested OpenCode error data")
	}
	if got.Code != "APIError" || got.Message != "request failed" {
		t.Fatalf("parseError() = %#v", got)
	}
}

func TestDiagnosticsAreBoundedPerRow(t *testing.T) {
	values := make([]model.Diagnostic, 10)
	for i := range values {
		values[i] = invalidDiagnostic("opencode.test.invalid", "A fixed test diagnostic.")
	}
	got := boundDiagnostics(values)
	if len(got) != 8 || got[7].Code != "opencode.diagnostics.truncated" {
		t.Fatalf("bounded diagnostics = %#v", got)
	}
}

func equalKinds(left, right []model.EventKind) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sourceFor(path string) importer.Source {
	return importer.Source{ID: "physical-opencode", LocalPath: path, Hint: "opencode"}
}

func TestProbeRequiresSchemaAndLocalPath(t *testing.T) {
	ctx := context.Background()
	adapter := New()
	probe, err := adapter.Probe(ctx, sourceFor(createFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain || probe.FormatVersion != FormatVersion {
		t.Fatalf("probe = %#v", probe)
	}
	jsonl := []byte(`{"type":"message"}`)
	stream := importer.Source{ID: "jsonl", Size: int64(len(jsonl)), OpenAt: func(context.Context, int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(jsonl))), nil
	}}
	probe, err = adapter.Probe(ctx, stream)
	if err != nil || probe.Confidence != importer.ProbeUnsupported {
		t.Fatalf("JSONL probe = %#v, %v", probe, err)
	}

	unrelatedPath := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", unrelatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	probe, err = adapter.Probe(ctx, sourceFor(unrelatedPath))
	if err != nil || probe.Confidence != importer.ProbeUnsupported {
		t.Fatalf("unrelated probe = %#v, %v", probe, err)
	}
}

func TestNormalizeRejectsNullDataAsStructurallyInvalid(t *testing.T) {
	events, diagnostics, err := (&prepared{}).normalize(logicalRecord{table: "message", data: []byte("null")}, "session", 0)
	if err != nil || len(events) != 0 || len(diagnostics) != 1 ||
		diagnostics[0].InterpretationReason != model.InterpretationStructurallyInvalidKnownRecord {
		t.Fatalf("normalize(null) = events %#v, diagnostics %#v, err %v", events, diagnostics, err)
	}
}

func TestNormalizeClassifiesPresentNonStringDiscriminatorAsInvalid(t *testing.T) {
	for _, table := range []string{"session_message", "event"} {
		t.Run(table, func(t *testing.T) {
			events, diagnostics, err := (&prepared{}).normalize(logicalRecord{
				table: table, rowTypePresent: true, rowTypeValid: false, data: []byte(`{}`),
			}, "session", 0)
			if err != nil || len(events) != 0 || len(diagnostics) != 1 ||
				diagnostics[0].InterpretationReason != model.InterpretationStructurallyInvalidKnownRecord {
				t.Fatalf("normalize(non-string type) = events %#v, diagnostics %#v, err %v", events, diagnostics, err)
			}
		})
	}
}

func TestDurablePromptObjectOnlyOverridesWithValidNestedText(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "missing nested preserves text", data: `{"text":"top-level","prompt":{}}`, want: "top-level"},
		{name: "invalid nested preserves text", data: `{"text":"top-level","prompt":{"text":{"bad":true}}}`, want: "top-level"},
		{name: "missing nested preserves content", data: `{"prompt":{},"content":"fallback"}`, want: "fallback"},
		{name: "valid nested overrides text", data: `{"text":"top-level","prompt":{"text":"nested"}}`, want: "nested"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			record := logicalRecord{
				table: "event", nativeID: "event", rowType: "session.next.prompted",
				rowTypePresent: true, rowTypeValid: true, data: []byte(test.data),
			}
			events, diagnostics, err := (&prepared{}).normalize(record, "session", 0)
			if err != nil || len(diagnostics) != 0 || len(events) != 1 {
				t.Fatalf("normalize durable prompt = events %#v, diagnostics %#v, err %v", events, diagnostics, err)
			}
			message, ok := events[0].Data.(model.MessageData)
			if !ok || message.Text != test.want {
				t.Fatalf("message = %#v, want text %q", events[0].Data, test.want)
			}
		})
	}
}

func TestCoordinatorImportsLogicalSessionsAndRetainsRows(t *testing.T) {
	ctx := context.Background()
	sourcePath := createFixture(t)
	indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	store, err := storageSQLite.NewImportStore(indexDB)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{codexAdapter.New(), claudeAdapter.New(), New()}, nil, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	results, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2", len(results))
	}
	if results[0].SessionID == results[1].SessionID || results[0].SourceID == results[1].SourceID {
		t.Fatal("logical identities are not distinct")
	}
	for _, result := range results {
		session, found, err := store.Session(ctx, result.SessionID)
		if err != nil || !found {
			t.Fatalf("session %q = %#v, %v, %v", result.SessionID, session, found, err)
		}
		if session.Import.AdapterVersion != AdapterVersion || session.Import.FormatVersion != LegacyFormatVersion ||
			session.Import.NormalizationVersion != NormalizationVersion {
			t.Fatalf("metadata = %#v", session.Import)
		}
	}
	summaries, err := store.EventSummaries(ctx, results[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 4 {
		t.Fatalf("event count = %d, want 4: %#v", len(summaries), summaries)
	}
	for i, summary := range summaries {
		if summary.Sequence != int64(i) {
			t.Fatalf("sequence %d = %d", i, summary.Sequence)
		}
	}
	var rawCount int
	if err := indexDB.QueryRow(`SELECT COUNT(*) FROM raw_records WHERE session_id = ?`, results[0].SessionID).Scan(&rawCount); err != nil {
		t.Fatal(err)
	}
	if rawCount != 5 {
		t.Fatalf("raw record count = %d, want 5", rawCount)
	}
	var rawID model.RawRecordID
	if err := indexDB.QueryRow(`SELECT id FROM raw_records WHERE session_id = ? ORDER BY record_sequence LIMIT 1`, results[0].SessionID).Scan(&rawID); err != nil {
		t.Fatal(err)
	}
	record, found, err := store.RawRecord(ctx, rawID)
	if err != nil || !found {
		t.Fatalf("raw record = %v, %v", found, err)
	}
	if !strings.Contains(string(record.Content), `"name":"extra","type":"blob","base64":"AP8="`) {
		t.Fatalf("session row did not preserve typed BLOB: %s", record.Content)
	}
	repeated, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	for i := range repeated {
		if repeated[i].Change != importer.SourceUnchanged || repeated[i].CanonicalChanged {
			t.Fatalf("repeat result %d = %#v", i, repeated[i])
		}
		if repeated[i].SessionID != results[i].SessionID {
			t.Fatal("session ID changed")
		}
	}
}

func TestCopiedContainerUsesDistinctEventIdentities(t *testing.T) {
	ctx := context.Background()
	sourcePath := createFixture(t)
	indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	store, err := storageSQLite.NewImportStore(indexDB)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}

	first, err := coordinator.ImportAll(ctx, importer.Source{ID: "physical-copy-a", LocalPath: sourcePath, Hint: "opencode"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.ImportAll(ctx, importer.Source{ID: "physical-copy-b", LocalPath: sourcePath, Hint: "opencode"})
	if err != nil {
		t.Fatalf("import copied container: %v", err)
	}
	if len(first) != len(second) || len(first) == 0 {
		t.Fatalf("logical result counts = %d and %d", len(first), len(second))
	}
	for i := range first {
		left, err := store.EventSummaries(ctx, first[i].SessionID)
		if err != nil {
			t.Fatal(err)
		}
		right, err := store.EventSummaries(ctx, second[i].SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if len(left) != len(right) {
			t.Fatalf("event counts for logical session %d = %d and %d", i, len(left), len(right))
		}
		for j := range left {
			if left[j].ID == right[j].ID {
				t.Fatalf("copied container event identity was reused: %q", left[j].ID)
			}
		}
	}
}

func TestContainerInventoryRemovesDeletedLogicalSession(t *testing.T) {
	ctx := context.Background()
	sourcePath := createFixture(t)
	indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	store, _ := storageSQLite.NewImportStore(indexDB)
	coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	first, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DELETE FROM part WHERE session_id = 'ses_beta'`,
		`DELETE FROM message WHERE session_id = 'ses_beta'`,
		`DELETE FROM session WHERE id = 'ses_beta'`,
	} {
		if _, err := sourceDB.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	_ = sourceDB.Close()
	second, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 {
		t.Fatalf("remaining results = %d", len(second))
	}
	if _, found, err := store.Session(ctx, first[1].SessionID); err != nil || found {
		t.Fatalf("deleted session still present: %v, %v", found, err)
	}
	if _, found, err := store.Checkpoint(ctx, first[1].SourceID); err != nil || found {
		t.Fatalf("deleted checkpoint still present: %v, %v", found, err)
	}
}

func TestMutationReconcilesAndMalformedDataIsRetained(t *testing.T) {
	ctx := context.Background()
	sourcePath := createFixture(t)
	indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	store, _ := storageSQLite.NewImportStore(indexDB)
	coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	first, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatal(err)
	}

	var malformedRawID model.RawRecordID
	rows, err := indexDB.Query(`SELECT id FROM raw_records WHERE session_id = ? ORDER BY record_sequence`, first[1].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id model.RawRecordID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		record, found, err := store.RawRecord(ctx, id)
		if err != nil || !found {
			t.Fatal(err)
		}
		if strings.Contains(string(record.Content), `"value":"{bad"`) {
			malformedRawID = id
		}
	}
	_ = rows.Close()
	if malformedRawID == "" {
		t.Fatal("malformed JSON TEXT was not retained exactly")
	}
	diagnostics, err := store.RecordDiagnostics(ctx, first[1].SessionID)
	if err != nil || len(diagnostics) == 0 {
		t.Fatalf("malformed row diagnostics = %#v, %v", diagnostics, err)
	}

	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.Exec(`UPDATE part SET data = '{"type":"text","text":"updated"}' WHERE id = 'part_text'`); err != nil {
		t.Fatal(err)
	}
	_ = sourceDB.Close()
	second, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	if second[0].Change != importer.SourceMutated || !second[0].Reconciled {
		t.Fatalf("mutation result = %#v", second[0])
	}
	summaries, err := store.EventSummaries(ctx, second[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) == 0 {
		t.Fatal("reconciled timeline is empty")
	}
	event, found, err := store.Event(ctx, summaries[0].ID)
	if err != nil || !found {
		t.Fatalf("read reconciled event: %v, %v", found, err)
	}
	message, ok := event.Data.(model.MessageData)
	if !ok || message.Text != "updated" {
		t.Fatalf("reconciled message = %#v", event.Data)
	}
}

func TestLargeRowUsesCompressedRawAndDetachedPayload(t *testing.T) {
	ctx := context.Background()
	sourcePath := createFixture(t)
	largeText := strings.Repeat("large-sanitized-payload-", 14000)
	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{"type": "text", "text": largeText})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.Exec(`INSERT INTO part (id, message_id, session_id, data) VALUES (?, ?, ?, ?)`, "part_zz_large", "msg_assistant", "ses_alpha", string(data)); err != nil {
		t.Fatal(err)
	}
	_ = sourceDB.Close()

	indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	store, _ := storageSQLite.NewImportStore(indexDB)
	coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	results, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatal(err)
	}
	var compressedRaw, detachedPayload int
	if err := indexDB.QueryRow(`SELECT COUNT(*) FROM raw_records WHERE session_id = ? AND storage_encoding = 'zlib' AND original_size > 262144`, results[0].SessionID).Scan(&compressedRaw); err != nil {
		t.Fatal(err)
	}
	if err := indexDB.QueryRow(`SELECT COUNT(*) FROM events WHERE session_id = ? AND payload_storage = 'detached'`, results[0].SessionID).Scan(&detachedPayload); err != nil {
		t.Fatal(err)
	}
	if compressedRaw != 1 || detachedPayload != 1 {
		t.Fatalf("large retention counts = raw %d, payload %d", compressedRaw, detachedPayload)
	}
	summaries, err := store.EventSummaries(ctx, results[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundLarge := false
	for _, summary := range summaries {
		event, found, err := store.Event(ctx, summary.ID)
		if err != nil || !found {
			t.Fatal(err)
		}
		if message, ok := event.Data.(model.MessageData); ok && message.Text == largeText {
			foundLarge = true
		}
	}
	if !foundLarge {
		t.Fatal("large normalized payload did not round-trip")
	}
}

func TestPreparedSnapshotRejectsWrites(t *testing.T) {
	ctx := context.Background()
	containerView, err := New().PrepareContainer(ctx, sourceFor(createFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer containerView.Close()
	prepared := containerView.(*container)
	if _, err := prepared.snapshot.tx.ExecContext(ctx, `INSERT INTO unrelated VALUES ('write')`); err == nil {
		t.Fatal("query-only OpenCode snapshot accepted a write")
	}
}

func TestPartIterationErrorAbortsImport(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "iteration-error.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, title TEXT, time_created INTEGER, time_updated INTEGER)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE part (id TEXT PRIMARY KEY, message_id TEXT, session_id TEXT, data TEXT, extra TEXT)`,
		`INSERT INTO session VALUES ('session', 'Iteration error', 1700000000000, 1700000000001)`,
		`INSERT INTO message VALUES ('message', 'session', 1700000000000, '{"role":"user"}')`,
		`INSERT INTO part VALUES ('part', 'message', 'session', '{"type":"text","text":"unreachable"}', 'not-json')`,
		`ALTER TABLE part ADD COLUMN broken TEXT GENERATED ALWAYS AS (json_extract(extra, '$')) VIRTUAL`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare iteration-error database: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	containerView, err := New().PrepareContainer(ctx, sourceFor(path))
	if err != nil {
		t.Fatal(err)
	}
	defer containerView.Close()
	children, err := containerView.Children(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 {
		t.Fatalf("child count = %d, want 1", len(children))
	}
	if err := children[0].Prepared.(*prepared).eachRecord(ctx, func(logicalRecord) error { return nil }); err == nil {
		t.Fatal("part-row iteration error was ignored")
	}
}

func TestNegativeTokenCounterIsDiagnosedAndOmitted(t *testing.T) {
	ctx := context.Background()
	sourcePath := createFixture(t)
	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sourceDB.Exec(`UPDATE message SET data = '{"role":"assistant","tokens":{"input":-3,"output":5}}' WHERE id = 'msg_assistant'`); err != nil {
		t.Fatal(err)
	}
	_ = sourceDB.Close()

	indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer indexDB.Close()
	store, _ := storageSQLite.NewImportStore(indexDB)
	coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	results, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
	if err != nil {
		t.Fatalf("import negative token evidence: %v", err)
	}
	diagnostics, err := store.RecordDiagnostics(ctx, results[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundDiagnostic := false
	for _, diagnostic := range diagnostics {
		if diagnostic.Diagnostic.Code == "opencode.message.tokens.invalid" {
			foundDiagnostic = true
		}
	}
	if !foundDiagnostic {
		t.Fatalf("negative token diagnostic missing: %#v", diagnostics)
	}
	summaries, err := store.EventSummaries(ctx, results[0].SessionID)
	if err != nil {
		t.Fatal(err)
	}
	foundUsage := false
	for _, summary := range summaries {
		if summary.Kind != model.EventKindUsage {
			continue
		}
		event, found, err := store.Event(ctx, summary.ID)
		if err != nil || !found {
			t.Fatalf("read usage event: %v, %v", found, err)
		}
		usage, ok := event.Data.(model.UsageData)
		if !ok || usage.InputTokens != nil || usage.OutputTokens == nil || *usage.OutputTokens != 5 {
			t.Fatalf("sanitized usage = %#v", event.Data)
		}
		foundUsage = true
	}
	if !foundUsage {
		t.Fatal("sanitized usage event missing")
	}
}

func TestMillisecondTimeRejectsValuesOutsideRFC3339Range(t *testing.T) {
	tests := []struct {
		name  string
		value int64
		valid bool
	}{
		{name: "ordinary", value: 1700000000000, valid: true},
		{name: "year zero", value: time.Date(0, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), valid: true},
		{name: "last millisecond of year 9999", value: time.Date(9999, time.December, 31, 23, 59, 59, int(time.Millisecond*999), time.UTC).UnixMilli(), valid: true},
		{name: "year 10000", value: time.Date(10000, time.January, 1, 0, 0, 0, 0, time.UTC).UnixMilli()},
		{name: "maximum integer", value: math.MaxInt64},
		{name: "minimum integer", value: math.MinInt64},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			value, diagnostic := millisecondTime(test.value, "timestamp.invalid")
			if test.valid && (value == nil || diagnostic != nil) {
				t.Fatalf("valid timestamp = %v, %#v", value, diagnostic)
			}
			if !test.valid && (value != nil || diagnostic == nil || diagnostic.Code != "timestamp.invalid") {
				t.Fatalf("invalid timestamp = %v, %#v", value, diagnostic)
			}
		})
	}
}
