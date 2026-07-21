package sqlite

import (
	"context"
	"encoding/json"
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
		model.SummaryData{Text: "summary"},
		model.UnknownData{OriginalKind: "future"},
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
	content := []byte(strings.Repeat("large raw evidence ", rawRecordCompressionThreshold/10))
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
	if encoding != rawEncodingZlib {
		t.Fatalf("raw-record encoding = %q, want %q", encoding, rawEncodingZlib)
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
		{name: "at threshold", size: storagecontract.InlinePayloadThresholdBytes, wantEncoding: rawEncodingIdentity},
		{name: "above threshold", size: storagecontract.InlinePayloadThresholdBytes + 1, wantEncoding: rawEncodingZlib},
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

func TestImportStoreReconciliationReplacesStaleEvidenceAndRegressedCheckpoint(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	secondSequence := int64(1)
	secondRef := model.RawRecordRef{
		ID:             "raw-2",
		SourceID:       original.Checkpoint.SourceID,
		RecordSequence: &secondSequence,
		ByteRange:      &model.ByteRange{Offset: 10, Length: 10},
		ContentHash:    "content-hash-2",
	}
	original.RawRecords = append(original.RawRecords, model.RawRecord{Ref: secondRef, Content: []byte("stale raw record")})
	original.Events = append(original.Events, model.Event{
		ID:             "event-2",
		SessionID:      original.Session.ID,
		Sequence:       1,
		Kind:           model.EventKindUnknown,
		Summary:        "stale event",
		SearchableText: "stale",
		Data:           model.UnknownData{OriginalKind: "stale"},
		RawRecord:      secondRef,
	})
	original.RecordDiagnostics = append(original.RecordDiagnostics, model.RecordDiagnostic{
		RawRecordID: secondRef.ID,
		Ordinal:     0,
		Diagnostic: model.Diagnostic{
			Code: "record.stale", Severity: model.SeverityWarning, Message: "stale diagnostic",
			RawRecordIDs: []model.RawRecordID{secondRef.ID},
		},
	})
	original.Checkpoint.RecordSequence = 1
	original.Checkpoint.Cursor = []byte("old-cursor")
	original.Checkpoint.Fingerprint = []byte("old-fingerprint")
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatalf("initial CommitBatch() error = %v", err)
	}
	if _, err := store.db.Exec(`
		CREATE TABLE reconcile_projection (
			session_id TEXT PRIMARY KEY REFERENCES sessions(id) ON DELETE CASCADE,
			value TEXT NOT NULL
		) STRICT
	`); err != nil {
		t.Fatalf("create projection fixture: %v", err)
	}
	if _, err := store.db.Exec(`INSERT INTO reconcile_projection (session_id, value) VALUES (?, 'stale')`, original.Session.ID); err != nil {
		t.Fatalf("insert projection fixture: %v", err)
	}

	replacement := testImportBatch()
	replacement.Session.Title = "Re-imported session"
	replacement.RawRecords[0].Content = []byte("new raw")
	replacement.RawRecords[0].Ref.ContentHash = "replacement-content-hash"
	replacement.RawRecords[0].Ref.ByteRange = &model.ByteRange{Offset: 0, Length: 5}
	replacement.Events[0].RawRecord = replacement.RawRecords[0].Ref
	replacement.Events[0].Summary = "re-normalized message"
	replacement.Events[0].SearchableText = "new"
	replacement.Events[0].Data = model.MessageData{Role: model.MessageRoleUser, Text: "new"}
	replacement.Checkpoint.Cursor = []byte("replacement-cursor")
	replacement.Checkpoint.Fingerprint = []byte("replacement-fingerprint")

	if err := reconcileBatches(context.Background(), store, original.Checkpoint, replacement); err != nil {
		t.Fatalf("reconcileBatches() error = %v", err)
	}
	gotSession, found, err := store.Session(context.Background(), replacement.Session.ID)
	if err != nil || !found || !reflect.DeepEqual(gotSession, replacement.Session) {
		t.Fatalf("Session() = (%#v, %v, %v), want replacement", gotSession, found, err)
	}
	gotEvents, err := store.Events(context.Background(), replacement.Session.ID)
	if err != nil || !reflect.DeepEqual(gotEvents, replacement.Events) {
		t.Fatalf("Events() = (%#v, %v), want replacement only", gotEvents, err)
	}
	gotDiagnostics, err := store.RecordDiagnostics(context.Background(), replacement.Session.ID)
	if err != nil || !reflect.DeepEqual(gotDiagnostics, replacement.RecordDiagnostics) {
		t.Fatalf("RecordDiagnostics() = (%#v, %v), want replacement only", gotDiagnostics, err)
	}
	assertRawRecord(t, store, replacement.RawRecords[0])
	if _, found, err := store.RawRecord(context.Background(), secondRef.ID); err != nil || found {
		t.Fatalf("stale RawRecord() = (found %v, error %v), want removed", found, err)
	}
	var projectionCount int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM reconcile_projection`).Scan(&projectionCount); err != nil || projectionCount != 0 {
		t.Fatalf("stale projection count = %d, error %v; want 0", projectionCount, err)
	}
	checkpoint, found, err := store.Checkpoint(context.Background(), replacement.Checkpoint.SourceID)
	if err != nil || !found || !importer.CheckpointEqual(checkpoint, replacement.Checkpoint) {
		t.Fatalf("Checkpoint() = (%#v, %v, %v), want regressed replacement", checkpoint, found, err)
	}
}

func TestImportStoreStagingIsInvisibleAndRepeatedReconciliationIsIdempotent(t *testing.T) {
	t.Parallel()
	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	replacement := testImportBatch()
	replacement.Session.Title = "replacement"
	replacement.Events[0].Summary = "replacement"
	replacement.Checkpoint.Cursor = []byte("replacement-cursor")
	replacement.Checkpoint.Fingerprint = []byte("replacement-fingerprint")

	reconciliation, err := store.BeginReconciliation(context.Background(), original.Checkpoint.SourceID, original.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconciliation.StageBatch(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	assertOriginalState(t, store, original)
	if err := reconciliation.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := reconcileBatches(context.Background(), store, replacement.Checkpoint, replacement); err != nil {
		t.Fatalf("identical reconciliation retry error = %v", err)
	}
	events, err := store.Events(context.Background(), replacement.Session.ID)
	if err != nil || len(events) != 1 || events[0].Summary != "replacement" {
		t.Fatalf("events after retry = (%#v, %v), want one replacement", events, err)
	}
}

func TestImportStoreNewReconciliationClearsAbandonedStaging(t *testing.T) {
	t.Parallel()
	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	first, err := store.BeginReconciliation(context.Background(), original.Checkpoint.SourceID, original.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.StageBatch(context.Background(), testImportBatch()); err != nil {
		t.Fatal(err)
	}
	second, err := store.BeginReconciliation(context.Background(), original.Checkpoint.SourceID, original.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Finalize(context.Background()); err == nil {
		t.Fatal("abandoned staging run remained promotable")
	}
	replacement := testImportBatch()
	replacement.Checkpoint.Cursor = []byte("replacement-cursor")
	replacement.Checkpoint.Fingerprint = []byte("replacement-fingerprint")
	if err := second.StageBatch(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	if err := second.Finalize(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestImportStoreReconcileCancellationRestoresPreviousSource(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatalf("initial CommitBatch() error = %v", err)
	}

	replacement := testImportBatch()
	replacement.Session.Title = "must roll back"
	replacement.RawRecords[0].Content = []byte("replacement")
	replacement.RawRecords[0].Ref.ContentHash = "replacement-hash"
	replacement.Events[0].RawRecord = replacement.RawRecords[0].Ref
	replacement.Events[0].Summary = "replacement"
	replacement.Checkpoint.Cursor = []byte("replacement-cursor")
	replacement.Checkpoint.Fingerprint = []byte("replacement-fingerprint")
	ctx, cancel := context.WithCancel(context.Background())
	store.beforeCommit = cancel

	err := reconcileBatches(ctx, store, original.Checkpoint, replacement)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("reconcileBatches() error = %v, want context.Canceled", err)
	}
	assertOriginalState(t, store, original)
	assertRawRecord(t, store, original.RawRecords[0])
}

func TestImportStoreRetryPreventsDuplicatesAndAdvancesCheckpoint(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	batch := testImportBatch()
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("first CommitBatch() error = %v", err)
	}
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("identical retry CommitBatch() error = %v", err)
	}

	advanced := batch
	advanced.Checkpoint.RecordSequence++
	advanced.Checkpoint.Cursor = []byte("advanced-cursor")
	advanced.Checkpoint.Fingerprint = []byte("advanced-fingerprint")
	if err := store.CommitBatch(context.Background(), advanced); err != nil {
		t.Fatalf("forward retry CommitBatch() error = %v", err)
	}

	events, err := store.Events(context.Background(), batch.Session.ID)
	if err != nil {
		t.Fatalf("Events() error = %v", err)
	}
	if len(events) != len(batch.Events) {
		t.Fatalf("Events() length = %d, want %d", len(events), len(batch.Events))
	}
	diagnostics, err := store.RecordDiagnostics(context.Background(), batch.Session.ID)
	if err != nil || len(diagnostics) != len(batch.RecordDiagnostics) {
		t.Fatalf("RecordDiagnostics() = (%#v, %v), want one idempotent copy", diagnostics, err)
	}
	checkpoint, found, err := store.Checkpoint(context.Background(), batch.Checkpoint.SourceID)
	if err != nil || !found || !importer.CheckpointEqual(checkpoint, advanced.Checkpoint) {
		t.Fatalf("Checkpoint() = (%#v, %v, %v), want advanced checkpoint", checkpoint, found, err)
	}
}

func TestImportStorePersistsRecordDiagnosticsAcrossIncrementalBatches(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	first := testImportBatch()
	if err := store.CommitBatch(context.Background(), first); err != nil {
		t.Fatalf("first CommitBatch() error = %v", err)
	}

	second := testImportBatch()
	sequence := int64(1)
	second.RawRecords[0].Ref.ID = "raw-2"
	second.RawRecords[0].Ref.RecordSequence = &sequence
	second.RawRecords[0].Ref.ByteRange = &model.ByteRange{Offset: 10, Length: 10}
	second.Events[0].ID = "event-2"
	second.Events[0].Sequence = sequence
	second.Events[0].RawRecord = second.RawRecords[0].Ref
	second.RecordDiagnostics[0].RawRecordID = "raw-2"
	second.RecordDiagnostics[0].Diagnostic.EventIDs = []model.EventID{"event-2"}
	second.RecordDiagnostics[0].Diagnostic.RawRecordIDs = []model.RawRecordID{"raw-2"}
	second.Checkpoint.RecordSequence = sequence
	second.Checkpoint.Cursor = []byte("second-cursor")
	second.Checkpoint.Fingerprint = []byte("second-fingerprint")
	if err := store.CommitBatch(context.Background(), second); err != nil {
		t.Fatalf("second CommitBatch() error = %v", err)
	}

	want := append(append([]model.RecordDiagnostic(nil), first.RecordDiagnostics...), second.RecordDiagnostics...)
	got, err := store.RecordDiagnostics(context.Background(), first.Session.ID)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("RecordDiagnostics() = (%#v, %v), want incremental diagnostics %#v", got, err, want)
	}
}

func TestImportStoreConflictingDuplicateRollsBackWholeBatch(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	original.Events[0].Sequence = 3
	original.Events[0].RawRecord.RecordSequence = int64Pointer(3)
	original.RawRecords[0].Ref.RecordSequence = int64Pointer(3)
	original.Checkpoint.RecordSequence = 3
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatalf("initial CommitBatch() error = %v", err)
	}

	changed := original
	changed.Session.Title = "must roll back"
	changed.Session.Diagnostics = []model.Diagnostic{{Code: "new", Severity: model.SeverityError, Message: "must roll back"}}
	newEvent := original.Events[0]
	newEvent.ID = "event-new"
	newEvent.Sequence = 2
	newEvent.RawRecord.ID = "raw-new"
	newEvent.RawRecord.RecordSequence = int64Pointer(2)
	newRawRecord := original.RawRecords[0]
	newRawRecord.Ref = newEvent.RawRecord
	newRawRecord.Content = []byte("new raw record")
	conflict := original.Events[0]
	conflict.Summary = "different canonical content"
	changed.Events = []model.Event{newEvent, conflict}
	changed.RawRecords = []model.RawRecord{newRawRecord, original.RawRecords[0]}
	changed.Checkpoint.RecordSequence++
	changed.Checkpoint.Cursor = []byte("new-cursor")
	changed.Checkpoint.Fingerprint = []byte("new-fingerprint")

	err := store.CommitBatch(context.Background(), changed)
	if !errors.Is(err, importer.ErrEventConflict) {
		t.Fatalf("CommitBatch() error = %v, want ErrEventConflict", err)
	}
	if !strings.Contains(err.Error(), `source "source-1"`) {
		t.Fatalf("CommitBatch() error = %q, want source context", err)
	}
	assertOriginalState(t, store, original)
}

func TestImportStoreCheckpointRegressionRollsBackSessionSnapshot(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatalf("initial CommitBatch() error = %v", err)
	}

	regressed := original
	regressed.Session.Title = "must roll back"
	regressed.Session.Diagnostics = nil
	regressed.RawRecords = nil
	regressed.Events = nil
	regressed.RecordDiagnostics = nil
	regressed.Checkpoint.RecordSequence = importer.NoRecordSequence
	err := store.CommitBatch(context.Background(), regressed)
	if !errors.Is(err, importer.ErrCheckpointRegression) {
		t.Fatalf("CommitBatch() error = %v, want ErrCheckpointRegression", err)
	}
	assertOriginalState(t, store, original)
}

func TestImportStoreRejectsDifferentFingerprintAtSameSequence(t *testing.T) {
	t.Parallel()
	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	changed := original
	changed.RawRecords = nil
	changed.Events = nil
	changed.RecordDiagnostics = nil
	changed.Checkpoint.Fingerprint = []byte("different-fingerprint")
	if err := store.CommitBatch(context.Background(), changed); !errors.Is(err, importer.ErrCheckpointRegression) {
		t.Fatalf("CommitBatch() error = %v, want ErrCheckpointRegression", err)
	}
	assertOriginalState(t, store, original)
}

func TestImportStoreReconciliationCompareAndSwapProtectsNewerImport(t *testing.T) {
	t.Parallel()
	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatal(err)
	}
	reconciliation, err := store.BeginReconciliation(context.Background(), original.Checkpoint.SourceID, original.Checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	replacement := testImportBatch()
	replacement.Checkpoint.Fingerprint = []byte("replacement-fingerprint")
	if err := reconciliation.StageBatch(context.Background(), replacement); err != nil {
		t.Fatal(err)
	}
	advanced := original
	advanced.RawRecords = nil
	advanced.Events = nil
	advanced.RecordDiagnostics = nil
	advanced.Checkpoint.RecordSequence++
	advanced.Checkpoint.Cursor = []byte("advanced-cursor")
	advanced.Checkpoint.Fingerprint = []byte("advanced-fingerprint")
	if err := store.CommitBatch(context.Background(), advanced); err != nil {
		t.Fatal(err)
	}
	if err := reconciliation.Finalize(context.Background()); !errors.Is(err, importer.ErrCheckpointConflict) {
		t.Fatalf("Finalize() error = %v, want ErrCheckpointConflict", err)
	}
	checkpoint, found, err := store.Checkpoint(context.Background(), original.Checkpoint.SourceID)
	if err != nil || !found || !importer.CheckpointEqual(checkpoint, advanced.Checkpoint) {
		t.Fatalf("live checkpoint after conflict = (%#v, %v, %v), want advanced", checkpoint, found, err)
	}
}

func TestImportStoreCancellationBeforeCommitRollsBack(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	store.beforeCommit = cancel
	batch := testImportBatch()

	err := store.CommitBatch(ctx, batch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("CommitBatch() error = %v, want context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "check cancellation before commit") || !strings.Contains(err.Error(), `source "source-1"`) {
		t.Fatalf("CommitBatch() error = %q, want operation and source context", err)
	}
	if _, found, err := store.Session(context.Background(), batch.Session.ID); err != nil || found {
		t.Fatalf("Session() after cancellation = (found %v, error %v), want absent", found, err)
	}
	events, err := store.Events(context.Background(), batch.Session.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("Events() after cancellation = (%#v, %v), want empty", events, err)
	}
	if _, found, err := store.Checkpoint(context.Background(), batch.Checkpoint.SourceID); err != nil || found {
		t.Fatalf("Checkpoint() after cancellation = (found %v, error %v), want absent", found, err)
	}
}

func TestImportStoreDuplicateSequenceRollsBack(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	if err := store.CommitBatch(context.Background(), original); err != nil {
		t.Fatalf("initial CommitBatch() error = %v", err)
	}

	duplicateSequence := original
	duplicateSequence.Events = append([]model.Event(nil), original.Events...)
	duplicateSequence.Events[0].ID = "another-event"
	duplicateSequence.Events[0].RawRecord.ID = "another-raw"
	duplicateSequence.Checkpoint.RecordSequence++
	duplicateSequence.Checkpoint.Cursor = []byte("next-cursor")
	duplicateSequence.Checkpoint.Fingerprint = []byte("next-fingerprint")
	err := store.CommitBatch(context.Background(), duplicateSequence)
	if err == nil {
		t.Fatal("CommitBatch() error = nil, want unique source sequence failure")
	}
	assertOriginalState(t, store, original)
}

func openImportStore(t *testing.T) *ImportStore {
	t.Helper()
	db, err := Open(context.Background(), filepath.Join(t.TempDir(), "agentsession.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	store, err := NewImportStore(db)
	if err != nil {
		t.Fatalf("NewImportStore() error = %v", err)
	}
	return store
}

func reconcileBatches(ctx context.Context, store *ImportStore, expected importer.ImportCheckpoint, batches ...importer.ImportBatch) error {
	reconciliation, err := store.BeginReconciliation(ctx, expected.SourceID, expected)
	if err != nil {
		return err
	}
	for _, batch := range batches {
		if err := reconciliation.StageBatch(ctx, batch); err != nil {
			_ = reconciliation.Abort(context.WithoutCancel(ctx))
			return err
		}
	}
	if err := reconciliation.Finalize(ctx); err != nil {
		_ = reconciliation.Abort(context.WithoutCancel(ctx))
		return err
	}
	return nil
}

func testImportBatch() importer.ImportBatch {
	startedAt := time.Date(2026, 7, 15, 8, 0, 0, 0, time.UTC)
	endedAt := startedAt.Add(time.Hour)
	recordSequence := int64(0)
	return importer.ImportBatch{
		Session: model.Session{
			ID:        "session-1",
			Title:     "Session",
			Summary:   "Summary",
			StartedAt: &startedAt,
			EndedAt:   &endedAt,
			Import: model.ImportMetadata{
				SourceID:             "source-1",
				AdapterName:          "test",
				AdapterVersion:       "1",
				FormatVersion:        "1",
				ModelVersion:         "1",
				NormalizationVersion: "1",
			},
			Diagnostics: []model.Diagnostic{{
				Code:     "session.partial",
				Severity: model.SeverityWarning,
				Message:  "session metadata is partial",
			}},
		},
		RawRecords: []model.RawRecord{{
			Ref: model.RawRecordRef{
				ID:             "raw-1",
				SourceID:       "source-1",
				RecordSequence: &recordSequence,
				ByteRange:      &model.ByteRange{Offset: 0, Length: 10},
				ContentHash:    "content-hash",
			},
			Content: []byte("raw record"),
		}},
		Events: []model.Event{{
			ID:             "event-1",
			SessionID:      "session-1",
			Sequence:       0,
			Kind:           model.EventKindMessage,
			Summary:        "message",
			SearchableText: "hello",
			Data:           model.MessageData{Role: model.MessageRoleUser, Text: "hello"},
			RawRecord: model.RawRecordRef{
				ID:             "raw-1",
				SourceID:       "source-1",
				RecordSequence: &recordSequence,
				ByteRange:      &model.ByteRange{Offset: 0, Length: 10},
				ContentHash:    "content-hash",
			},
		}},
		RecordDiagnostics: []model.RecordDiagnostic{{
			RawRecordID: "raw-1",
			Ordinal:     0,
			Diagnostic: model.Diagnostic{
				Code:         "record.partial",
				Severity:     model.SeverityWarning,
				Message:      "partial record",
				EventIDs:     []model.EventID{"event-1"},
				RawRecordIDs: []model.RawRecordID{"raw-1"},
			},
		}},
		Checkpoint: importer.ImportCheckpoint{
			SourceID:       "source-1",
			RecordSequence: 0,
			StateVersion:   "fixture-v1",
			Cursor:         []byte("cursor"),
			Fingerprint:    []byte("fingerprint"),
		},
	}
}

func assertOriginalState(t *testing.T, store *ImportStore, original importer.ImportBatch) {
	t.Helper()
	session, found, err := store.Session(context.Background(), original.Session.ID)
	if err != nil || !found || !reflect.DeepEqual(session, original.Session) {
		t.Fatalf("Session() after rollback = (%#v, %v, %v), want original", session, found, err)
	}
	events, err := store.Events(context.Background(), original.Session.ID)
	if err != nil || !reflect.DeepEqual(events, original.Events) {
		t.Fatalf("Events() after rollback = (%#v, %v), want original", events, err)
	}
	diagnostics, err := store.RecordDiagnostics(context.Background(), original.Session.ID)
	if err != nil || !reflect.DeepEqual(diagnostics, original.RecordDiagnostics) {
		t.Fatalf("RecordDiagnostics() after rollback = (%#v, %v), want original", diagnostics, err)
	}
	checkpoint, found, err := store.Checkpoint(context.Background(), original.Checkpoint.SourceID)
	if err != nil || !found || !importer.CheckpointEqual(checkpoint, original.Checkpoint) {
		t.Fatalf("Checkpoint() after rollback = (%#v, %v, %v), want original", checkpoint, found, err)
	}
}

func assertRawRecord(t *testing.T, store *ImportStore, want model.RawRecord) {
	t.Helper()
	got, found, err := store.RawRecord(context.Background(), want.Ref.ID)
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("RawRecord(%q) = (%#v, %v, %v), want %#v", want.Ref.ID, got, found, err, want)
	}
}

func payloadKind(payload model.NormalizedData) model.EventKind {
	switch payload.(type) {
	case model.MessageData:
		return model.EventKindMessage
	case model.ToolCallData:
		return model.EventKindToolCall
	case model.ToolResultData:
		return model.EventKindToolResult
	case model.CommandData:
		return model.EventKindCommand
	case model.FileReadData:
		return model.EventKindFileRead
	case model.FileMutationData:
		return model.EventKindFileMutation
	case model.PatchData:
		return model.EventKindPatch
	case model.UsageData:
		return model.EventKindUsage
	case model.ErrorData:
		return model.EventKindError
	case model.SummaryData:
		return model.EventKindSummary
	case model.UnknownData:
		return model.EventKindUnknown
	default:
		panic("unsupported test payload")
	}
}

func summaryPayloadWithEncodedSize(t *testing.T, size int) model.SummaryData {
	t.Helper()
	empty, err := json.Marshal(model.SummaryData{})
	if err != nil {
		t.Fatalf("marshal empty summary payload: %v", err)
	}
	if size < len(empty) {
		t.Fatalf("requested encoded payload size %d is smaller than JSON envelope %d", size, len(empty))
	}
	payload := model.SummaryData{Text: strings.Repeat("x", size-len(empty))}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal sized summary payload: %v", err)
	}
	if len(encoded) != size {
		t.Fatalf("sized summary payload length = %d, want %d", len(encoded), size)
	}
	return payload
}

func boolPointer(value bool) *bool    { return &value }
func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }
