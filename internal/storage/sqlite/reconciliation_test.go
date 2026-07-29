package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

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
		Data:           model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "stale"},
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
	revision, revisionFound, revisionErr := store.CanonicalRevision(context.Background(), replacement.Session.ID)
	if revisionErr != nil || !revisionFound || revision != 2 {
		t.Fatalf("canonical revision after reconciliation = (%d, %v, %v), want monotonic revision 2", revision, revisionFound, revisionErr)
	}
	for _, state := range mustProjectionStates(t, store, replacement.Session.ID) {
		if state.Status != projection.StatusPending || state.TargetRevision != revision {
			t.Fatalf("projection state after reconciliation = %#v", state)
		}
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

func mustProjectionStates(t *testing.T, store *ImportStore, sessionID model.SessionID) []projection.State {
	t.Helper()
	states, err := store.States(context.Background(), sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return states
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
	claim, claimed, err := store.Claim(context.Background(), replacement.Session.ID, projection.KindSearch)
	if err != nil || !claimed {
		t.Fatalf("Claim() before reconciliation retry = (%#v, %v, %v)", claim, claimed, err)
	}
	if completed, err := store.Complete(context.Background(), claim); err != nil || !completed {
		t.Fatalf("Complete() before reconciliation retry = (%v, %v)", completed, err)
	}
	if err := reconcileBatches(context.Background(), store, replacement.Checkpoint, replacement); err != nil {
		t.Fatalf("identical reconciliation retry error = %v", err)
	}
	if revision, found, err := store.CanonicalRevision(context.Background(), replacement.Session.ID); err != nil || !found || revision != 2 {
		t.Fatalf("canonical revision after identical reconciliation retry = (%d, %v, %v), want 2", revision, found, err)
	}
	if state := projectionState(t, store, replacement.Session.ID, projection.KindSearch); !state.Usable() {
		t.Fatalf("ready projection invalidated by identical reconciliation retry: %#v", state)
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
	claim, claimed, err := store.Claim(context.Background(), batch.Session.ID, projection.KindSearch)
	if err != nil || !claimed {
		t.Fatalf("Claim() before retry = (%#v, %v, %v)", claim, claimed, err)
	}
	if completed, err := store.Complete(context.Background(), claim); err != nil || !completed {
		t.Fatalf("Complete() before retry = (%v, %v)", completed, err)
	}
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatalf("identical retry CommitBatch() error = %v", err)
	}
	if revision, found, err := store.CanonicalRevision(context.Background(), batch.Session.ID); err != nil || !found || revision != 1 {
		t.Fatalf("canonical revision after identical retry = (%d, %v, %v), want 1", revision, found, err)
	}
	if state := projectionState(t, store, batch.Session.ID, projection.KindSearch); !state.Usable() {
		t.Fatalf("ready projection invalidated by identical retry: %#v", state)
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
