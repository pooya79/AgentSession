package sqlite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

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
