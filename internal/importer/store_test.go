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

	err := batch.Validate()
	if err == nil || !strings.Contains(err.Error(), "exceeds checkpoint sequence") {
		t.Fatalf("Validate() error = %v, want checkpoint-bound error", err)
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
		Events: []model.Event{{
			ID:        "event-1",
			SessionID: "session-1",
			Sequence:  sequence,
			Kind:      model.EventKindUnknown,
			Summary:   "record",
			Data:      model.UnknownData{OriginalKind: "test"},
			RawRecord: model.RawRecordRef{ID: "raw-1", SourceID: "source-1", RecordSequence: &recordSequence},
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
