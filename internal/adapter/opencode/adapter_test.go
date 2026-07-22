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
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := adaptertest.LoadSanitizedFixture(t, filepath.Join("testdata", "valid_multi_session.sql"), "private-project")
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatalf("execute fixture: %v", err)
	}
	return path
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
	stream := importer.Source{ID: "jsonl", Size: int64(len(jsonl)), Open: func(context.Context) (io.ReadCloser, error) {
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
		if session.Import.AdapterVersion != AdapterVersion || session.Import.FormatVersion != FormatVersion {
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
		if diagnostic.Diagnostic.Code == "opencode.message.tokens.negative" {
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
