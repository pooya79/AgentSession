package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	storageSQLite "github.com/pooya79/AgentSession/internal/storage/sqlite"
)

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
			for _, summary := range summaries {
				if summary.Kind != model.EventKindSummary {
					continue
				}
				event, found, err := store.Event(ctx, summary.ID)
				if err != nil || !found {
					t.Fatalf("summary event %q = %#v, %v, %v", summary.ID, event, found, err)
				}
				if event.Data.(model.SummaryData).Category != model.SummaryCategoryContext {
					t.Fatalf("summary category = %#v", event.Data)
				}
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
