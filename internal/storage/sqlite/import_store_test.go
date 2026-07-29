package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	storagecontract "github.com/pooya79/AgentSession/internal/storage"
)

func TestImportStoreRoundTripAndStableSourceOrder(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	batch := testImportBatch()
	payloads := []model.NormalizedData{
		model.MessageData{Role: model.MessageRoleUser, Text: "hello"},
		model.ToolCallData{CallID: "call", ToolName: "read", Input: "input"},
		model.ToolResultData{CallID: "call", ToolName: "read", Output: "output", IsError: boolPointer(false)},
		model.CommandData{Command: "go test", WorkingDirectory: "/repo", ExitCode: intPointer(0), Output: "ok"},
		model.FileReadData{Path: "main.go", StartLine: int64Pointer(1), EndLine: int64Pointer(2)},
		model.FileMutationData{Path: "main.go", Operation: model.FileMutationRename, PreviousPath: "old.go"},
		model.PatchData{Text: "patch", Paths: []string{"main.go"}},
		model.UsageData{InputTokens: int64Pointer(10), OutputTokens: int64Pointer(5)},
		model.ErrorData{Code: "failed", Message: "failure"},
		model.SummaryData{Category: model.SummaryCategorySummary, Text: "summary"},
		model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"},
	}
	batch.Events = make([]model.Event, 0, len(payloads))
	batch.RawRecords = make([]model.RawRecord, 0, len(payloads))
	for i, payload := range payloads {
		sequence := int64(i)
		timestamp := time.Date(2026, 7, 15, 12-i, 0, 0, 0, time.UTC)
		batch.Events = append(batch.Events, model.Event{
			ID:             model.EventID("event-" + string(rune('a'+i))),
			SessionID:      batch.Session.ID,
			Sequence:       sequence,
			Timestamp:      &timestamp,
			Kind:           payloadKind(payload),
			Summary:        "event summary",
			SearchableText: "search text",
			Data:           payload,
			RawRecord: model.RawRecordRef{
				ID:             model.RawRecordID("raw-" + string(rune('a'+i))),
				SourceID:       batch.Session.Import.SourceID,
				RecordSequence: int64Pointer(sequence),
				ContentHash:    "content-hash",
			},
		})
		batch.RawRecords = append(batch.RawRecords, model.RawRecord{
			Ref:     batch.Events[i].RawRecord,
			Content: []byte("original raw record " + string(rune('a'+i))),
		})
	}
	batch.Checkpoint.RecordSequence = int64(len(payloads) - 1)
	batch.Session.Diagnostics = []model.Diagnostic{{
		Code:     "session.partial",
		Severity: model.SeverityWarning,
		Message:  "session metadata is partial",
	}}
	batch.RecordDiagnostics = []model.RecordDiagnostic{{
		RawRecordID: batch.Events[0].RawRecord.ID,
		Ordinal:     0,
		Diagnostic: model.Diagnostic{
			Code:         "record.partial",
			Severity:     model.SeverityWarning,
			Message:      "partial evidence",
			EventIDs:     []model.EventID{batch.Events[0].ID},
			RawRecordIDs: []model.RawRecordID{batch.Events[0].RawRecord.ID},
		},
	}}

	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("CommitBatch() error = %v", err)
	}

	gotSession, found, err := store.Session(context.Background(), batch.Session.ID)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if !found || !reflect.DeepEqual(gotSession, batch.Session) {
		t.Fatalf("Session() = (%#v, %v), want (%#v, true)", gotSession, found, batch.Session)
	}
	gotEvents, err := store.Events(context.Background(), batch.Session.ID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if !reflect.DeepEqual(gotEvents, batch.Events) {
		t.Fatalf("Events() = %#v, want %#v", gotEvents, batch.Events)
	}
	gotDiagnostics, err := store.RecordDiagnostics(context.Background(), batch.Session.ID)
	if err != nil || !reflect.DeepEqual(gotDiagnostics, batch.RecordDiagnostics) {
		t.Fatalf("RecordDiagnostics() = (%#v, %v), want %#v", gotDiagnostics, err, batch.RecordDiagnostics)
	}
	for i, event := range gotEvents {
		if event.Sequence != int64(i) {
			t.Errorf("Events()[%d].Sequence = %d, want %d", i, event.Sequence, i)
		}
		gotRawRecord, found, err := store.RawRecord(context.Background(), event.RawRecord.ID)
		if err != nil || !found || !reflect.DeepEqual(gotRawRecord, batch.RawRecords[i]) {
			t.Errorf("RawRecord(%q) = (%#v, %v, %v), want retained record", event.RawRecord.ID, gotRawRecord, found, err)
		}
	}
	checkpoint, found, err := store.Checkpoint(context.Background(), batch.Checkpoint.SourceID)
	if err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
	if !found || !importer.CheckpointEqual(checkpoint, batch.Checkpoint) {
		t.Fatalf("Checkpoint() = (%#v, %v), want (%#v, true)", checkpoint, found, batch.Checkpoint)
	}
	state, found, err := store.SourceState(context.Background(), batch.Checkpoint.SourceID)
	if err != nil || !found {
		t.Fatalf("SourceState() = (%#v, %v, %v), want durable state", state, found, err)
	}
	if state.SessionID != batch.Session.ID || state.Import != batch.Session.Import || !reflect.DeepEqual(state.Session, batch.Session) || !importer.CheckpointEqual(state.Checkpoint, batch.Checkpoint) ||
		state.LastEventSequence == nil || *state.LastEventSequence != batch.Events[len(batch.Events)-1].Sequence {
		t.Fatalf("SourceState() = %#v, want session metadata, checkpoint, and last event sequence", state)
	}
}

func TestImportStoreCommitsZeroRecordCheckpoint(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	batch := testImportBatch()
	batch.RawRecords = nil
	batch.Events = nil
	batch.RecordDiagnostics = nil
	batch.Checkpoint = importer.ImportCheckpoint{
		SourceID: batch.Session.Import.SourceID, RecordSequence: importer.NoRecordSequence,
		StateVersion: "fixture-v1", Cursor: []byte("start"), Fingerprint: []byte(model.HashRecord(nil)),
	}
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("CommitBatch() zero-record error = %v", err)
	}
	state, found, err := store.SourceState(context.Background(), batch.Checkpoint.SourceID)
	if err != nil || !found || !importer.CheckpointEqual(state.Checkpoint, batch.Checkpoint) || state.LastEventSequence != nil {
		t.Fatalf("SourceState() = (%#v, %v, %v), want zero-record state", state, found, err)
	}
}

func TestImportStoreCompressesAndRestoresLargeRawRecord(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	batch := testImportBatch()
	content := []byte(strings.Repeat("large raw evidence ", storagecontract.InlinePayloadThresholdBytes/10))
	batch.RawRecords[0].Content = content
	batch.RawRecords[0].Ref.ByteRange = nil
	batch.Events[0].RawRecord = batch.RawRecords[0].Ref

	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("CommitBatch() error = %v", err)
	}
	var encoding string
	if err := store.db.QueryRow(`SELECT storage_encoding FROM raw_records WHERE id = ?`, batch.RawRecords[0].Ref.ID).Scan(&encoding); err != nil {
		t.Fatalf("query raw-record encoding: %v", err)
	}
	if encoding != storagecontract.EncodingZlib {
		t.Fatalf("raw-record encoding = %q, want %q", encoding, storagecontract.EncodingZlib)
	}
	got, found, err := store.RawRecord(context.Background(), batch.RawRecords[0].Ref.ID)
	if err != nil || !found || !reflect.DeepEqual(got, batch.RawRecords[0]) {
		t.Fatalf("RawRecord() = (%#v, %v, %v), want original large content", got, found, err)
	}
}

func TestImportStoreRawRecordThresholdBoundaries(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		size         int
		wantEncoding string
	}{
		{name: "at threshold", size: storagecontract.InlinePayloadThresholdBytes, wantEncoding: storagecontract.EncodingIdentity},
		{name: "above threshold", size: storagecontract.InlinePayloadThresholdBytes + 1, wantEncoding: storagecontract.EncodingZlib},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openImportStore(t)
			batch := testImportBatch()
			batch.RawRecords[0].Content = []byte(strings.Repeat("r", tt.size))
			batch.RawRecords[0].Ref.ByteRange = nil
			batch.Events[0].RawRecord = batch.RawRecords[0].Ref

			if err := store.CommitBatch(context.Background(), batch); err != nil {
				t.Fatalf("CommitBatch() error = %v", err)
			}
			var encoding string
			var policyVersion int
			if err := store.db.QueryRow(`
				SELECT storage_encoding, retention_policy_version FROM raw_records WHERE id = ?
			`, batch.RawRecords[0].Ref.ID).Scan(&encoding, &policyVersion); err != nil {
				t.Fatalf("query retained raw-record storage: %v", err)
			}
			if encoding != tt.wantEncoding || policyVersion != storagecontract.FullRetentionPolicyVersion {
				t.Fatalf("raw storage = (%q, %d), want (%q, %d)", encoding, policyVersion, tt.wantEncoding, storagecontract.FullRetentionPolicyVersion)
			}
			assertRawRecord(t, store, batch.RawRecords[0])
		})
	}
}

func TestImportStoreNormalizedPayloadThresholdBoundaries(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name        string
		encodedSize int
		wantStorage string
		wantPayload int
	}{
		{name: "at threshold", encodedSize: storagecontract.InlinePayloadThresholdBytes, wantStorage: payloadInline, wantPayload: 0},
		{name: "above threshold", encodedSize: storagecontract.InlinePayloadThresholdBytes + 1, wantStorage: payloadDetached, wantPayload: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openImportStore(t)
			batch := testImportBatch()
			batch.Events[0].Kind = model.EventKindSummary
			batch.Events[0].Data = summaryPayloadWithEncodedSize(t, tt.encodedSize)

			if err := store.CommitBatch(context.Background(), batch); err != nil {
				t.Fatalf("CommitBatch() error = %v", err)
			}
			var storage string
			var policyVersion int
			if err := store.db.QueryRow(`
				SELECT payload_storage, retention_policy_version FROM events WHERE id = ?
			`, batch.Events[0].ID).Scan(&storage, &policyVersion); err != nil {
				t.Fatalf("query event payload storage: %v", err)
			}
			if storage != tt.wantStorage || policyVersion != storagecontract.FullRetentionPolicyVersion {
				t.Fatalf("event storage = (%q, %d), want (%q, %d)", storage, policyVersion, tt.wantStorage, storagecontract.FullRetentionPolicyVersion)
			}
			var payloadCount int
			if err := store.db.QueryRow(`SELECT COUNT(*) FROM event_payloads WHERE event_id = ?`, batch.Events[0].ID).Scan(&payloadCount); err != nil {
				t.Fatalf("query event payload count: %v", err)
			}
			if payloadCount != tt.wantPayload {
				t.Fatalf("event payload count = %d, want %d", payloadCount, tt.wantPayload)
			}
			got, found, err := store.Event(context.Background(), batch.Events[0].ID)
			if err != nil || !found || !reflect.DeepEqual(got, batch.Events[0]) {
				t.Fatalf("Event() = (%#v, %v, %v), want full event", got, found, err)
			}
		})
	}
}

func TestImportStoreRestoresLargeNormalizedEvidenceWithoutTruncation(t *testing.T) {
	t.Parallel()

	large := strings.Repeat("normalized evidence ", storagecontract.InlinePayloadThresholdBytes/8)
	for _, tt := range []struct {
		name string
		data model.NormalizedData
	}{
		{name: "command output", data: model.CommandData{Command: "test", Output: large}},
		{name: "tool output", data: model.ToolResultData{ToolName: "test", Output: large}},
		{name: "patch", data: model.PatchData{Text: large, Paths: []string{"main.go"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := openImportStore(t)
			batch := testImportBatch()
			batch.Events[0].Kind = payloadKind(tt.data)
			batch.Events[0].Data = tt.data
			if err := store.CommitBatch(context.Background(), batch); err != nil {
				t.Fatalf("CommitBatch() error = %v", err)
			}
			var encoding string
			if err := store.db.QueryRow(`SELECT storage_encoding FROM event_payloads WHERE event_id = ?`, batch.Events[0].ID).Scan(&encoding); err != nil {
				t.Fatalf("query detached payload encoding: %v", err)
			}
			if encoding != storagecontract.EncodingZlib {
				t.Fatalf("detached payload encoding = %q, want zlib", encoding)
			}
			got, found, err := store.Event(context.Background(), batch.Events[0].ID)
			if err != nil || !found || !reflect.DeepEqual(got.Data, tt.data) {
				t.Fatalf("Event() normalized data = (%#v, %v, %v), want untruncated payload", got.Data, found, err)
			}
		})
	}
}

func TestImportStoreTimelineDoesNotLoadDetachedPayloads(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	batch := testImportBatch()
	batch.Events[0].Kind = model.EventKindSummary
	batch.Events[0].Data = summaryPayloadWithEncodedSize(t, storagecontract.InlinePayloadThresholdBytes+1)
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("CommitBatch() error = %v", err)
	}
	if _, err := store.db.Exec(`UPDATE event_payloads SET content = ? WHERE event_id = ?`, []byte("corrupt"), batch.Events[0].ID); err != nil {
		t.Fatalf("corrupt detached payload: %v", err)
	}

	summaries, err := store.EventSummaries(context.Background(), batch.Session.ID)
	if err != nil {
		t.Fatalf("EventSummaries() error = %v; timeline must not fetch detached content", err)
	}
	want := []model.EventSummary{{
		ID: batch.Events[0].ID, SessionID: batch.Session.ID, Sequence: batch.Events[0].Sequence,
		Timestamp: batch.Events[0].Timestamp, Kind: batch.Events[0].Kind, Summary: batch.Events[0].Summary,
	}}
	if !reflect.DeepEqual(summaries, want) {
		t.Fatalf("EventSummaries() = %#v, want %#v", summaries, want)
	}
	if _, _, err := store.Event(context.Background(), batch.Events[0].ID); err == nil {
		t.Fatal("Event() error = nil after payload corruption, want detail decode error")
	}
	page, more, err := store.EventSummaryPage(context.Background(), batch.Session.ID, nil, 10)
	if err != nil || more || !reflect.DeepEqual(page, want) {
		t.Fatalf("EventSummaryPage() = (%#v, %v, %v), want payload-free summary", page, more, err)
	}
	envelope, found, err := store.EventEnvelope(context.Background(), batch.Session.ID, batch.Events[0].ID)
	if err != nil || !found || envelope.ID != batch.Events[0].ID {
		t.Fatalf("EventEnvelope() = (%#v, %v, %v), want payload-free detail", envelope, found, err)
	}
	if _, found, err := store.EventPayload(context.Background(), batch.Session.ID, batch.Events[0].ID); err == nil || !found {
		t.Fatalf("EventPayload() = (found=%v, err=%v), want explicit corrupt-payload error", found, err)
	}
	diagnostics, err := store.Diagnostics(context.Background(), batch.Session.ID, nil, 1)
	if err != nil || diagnostics.Total != 2 || len(diagnostics.Diagnostics) != 1 {
		t.Fatalf("Diagnostics(session) = (%#v, %v), want bounded exact total", diagnostics, err)
	}
	diagnostics, err = store.Diagnostics(context.Background(), batch.Session.ID, &batch.Events[0].ID, 10)
	if err != nil || diagnostics.Total != 1 || diagnostics.Diagnostics[0].Code != "record.partial" {
		t.Fatalf("Diagnostics(event) = (%#v, %v), want relevant record diagnostic", diagnostics, err)
	}
}

func TestImportStoreDoesNotDeriveSearchableTextFromRawContent(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	batch := testImportBatch()
	const rawOnly = "RAW-SECRET-MUST-NOT-BE-INDEXED"
	batch.RawRecords[0].Content = []byte(rawOnly)
	batch.Events[0].SearchableText = "normalized only"
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("CommitBatch() error = %v", err)
	}
	var searchable string
	if err := store.db.QueryRow(`SELECT searchable_text FROM events WHERE id = ?`, batch.Events[0].ID).Scan(&searchable); err != nil {
		t.Fatalf("query searchable text: %v", err)
	}
	if searchable != batch.Events[0].SearchableText || strings.Contains(searchable, rawOnly) {
		t.Fatalf("stored searchable text = %q, want normalized text only", searchable)
	}
}

func TestImportStoreDeleteSessionRemovesOwnedDataWithoutTouchingSource(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	batch := testImportBatch()
	batch.Events[0].Kind = model.EventKindSummary
	batch.Events[0].Data = summaryPayloadWithEncodedSize(t, storagecontract.InlinePayloadThresholdBytes+1)
	sourcePath := filepath.Join(t.TempDir(), "source.jsonl")
	sourceContent := []byte("read-only source evidence\n")
	if err := os.WriteFile(sourcePath, sourceContent, 0o600); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("CommitBatch() error = %v", err)
	}
	if _, err := store.db.Exec(`
		CREATE TABLE test_projection (
			session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
			value TEXT NOT NULL
		) STRICT
	`); err != nil {
		t.Fatalf("create projection fixture: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO test_projection (session_id, value) VALUES (?, 'projection')`, batch.Session.ID); err != nil {
		t.Fatalf("insert projection fixture: %v", err)
	}

	deleted, err := store.DeleteSession(context.Background(), batch.Session.ID)
	if err != nil || !deleted {
		t.Fatalf("DeleteSession() = (%v, %v), want (true, nil)", deleted, err)
	}
	for _, table := range []string{"sessions", "events", "event_payloads", "raw_records", "session_diagnostics", "record_diagnostics", "import_checkpoints", "reconciliation_runs", "reconciliation_batches", "test_projection"} {
		var count int
		if err := store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatalf("count %s after deletion: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s row count after deletion = %d, want 0", table, count)
		}
	}
	gotSource, err := os.ReadFile(sourcePath)
	if err != nil || !reflect.DeepEqual(gotSource, sourceContent) {
		t.Fatalf("source after deletion = (%q, %v), want unchanged", gotSource, err)
	}
	if deleted, err := store.DeleteSession(context.Background(), batch.Session.ID); err != nil || deleted {
		t.Fatalf("second DeleteSession() = (%v, %v), want (false, nil)", deleted, err)
	}
}

func TestImportStoreConflictingRawRecordRollsBackWholeBatch(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatalf("initial CommitBatch() error = %v", err)
	}
	changed := original
	changed.Session.Title = "must roll back"
	changed.RawRecords = append([]model.RawRecord(nil), original.RawRecords...)
	changed.RawRecords[0].Content = []byte("different original evidence")
	err := store.CommitBatch(context.Background(), changed)
	if !errors.Is(err, importer.ErrRawRecordConflict) {
		t.Fatalf("CommitBatch() error = %v, want ErrRawRecordConflict", err)
	}
	assertOriginalState(t, store, original)
	assertRawRecord(t, store, original.RawRecords[0])
}

func TestImportStoreConflictingRecordDiagnosticRollsBackWholeBatch(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatalf("initial CommitBatch() error = %v", err)
	}

	changed := original
	changed.Session.Title = "must roll back"
	changed.RecordDiagnostics = append([]model.RecordDiagnostic(nil), original.RecordDiagnostics...)
	changed.RecordDiagnostics[0].Diagnostic.Message = "different diagnostic evidence"
	err := store.CommitBatch(context.Background(), changed)
	if !errors.Is(err, importer.ErrDiagnosticConflict) {
		t.Fatalf("CommitBatch() error = %v, want ErrDiagnosticConflict", err)
	}
	assertOriginalState(t, store, original)
	diagnostics, readErr := store.RecordDiagnostics(context.Background(), original.Session.ID)
	if readErr != nil || !reflect.DeepEqual(diagnostics, original.RecordDiagnostics) {
		t.Fatalf("RecordDiagnostics() after rollback = (%#v, %v), want original", diagnostics, readErr)
	}
}
