package claude

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
)

func TestZeroRecordResumeAndFallbackIdentitiesAreStable(t *testing.T) {
	partial := []byte(`{"type":"user","message":{"role":"user","content":"later"}`)
	first, _ := importSource(t, bytesSource("fallback-partial", partial), nil, nil)
	if len(first.records) != 0 || first.checkpoint.RecordSequence != importer.NoRecordSequence || first.session.Import.FormatVersion != "claude-code-jsonl-v1+cli-unknown" {
		t.Fatalf("partial import = %#v", first)
	}
	state := sourceState(first)
	complete := append(append([]byte(nil), partial...), []byte("}\n")...)
	second, change := importSource(t, bytesSource("fallback-partial", complete), &state.Checkpoint, &state)
	third, _ := importSource(t, bytesSource("fallback-partial", complete), nil, nil)
	if change != importer.SourceAppend || second.session.ID != third.session.ID || !reflect.DeepEqual(events(second.records), events(third.records)) {
		t.Fatal("fallback session/event identities are not stable")
	}
}

func TestVerificationAndReconciliationClassifyChanges(t *testing.T) {
	baseData, err := os.ReadFile(filepath.Join("testdata", "main.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	initial, _ := importSource(t, bytesSource("verify", baseData), nil, nil)
	state := sourceState(initial)
	tests := []struct {
		name string
		data []byte
		want importer.SourceChange
	}{
		{"unchanged", baseData, importer.SourceUnchanged},
		{"append", append(append([]byte(nil), baseData...), []byte("{\"type\":\"summary\",\"sessionId\":\"claude-session-1\",\"version\":\"2.1.3\",\"summary\":\"late\"}\n")...), importer.SourceAppend},
		{"truncated", baseData[:len(baseData)-20], importer.SourceTruncated},
		{"mutated", bytes.Replace(baseData, []byte("sanitized fixture"), []byte("sanitized fixturE"), 1), importer.SourceMutated},
		{"replaced", bytes.Replace(baseData, []byte("claude-session-1"), []byte("claude-session-9"), 1), importer.SourceReplaced},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view, err := New().Prepare(context.Background(), bytesSource("verify", tt.data))
			if err != nil {
				t.Fatal(err)
			}
			defer view.Close()
			got, err := view.Verify(context.Background(), state)
			if err != nil || got != tt.want {
				t.Fatalf("Verify() = %q, %v, want %q", got, err, tt.want)
			}
			if got == importer.SourceMutated {
				sink := &captureSink{}
				if err := view.Reconcile(context.Background(), sink); err != nil {
					t.Fatal(err)
				}
				if len(sink.records) != 5 || !bytes.Contains(sink.records[1].RawRecord.Content, []byte("fixturE")) {
					t.Fatalf("reconciled records = %d", len(sink.records))
				}
			}
		})
	}
}
