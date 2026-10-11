package importer

import (
	"context"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestBatchSinkRetainsImmutableInitialSession(t *testing.T) {
	source := testSource(nil)
	session := testSession(source)
	session.Diagnostics = []model.Diagnostic{{
		Code: "source.partial", Severity: model.SeverityWarning, Message: "original",
		EventIDs: []model.EventID{"event-1"}, RawRecordIDs: []model.RawRecordID{"record-1"},
	}}
	sink := &batchSink{source: source, result: &ImportResult{}, options: Options{}.withDefaults()}
	if err := sink.Begin(context.Background(), session); err != nil {
		t.Fatal(err)
	}
	session.Diagnostics[0].Message = "changed"
	session.Diagnostics[0].EventIDs[0] = "event-2"
	session.Diagnostics[0].RawRecordIDs[0] = "record-2"

	checkpoint := ImportCheckpoint{
		SourceID: source.ID, RecordSequence: NoRecordSequence, StateVersion: "fixture-stream-v1",
		Cursor: []byte("offset:0"), Fingerprint: []byte(model.HashRecord(nil)),
	}
	if err := sink.Complete(context.Background(), session, checkpoint); err == nil {
		t.Fatal("Complete() accepted an adapter mutation of the initial session")
	}
}
