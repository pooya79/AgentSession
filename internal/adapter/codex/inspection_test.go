package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestDelayedMetadataUsesOneProbePrepareInspection(t *testing.T) {
	source := fixtureSource(t, "delayed_metadata_0.145.0.jsonl")
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	prepared := view.(*prepared)
	defer prepared.Close()
	if prepared.format != probe.FormatVersion ||
		prepared.sessionID != "019c1000-0000-7000-8000-000000000145" ||
		prepared.cliVersion != "0.145.0" || !prepared.ordinal {
		t.Fatalf("prepared inspection = format %q session %q cli %q ordinal %t", prepared.format, prepared.sessionID, prepared.cliVersion, prepared.ordinal)
	}
	wantStarted := time.Date(2026, 7, 21, 10, 0, 5, 0, time.UTC)
	if prepared.startedAt == nil || !prepared.startedAt.Equal(wantStarted) {
		t.Fatalf("started at = %v, want %v", prepared.startedAt, wantStarted)
	}
	sink := &captureSink{}
	if err := prepared.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != 9 || !bytes.Equal(sink.records[0].RawRecord.Content, []byte("{\"timestamp\":\"broken\"\n")) {
		t.Fatalf("replayed records = %d", len(sink.records))
	}
}

func TestOversizedInitialRecordUsesBoundedInspectionAndStillReplays(t *testing.T) {
	large := "{\"timestamp\":\"2026-07-24T00:00:00Z\",\"ordinal\":0,\"type\":\"future_initial\",\"payload\":{\"value\":\"" +
		strings.Repeat("x", inspectionPrefix+(32<<10)) + "\"}}\n"
	meta := "{\"timestamp\":\"2026-07-24T00:00:01Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"after-large\",\"cli_version\":\"0.145.0\"}}\n"
	source := bytesSource("oversized-initial", []byte(large+meta))
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain ||
		probe.FormatVersion != "codex-rollout-jsonl-v2-ordinal+cli-0.145.0" {
		t.Fatalf("Probe() = %#v", probe)
	}
	sink, _ := importSource(t, source, nil, nil)
	if sink.session.ID != "after-large" || len(sink.records) != 2 ||
		len(sink.records[0].RawRecord.Content) != len(large) ||
		len(sink.records[0].Events) != 1 || sink.records[0].Events[0].Kind != model.EventKindUnknown {
		t.Fatalf("oversized initial replay = session %q records %d raw %d", sink.session.ID, len(sink.records), len(sink.records[0].RawRecord.Content))
	}
}

func TestOversizedSessionMetadataUsesOnlyBoundedFields(t *testing.T) {
	meta := "{\"timestamp\":\"2026-07-24T01:00:00Z\",\"ordinal\":0,\"type\":\"session_meta\",\"payload\":{\"id\":\"large-meta\",\"session_id\":\"large-meta\",\"timestamp\":\"2026-07-24T01:00:00Z\",\"cli_version\":\"0.145.0\",\"base_instructions\":\"" +
		strings.Repeat("x", inspectionPrefix+(32<<10)) + "\"}}\n"
	message := "{\"timestamp\":\"2026-07-24T01:00:01Z\",\"ordinal\":1,\"type\":\"response_item\",\"payload\":{\"type\":\"message\",\"id\":\"after_large_meta\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"ready\"}]}}\n"
	source := bytesSource("oversized-meta", []byte(meta+message))
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	sink, _ := importSource(t, source, nil, nil)
	wantStarted := time.Date(2026, 7, 24, 1, 0, 0, 0, time.UTC)
	if probe.FormatVersion != "codex-rollout-jsonl-v2-ordinal+cli-0.145.0" ||
		sink.session.ID != "large-meta" || sink.session.StartedAt == nil ||
		!sink.session.StartedAt.Equal(wantStarted) || len(events(sink.records)) != 1 {
		t.Fatalf("oversized metadata = probe %#v session %#v events %d", probe, sink.session, len(events(sink.records)))
	}
}

func TestPrepareStopsAtWindowAndReplaysEveryByte(t *testing.T) {
	var data strings.Builder
	for index := 0; index < initialRecords+1; index++ {
		fmt.Fprintf(&data, "{\"timestamp\":\"2026-07-23T10:00:%02dZ\",\"ordinal\":%d,\"type\":\"turn_context\",\"payload\":{\"index\":%d}}\n", index, index, index)
	}
	bytesValue := []byte(data.String())
	windowEnd := 0
	for count := 0; count < initialRecords; count++ {
		windowEnd += bytes.IndexByte(bytesValue[windowEnd:], '\n') + 1
	}
	tracker := &trackingReadCloser{reader: bytes.NewReader(bytesValue)}
	source := importer.Source{
		ID: "bounded-window", Size: int64(len(bytesValue)),
		OpenAt: func(context.Context, int64) (io.ReadCloser, error) { return tracker, nil },
	}
	view, err := New().Prepare(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	prepared := view.(*prepared)
	replayName := prepared.replay.Name()
	replayInfo, err := prepared.replay.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if replayInfo.Size() != int64(windowEnd) {
		t.Fatalf("Prepare() replayed %d bytes, want exact eight-record window %d", replayInfo.Size(), windowEnd)
	}
	sink := &captureSink{}
	if err := prepared.Import(context.Background(), nil, sink); err != nil {
		t.Fatal(err)
	}
	if len(sink.records) != initialRecords+1 {
		t.Fatalf("replayed records = %d, want %d", len(sink.records), initialRecords+1)
	}
	var replayed []byte
	for _, record := range sink.records {
		replayed = append(replayed, record.RawRecord.Content...)
	}
	if !bytes.Equal(replayed, bytesValue) {
		t.Fatal("prepared import did not replay every byte exactly once")
	}
	if err := prepared.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(replayName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replay file still exists after Close(): %v", err)
	}
}

func TestPrepareHonorsCancellationDuringWindow(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := New().Prepare(ctx, bytesSource("cancel-window", []byte("{}\n")))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Prepare() error = %v, want context cancellation", err)
	}
}

func TestReadFailureStopsPrepare(t *testing.T) {
	line := []byte("{\"timestamp\":\"2025-01-01T00:00:00Z\",\"type\":\"session_meta\",\"payload\":{\"id\":\"read-failure\"}}\n")
	want := errors.New("injected read failure")
	reader := &trackingReadCloser{reader: bytes.NewReader(line), failAfter: len(line), fail: want}
	source := importer.Source{ID: "read-failure", Size: int64(len(line) + 1), OpenAt: func(context.Context, int64) (io.ReadCloser, error) { return reader, nil }}
	if _, err := New().Prepare(context.Background(), source); !errors.Is(err, want) {
		t.Fatalf("Prepare() error = %v, want injected failure", err)
	}
}
