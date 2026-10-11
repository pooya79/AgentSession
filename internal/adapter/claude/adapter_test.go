package claude

import (
	"context"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
)

func TestProbeTreatsClaudeStructuralRecordsAsCertain(t *testing.T) {
	tests := map[string]string{
		"system variant":          `{"type":"system"}`,
		"progress variant":        `{"type":"progress"}`,
		"queue operation variant": `{"type":"queue-operation"}`,
		"session ID":              `{"type":"future-record","sessionId":"claude-session"}`,
		"sidechain field":         `{"type":"future-record","isSidechain":false}`,
	}
	for name, record := range tests {
		t.Run(name, func(t *testing.T) {
			probe, err := New().Probe(context.Background(), bytesSource(name, []byte(record+"\n")))
			if err != nil || probe.Confidence != importer.ProbeCertain {
				t.Fatalf("Probe() = %#v, %v", probe, err)
			}
		})
	}
}

func TestAgentTaskLogProbesAsClaude(t *testing.T) {
	source := bytesSource("agent-task-log", []byte(`{"type":"started","agentId":"agent-one","key":"task"}`+"\n"))
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain {
		t.Fatalf("probe confidence = %d", probe.Confidence)
	}
}
