package app

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pooya79/AgentSession/internal/discovery"
	"github.com/pooya79/AgentSession/internal/model"
)

func TestOpenCodeFindingsReconciliationResumeAndOfflineRebuild(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "sources", "opencode.db")
	createOpenCodeFixture(t, filepath.Join("..", "adapter", "opencode", "testdata", "findings_evidence_v1.sql"), source)
	inputs := discovery.Inputs{FileSystem: discovery.OSFileSystem{}, HomeDir: root, WorkingDir: root, GOOS: "linux", ExplicitPaths: []discovery.ConfiguredPath{{Kind: discovery.SourceOpenCode, Path: source}}}
	paths := PathInputs{GOOS: "linux", HomeDir: root, WorkingDir: root}
	runtime, err := OpenRuntime(ctx, RuntimeConfig{DataDir: filepath.Join(root, "data"), ConfigDir: filepath.Join(root, "config"), PathInputs: &paths, DiscoveryInputs: &inputs})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Shutdown(context.Background()) })
	mutate := func(query string, args ...any) {
		t.Helper()
		db, err := sql.Open("sqlite", source)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var id model.SessionID
	read := func() FindingsPage {
		t.Helper()
		before := hashFile(t, source)
		if _, err := runtime.DiscoverAndImport(ctx); err != nil {
			t.Fatal(err)
		}
		if hashFile(t, source) != before {
			t.Fatal("import modified source")
		}
		if id == "" {
			sessions, err := runtime.ListSessions(ctx, ListSessionsRequest{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range sessions.Sessions {
				if s.Title == "durable" {
					id = s.ID
				}
			}
			if id == "" {
				t.Fatal("durable session missing")
			}
		}
		page, err := runtime.SessionFindings(ctx, id)
		if err != nil || page.State != EvidenceComplete || len(page.Assessments) != 3 {
			t.Fatalf("findings = %#v, %v", page, err)
		}
		return page
	}
	rule := func(page FindingsPage, name string) model.Finding {
		t.Helper()
		for _, f := range page.Assessments {
			if f.RuleID == name {
				return f
			}
		}
		t.Fatalf("missing rule %s", name)
		return model.Finding{}
	}
	initial := read()
	if f := rule(initial, "missing_verification"); f.State != model.FindingTriggered || f.Metadata["missing_changes"] != "1" {
		t.Fatalf("initial = %#v", f)
	}
	status, err := runtime.ProjectionStatus(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if again := read(); !reflect.DeepEqual(initial, again) {
		t.Fatal("unchanged findings differ")
	}
	unchanged, err := runtime.ProjectionStatus(ctx, id)
	if err != nil || !reflect.DeepEqual(status.Projections, unchanged.Projections) {
		t.Fatal("unchanged import invalidated projection")
	}
	// Duplicate command and change results do not add invocations or move edits.
	mutate(`INSERT INTO event SELECT 'duplicate-command',aggregate_id,15,type,data,extra FROM event WHERE id='ev_04'; INSERT INTO event SELECT 'duplicate-change',aggregate_id,16,type,data,extra FROM event WHERE id='ev_14'; UPDATE event_sequence SET seq=16 WHERE aggregate_id='durable'`)
	if duplicate := read(); !reflect.DeepEqual(initial, duplicate) {
		t.Fatalf("duplicate results changed findings: %#v", duplicate)
	}
	// Conflicting linked exits remove the claimed historical repetition.
	mutate(`INSERT INTO event VALUES ('contradiction','durable',17,'session.next.tool.success','{"callID":"test-first","content":[],"structured":{"exit":0,"truncated":false}}',x''); UPDATE event_sequence SET seq=17 WHERE aggregate_id='durable'`)
	if f := rule(read(), "repeated_command_failures"); f.State != model.FindingInsufficientEvidence {
		t.Fatalf("conflicting results = %#v", f)
	}
	// Truncation reconciles old results, preserving the logical identity.
	mutate(`DELETE FROM event WHERE aggregate_id='durable' AND seq>14; UPDATE event_sequence SET seq=14 WHERE aggregate_id='durable'`)
	if restored := read(); !reflect.DeepEqual(initial, restored) {
		t.Fatal("reconciliation did not restore clean evidence")
	}
	// Without an alternative history, incomplete durable ordering must disclose
	// potential intervening recovery and verification rather than infer absence.
	mutate(`DELETE FROM session_message WHERE session_id='durable'; DELETE FROM part WHERE session_id='durable'; DELETE FROM message WHERE session_id='durable'; UPDATE event_sequence SET seq=99 WHERE aggregate_id='durable'`)
	for _, f := range read().Assessments {
		if f.RuleID == "repeated_command_failures" {
			if f.State != model.FindingTriggered || f.Metadata["recovered"] != "1" {
				t.Fatalf("missing tail erased established repetition: %#v", f)
			}
		} else if f.State != model.FindingInsufficientEvidence {
			t.Fatalf("incomplete durable sequence inferred absence: %#v", f)
		}
	}
	tailGap := read()
	if again := read(); !reflect.DeepEqual(tailGap, again) {
		t.Fatal("unchanged incomplete sequence is not idempotent")
	}
	mutate(`UPDATE event_sequence SET seq=1 WHERE aggregate_id='durable'`)
	if f := rule(read(), "repeated_command_failures"); f.State != model.FindingInsufficientEvidence {
		t.Fatalf("marker-only change retained located gap: %#v", f)
	}
	mutate(`UPDATE event_sequence SET seq=99 WHERE aggregate_id='durable'; INSERT INTO event VALUES ('tail-text','durable',15,'session.next.text.ended','{"text":"still receiving evidence"}',x'')`)
	if f := rule(read(), "repeated_command_failures"); f.State != model.FindingTriggered || f.Metadata["uncertainties"] != "1" {
		t.Fatalf("incomplete append retained stale tail diagnostic: %#v", f)
	}
	mutate(`DELETE FROM event WHERE id='tail-text'`)
	mutate(`INSERT INTO session_message VALUES ('alternative-sequenced','durable','system',1,1,1,'{"text":"alternative history"}'); UPDATE event_sequence SET seq=14 WHERE aggregate_id='durable'`)
	// A start imported separately from its result survives incremental resume.
	mutate(`INSERT INTO event VALUES ('resume-start','durable',15,'session.next.tool.called','{"callID":"resume","tool":"bash","input":{"command":"go test ./...","workdir":"/workspace/example"}}',x''); UPDATE event_sequence SET seq=15 WHERE aggregate_id='durable'`)
	pending := read()
	if f := rule(pending, "missing_verification"); f.State != model.FindingInsufficientEvidence {
		t.Fatalf("pending check = %#v", f)
	}
	mutate(`INSERT INTO event VALUES ('resume-end','durable',16,'session.next.tool.success','{"callID":"resume","content":[],"structured":{"exit":1,"truncated":false}}',x''); UPDATE event_sequence SET seq=16 WHERE aggregate_id='durable'`)
	completed := read()
	if f := rule(completed, "missing_verification"); f.State != model.FindingNotTriggered || f.Metadata["verified_changes"] != "3" {
		t.Fatalf("resumed completion = %#v", f)
	}
	// Replacement chooses exactly the populated sequenced alternative history.
	mutate(`UPDATE event_sequence SET seq=99 WHERE aggregate_id='durable'`)
	fallback := read()
	for _, f := range fallback.Assessments {
		if f.State != model.FindingNotApplicable {
			t.Fatalf("generation replacement mixed evidence: %#v", f)
		}
	}
	mutate(`UPDATE event_sequence SET seq=16 WHERE aggregate_id='durable'`)
	if restored := read(); !reflect.DeepEqual(completed, restored) {
		t.Fatal("generation restoration differs")
	}
	// Oversized normalized command/output remains intact but limits analysis.
	output := strings.Repeat("sanitized output ", 1<<16)
	mutate(`INSERT INTO event VALUES ('large-start','durable',17,'session.next.tool.called',json_object('callID','large','tool','bash','input',json_object('command',?,'workdir','/workspace/example')),x'')`, strings.Repeat("x", 17000))
	mutate(`INSERT INTO event VALUES ('large-end','durable',18,'session.next.tool.success',json_object('callID','large','content',json_array(json_object('type','text','text',?)),'structured',json_object('exit',0,'truncated',json('false'))),x'')`, output)
	mutate(`UPDATE event_sequence SET seq=18 WHERE aggregate_id='durable'`)
	large := read()
	if f := rule(large, "missing_verification"); f.Metadata["verified_changes"] != "3" {
		t.Fatalf("oversized evidence erased supported verification: %#v", f)
	}
	timeline, err := runtime.Timeline(ctx, TimelineRequest{SessionID: id, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	foundLarge := false
	for _, event := range timeline.Events {
		detail, err := runtime.EventDetail(ctx, EventDetailRequest{SessionID: id, EventID: event.ID, IncludePayload: true})
		if err != nil {
			t.Fatal(err)
		}
		if c, ok := detail.Payload.(model.CommandData); ok && c.InvocationID == "large" && c.Phase == model.CommandCompleted {
			if c.Output != output {
				t.Fatal("large normalized output did not round-trip")
			}
			foundLarge = true
		}
	}
	if !foundLarge {
		t.Fatal("large completion missing")
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.RebuildProjections(ctx, id, "findings"); err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		status, err := runtime.ProjectionStatus(waitCtx, id)
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
	rebuilt, err := runtime.SessionFindings(ctx, id)
	if err != nil || !reflect.DeepEqual(large, rebuilt) {
		t.Fatalf("offline rebuild differs: %#v, %v", rebuilt, err)
	}
}
