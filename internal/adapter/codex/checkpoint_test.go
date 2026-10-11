package codex

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
)

func TestVerificationClassifiesSourceChanges(t *testing.T) {
	baseSource := fixtureSource(t, "legacy.jsonl")
	baseData, _ := os.ReadFile(filepath.Join("testdata", "legacy.jsonl"))
	initial, _ := importSource(t, baseSource, nil, nil)
	state := sourceState(initial)
	tests := []struct {
		name string
		data []byte
		want importer.SourceChange
	}{
		{"unchanged", baseData, importer.SourceUnchanged},
		{"append", append(append([]byte(nil), baseData...), []byte("{\"timestamp\":\"2025-01-02T03:04:10Z\",\"type\":\"event_msg\",\"payload\":{\"type\":\"error\",\"message\":\"late\"}}\n")...), importer.SourceAppend},
		{"truncated", baseData[:len(baseData)-20], importer.SourceTruncated},
		{"mutated", bytes.Replace(baseData, []byte("Please inspect the fixture."), []byte("Please inspect the fixturE."), 1), importer.SourceMutated},
		{"replaced", bytes.Replace(baseData, []byte("01940000-0000-7000-8000-000000000001"), []byte("01940000-0000-7000-8000-000000000009"), 1), importer.SourceReplaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, err := New().Prepare(context.Background(), bytesSource(string(baseSource.ID), tt.data))
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			got, err := view.Verify(context.Background(), state)
			if err != nil || got != tt.want {
				t.Fatalf("Verify() = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestStableRepeatedImportAndFallbackSessionID(t *testing.T) {
	data := []byte("{\"timestamp\":\"bad\",\"type\":\"future\",\"payload\":{}}\n")
	first, _ := importSource(t, bytesSource("fallback", data), nil, nil)
	second, _ := importSource(t, bytesSource("fallback", data), nil, nil)
	if first.session.ID != second.session.ID || !reflect.DeepEqual(events(first.records), events(second.records)) {
		t.Fatal("fallback session or event identities are not stable")
	}
	if len(first.records[0].Diagnostics) != 1 || first.records[0].Events[0].Timestamp != nil {
		t.Fatalf("malformed timestamp evidence = %#v", first.records[0])
	}
}
