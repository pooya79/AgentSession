package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestMoveConfigurationFindingsAppendAndOfflineRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fixture := filepath.Join("..", "adapter", "codex", "testdata", "moves_configuration.jsonl")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	source := copyFixture(t, fixture, filepath.Join(root, "sources", "scope.jsonl"))
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: source}}}
	paths := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(ctx, RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	read := func() FindingsPage {
		t.Helper()
		page, err := runtime.SessionFindings(ctx, "move-configuration")
		if err != nil || page.State != EvidenceComplete || len(page.Assessments) != 3 {
			t.Fatalf("findings = %#v, %v", page, err)
		}
		return page
	}
	missing := func(page FindingsPage) model.Finding {
		t.Helper()
		for _, f := range page.Assessments {
			if f.RuleID == "missing_verification" {
				return f
			}
		}
		t.Fatal("missing rule absent")
		return model.Finding{}
	}
	importContent := func(content []byte) FindingsPage {
		t.Helper()
		if err := os.WriteFile(source, content, 0600); err != nil {
			t.Fatal(err)
		}
		before := hashFile(t, source)
		if _, err := runtime.DiscoverAndImport(ctx); err != nil {
			t.Fatal(err)
		}
		if hashFile(t, source) != before {
			t.Fatal("source mutated")
		}
		return read()
	}
	narrow := importContent(bytes.Join(lines[:5], nil))
	f := missing(narrow)
	if f.State != model.FindingTriggered || f.Metadata["missing_changes"] != "3" || f.Metadata["move_origins_missing"] != "1" || f.Metadata["move_destinations_verified"] != "1" || f.Metadata["configuration_scopes"] != "2" {
		t.Fatalf("narrow scope = %#v", f)
	}
	pending := importContent(bytes.Join(lines[:6], nil))
	if missing(pending).State != model.FindingInsufficientEvidence {
		t.Fatalf("pending scope = %#v", pending)
	}
	complete := importContent(data)
	if missing(complete).State != model.FindingNotTriggered || missing(complete).Metadata["verified_changes"] != "4" {
		t.Fatalf("whole module = %#v", complete)
	}
	if again := importContent(data); !reflect.DeepEqual(complete, again) {
		t.Fatal("unstable unchanged import")
	}
	if f.ID != missing(complete).ID {
		t.Fatal("assessment identity changed after append")
	}
	// Workspace membership cannot be recovered from a path or command directory.
	workspace := []byte(`{"type":"event_msg","payload":{"type":"patch_apply_end","call_id":"workspace","stdout":"","stderr":"","success":true,"status":"completed","changes":{"go.work":{"type":"add","content":"go 1.26\nuse ."}}}}` + "\n")
	uncertain := importContent(append(append([]byte(nil), data...), workspace...))
	if missing(uncertain).State != model.FindingInsufficientEvidence || !strings.Contains(missing(uncertain).Explanation, "Workspace membership") {
		t.Fatalf("workspace scope = %#v", uncertain)
	}
	if restored := importContent(data); !reflect.DeepEqual(complete, restored) {
		t.Fatal("truncation retained workspace uncertainty")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RebuildProjections(ctx, "move-configuration", "findings"); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		status, err := runtime.ProjectionStatus(waitCtx, "move-configuration")
		if err != nil {
			t.Fatal(err)
		}
		if !status.Active {
			break
		}
		select {
		case <-waitCtx.Done():
			t.Fatal(waitCtx.Err())
		case <-tick.C:
		}
	}
	if rebuilt := read(); !reflect.DeepEqual(complete, rebuilt) {
		t.Fatal("offline rebuild changed scope assessment")
	}
}
