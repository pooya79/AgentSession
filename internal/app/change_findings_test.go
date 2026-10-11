package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestChangeFindingsIncrementalReconciliationAndOfflineRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fixture := filepath.Join("..", "adapter", "codex", "testdata", "changes.jsonl")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	source := copyFixture(t, fixture, filepath.Join(root, "sources", "changes.jsonl"))
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: source}}}
	paths := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(ctx, RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	read := func() FindingsPage {
		t.Helper()
		page, err := runtime.SessionFindings(ctx, "confirmed-changes")
		if err != nil || page.State != EvidenceComplete || len(page.Assessments) != 3 {
			t.Fatalf("findings = %#v, %v", page, err)
		}
		return page
	}
	missing := func(page FindingsPage) model.Finding {
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
	requestOnly := importContent(bytes.Join(lines[:2], nil))
	if missing(requestOnly).State != model.FindingInsufficientEvidence || missing(requestOnly).Metadata["confirmed_changes"] != "0" {
		t.Fatalf("request = %#v", requestOnly)
	}
	confirmed := importContent(bytes.Join(lines[:3], nil))
	if missing(confirmed).State != model.FindingTriggered || missing(confirmed).Metadata["missing_changes"] != "1" {
		t.Fatalf("confirmed = %#v", confirmed)
	}
	pending := importContent(bytes.Join(lines[:5], nil))
	if missing(pending).State != model.FindingInsufficientEvidence {
		t.Fatalf("pending = %#v", pending)
	}
	completed := importContent(data)
	if missing(completed).State != model.FindingNotTriggered || completed.Assessments[0].RuleID != "unresolved_failed_verification" || completed.Assessments[0].State != model.FindingTriggered {
		t.Fatalf("failed verification = %#v", completed)
	}
	if again := importContent(data); !reflect.DeepEqual(completed, again) {
		t.Fatal("unchanged import changed results")
	}
	duplicate := importContent(append(append([]byte(nil), data...), lines[2]...))
	if !reflect.DeepEqual(completed, duplicate) {
		t.Fatal("duplicate completion became a second/later change")
	}
	contradiction := bytes.Replace(lines[2], []byte(`"success":true,"status":"completed"`), []byte(`"success":false,"status":"failed"`), 1)
	conflicting := importContent(append(append([]byte(nil), data...), contradiction...))
	if missing(conflicting).State != model.FindingInsufficientEvidence || missing(conflicting).Metadata["confirmed_changes"] != "0" {
		t.Fatalf("contradiction = %#v", conflicting)
	}
	if truncated := importContent(bytes.Join(lines[:3], nil)); !reflect.DeepEqual(confirmed, truncated) {
		t.Fatal("truncation retained stale checks/results")
	}
	if restored := importContent(data); !reflect.DeepEqual(completed, restored) {
		t.Fatal("restored evidence differs")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RebuildProjections(ctx, "confirmed-changes", "findings"); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		status, err := runtime.ProjectionStatus(waitCtx, "confirmed-changes")
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
	if rebuilt := read(); !reflect.DeepEqual(completed, rebuilt) {
		t.Fatal("offline rebuild differs")
	}
}
