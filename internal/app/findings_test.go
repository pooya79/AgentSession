package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

func TestFindingsRebuildAfterSourceDisappears(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := copyFixture(t, filepath.Join("..", "adapter", "codex", "testdata", "explicit_commands.jsonl"), filepath.Join(root, "sources", "commands.jsonl"))
	before := hashFile(t, source)
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: source}}}
	paths := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(ctx, RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	if _, err := runtime.DiscoverAndImport(ctx); err != nil {
		t.Fatal(err)
	}
	if before != hashFile(t, source) {
		t.Fatal("source was modified")
	}
	sessions, err := runtime.ListSessions(ctx, ListSessionsRequest{})
	if err != nil || len(sessions.Sessions) != 1 {
		t.Fatalf("sessions=%#v, %v", sessions, err)
	}
	session := sessions.Sessions[0].ID
	first, err := runtime.SessionFindings(ctx, session)
	if err != nil || first.State != EvidenceComplete || len(first.Assessments) != 1 || first.Assessments[0].State != model.FindingTriggered {
		t.Fatalf("assessment=%#v, %v", first, err)
	}
	if first.Assessments[0].Metadata["invocations"] != "3" {
		t.Fatal("linked duplicate counted as another attempt")
	}
	for _, id := range first.Assessments[0].EventIDs {
		focused, err := runtime.Timeline(ctx, TimelineRequest{SessionID: session, FocusedEvent: id, Limit: 1})
		if err != nil || focused.FocusedEvent != id {
			t.Fatalf("focused evidence=%#v, %v", focused, err)
		}
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	kind := projection.KindFindings
	if err := runtime.projections.controller.Rebuild(ctx, session, &kind); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := runtime.SessionFindings(ctx, session)
	if err != nil || !reflect.DeepEqual(first, rebuilt) {
		t.Fatalf("rebuild=%#v, %v", rebuilt, err)
	}
}
