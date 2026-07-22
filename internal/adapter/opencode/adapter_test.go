package opencode

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

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
