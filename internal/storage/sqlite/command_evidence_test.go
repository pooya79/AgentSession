package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

type commandRecord struct {
	invocation   string
	phase        model.CommandPhase
	command, dir string
	exit         *int
	gap          bool
	malformed    bool
}

func commandBatch(records []commandRecord) importer.ImportBatch {
	batch := testImportBatch()
	batch.Events = nil
	batch.RawRecords = nil
	batch.RecordDiagnostics = nil
	batch.Session.Diagnostics = nil
	for i, r := range records {
		n := int64(i)
		raw := model.RawRecord{Ref: model.RawRecordRef{ID: model.RawRecordID(fmt.Sprintf("raw-%d", i)), SourceID: batch.Session.Import.SourceID, RecordSequence: &n, ContentHash: fmt.Sprintf("hash-%d", i)}, Content: []byte("sanitized")}
		batch.RawRecords = append(batch.RawRecords, raw)
		if r.malformed {
			batch.RecordDiagnostics = append(batch.RecordDiagnostics, model.RecordDiagnostic{RawRecordID: raw.Ref.ID, Diagnostic: model.Diagnostic{Code: "malformed", Severity: model.SeverityWarning, Message: "Malformed record", InterpretationReason: model.InterpretationStructurallyInvalidKnownRecord}})
			continue
		}
		data := model.NormalizedData(model.CommandData{InvocationID: r.invocation, Phase: r.phase, Command: r.command, WorkingDirectory: r.dir, ExitCode: r.exit, Output: strings.Repeat("output", 50000)})
		kind := model.EventKindCommand
		if r.gap {
			data = model.UnknownData{Reason: model.UnknownUnsupportedRecordKind, OriginalKind: "future"}
			kind = model.EventKindUnknown
		}
		batch.Events = append(batch.Events, model.Event{ID: model.EventID(fmt.Sprintf("evt_%064x", i)), SessionID: batch.Session.ID, Sequence: int64(len(batch.Events)), Kind: kind, Summary: "Evidence", Data: data, RawRecord: raw.Ref})
	}
	batch.Checkpoint.RecordSequence = int64(len(records) - 1)
	return batch
}
func TestStoredCommandEvidence(t *testing.T) {
	fail, success := 1, 0
	start := func(id string) commandRecord {
		return commandRecord{invocation: id, phase: model.CommandStarted, command: " go test ./... \n", dir: "/workspace"}
	}
	end := func(id string, code *int) commandRecord {
		return commandRecord{invocation: id, phase: model.CommandCompleted, command: "go test ./...", dir: "/workspace", exit: code}
	}
	tests := []struct {
		name                           string
		records                        []commandRecord
		state                          model.FindingState
		attempts, sequences, recovered string
	}{
		{"conflicting invocation identity", []commandRecord{start("a"), end("a", &fail), {invocation: "mixed", phase: model.CommandStarted, command: "aaa", dir: "/workspace"}, start("mixed"), end("mixed", &success), start("b"), end("b", &fail)}, model.FindingInsufficientEvidence, "3", "0", "0"},
		{"duplicate result", []commandRecord{start("a"), end("a", &fail), end("a", &fail)}, model.FindingNotTriggered, "1", "0", "0"},
		{"repeat and recovery", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", &fail), start("c"), end("c", &success)}, model.FindingTriggered, "3", "1", "1"},
		{"late contradiction", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", &fail), end("a", &success)}, model.FindingInsufficientEvidence, "2", "0", "0"},
		{"missing result", []commandRecord{start("a"), end("a", &fail), start("b"), start("c"), end("c", &fail)}, model.FindingInsufficientEvidence, "3", "0", "0"},
		{"unknown result", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", nil), start("c"), end("c", &fail)}, model.FindingInsufficientEvidence, "3", "0", "0"},
		{"gap between", []commandRecord{start("a"), end("a", &fail), {gap: true}, start("b"), end("b", &fail)}, model.FindingInsufficientEvidence, "2", "0", "0"},
		{"malformed between", []commandRecord{start("a"), end("a", &fail), {malformed: true}, start("b"), end("b", &fail)}, model.FindingInsufficientEvidence, "2", "0", "0"},
		{"different command unknown directory", []commandRecord{start("a"), end("a", &fail), {invocation: "other", phase: model.CommandStarted, command: "pwd"}, {invocation: "other", phase: model.CommandCompleted, command: "pwd", exit: &success}, start("b"), end("b", &fail)}, model.FindingTriggered, "3", "1", "0"},
		{"linked result without identity fields", []commandRecord{start("a"), {invocation: "a", phase: model.CommandCompleted, exit: &fail}, start("b"), {invocation: "b", phase: model.CommandCompleted, exit: &fail}}, model.FindingTriggered, "2", "1", "0"},
		{"gap before", []commandRecord{{gap: true}, start("a"), end("a", &fail), start("b"), end("b", &fail)}, model.FindingTriggered, "2", "1", "0"},
		{"gap after", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", &fail), {gap: true}}, model.FindingTriggered, "2", "1", "0"},
		{"overlap", []commandRecord{start("a"), start("b"), end("a", &fail), end("b", &fail)}, model.FindingTriggered, "2", "1", "0"},
		{"independent overlapping sequence recovery", []commandRecord{start("a"), start("b"), end("b", &fail), start("c"), end("c", &success), start("d"), end("d", &fail), start("e"), end("e", &fail), start("f"), end("f", &success), end("a", &fail)}, model.FindingTriggered, "6", "2", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := openImportStore(t)
			batch := commandBatch(tt.records)
			if err := store.CommitBatch(ctx, batch); err != nil {
				t.Fatal(err)
			}
			if err := store.CommitBatch(ctx, batch); err != nil {
				t.Fatal("duplicate import:", err)
			}
			got, err := analysis.AssessRepeatedFailures(ctx, batch.Session.ID, store)
			if err != nil || got.State != tt.state || got.Metadata["invocations"] != tt.attempts || got.Metadata["sequences"] != tt.sequences || got.Metadata["recovered"] != tt.recovered {
				t.Fatalf("assessment = %#v, %v", got, err)
			}
			if strings.Contains(got.Explanation, "output") {
				t.Fatal("output prose entered findings")
			}
		})
	}
}
func TestOversizedCommandEvidenceRetainsRawAndUncertainty(t *testing.T) {
	ctx := context.Background()
	store := openImportStore(t)
	code := 1
	batch := commandBatch([]commandRecord{{invocation: "a", phase: model.CommandStarted, command: strings.Repeat("x", analysis.MaxCommandBytes+1), dir: "/workspace"}, {invocation: "a", phase: model.CommandCompleted, command: strings.Repeat("x", analysis.MaxCommandBytes+1), dir: "/workspace", exit: &code}})
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	got, err := analysis.AssessRepeatedFailures(ctx, batch.Session.ID, store)
	if err != nil || got.State != model.FindingInsufficientEvidence {
		t.Fatalf("oversized = %#v, %v", got, err)
	}
	original, found, err := store.Event(ctx, batch.Events[0].ID)
	if err != nil || !found || len(original.Data.(model.CommandData).Command) != analysis.MaxCommandBytes+1 {
		t.Fatal("oversized canonical field lost")
	}
}

func TestUnlocatedMalformedEvidenceCannotProveConsecutiveFailures(t *testing.T) {
	ctx := context.Background()
	store := openImportStore(t)
	code := 1
	batch := commandBatch([]commandRecord{{invocation: "a", phase: model.CommandStarted, command: "false", dir: "/workspace"}, {invocation: "a", phase: model.CommandCompleted, command: "false", dir: "/workspace", exit: &code}, {malformed: true}, {invocation: "b", phase: model.CommandStarted, command: "false", dir: "/workspace"}, {invocation: "b", phase: model.CommandCompleted, command: "false", dir: "/workspace", exit: &code}})
	batch.RawRecords[2].Ref.RecordSequence = nil
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	got, err := analysis.AssessRepeatedFailures(ctx, batch.Session.ID, store)
	if err != nil || got.State != model.FindingInsufficientEvidence {
		t.Fatalf("unlocated evidence=%#v, %v", got, err)
	}
}

func TestCommandWalkHonorsCancellationDuringEvidenceRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store := openImportStore(t)
	code := 1
	batch := commandBatch([]commandRecord{{invocation: "a", phase: model.CommandStarted, command: "false", dir: "/workspace"}, {invocation: "a", phase: model.CommandCompleted, command: "false", dir: "/workspace", exit: &code}, {invocation: "b", phase: model.CommandStarted, command: "false", dir: "/workspace"}})
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	_, err := store.WalkCommands(ctx, batch.Session.ID, func(analysis.CommandEvidence) error { cancel(); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestUncertaintyOnlyAssessmentLinksRecordedEvidence(t *testing.T) {
	for _, record := range []commandRecord{{gap: true}, {invocation: "orphan", phase: model.CommandCompleted, command: "false", dir: "/workspace"}} {
		ctx := context.Background()
		store := openImportStore(t)
		batch := commandBatch([]commandRecord{record})
		if err := store.CommitBatch(ctx, batch); err != nil {
			t.Fatal(err)
		}
		got, err := analysis.AssessRepeatedFailures(ctx, batch.Session.ID, store)
		if err != nil || got.State != model.FindingInsufficientEvidence || len(got.EventIDs) != 1 || got.EventIDs[0] != batch.Events[0].ID {
			t.Fatalf("uncertainty assessment=%#v, %v", got, err)
		}
	}
}

func TestUnrelatedDirectoryContradictionsDoNotBreakRepetition(t *testing.T) {
	ctx := context.Background()
	store := openImportStore(t)
	fail, success := 1, 0
	batch := commandBatch([]commandRecord{{invocation: "a", phase: model.CommandStarted, command: "false", dir: "/workspace"}, {invocation: "a", phase: model.CommandCompleted, command: "false", dir: "/workspace", exit: &fail}, {invocation: "other", phase: model.CommandStarted, command: "pwd", dir: "/a"}, {invocation: "other", phase: model.CommandCompleted, command: "pwd", dir: "/b", exit: &success}, {invocation: "b", phase: model.CommandStarted, command: "false", dir: "/workspace"}, {invocation: "b", phase: model.CommandCompleted, command: "false", dir: "/workspace", exit: &fail}})
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	got, err := analysis.AssessRepeatedFailures(ctx, batch.Session.ID, store)
	if err != nil || got.State != model.FindingTriggered {
		t.Fatalf("unrelated contradictions=%#v, %v", got, err)
	}
}

func TestStoredUnresolvedVerification(t *testing.T) {
	fail, success := 1, 0
	start := func(id string) commandRecord {
		return commandRecord{invocation: id, phase: model.CommandStarted, command: "go test ./...", dir: "/workspace"}
	}
	end := func(id string, code *int) commandRecord {
		return commandRecord{invocation: id, phase: model.CommandCompleted, command: "go test ./...", dir: "/workspace", exit: code}
	}
	tests := []struct {
		name                  string
		records               []commandRecord
		state                 model.FindingState
		unresolved, recovered string
	}{
		{"failure", []commandRecord{start("a"), end("a", &fail)}, model.FindingTriggered, "1", "0"},
		{"recovery", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", &success)}, model.FindingNotTriggered, "0", "1"},
		{"reopening", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", &success), start("c"), end("c", &fail)}, model.FindingTriggered, "1", "1"},
		{"parallel success", []commandRecord{start("a"), start("b"), end("a", &fail), end("b", &success)}, model.FindingTriggered, "1", "0"},
		{"parallel pending", []commandRecord{start("a"), start("b"), end("a", &fail)}, model.FindingTriggered, "1", "0"},
		{"parallel conflicting identity", []commandRecord{start("a"), {invocation: "b", phase: model.CommandStarted, command: "go vet ./...", dir: "/workspace"}, end("a", &fail), end("b", &success)}, model.FindingTriggered, "1", "0"},
		{"pending rerun", []commandRecord{start("a"), end("a", &fail), start("b")}, model.FindingInsufficientEvidence, "0", "0"},
		{"unknown completed result then recovery", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", nil), start("c"), end("c", &success)}, model.FindingNotTriggered, "0", "1"},
		{"contradictory result then recovery", []commandRecord{start("a"), end("a", &fail), end("a", &success), start("b"), end("b", &success)}, model.FindingNotTriggered, "0", "0"},
		{"missing completion despite later success", []commandRecord{start("a"), start("b"), end("b", &success)}, model.FindingInsufficientEvidence, "0", "0"},
		{"identity conflict despite later success", []commandRecord{start("a"), {invocation: "a", phase: model.CommandCompleted, command: "go vet ./...", dir: "/workspace", exit: &fail}, start("b"), end("b", &success)}, model.FindingInsufficientEvidence, "0", "0"},
		{"end before start despite later success", []commandRecord{end("a", nil), start("a"), start("b"), end("b", &success)}, model.FindingInsufficientEvidence, "0", "0"},
		{"unknown rerun result", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", nil)}, model.FindingInsufficientEvidence, "0", "0"},
		{"contradictory recovery", []commandRecord{start("a"), end("a", &fail), start("b"), end("b", &success), end("b", &fail)}, model.FindingInsufficientEvidence, "0", "0"},
		{"contradictory failure", []commandRecord{start("a"), end("a", &fail), end("a", &success)}, model.FindingInsufficientEvidence, "0", "0"},
		{"gap after", []commandRecord{start("a"), end("a", &fail), {gap: true}}, model.FindingInsufficientEvidence, "0", "0"},
		{"gap before", []commandRecord{{gap: true}, start("a"), end("a", &fail)}, model.FindingTriggered, "1", "0"},
		{"gap during", []commandRecord{start("a"), {gap: true}, end("a", &fail)}, model.FindingTriggered, "1", "0"},
		{"malformed after", []commandRecord{start("a"), end("a", &fail), {malformed: true}}, model.FindingInsufficientEvidence, "0", "0"},
		{"recovery after gap", []commandRecord{start("a"), end("a", &fail), {gap: true}, start("b"), end("b", &success)}, model.FindingInsufficientEvidence, "0", "1"},
		{"independent trigger", []commandRecord{start("a"), end("a", &fail), {gap: true}, start("b"), end("b", &fail)}, model.FindingTriggered, "1", "0"},
		{"unrelated unknown command", []commandRecord{start("a"), end("a", &fail), {invocation: "other", phase: model.CommandStarted, command: "go vet ./...", dir: "/workspace"}}, model.FindingTriggered, "1", "0"},
		{"unrelated orphan result", []commandRecord{start("a"), end("a", &fail), {invocation: "other", phase: model.CommandCompleted, command: "go vet ./...", dir: "/workspace", exit: &success}}, model.FindingTriggered, "1", "0"},
		{"other directory", []commandRecord{start("a"), end("a", &fail), {invocation: "other", phase: model.CommandStarted, command: "go test ./...", dir: "/elsewhere"}}, model.FindingTriggered, "1", "0"},
		{"orphan recovery", []commandRecord{start("a"), end("a", &fail), end("orphan", &success)}, model.FindingInsufficientEvidence, "0", "0"},
		{"unknown start", []commandRecord{end("orphan", &fail)}, model.FindingInsufficientEvidence, "0", "0"},
		{"end before start", []commandRecord{end("a", &fail), start("a")}, model.FindingInsufficientEvidence, "0", "0"},
		{"duplicate result", []commandRecord{start("a"), end("a", &fail), end("a", &fail)}, model.FindingTriggered, "1", "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := openImportStore(t)
			batch := commandBatch(tt.records)
			if err := store.CommitBatch(ctx, batch); err != nil {
				t.Fatal(err)
			}
			got, err := analysis.AssessUnresolvedVerification(ctx, batch.Session.ID, store)
			if err != nil || got.State != tt.state || got.Metadata["unresolved"] != tt.unresolved || got.Metadata["recovered"] != tt.recovered {
				t.Fatalf("assessment = %#v, %v", got, err)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestInterpretationGapBoundariesRoundTripAndLimitRecovery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		boundary model.InterpretationBoundary
		state    model.FindingState
	}{
		{"before final result", "", model.FindingTriggered},
		{"after final result", model.InterpretationAfterRecord, model.FindingInsufficientEvidence},
		{"unlocated", model.InterpretationUnlocated, model.FindingInsufficientEvidence},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := openImportStore(t)
			fail := 1
			batch := commandBatch([]commandRecord{{invocation: "a", phase: model.CommandStarted, command: "go test ./...", dir: "/workspace"}, {invocation: "a", phase: model.CommandCompleted, exit: &fail}})
			raw := batch.RawRecords[1].Ref.ID
			batch.RecordDiagnostics = []model.RecordDiagnostic{{RawRecordID: raw, Diagnostic: model.Diagnostic{Code: "sequence-gap", Severity: model.SeverityWarning, Message: "Missing source-order evidence", InterpretationReason: model.InterpretationIncompleteSourceSequence, InterpretationBoundary: tc.boundary}}}
			if err := store.CommitBatch(ctx, batch); err != nil {
				t.Fatal(err)
			}
			if err := store.CommitBatch(ctx, batch); err != nil {
				t.Fatal(err)
			}
			diagnostics, err := store.RecordDiagnostics(ctx, batch.Session.ID)
			if err != nil || len(diagnostics) != 1 || diagnostics[0].Diagnostic.InterpretationBoundary != tc.boundary {
				t.Fatalf("diagnostics = %#v, %v", diagnostics, err)
			}
			page, err := store.Diagnostics(ctx, batch.Session.ID, nil, 10)
			if err != nil || len(page.Diagnostics) != 1 || page.Diagnostics[0].InterpretationBoundary != tc.boundary {
				t.Fatalf("diagnostic read = %#v, %v", page, err)
			}
			finding, err := analysis.AssessUnresolvedVerification(ctx, batch.Session.ID, store)
			if err != nil || finding.State != tc.state {
				t.Fatalf("assessment = %#v, %v", finding, err)
			}
		})
	}
}
