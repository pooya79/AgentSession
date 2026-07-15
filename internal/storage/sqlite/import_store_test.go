package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
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
	}
	batch.Checkpoint.RecordSequence = int64(len(payloads) - 1)
	batch.Session.Diagnostics = []model.Diagnostic{{
		Code:         "record.partial",
		Severity:     model.SeverityWarning,
		Message:      "partial evidence",
		EventIDs:     []model.EventID{batch.Events[0].ID},
		RawRecordIDs: []model.RawRecordID{batch.Events[0].RawRecord.ID},
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
	for i, event := range gotEvents {
		if event.Sequence != int64(i) {
			t.Errorf("Events()[%d].Sequence = %d, want %d", i, event.Sequence, i)
		}
	}
	checkpoint, found, err := store.Checkpoint(context.Background(), batch.Checkpoint.SourceID)
	if err != nil {
		t.Fatalf("Checkpoint() error = %v", err)
	}
	if !found || checkpoint != batch.Checkpoint {
		t.Fatalf("Checkpoint() = (%#v, %v), want (%#v, true)", checkpoint, found, batch.Checkpoint)
	}
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
	advanced.Checkpoint.ByteOffset++
	advanced.Checkpoint.SourceSize++
	advanced.Checkpoint.RecordSequence++
	advanced.Checkpoint.PrefixHash = "advanced-prefix"
	advanced.Checkpoint.LastRecordHash = "advanced-record"
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
	checkpoint, found, err := store.Checkpoint(context.Background(), batch.Checkpoint.SourceID)
	if err != nil || !found || checkpoint != advanced.Checkpoint {
		t.Fatalf("Checkpoint() = (%#v, %v, %v), want advanced checkpoint", checkpoint, found, err)
	}
}

func TestImportStoreConflictingDuplicateRollsBackWholeBatch(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	original := testImportBatch()
	original.Events[0].Sequence = 3
	original.Events[0].RawRecord.RecordSequence = int64Pointer(3)
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
	conflict := original.Events[0]
	conflict.Summary = "different canonical content"
	changed.Events = []model.Event{newEvent, conflict}
	changed.Checkpoint.ByteOffset++
	changed.Checkpoint.SourceSize++
	changed.Checkpoint.PrefixHash = "new-prefix"

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
	regressed.Events = nil
	regressed.Checkpoint.ByteOffset--
	err := store.CommitBatch(context.Background(), regressed)
	if !errors.Is(err, importer.ErrCheckpointRegression) {
		t.Fatalf("CommitBatch() error = %v, want ErrCheckpointRegression", err)
	}
	assertOriginalState(t, store, original)
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
	duplicateSequence.Checkpoint.ByteOffset++
	duplicateSequence.Checkpoint.SourceSize++
	duplicateSequence.Checkpoint.RecordSequence++
	duplicateSequence.Checkpoint.PrefixHash = "next-prefix"
	duplicateSequence.Checkpoint.LastRecordHash = "next-record"
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
				Code:         "partial",
				Severity:     model.SeverityWarning,
				Message:      "partial record",
				EventIDs:     []model.EventID{"event-1"},
				RawRecordIDs: []model.RawRecordID{"raw-1"},
			}},
		},
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
		Checkpoint: importer.ImportCheckpoint{
			SourceID:       "source-1",
			ByteOffset:     10,
			RecordSequence: 0,
			PrefixHash:     "prefix-hash",
			LastRecordHash: "record-hash",
			SourceSize:     10,
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
	checkpoint, found, err := store.Checkpoint(context.Background(), original.Checkpoint.SourceID)
	if err != nil || !found || checkpoint != original.Checkpoint {
		t.Fatalf("Checkpoint() after rollback = (%#v, %v, %v), want original", checkpoint, found, err)
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

func boolPointer(value bool) *bool    { return &value }
func intPointer(value int) *int       { return &value }
func int64Pointer(value int64) *int64 { return &value }
