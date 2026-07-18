package importer

import (
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestImportBatchValidateRejectsEvidenceBeyondCheckpoint(t *testing.T) {
	sequence := int64(3)
	eventSequence := int64(4)
	batch := validBatchForTest(sequence)
	batch.Events[0].RawRecord.RecordSequence = &eventSequence
	batch.RawRecords[0].Ref.RecordSequence = &eventSequence

	err := batch.Validate()
	if err == nil || !strings.Contains(err.Error(), "exceeds checkpoint sequence") {
		t.Fatalf("Validate() error = %v, want checkpoint-bound error", err)
	}
}

func TestImportBatchAllowsEvidenceBeforeFinalCheckpoint(t *testing.T) {
	batch := validBatchForTest(3)
	batch.Checkpoint.RecordSequence = 9
	if err := batch.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want an earlier record to remain valid under the final batch checkpoint", err)
	}
}

func TestImportBatchValidateRequiresMatchingRetainedRawRecord(t *testing.T) {
	tests := []struct {
		name      string
		mutate    func(*ImportBatch)
		wantError string
	}{
		{
			name: "missing retained record",
			mutate: func(batch *ImportBatch) {
				batch.RawRecords = nil
			},
			wantError: "is not retained in the batch",
		},
		{
			name: "reference differs",
			mutate: func(batch *ImportBatch) {
				batch.RawRecords[0].Ref.ContentHash = "different-hash"
			},
			wantError: "reference does not match retained record",
		},
		{
			name: "duplicate retained record",
			mutate: func(batch *ImportBatch) {
				batch.RawRecords = append(batch.RawRecords, batch.RawRecords[0])
			},
			wantError: "repeats ID",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := validBatchForTest(0)
			tt.mutate(&batch)
			err := batch.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func TestImportBatchValidateRecordDiagnostics(t *testing.T) {
	valid := validBatchForTest(0)
	valid.RecordDiagnostics = []model.RecordDiagnostic{{
		RawRecordID: "raw-1",
		Ordinal:     0,
		Diagnostic: model.Diagnostic{
			Code: "record.partial", Severity: model.SeverityWarning, Message: "partial record",
			EventIDs: []model.EventID{"event-1"}, RawRecordIDs: []model.RawRecordID{"raw-1"},
		},
	}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name      string
		mutate    func(*ImportBatch)
		wantError string
	}{
		{name: "missing retained record", mutate: func(batch *ImportBatch) {
			batch.RecordDiagnostics[0].RawRecordID = "raw-other"
			batch.RecordDiagnostics[0].Diagnostic.RawRecordIDs = nil
		}, wantError: "is not retained in the batch"},
		{name: "duplicate position", mutate: func(batch *ImportBatch) {
			batch.RecordDiagnostics = append(batch.RecordDiagnostics, batch.RecordDiagnostics[0])
		}, wantError: "repeats raw record"},
		{name: "event outside batch", mutate: func(batch *ImportBatch) {
			batch.RecordDiagnostics[0].Diagnostic.EventIDs = []model.EventID{"event-other"}
		}, wantError: "outside the batch"},
		{name: "unrelated raw record evidence", mutate: func(batch *ImportBatch) {
			batch.RecordDiagnostics[0].Diagnostic.RawRecordIDs = []model.RawRecordID{"raw-other"}
		}, wantError: "unrelated raw record"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			batch := valid
			batch.RecordDiagnostics = append([]model.RecordDiagnostic(nil), valid.RecordDiagnostics...)
			batch.RecordDiagnostics[0].Diagnostic.EventIDs = append([]model.EventID(nil), valid.RecordDiagnostics[0].Diagnostic.EventIDs...)
			batch.RecordDiagnostics[0].Diagnostic.RawRecordIDs = append([]model.RawRecordID(nil), valid.RecordDiagnostics[0].Diagnostic.RawRecordIDs...)
			tt.mutate(&batch)
			err := batch.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("Validate() error = %v, want containing %q", err, tt.wantError)
			}
		})
	}
}

func validBatchForTest(sequence int64) ImportBatch {
	recordSequence := sequence
	return ImportBatch{
		Session: model.Session{
			ID: "session-1",
			Import: model.ImportMetadata{
				SourceID:             "source-1",
				AdapterName:          "test",
				AdapterVersion:       "1",
				FormatVersion:        "1",
				ModelVersion:         "1",
				NormalizationVersion: "1",
			},
		},
		RawRecords: []model.RawRecord{{
			Ref: model.RawRecordRef{
				ID:             "raw-1",
				SourceID:       "source-1",
				RecordSequence: &recordSequence,
				ContentHash:    "raw-hash",
			},
			Content: []byte(`{"type":"test"}`),
		}},
		Events: []model.Event{{
			ID:        "event-1",
			SessionID: "session-1",
			Sequence:  sequence,
			Kind:      model.EventKindUnknown,
			Summary:   "record",
			Data:      model.UnknownData{OriginalKind: "test"},
			RawRecord: model.RawRecordRef{ID: "raw-1", SourceID: "source-1", RecordSequence: &recordSequence, ContentHash: "raw-hash"},
		}},
		Checkpoint: ImportCheckpoint{
			SourceID:       "source-1",
			ByteOffset:     10,
			RecordSequence: sequence,
			PrefixHash:     "prefix",
			LastRecordHash: "record",
			SourceSize:     10,
		},
	}
}
