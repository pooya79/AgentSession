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
	"github.com/pooya79/AgentSession/internal/projection"
)

func TestStreamedFindingsIncrementalRebuildAndReconciliation(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	fixture := filepath.Join("..", "adapter", "codex", "testdata", "streamed_commands.jsonl")
	data, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	prefix := bytes.Join(lines[:3], nil)
	source := copyFixture(t, fixture, filepath.Join(root, "sources", "streamed.jsonl"))
	writeSource := func(content []byte) {
		t.Helper()
		if err := os.WriteFile(source, content, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeSource(append(append([]byte(nil), prefix...), lines[3][:len(lines[3])/2]...))
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: source}}}
	paths := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(ctx, RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	importAndRead := func(session model.SessionID) FindingsPage {
		t.Helper()
		before := hashFile(t, source)
		if _, err := runtime.DiscoverAndImport(ctx); err != nil {
			t.Fatal(err)
		}
		if before != hashFile(t, source) {
			t.Fatal("import modified source")
		}
		page, err := runtime.SessionFindings(ctx, session)
		if err != nil || page.State != EvidenceComplete || len(page.Assessments) != 2 {
			t.Fatalf("findings = %#v, %v", page, err)
		}
		return page
	}
	initial := importAndRead("streamed-commands")
	if initial.Assessments[0].State != model.FindingInsufficientEvidence || initial.Assessments[0].Metadata["invocations"] != "1" {
		t.Fatalf("pending = %#v", initial)
	}
	firstPage, err := runtime.Timeline(ctx, TimelineRequest{SessionID: "streamed-commands", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	writeSource(data)
	completed := importAndRead("streamed-commands")
	f := completed.Assessments[0]
	if f.State != model.FindingTriggered || f.Metadata["invocations"] != "4" || f.Metadata["failures"] != "2" || f.Metadata["recovered"] != "1" || f.Metadata["uncertainties"] != "1" {
		t.Fatalf("completed = %#v", f)
	}
	all, err := runtime.Timeline(ctx, TimelineRequest{SessionID: "streamed-commands", Limit: 100})
	if err != nil || len(all.Events) < len(firstPage.Events) || !reflect.DeepEqual(firstPage.Events, all.Events[:len(firstPage.Events)]) {
		t.Fatal("append changed existing events")
	}
	status, err := runtime.ProjectionStatus(ctx, "streamed-commands")
	if err != nil {
		t.Fatal(err)
	}
	if repeat := importAndRead("streamed-commands"); !reflect.DeepEqual(completed, repeat) {
		t.Fatal("unchanged import changed assessment")
	}
	again, err := runtime.ProjectionStatus(ctx, "streamed-commands")
	if err != nil || !reflect.DeepEqual(status.Projections, again.Projections) {
		t.Fatal("unchanged import invalidated projections")
	}
	// A late conflicting final result invalidates the earlier repetition.
	contradiction := []byte("{\"type\":\"response_item\",\"payload\":{\"type\":\"function_call_output\",\"call_id\":\"poll-first\",\"output\":\"Wall time: 0.1 seconds\\nProcess exited with code 0\\nOutput:\\nok\"}}\n")
	writeSource(append(append([]byte(nil), data...), contradiction...))
	conflicted := importAndRead("streamed-commands")
	if conflicted.Assessments[0].State != model.FindingInsufficientEvidence || conflicted.Assessments[0].Metadata["failures"] != "1" {
		t.Fatalf("contradiction = %#v", conflicted)
	}
	writeSource(prefix)
	truncated := importAndRead("streamed-commands")
	if !reflect.DeepEqual(initial, truncated) {
		t.Fatalf("truncation left stale evidence: %#v", truncated)
	}
	writeSource(data)
	restored := importAndRead("streamed-commands")
	if !reflect.DeepEqual(completed, restored) {
		t.Fatal("restoration differs from clean evidence")
	}
	uncertain, err := os.ReadFile(filepath.Join("..", "adapter", "codex", "testdata", "uncertain_commands.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	writeSource(uncertain)
	replaced := importAndRead("uncertain-commands")
	if replaced.Assessments[0].State != model.FindingInsufficientEvidence || replaced.Assessments[0].Metadata["failures"] != "0" {
		t.Fatalf("uncertain replacement = %#v", replaced)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RebuildProjections(ctx, "uncertain-commands", "findings"); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		status, err := runtime.ProjectionStatus(waitCtx, "uncertain-commands")
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
	rebuilt, err := runtime.SessionFindings(ctx, "uncertain-commands")
	if err != nil || !reflect.DeepEqual(replaced, rebuilt) {
		t.Fatalf("offline rebuild = %#v, %v", rebuilt, err)
	}
}

func TestFindingsRebuildAfterSourceDisappears(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := copyFixture(t, filepath.Join("..", "adapter", "codex", "testdata", "go_verification.jsonl"), filepath.Join(root, "sources", "commands.jsonl"))
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
	if err != nil || first.State != EvidenceComplete || len(first.Assessments) != 2 || first.Assessments[0].State != model.FindingTriggered {
		t.Fatalf("assessment=%#v, %v", first, err)
	}
	if first.Assessments[0].RuleID != "unresolved_failed_verification" || first.Assessments[0].Metadata["invocations"] != "8" || first.Assessments[0].Metadata["unresolved"] != "1" || first.Assessments[0].Metadata["recovered"] != "2" {
		t.Fatal("linked duplicate counted as another attempt")
	}
	var references []model.EventID
	for _, f := range first.Assessments {
		references = append(references, f.EventIDs...)
	}
	for _, id := range references {
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

func TestExplicitVerificationIncrementalStates(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	data, err := os.ReadFile(filepath.Join("..", "adapter", "codex", "testdata", "go_verification.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	source := filepath.Join(root, "verification.jsonl")
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceCodex, Path: source}}}
	paths := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(ctx, RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	var identity model.FindingID
	for _, tt := range []struct {
		name                  string
		lines                 int
		state                 model.FindingState
		unresolved, recovered string
	}{
		{"no checks", 1, model.FindingNotApplicable, "0", "0"},
		{"pending", 2, model.FindingInsufficientEvidence, "0", "0"},
		{"failed", 4, model.FindingTriggered, "1", "0"},
		{"recovered", 8, model.FindingNotTriggered, "0", "1"},
		{"reopened", len(lines), model.FindingTriggered, "1", "2"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := os.WriteFile(source, bytes.Join(lines[:tt.lines], nil), 0600); err != nil {
				t.Fatal(err)
			}
			before := hashFile(t, source)
			if _, err := runtime.DiscoverAndImport(ctx); err != nil {
				t.Fatal(err)
			}
			if before != hashFile(t, source) {
				t.Fatal("import modified evidence")
			}
			page, err := runtime.SessionFindings(ctx, "go-verification")
			if err != nil || page.State != EvidenceComplete || len(page.Assessments) != 2 {
				t.Fatalf("findings = %#v, %v", page, err)
			}
			var f model.Finding
			for _, assessment := range page.Assessments {
				if assessment.RuleID == "unresolved_failed_verification" {
					f = assessment
				}
			}
			if f.State != tt.state || f.Metadata["unresolved"] != tt.unresolved || f.Metadata["recovered"] != tt.recovered {
				t.Fatalf("assessment = %#v", f)
			}
			if identity != "" && identity != f.ID {
				t.Fatal("assessment identity changed after append")
			}
			identity = f.ID
			if _, err := runtime.DiscoverAndImport(ctx); err != nil {
				t.Fatal(err)
			}
			again, err := runtime.SessionFindings(ctx, "go-verification")
			if err != nil || !reflect.DeepEqual(page, again) {
				t.Fatal("duplicate import changed assessment")
			}
		})
	}
}
