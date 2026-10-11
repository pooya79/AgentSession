package importer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestCoordinatorFirstImportAppendUnchangedMalformedUnknownAndPartial(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\nbad\nfuture\npartial")
	store := newMemoryImportStore()
	projectionCalls := 0
	coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, projectorFunc(func(context.Context, ProjectionRequest) error {
		projectionCalls++
		return nil
	}), Options{BatchRecords: 2})
	if err != nil {
		t.Fatal(err)
	}

	first, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("first Import() error = %v", err)
	}
	if first.RecordsCommitted != 3 || len(store.raw) != 3 || len(store.events) != 2 || len(store.diagnostics) != 1 {
		t.Fatalf("first import result/state = %#v, raw %d events %d diagnostics %d", first, len(store.raw), len(store.events), len(store.diagnostics))
	}
	wantOffset := int64(len("one\nbad\nfuture\n"))
	if got := checkpointByteOffset(first.Checkpoint); got != wantOffset {
		t.Fatalf("checkpoint offset = %d, want %d", got, wantOffset)
	}

	fixture.set("one\nbad\nfuture\npartial\n")
	second, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("append Import() error = %v", err)
	}
	if second.RecordsCommitted != 1 || len(store.raw) != 4 || len(store.events) != 3 {
		t.Fatalf("append result/state = %#v, raw %d events %d", second, len(store.raw), len(store.events))
	}
	fixture.mu.Lock()
	lastOpen := fixture.openedAt[len(fixture.openedAt)-1]
	fixture.mu.Unlock()
	if lastOpen != 0 {
		t.Fatalf("append snapshot opened original source at %d, want 0", lastOpen)
	}

	checkpoint := store.state.Checkpoint
	third, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("unchanged Import() error = %v", err)
	}
	if third.RecordsCommitted != 0 || !CheckpointEqual(store.state.Checkpoint, checkpoint) || len(store.raw) != 4 || len(store.events) != 3 {
		t.Fatalf("unchanged result/state changed: %#v", third)
	}
	if projectionCalls != 3 {
		t.Fatalf("projection calls = %d, want 3 successful imports", projectionCalls)
	}
}

func TestCoordinatorEmptySourceUsesZeroRecordCheckpoint(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("")
	store := newMemoryImportStore()
	coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Checkpoint.RecordSequence != NoRecordSequence || checkpointByteOffset(result.Checkpoint) != 0 {
		t.Fatalf("empty checkpoint = %#v", result.Checkpoint)
	}
	if len(store.raw) != 0 || store.commits != 1 {
		t.Fatalf("empty state raw=%d commits=%d", len(store.raw), store.commits)
	}
}

func TestCoordinatorChangedSourceReconciles(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\n")
	store := newMemoryImportStore()
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{})
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}
	fixture.set("changed\n")
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if result.Change != SourceMutated || !result.Reconciled || len(store.raw) != 1 || len(store.events) != 1 {
		t.Fatalf("reconciled result=%#v raw=%d events=%d", result, len(store.raw), len(store.events))
	}
}

func TestCoordinatorClassifiesEverySourceChangeMode(t *testing.T) {
	tests := []struct {
		name       string
		change     SourceChange
		mutate     func(*coordinatorFixture)
		reconciled bool
		wantRaw    int
		wantEvents int
	}{
		{name: "unchanged", change: SourceUnchanged, mutate: func(*coordinatorFixture) {}, wantRaw: 2, wantEvents: 2},
		{name: "append", change: SourceAppend, mutate: func(f *coordinatorFixture) { f.set("one\ntwo\nthree\n") }, wantRaw: 3, wantEvents: 3},
		{name: "truncation", change: SourceTruncated, mutate: func(f *coordinatorFixture) { f.set("one\n") }, reconciled: true, wantRaw: 1, wantEvents: 1},
		{name: "mutation", change: SourceMutated, mutate: func(f *coordinatorFixture) { f.set("one\nbad\n") }, reconciled: true, wantRaw: 2, wantEvents: 1},
		{name: "replacement", change: SourceReplaced, mutate: func(f *coordinatorFixture) { f.replace("fixture-source-2", "new\n") }, reconciled: true, wantRaw: 1, wantEvents: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := &coordinatorFixture{}
			fixture.set("one\ntwo\n")
			store := newMemoryImportStore()
			coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
				t.Fatal(err)
			}
			tt.mutate(fixture)
			result, err := coordinator.Import(context.Background(), fixture.source())
			if err != nil {
				t.Fatalf("Import() error = %v", err)
			}
			if result.Change != tt.change || result.Reconciled != tt.reconciled {
				t.Fatalf("classification = (%q, reconciled %v), want (%q, %v)", result.Change, result.Reconciled, tt.change, tt.reconciled)
			}
			if len(store.raw) != tt.wantRaw || len(store.events) != tt.wantEvents {
				t.Fatalf("canonical evidence raw=%d events=%d, want %d and %d", len(store.raw), len(store.events), tt.wantRaw, tt.wantEvents)
			}
		})
	}
}

func TestCoordinatorInterruptionDuringRecoveryKeepsOldGeneration(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\ntwo\nthree\n")
	store := newMemoryImportStore()
	coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}
	checkpoint := cloneCheckpoint(store.state.Checkpoint)
	fixture.set("changed\ntwo\nthree\n")
	fixture.reconcileStopAfter = 2
	if _, err := coordinator.Import(context.Background(), fixture.source()); !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted reconciliation error = %v, want context.Canceled", err)
	}
	if !CheckpointEqual(store.state.Checkpoint, checkpoint) || len(store.raw) != 3 || len(store.events) != 3 {
		t.Fatalf("interrupted reconciliation exposed partial state: checkpoint=%#v raw=%d events=%d", store.state.Checkpoint, len(store.raw), len(store.events))
	}
	fixture.reconcileStopAfter = 0
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil || !result.Reconciled || result.Change != SourceMutated {
		t.Fatalf("reconciliation retry = (%#v, %v)", result, err)
	}
}

func TestCoordinatorFailedReconciliationPromotionDoesNotPublishCommittedCounts(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\ntwo\n")
	store := newMemoryImportStore()
	coordinator, err := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}

	fixture.set("changed\ntwo\n")
	store.finalizeErr = ErrCheckpointConflict
	var progress []Progress
	_, err = coordinator.ImportAllObserved(context.Background(), fixture.source(), func(update Progress) {
		progress = append(progress, update)
	})
	if !errors.Is(err, ErrCheckpointConflict) {
		t.Fatalf("ImportAllObserved() error = %v, want ErrCheckpointConflict", err)
	}
	if len(progress) == 0 {
		t.Fatal("ImportAllObserved() emitted no progress")
	}
	for _, update := range progress {
		if update.RecordsCommitted != 0 || update.BatchesCommitted != 0 {
			t.Fatalf("failed promotion published committed counts: %#v", update)
		}
	}
}

func TestCoordinatorResumeUsesVerifiedSourceSnapshot(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\n")
	store := newMemoryImportStore()
	adapter := &streamingFixtureAdapter{fixture: fixture}
	coordinator, _ := NewCoordinator(store, []Adapter{adapter}, nil, Options{})
	if _, err := coordinator.Import(context.Background(), fixture.source()); err != nil {
		t.Fatal(err)
	}

	fixture.set("one\ntwo\n")
	source := fixture.source()
	fixture.afterVerify = func() {
		fixture.set("one\nbad\n")
		fixture.afterVerify = nil
	}
	result, err := coordinator.Import(context.Background(), source)
	if err != nil {
		t.Fatalf("resumed Import() error = %v", err)
	}
	if result.RecordsCommitted != 1 || len(store.events) != 2 || len(store.diagnostics) != 0 {
		t.Fatalf("resumed snapshot result=%#v events=%d diagnostics=%d", result, len(store.events), len(store.diagnostics))
	}
	wantRecordID := testEnvelopeForSource(source, 1, int64(len("one\n")), []byte("two\n")).RawRecord.Ref.ID
	if _, ok := store.raw[wantRecordID]; !ok {
		t.Fatalf("resumed import did not retain verified snapshot record %q", wantRecordID)
	}
}

func TestCoordinatorCancellationKeepsOnlyEarlierBatches(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\ntwo\nthree\n")
	store := newMemoryImportStore()
	ctx, cancel := context.WithCancel(context.Background())
	store.beforeCommit = func(call int, _ context.Context) error {
		if call == 2 {
			cancel()
			return context.Canceled
		}
		return nil
	}
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
	_, err := coordinator.Import(ctx, fixture.source())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Import() error = %v, want cancellation", err)
	}
	if len(store.raw) != 1 || store.state.Checkpoint.RecordSequence != 0 {
		t.Fatalf("durable records=%d checkpoint=%#v", len(store.raw), store.state.Checkpoint)
	}
}

func TestCoordinatorBoundsLargeSourceAndRejectsOversizedRecord(t *testing.T) {
	fixture := &coordinatorFixture{}
	var source bytes.Buffer
	for i := 0; i < 1000; i++ {
		fmt.Fprintf(&source, "record-%04d\n", i)
	}
	fixture.set(source.String())
	store := newMemoryImportStore()
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 10, BatchBytes: 1024, MaxRecordBytes: 1024})
	result, err := coordinator.Import(context.Background(), fixture.source())
	if err != nil {
		t.Fatalf("large Import() error = %v", err)
	}
	if result.RecordsCommitted != 1000 || store.maxRecords > 10 {
		t.Fatalf("result=%#v max batch records=%d", result, store.maxRecords)
	}

	fixture2 := &coordinatorFixture{}
	fixture2.set("this-record-is-too-large\n")
	store2 := newMemoryImportStore()
	coordinator2, _ := NewCoordinator(store2, []Adapter{&streamingFixtureAdapter{fixture2}}, nil, Options{MaxRecordBytes: 8})
	_, err = coordinator2.Import(context.Background(), fixture2.source())
	if !errors.Is(err, ErrRecordTooLarge) || store2.commits != 0 {
		t.Fatalf("oversized error=%v commits=%d", err, store2.commits)
	}
}

func TestCoordinatorProjectionFailureIsSeparateFromCanonicalSuccess(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("one\n")
	store := newMemoryImportStore()
	projectionErr := errors.New("secret=raw-token path=/private/session.jsonl")
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, projectorFunc(func(context.Context, ProjectionRequest) error { return projectionErr }), Options{})
	var progress []Progress
	results, err := coordinator.ImportAllObserved(context.Background(), fixture.source(), func(update Progress) {
		progress = append(progress, update)
	})
	if err != nil {
		t.Fatalf("Import() canonical error = %v", err)
	}
	result := results[0]
	if !errors.Is(result.ProjectionError, projectionErr) || len(store.raw) != 1 {
		t.Fatalf("result=%#v raw=%d", result, len(store.raw))
	}
	for _, update := range progress {
		for _, diagnostic := range update.Diagnostics {
			if strings.Contains(diagnostic.Message, "raw-token") || strings.Contains(diagnostic.Message, "/private") {
				t.Fatalf("projection progress leaked raw error: %#v", diagnostic)
			}
		}
	}
}

func TestCoordinatorObservedProgressIncludesCountsDiagnosticsAndPhases(t *testing.T) {
	fixture := &coordinatorFixture{}
	fixture.set("bad\none\n")
	store := newMemoryImportStore()
	coordinator, _ := NewCoordinator(store, []Adapter{&streamingFixtureAdapter{fixture}}, nil, Options{BatchRecords: 1})
	var progress []Progress
	results, err := coordinator.ImportAllObserved(context.Background(), fixture.source(), func(update Progress) {
		progress = append(progress, update)
	})
	if err != nil || len(results) != 1 {
		t.Fatalf("ImportAllObserved() = (%#v, %v)", results, err)
	}
	if len(progress) == 0 {
		t.Fatal("ImportAllObserved() emitted no progress")
	}
	last := progress[len(progress)-1]
	if last.SourceID != fixture.source().ID || last.ActiveSourceID != fixture.source().ID || last.Phase != PhaseFinalizing {
		t.Fatalf("last progress identity/phase = %#v", last)
	}
	if last.RecordsProcessed != 2 || last.EventsProcessed != 1 || last.RecordsCommitted != 2 || last.BatchesCommitted != 2 || last.DiagnosticsObserved != 1 {
		t.Fatalf("last progress counts = %#v", last)
	}
	seenDiagnostic := false
	seenCommit := false
	for _, update := range progress {
		seenCommit = seenCommit || update.Phase == PhaseCommitting
		seenDiagnostic = seenDiagnostic || len(update.Diagnostics) == 1 && update.Diagnostics[0].Code == "record.malformed"
	}
	if !seenDiagnostic || !seenCommit {
		t.Fatalf("progress missing diagnostic or commit phase: %#v", progress)
	}
}
