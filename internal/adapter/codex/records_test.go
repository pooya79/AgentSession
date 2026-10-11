package codex

import (
	"fmt"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
)

func TestRecordDiagnosticBoundIncludesTruncationMarker(t *testing.T) {
	diagnostics := make([]model.Diagnostic, maxRecordDiagnostics+3)
	for index := range diagnostics {
		diagnostics[index] = model.Diagnostic{
			Code: fmt.Sprintf("codex.synthetic.%d", index), Severity: model.SeverityWarning,
			Message:      "Synthetic bounded diagnostic.",
			RawRecordIDs: []model.RawRecordID{"raw-sanitized"},
		}
	}
	got := boundDiagnostics(diagnostics)
	if len(got) != maxRecordDiagnostics ||
		got[len(got)-1].Code != "codex.record.diagnostics.truncated" ||
		len(got[len(got)-1].RawRecordIDs) != 1 {
		t.Fatalf("bounded diagnostics = %#v", got)
	}
}
