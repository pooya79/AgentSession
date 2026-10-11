package codex

import (
	"context"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
)

func TestProbeDetectsLegacyAndOrdinalCompositeVersions(t *testing.T) {
	tests := []struct {
		name, want string
	}{
		{"legacy.jsonl", "codex-rollout-jsonl-v1+cli-0.42.0"},
		{"ordinal.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.133.0"},
		{"current_0.145.0.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.145.0"},
		{"delayed_metadata_0.145.0.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.145.0"},
		{"malformed_future_0.145.0.jsonl", "codex-rollout-jsonl-v2-ordinal+cli-0.145.0"},
		{"malformed_unknown.jsonl", "codex-rollout-jsonl-v1+cli-unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			probe, err := New().Probe(context.Background(), fixtureSource(t, tt.name))
			if err != nil {
				t.Fatal(err)
			}
			if probe.Confidence != importer.ProbeCertain || string(probe.FormatVersion) != tt.want {
				t.Fatalf("Probe() = (%v, %q), want certain %q", probe.Confidence, probe.FormatVersion, tt.want)
			}
		})
	}
	empty, err := New().Probe(context.Background(), bytesSource("empty", nil))
	if err != nil || empty.Confidence != importer.ProbeUnsupported {
		t.Fatalf("empty Probe() = %#v, %v", empty, err)
	}
	possible, err := New().Probe(context.Background(), bytesSource("jsonl", []byte("{\"custom\":true}\n")))
	if err != nil || possible.Confidence != importer.ProbePossible {
		t.Fatalf("generic JSONL Probe() = %#v, %v", possible, err)
	}
}
