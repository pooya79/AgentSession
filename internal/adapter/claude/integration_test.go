package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage/sqlite"
)

func TestCoordinatorSQLiteRepeatedImportPreservesIDsAndOrdering(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agentsession.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqlite.NewImportStore(db)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	source := fixtureSource(t, "main.jsonl")
	first, err := coordinator.Import(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	before, err := store.Events(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := coordinator.Import(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	after, err := store.Events(ctx, first.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Change != importer.SourceUnchanged || second.RecordsCommitted != 0 || len(before) != 7 || len(after) != len(before) {
		t.Fatalf("repeat result/events = %#v, %d/%d", second, len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Sequence != int64(i) || after[i].Sequence != before[i].Sequence {
			t.Fatalf("event %d changed across repeated import", i)
		}
	}
	session, found, err := store.Session(ctx, first.SessionID)
	if err != nil || !found {
		t.Fatalf("Session() = %#v, %v, %v", session, found, err)
	}
	if session.Import.AdapterName != "claude" || session.Import.AdapterVersion != AdapterVersion || session.Import.FormatVersion != "claude-code-jsonl-v1+cli-2.1.3" || session.Import.NormalizationVersion != NormalizationVersion {
		t.Fatalf("import versions = %#v", session.Import)
	}
}

func TestCoordinatorSQLiteDetachedRawAndNormalizedPayloadsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agentsession.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqlite.NewImportStore(db)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Repeat("large-claude-evidence-", 16<<10)
	record, err := json.Marshal(map[string]any{
		"type": "assistant", "sessionId": "sqlite-large", "version": "6.0.0", "uuid": "large-message",
		"timestamp": "2026-01-01T00:00:00Z",
		"message":   map[string]any{"role": "assistant", "content": text},
	})
	if err != nil {
		t.Fatal(err)
	}
	record = append(record, '\n')
	result, err := coordinator.Import(ctx, bytesSource("sqlite-large", record))
	if err != nil {
		t.Fatal(err)
	}
	storedEvents, err := store.Events(ctx, result.SessionID)
	if err != nil || len(storedEvents) != 1 {
		t.Fatalf("Events() = %d, %v", len(storedEvents), err)
	}
	message := storedEvents[0].Data.(model.MessageData)
	if message.Text != text {
		t.Fatal("large normalized message was truncated")
	}
	raw, found, err := store.RawRecord(ctx, storedEvents[0].RawRecord.ID)
	if err != nil || !found || !bytes.Equal(raw.Content, record) {
		t.Fatalf("RawRecord() = found %v, error %v, bytes %d", found, err, len(raw.Content))
	}
	var rawEncoding, payloadStorage string
	if err := db.QueryRowContext(ctx, `SELECT storage_encoding FROM raw_records WHERE id = ?`, raw.Ref.ID).Scan(&rawEncoding); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload_storage FROM events WHERE id = ?`, storedEvents[0].ID).Scan(&payloadStorage); err != nil {
		t.Fatal(err)
	}
	if rawEncoding != "zlib" || payloadStorage != "detached" {
		t.Fatalf("large storage = raw %q, payload %q", rawEncoding, payloadStorage)
	}
}

func TestCoordinatorSQLiteReconcilesMutationWithStableUnaffectedIDs(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agentsession.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := sqlite.NewImportStore(db)
	coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	base := []byte("{\"type\":\"user\",\"sessionId\":\"mutation\",\"version\":\"1.0\",\"uuid\":\"first\",\"message\":{\"role\":\"user\",\"content\":\"one\"}}\n{\"type\":\"assistant\",\"sessionId\":\"mutation\",\"version\":\"1.0\",\"uuid\":\"second\",\"message\":{\"role\":\"assistant\",\"content\":\"two\"}}\n")
	first, err := coordinator.Import(ctx, bytesSource("mutation", base))
	if err != nil {
		t.Fatal(err)
	}
	before, _ := store.Events(ctx, first.SessionID)
	mutated := bytes.Replace(base, []byte(`"one"`), []byte(`"ONE"`), 1)
	result, err := coordinator.Import(ctx, bytesSource("mutation", mutated))
	if err != nil {
		t.Fatal(err)
	}
	after, _ := store.Events(ctx, first.SessionID)
	if result.Change != importer.SourceMutated || !result.Reconciled || len(after) != 2 {
		t.Fatalf("reconciliation = %#v, events %d", result, len(after))
	}
	if before[0].ID != after[0].ID || before[1].ID != after[1].ID || after[0].Data.(model.MessageData).Text != "ONE" {
		t.Fatalf("reconciled events = %#v", after)
	}
}
