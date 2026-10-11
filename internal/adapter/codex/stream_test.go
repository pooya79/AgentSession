package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestMalformedAndUnknownRecordsRetainExactBytes(t *testing.T) {
	source := fixtureSource(t, "malformed_unknown.jsonl")
	sink, _ := importSource(t, source, nil, nil)
	if len(sink.records) != 3 {
		t.Fatalf("records = %d", len(sink.records))
	}
	if len(sink.records[1].Diagnostics) != 1 || len(sink.records[1].Events) != 0 {
		t.Fatalf("malformed envelope = %#v", sink.records[1])
	}
	if string(sink.records[1].RawRecord.Content) != "{\"timestamp\":\"broken\"\n" {
		t.Fatalf("malformed bytes = %q", sink.records[1].RawRecord.Content)
	}
	unknown := sink.records[2]
	if len(unknown.Events) != 1 || unknown.Events[0].Kind != model.EventKindUnknown ||
		unknown.Events[0].Data.(model.UnknownData).OriginalKind != "future_rollout_item" ||
		unknown.Events[0].Data.(model.UnknownData).Reason != model.UnknownUnsupportedRecordKind {
		t.Fatalf("unknown envelope = %#v", unknown)
	}
}

func TestPartialTailIsDeferredThenImportedAfterAppend(t *testing.T) {
	complete := []byte("{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"partial-session\"}}\n")
	partial := []byte("{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"later\"")
	firstData := append(append([]byte(nil), complete...), partial...)
	first, _ := importSource(t, bytesSource("partial", firstData), nil, nil)
	if len(first.records) != 1 || first.checkpoint.RecordSequence != 0 {
		t.Fatalf("first import records/checkpoint = %d/%d", len(first.records), first.checkpoint.RecordSequence)
	}
	state := sourceState(first)
	appended := append(append([]byte(nil), firstData...), []byte("}}\n")...)
	second, change := importSource(t, bytesSource("partial", appended), &state.Checkpoint, &state)
	if change != importer.SourceAppend || len(second.records) != 1 || len(second.records[0].Events) != 1 {
		t.Fatalf("append change/records = %q/%d", change, len(second.records))
	}
	if second.records[0].RawRecord.Ref.ByteRange.Offset != int64(len(complete)) {
		t.Fatalf("appended record offset = %d", second.records[0].RawRecord.Ref.ByteRange.Offset)
	}
}

func TestPartialFirstRecordResumesFromZeroOffsetAfterAppend(t *testing.T) {
	partial := []byte("{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"user_message\",\"message\":\"later\"")
	first, _ := importSource(t, bytesSource("partial-first", partial), nil, nil)
	if len(first.records) != 0 || first.checkpoint.RecordSequence != -1 {
		t.Fatalf("first import records/checkpoint = %d/%d, want 0/-1", len(first.records), first.checkpoint.RecordSequence)
	}

	state := sourceState(first)
	appended := append(append([]byte(nil), partial...), []byte("}}\n")...)
	second, change := importSource(t, bytesSource("partial-first", appended), &state.Checkpoint, &state)
	if change != importer.SourceAppend || len(second.records) != 1 || len(second.records[0].Events) != 1 {
		t.Fatalf("append change/records/events = %q/%d/%d", change, len(second.records), len(events(second.records)))
	}
	if second.records[0].RawRecord.Ref.ByteRange.Offset != 0 {
		t.Fatalf("completed first record offset = %d, want 0", second.records[0].RawRecord.Ref.ByteRange.Offset)
	}
}

func TestReconcileStreamsCompleteReplacementAfterMutation(t *testing.T) {
	baseData, _ := os.ReadFile(filepath.Join("testdata", "legacy.jsonl"))
	initial, _ := importSource(t, bytesSource("reconcile", baseData), nil, nil)
	state := sourceState(initial)
	mutated := bytes.Replace(baseData, []byte("Please inspect the fixture."), []byte("Please inspect the changed fixture."), 1)
	view, err := New().Prepare(context.Background(), bytesSource("reconcile", mutated))
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	change, err := view.Verify(context.Background(), state)
	if err != nil || change != importer.SourceMutated {
		t.Fatalf("Verify() = %q, %v", change, err)
	}
	sink := &captureSink{}
	if err := view.Reconcile(context.Background(), sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 6 || !bytes.Contains(sink.records[1].RawRecord.Content, []byte("changed fixture")) {
		t.Fatalf("reconciled records = %d", len(sink.records))
	}
}

func TestCancellationAndSinkBackpressureStopStreaming(t *testing.T) {
	source := fixtureSource(t, "legacy.jsonl")
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := view.Import(ctx, nil, &captureSink{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Import() error = %v", err)
	}
	view.Close()

	view, _ = New().Prepare(context.Background(), source)
	defer view.Close()
	want := errors.New("backpressure")
	if err := view.Import(context.Background(), nil, &captureSink{acceptError: want}); !errors.Is(err, want) {
		t.Fatalf("backpressure Import() error = %v", err)
	}
}

func TestLargeRecordExceedsScannerLimitAndIsDelivered(t *testing.T) {
	meta := "{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"large\"}}\n"
	text := strings.Repeat("x", 300<<10)
	large := "{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"" + text + "\"}}\n"
	sink, _ := importSource(t, bytesSource("large", []byte(meta+large)), nil, nil)
	if len(sink.records) != 2 || len(sink.records[1].RawRecord.Content) <= 256<<10 || len(events(sink.records)) != 1 {
		t.Fatalf("large record delivery = records %d, bytes %d", len(sink.records), len(sink.records[1].RawRecord.Content))
	}
}

func TestLargeRecordIsDeliveredBeforeRemainderIsConsumed(t *testing.T) {
	meta := "{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"streaming\"}}\n"
	for i := 1; i < initialRecords; i++ {
		meta += fmt.Sprintf("{\"timestamp\":\"2025-01-01T00:00:%02dZ\",\"type\":\"turn_context\",\"payload\":{}}\n", i)
	}
	large := "{\"timestamp\":\"2025-01-01T00:00:01Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"" + strings.Repeat("x", 300<<10) + "\"}}\n"
	tail := "{\"timestamp\":\"2025-01-01T00:00:02Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"agent_message\",\"message\":\"" + strings.Repeat("y", 128<<10) + "\"}}\n"
	data := []byte(meta + large + tail)
	tracker := &trackingReadCloser{reader: bytes.NewReader(data)}
	source := importer.Source{ID: "streaming", Size: int64(len(data)), OpenAt: func(context.Context, int64) (io.ReadCloser, error) { return tracker, nil }}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	accepted := 0
	sink := &captureSink{onAccept: func(importer.RecordEnvelope) {
		accepted++
		if accepted == initialRecords+1 && tracker.readBytes >= len(data) {
			t.Fatal("large record was not delivered until after the entire source was consumed")
		}
	}}
	if err := view.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
}
