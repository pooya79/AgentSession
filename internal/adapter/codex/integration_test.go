package codex

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage/sqlite"
)

func TestCoordinatorSQLiteRepeatedImportAndVersionRoundTrip(t *testing.T) {
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
	source := fixtureSource(t, "ordinal.jsonl")
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
	if second.Change != importer.SourceUnchanged || second.RecordsCommitted != 0 || len(before) != 3 || len(after) != len(before) {
		t.Fatalf("repeat result/events = %#v, %d/%d", second, len(before), len(after))
	}
	for i := range before {
		if before[i].ID != after[i].ID || before[i].Sequence != after[i].Sequence {
			t.Fatalf("event %d changed across repeated import", i)
		}
	}
	session, found, err := store.Session(ctx, first.SessionID)
	if err != nil || !found {
		t.Fatalf("Session() = %#v, %v, %v", session, found, err)
	}
	if session.Import.AdapterVersion != AdapterVersion || session.Import.FormatVersion != "codex-rollout-jsonl-v2-ordinal+cli-0.133.0" || session.Import.NormalizationVersion != NormalizationVersion {
		t.Fatalf("import versions = %#v", session.Import)
	}
}

func TestCoordinatorSQLiteLargeRawAndNormalizedPayloadsRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agentsession.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, _ := sqlite.NewImportStore(db)
	coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
	text := strings.Repeat("large-evidence-", 24<<10)
	data := []byte(fmt.Sprintf("{\"timestamp\":\"2026-01-01T00:00:00Z\",\"ordinal\":0,\"type\":\"session_meta\",\"payload\":{\"id\":\"sqlite-large\",\"cli_version\":\"0.133.0\"}}\n{\"timestamp\":\"2026-01-01T00:00:01Z\",\"ordinal\":1,\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"large-message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":%q}]}}\n", text))
	result, err := coordinator.Import(ctx, bytesSource("sqlite-large", data))
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.Events(ctx, result.SessionID)
	if err != nil || len(events) != 1 {
		t.Fatalf("Events() = %d, %v", len(events), err)
	}
	if events[0].Data.(model.MessageData).Text != text {
		t.Fatal("large normalized message was truncated")
	}
	raw, found, err := store.RawRecord(ctx, events[0].RawRecord.ID)
	if err != nil || !found || !bytes.Equal(raw.Content, data[bytes.IndexByte(data, '\n')+1:]) {
		t.Fatalf("large RawRecord() = found %v, error %v, bytes %d", found, err, len(raw.Content))
	}
	var rawEncoding, payloadStorage string
	if err := db.QueryRowContext(ctx, `SELECT storage_encoding FROM raw_records WHERE id = ?`, raw.Ref.ID).Scan(&rawEncoding); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload_storage FROM events WHERE id = ?`, events[0].ID).Scan(&payloadStorage); err != nil {
		t.Fatal(err)
	}
	if rawEncoding != "zlib" || payloadStorage != "detached" {
		t.Fatalf("large storage = raw %q, payload %q", rawEncoding, payloadStorage)
	}
}

func TestCoordinatorSQLiteRetainsSparseEvidenceHonestly(t *testing.T) {
	tests := []struct {
		name                                     string
		data                                     []byte
		wantRecords, wantEvents, wantDiagnostics int
		wantSequence                             int64
	}{
		{"metadata", []byte("{\"timestamp\":\"2026-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"only-meta\"}}\n"), 1, 0, 0, 0},
		{"malformed", []byte("{\"timestamp\":\"bad\",\"type\":\"event_msg\",BROKEN}\n"), 1, 0, 1, 0},
		{"unknown", []byte("{\"timestamp\":\"2026-01-01T00:00:00Z\",\"type\":\"future\",\"payload\":{}}\n"), 1, 1, 0, 0},
		{"partial", []byte("{\"timestamp\":\"2026-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"partial"), 0, 0, 0, importer.NoRecordSequence},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "agentsession.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			store, _ := sqlite.NewImportStore(db)
			coordinator, _ := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
			result, err := coordinator.Import(ctx, bytesSource("sparse-"+tt.name, tt.data))
			if err != nil {
				t.Fatal(err)
			}
			var records int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM raw_records WHERE session_id = ?`, result.SessionID).Scan(&records); err != nil {
				t.Fatal(err)
			}
			events, _ := store.Events(ctx, result.SessionID)
			diagnostics, _ := store.RecordDiagnostics(ctx, result.SessionID)
			if records != tt.wantRecords || len(events) != tt.wantEvents || len(diagnostics) != tt.wantDiagnostics || result.Checkpoint.RecordSequence != tt.wantSequence {
				t.Fatalf("evidence = records %d, events %d, diagnostics %d, sequence %d", records, len(events), len(diagnostics), result.Checkpoint.RecordSequence)
			}
		})
	}
}
