package analysis_test

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestMissingVerificationMoveScopes(t *testing.T) {
	zero := 0
	move := analysis.ChangeEvidence{EventID: "move", Position: 3, Status: model.ChangeConfirmed, Path: "new/core.go", PreviousPath: "old/core.go", Directory: "/workspace", Operation: model.FileMutationRename}
	check := func(target string, start, end int64) analysis.CommandEvidence {
		c := command(int(start), &zero)
		c.Command, c.Start, c.Completion = "go test "+target, start, end
		return c
	}
	altered := func(old, new string) analysis.ChangeEvidence { c := move; c.PreviousPath, c.Path = old, new; return c }
	tests := []struct {
		name                         string
		change                       analysis.ChangeEvidence
		commands                     []analysis.CommandEvidence
		state                        model.FindingState
		missing, verified, uncertain string
	}{
		{"both missing", move, nil, model.FindingTriggered, "2", "0", "0"},
		{"destination only", move, []analysis.CommandEvidence{check("./new", 6, 7)}, model.FindingTriggered, "1", "1", "0"},
		{"origin only", move, []analysis.CommandEvidence{check("./old", 6, 7)}, model.FindingTriggered, "1", "1", "0"},
		{"both checked", move, []analysis.CommandEvidence{check("./new", 6, 7), check("./old", 8, 9)}, model.FindingNotTriggered, "0", "2", "0"},
		{"recursive", move, []analysis.CommandEvidence{check("./...", 6, 7)}, model.FindingNotTriggered, "0", "2", "0"},
		{"interleaved", move, []analysis.CommandEvidence{check("./old", 1, 7), check("./new", 8, 9)}, model.FindingTriggered, "1", "1", "0"},
		{"unknown origin", altered("", "new/core.go"), []analysis.CommandEvidence{check("./new", 6, 7)}, model.FindingInsufficientEvidence, "0", "1", "1"},
		{"unknown origin independent missing", altered("", "new/core.go"), nil, model.FindingTriggered, "1", "0", "1"},
		{"unknown destination", altered("old/core.go", ""), []analysis.CommandEvidence{check("./old", 6, 7)}, model.FindingInsufficientEvidence, "0", "1", "1"},
		{"unknown role", altered("old/core.py", "new/core.go"), []analysis.CommandEvidence{check("./new", 6, 7)}, model.FindingInsufficientEvidence, "0", "1", "1"},
		{"documentation origin", altered("docs/guide.md", "new/core.go"), []analysis.CommandEvidence{check("./new", 6, 7)}, model.FindingNotTriggered, "0", "1", "0"},
		{"documentation destination", altered("old/core.go", "docs/guide.md"), nil, model.FindingTriggered, "1", "0", "0"},
		{"docs move", altered("docs/a.md", "docs/b.md"), nil, model.FindingNotApplicable, "0", "0", "0"},
		{"excluded origin", altered("testdata/core.go", "new/core.go"), []analysis.CommandEvidence{check("./...", 6, 7)}, model.FindingInsufficientEvidence, "0", "1", "1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := analysis.AssessMissingVerification(context.Background(), "session", changeEvidence{evidence: evidence{commands: tt.commands}, changes: []analysis.ChangeEvidence{tt.change}})
			if got.Metadata["move_origins"] != "1" || got.Metadata["move_destinations"] != "1" || !strings.Contains(got.Explanation, "origin") || !strings.Contains(got.Explanation, "destination") {
				t.Fatalf("missing move scope explanation: %#v", got)
			}
			again, _ := analysis.AssessMissingVerification(context.Background(), "session", changeEvidence{evidence: evidence{commands: tt.commands}, changes: []analysis.ChangeEvidence{tt.change}})
			if !reflect.DeepEqual(got, again) {
				t.Fatal("unstable move assessment")
			}
			if err != nil || got.State != tt.state || got.Metadata["missing_changes"] != tt.missing || got.Metadata["verified_changes"] != tt.verified || got.Metadata["uncertainties"] != tt.uncertain {
				t.Fatalf("assessment = %#v, %v", got, err)
			}
		})
	}
}

func TestMissingVerificationMoveBoundsAndCheckKinds(t *testing.T) {
	input := changeEvidence{}
	for i := range 140 {
		input.changes = append(input.changes, analysis.ChangeEvidence{EventID: model.EventID(fmt.Sprintf("move%d", i)), Position: int64(i), Status: model.ChangeConfirmed, Path: "new/core.go", PreviousPath: "old/core.go", Directory: "/workspace", Operation: model.FileMutationRename})
	}
	got, err := analysis.AssessMissingVerification(context.Background(), "session", input)
	if err != nil || got.Metadata["missing_changes"] != "280" || got.Metadata["move_origins_missing"] != "140" || got.Metadata["move_destinations_missing"] != "140" || got.Metadata["omitted_references"] != "128" || len(got.EventIDs) != 12 || len(got.Explanation) > 4096 {
		t.Fatalf("bounded move assessment = %#v, %v", got, err)
	}
	zero := 0
	build := command(2, &zero)
	build.Command = "go build ./..."
	input.changes = []analysis.ChangeEvidence{{EventID: "move", Position: 3, Status: model.ChangeConfirmed, Path: "new/core_test.go", PreviousPath: "old/core.go", Directory: "/workspace", Operation: model.FileMutationRename}}
	input.commands = []analysis.CommandEvidence{build}
	got, err = analysis.AssessMissingVerification(context.Background(), "session", input)
	if err != nil || got.Metadata["move_origins_verified"] != "1" || got.Metadata["move_destinations_missing"] != "1" {
		t.Fatalf("build excludes moved test file = %#v, %v", got, err)
	}
	input.changes[0].Status = model.ChangeRequested
	got, err = analysis.AssessMissingVerification(context.Background(), "session", input)
	if err != nil || got.State != model.FindingInsufficientEvidence || got.Metadata["confirmed_changes"] != "0" || got.Metadata["move_origins"] != "0" {
		t.Fatalf("move intent became effect = %#v, %v", got, err)
	}
}
