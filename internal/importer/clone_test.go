package importer

import (
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestCloneRecordEnvelopeOwnsEventTimestamp(t *testing.T) {
	source := testSource([]byte("one\n"))
	envelope := testEnvelopeForSource(source, 0, 0, []byte("one\n"))
	event := testUnknownEventForSource(envelope.RawRecord.Ref, 0)
	event.SessionID = testSession(source).ID
	timestamp := time.Unix(100, 0).UTC()
	event.Timestamp = &timestamp
	envelope.Events = []model.Event{event}

	clone := cloneRecordEnvelope(envelope)
	timestamp = time.Unix(200, 0).UTC()
	if got := clone.Events[0].Timestamp; got == nil || !got.Equal(time.Unix(100, 0).UTC()) {
		t.Fatalf("cloned timestamp = %v, want original value", got)
	}
}
