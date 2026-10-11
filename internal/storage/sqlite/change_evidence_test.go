package sqlite

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestStoredConfirmedChangesReconcileResults(t *testing.T) {
	tests := []struct {
		name      string
		patches   []model.PatchData
		state     model.FindingState
		confirmed string
	}{
		{"confirmed", []model.PatchData{confirmedPatch("a", "core.go")}, model.FindingTriggered, "1"},
		{"duplicate completion", []model.PatchData{confirmedPatch("a", "core.go"), confirmedPatch("a", "core.go")}, model.FindingTriggered, "1"},
		{"requested only", []model.PatchData{{ChangeID: "a", Status: model.ChangeRequested}}, model.FindingInsufficientEvidence, "0"},
		{"failed only", []model.PatchData{{ChangeID: "a", Status: model.ChangeFailed}}, model.FindingNotApplicable, "0"},
		{"noop only", []model.PatchData{{ChangeID: "a", Status: model.ChangeNoOp}}, model.FindingNotApplicable, "0"},
		{"request then confirmed", []model.PatchData{{ChangeID: "a", Status: model.ChangeRequested}, confirmedPatch("a", "core.go")}, model.FindingTriggered, "1"},
		{"contradictory result", []model.PatchData{confirmedPatch("a", "core.go"), {ChangeID: "a", Status: model.ChangeFailed}}, model.FindingInsufficientEvidence, "0"},
		{"conflicting paths", []model.PatchData{confirmedPatch("a", "core.go"), confirmedPatch("a", "other.go")}, model.FindingInsufficientEvidence, "0"},
		{"oversized retains uncertainty", []model.PatchData{confirmedPatch("a", strings.Repeat("a", 5000)+".go")}, model.FindingInsufficientEvidence, "0"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := openImportStore(t)
			batch := commandBatch(make([]commandRecord, len(tt.patches)))
			for i, p := range tt.patches {
				batch.Events[i].Data = p
				batch.Events[i].Kind = model.EventKindPatch
			}
			if err := store.CommitBatch(ctx, batch); err != nil {
				t.Fatal(err)
			}
			if err := store.CommitBatch(ctx, batch); err != nil {
				t.Fatal(err)
			}
			got, err := analysis.AssessMissingVerification(ctx, batch.Session.ID, store)
			if err != nil || got.State != tt.state || got.Metadata["confirmed_changes"] != tt.confirmed {
				t.Fatalf("assessment = %#v, %v", got, err)
			}
			again, _ := analysis.AssessMissingVerification(ctx, batch.Session.ID, store)
			if !reflect.DeepEqual(got, again) {
				t.Fatal("unstable stored assessment")
			}
			for _, event := range batch.Events {
				if p, ok := event.Data.(model.PatchData); ok && len(p.Changes) > 0 && len(p.Changes[0].Path) > 4096 {
					if p.Changes[0].Path != tt.patches[0].Changes[0].Path {
						t.Fatal("storage altered canonical path")
					}
				}
			}
		})
	}
}

func confirmedPatch(id, path string) model.PatchData {
	return model.PatchData{ChangeID: id, Status: model.ChangeConfirmed, WorkingDirectory: "/workspace", Changes: []model.FileMutationData{{Path: path, Operation: model.FileMutationUpdate}}}
}

func TestStoredChangesEvaluateBeyondReadPage(t *testing.T) {
	p := confirmedPatch("large", "core.go")
	p.Changes = nil
	for range 139 {
		p.Changes = append(p.Changes, model.FileMutationData{Path: "docs/guide.md", Operation: model.FileMutationUpdate})
	}
	p.Changes = append(p.Changes, model.FileMutationData{Path: "last.go", Operation: model.FileMutationUpdate})
	batch := commandBatch([]commandRecord{{}})
	batch.Events[0].Kind = model.EventKindPatch
	batch.Events[0].Data = p
	store := openImportStore(t)
	if err := store.CommitBatch(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	got, err := analysis.AssessMissingVerification(context.Background(), batch.Session.ID, store)
	if err != nil || got.State != model.FindingTriggered || got.Metadata["confirmed_changes"] != "140" || got.Metadata["documentation_changes"] != "139" || got.Metadata["missing_changes"] != "1" {
		t.Fatalf("paged changes = %#v, %v", got, err)
	}
}
