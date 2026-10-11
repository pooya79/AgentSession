package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestIncompleteTailIsDeferredAndAppendResumesEventSequence(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "incomplete.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	first, _ := importSource(t, bytesSource("partial", data), nil, nil)
	if len(first.records) != 1 || first.checkpoint.RecordSequence != 0 || len(events(first.records)) != 1 {
		t.Fatalf("first import = records %d checkpoint %d events %d", len(first.records), first.checkpoint.RecordSequence, len(events(first.records)))
	}
	state := sourceState(first)
	appended := append(append([]byte(nil), data...), []byte("}}\n")...)
	second, change := importSource(t, bytesSource("partial", appended), &state.Checkpoint, &state)
	if change != importer.SourceAppend || len(second.records) != 1 || len(events(second.records)) != 1 {
		t.Fatalf("append = change %q records %d events %d", change, len(second.records), len(events(second.records)))
	}
	if second.records[0].RawRecord.Ref.RecordSequence == nil || *second.records[0].RawRecord.Ref.RecordSequence != 1 || second.records[0].Events[0].Sequence != 1 {
		t.Fatalf("resumed progress = %#v", second.records[0])
	}
}

func TestCancellationSinkBackpressureAndReadFailure(t *testing.T) {
	source := fixtureSource(t, "main.jsonl")
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

	line := []byte("{\"type\":\"user\",\"sessionId\":\"read-failure\",\"message\":{\"role\":\"user\",\"content\":\"ok\"}}\n")
	reader := &trackingReadCloser{reader: bytes.NewReader(line), failAfter: len(line), fail: errors.New("injected read failure")}
	failing := importer.Source{ID: "read-failure", Size: int64(len(line) + 1), OpenAt: func(context.Context, int64) (io.ReadCloser, error) { return reader, nil }}
	if _, err := New().Prepare(context.Background(), failing); !errors.Is(err, reader.fail) {
		t.Fatalf("Prepare() error = %v, want read failure", err)
	}
}

func TestOversizedRecordIsDeliveredBeforeRemainingSourceIsConsumed(t *testing.T) {
	largeText := strings.Repeat("large-evidence-", 24<<10)
	large, _ := json.Marshal(map[string]any{"type": "assistant", "sessionId": "streaming", "version": "5.0.0", "uuid": "large", "message": map[string]any{"role": "assistant", "content": largeText}})
	var records [][]byte
	for i := 0; i < 7; i++ {
		records = append(records, []byte(`{"type":"file-history-snapshot","sessionId":"streaming","version":"5.0.0"}`))
	}
	records = append(records, large)
	for i := 0; i < 1024; i++ {
		records = append(records, []byte(`{"type":"summary","sessionId":"streaming","version":"5.0.0","summary":"tail"}`))
	}
	data := bytes.Join(records, []byte("\n"))
	data = append(data, '\n')
	tracker := &trackingReadCloser{reader: bytes.NewReader(data)}
	source := importer.Source{ID: "streaming", Size: int64(len(data)), OpenAt: func(context.Context, int64) (io.ReadCloser, error) { return tracker, nil }}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer view.Close()
	preparedView := view.(*prepared)
	if preparedView.replay == nil {
		t.Fatal("probe prefix was not moved to replay storage")
	}
	if preparedView.sessionID != "streaming" || preparedView.format != "claude-code-jsonl-v1+cli-5.0.0" {
		t.Fatalf("large probe metadata = session %q format %q", preparedView.sessionID, preparedView.format)
	}
	if info, statErr := preparedView.replay.Stat(); statErr != nil || info.Size() <= int64(len(large)) {
		t.Fatalf("probe replay size = %v, %v", info, statErr)
	}
	accepted := 0
	sink := &captureSink{onAccept: func(envelope importer.RecordEnvelope) {
		accepted++
		if accepted == 8 {
			if len(envelope.RawRecord.Content) <= 256<<10 || len(envelope.Events) != 1 || len(envelope.Events[0].Data.(model.MessageData).Text) != len(largeText) {
				t.Fatalf("large envelope = raw %d events %d", len(envelope.RawRecord.Content), len(envelope.Events))
			}
			if tracker.readBytes >= len(data) {
				t.Fatal("large record was delivered only after the remaining source was consumed")
			}
		}
	}}
	if err := view.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
}
