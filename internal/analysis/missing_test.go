package analysis_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/model"
)

type changeEvidence struct {
	evidence
	changes        []analysis.ChangeEvidence
	interpretation []analysis.EvidenceGap
}

func TestMissingVerificationStatesTimingAndScope(t *testing.T) {
	zero := 0
	edit := analysis.ChangeEvidence{EventID: "edit", Position: 3, Status: model.ChangeConfirmed, Path: "internal/core/core.go", Directory: "/workspace", Operation: model.FileMutationUpdate}
	check := command(2, &zero)
	check.Command = "go test ./internal/core"
	change := func(p string) analysis.ChangeEvidence { v := edit; v.Path = p; return v }
	cmd := func(text string, start, end int64) analysis.CommandEvidence {
		v := check
		v.Command = text
		v.Start = start
		v.Completion = end
		return v
	}
	pending := check
	pending.ResultEvent = ""
	pending.ExitCode = nil
	unknown := check
	unknown.ExitCode = nil
	noBase := edit
	noBase.Directory = ""
	move := edit
	move.Operation = model.FileMutationRename
	move.PreviousPath = "old/core.go"
	before, after := int64(2), int64(4)
	tests := []struct {
		name     string
		changes  []analysis.ChangeEvidence
		commands []analysis.CommandEvidence
		gaps     []analysis.EvidenceGap
		state    model.FindingState
	}{
		{"empty", nil, nil, nil, model.FindingNotApplicable},
		{"missing", []analysis.ChangeEvidence{edit}, nil, nil, model.FindingTriggered},
		{"relevant", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{check}, nil, model.FindingNotTriggered},
		{"simple target is narrow", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{cmd("go test .", 6, 7)}, nil, model.FindingTriggered},
		{"recursive target", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{cmd("go vet ./internal/...", 6, 7)}, nil, model.FindingNotTriggered},
		{"recursive root", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{cmd("go build ./...", 6, 7)}, nil, model.FindingNotTriggered},
		{"prefix sibling", []analysis.ChangeEvidence{change("internal/core2/core.go")}, []analysis.CommandEvidence{check}, nil, model.FindingTriggered},
		{"check before edit", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{cmd(check.Command, 1, 2)}, nil, model.FindingTriggered},
		{"edit during check", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{cmd(check.Command, 1, 7)}, nil, model.FindingTriggered},
		{"pending check", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{pending}, nil, model.FindingInsufficientEvidence},
		{"unknown result", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{unknown}, nil, model.FindingInsufficientEvidence},
		{"unknown then known check", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{unknown, check}, nil, model.FindingNotTriggered},
		{"unsupported later command", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{cmd("make test", 6, 7)}, nil, model.FindingInsufficientEvidence},
		{"unsupported earlier command", []analysis.ChangeEvidence{edit}, []analysis.CommandEvidence{cmd("make test", 1, 2)}, nil, model.FindingTriggered},
		{"test excluded by build", []analysis.ChangeEvidence{change("internal/core/core_test.go")}, []analysis.CommandEvidence{cmd("go build ./...", 6, 7)}, nil, model.FindingTriggered},
		{"vet includes test files", []analysis.ChangeEvidence{change("internal/core/core_test.go")}, []analysis.CommandEvidence{cmd("go vet ./...", 6, 7)}, nil, model.FindingNotTriggered},
		{"docs only", []analysis.ChangeEvidence{change("docs/guide.md")}, nil, nil, model.FindingNotApplicable},
		{"mixed docs code", []analysis.ChangeEvidence{change("README.md"), edit}, nil, nil, model.FindingTriggered},
		{"unknown language", []analysis.ChangeEvidence{change("main.py")}, nil, nil, model.FindingInsufficientEvidence},
		{"unknown role", []analysis.ChangeEvidence{change("settings.conf")}, nil, nil, model.FindingInsufficientEvidence},
		{"module unknown boundary", []analysis.ChangeEvidence{change("go.mod")}, []analysis.CommandEvidence{cmd("go test ./...", 6, 7)}, nil, model.FindingInsufficientEvidence},
		{"workspace unknown boundary", []analysis.ChangeEvidence{change("go.work")}, nil, nil, model.FindingInsufficientEvidence},
		{"move scopes uncertain", []analysis.ChangeEvidence{move}, []analysis.CommandEvidence{cmd("go test ./...", 6, 7)}, nil, model.FindingInsufficientEvidence},
		{"path base unknown", []analysis.ChangeEvidence{noBase}, nil, nil, model.FindingInsufficientEvidence},
		{"absolute path establishes scope", []analysis.ChangeEvidence{change("/workspace/internal/core/core.go")}, []analysis.CommandEvidence{check}, nil, model.FindingNotTriggered},
		{"traversal uncertain", []analysis.ChangeEvidence{change("../other/core.go")}, nil, nil, model.FindingInsufficientEvidence},
		{"known Go exclusion", []analysis.ChangeEvidence{change("testdata/main.go")}, nil, nil, model.FindingInsufficientEvidence},
		{"oversized path", []analysis.ChangeEvidence{change(strings.Repeat("a", 4097) + ".go")}, nil, nil, model.FindingInsufficientEvidence},
		{"gap before edit", []analysis.ChangeEvidence{edit}, nil, []analysis.EvidenceGap{{Position: &before}}, model.FindingTriggered},
		{"gap after edit", []analysis.ChangeEvidence{edit}, nil, []analysis.EvidenceGap{{Position: &after}}, model.FindingInsufficientEvidence},
		{"gap from earlier invocation", []analysis.ChangeEvidence{edit}, nil, []analysis.EvidenceGap{{Position: &after, InvocationStart: &before}}, model.FindingTriggered},
		{"unlocated gap", []analysis.ChangeEvidence{edit}, nil, []analysis.EvidenceGap{{}}, model.FindingInsufficientEvidence},
		{"independent missing wins", []analysis.ChangeEvidence{edit, change("main.py")}, nil, nil, model.FindingTriggered},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := changeEvidence{evidence: evidence{commands: tt.commands}, changes: tt.changes, interpretation: tt.gaps}
			got, err := analysis.AssessMissingVerification(context.Background(), "session", input)
			if err != nil || got.State != tt.state {
				t.Fatalf("assessment = %#v, %v", got, err)
			}
			if err := got.Validate(); err != nil {
				t.Fatal(err)
			}
			again, _ := analysis.AssessMissingVerification(context.Background(), "session", input)
			if !reflect.DeepEqual(got, again) {
				t.Fatal("unstable assessment")
			}
		})
	}
}

func TestMissingVerificationBoundsAndCancellation(t *testing.T) {
	input := changeEvidence{}
	for i := range 140 {
		input.changes = append(input.changes, analysis.ChangeEvidence{EventID: model.EventID(fmt.Sprintf("edit%d", i)), Position: int64(i), Status: model.ChangeConfirmed, Path: "core.go", Directory: "/workspace", Operation: model.FileMutationUpdate})
	}
	got, err := analysis.AssessMissingVerification(context.Background(), "session", input)
	if err != nil || got.Metadata["missing_changes"] != "140" || got.Metadata["omitted_references"] != "128" || len(got.EventIDs) != 12 || !strings.Contains(got.Explanation, "Representative event links only") {
		t.Fatalf("bounded = %#v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := analysis.AssessMissingVerification(ctx, "session", input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v", err)
	}
}

func (e changeEvidence) WalkChanges(ctx context.Context, _ model.SessionID, visit func(analysis.ChangeEvidence) error) error {
	for _, c := range e.changes {
		if err := visit(c); err != nil {
			return err
		}
	}
	return ctx.Err()
}
func (e changeEvidence) WalkEvidenceGaps(ctx context.Context, _ model.SessionID, visit func(analysis.EvidenceGap) error) error {
	for _, g := range e.interpretation {
		if err := visit(g); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func TestMissingVerificationCountsFailedRelevantCheck(t *testing.T) {
	fail := 1
	check := command(2, &fail)
	check.Command = "go test ./internal/core"
	input := changeEvidence{evidence: evidence{commands: []analysis.CommandEvidence{check}}, changes: []analysis.ChangeEvidence{{EventID: "edit", Position: 3, Status: model.ChangeConfirmed, Path: "internal/core/core.go", Directory: "/workspace", Operation: model.FileMutationUpdate}}}
	got, err := analysis.AssessMissingVerification(context.Background(), "session", input)
	if err != nil || got.State != model.FindingNotTriggered || got.Metadata["verified_changes"] != "1" {
		t.Fatalf("assessment = %#v, %v", got, err)
	}
	unresolved, err := analysis.AssessUnresolvedVerification(context.Background(), "session", input)
	if err != nil || unresolved.State != model.FindingTriggered {
		t.Fatalf("failed check = %#v, %v", unresolved, err)
	}
}
