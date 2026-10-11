package sqlite

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/analysis"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

func TestFindingsPublicationAndRebuild(t *testing.T) {
	ctx := context.Background()
	store := openImportStore(t)
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	builder, err := analysis.NewBuilder(store)
	if err != nil {
		t.Fatal(err)
	}
	registrations := []projection.Registration{}
	for _, definition := range projection.DefaultDefinitions() {
		r := projection.Registration{Definition: definition}
		if definition.Kind == projection.KindFindings {
			r.Builder = builder
		}
		registrations = append(registrations, r)
	}
	manager, err := projection.NewManager(ctx, store, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Project(ctx, importer.ProjectionRequest{SessionID: batch.Session.ID}); err != nil {
		t.Fatal(err)
	}
	first, err := store.SessionFindings(ctx, batch.Session.ID)
	if err != nil || !first.Usable || len(first.Assessments) != 2 || first.Assessments[0].State != model.FindingNotApplicable || first.Assessments[1].RuleID != analysis.UnresolvedVerificationRule || first.Assessments[1].State != model.FindingNotApplicable {
		t.Fatalf("findings = %#v, %v", first, err)
	}
	kind := projection.KindFindings
	if err := store.Invalidate(ctx, batch.Session.ID, &kind); err != nil {
		t.Fatal(err)
	}
	stale, err := store.SessionFindings(ctx, batch.Session.ID)
	if err != nil || stale.Usable || len(stale.Assessments) != 0 {
		t.Fatalf("stale = %#v, %v", stale, err)
	}
	if err := manager.Project(ctx, importer.ProjectionRequest{SessionID: batch.Session.ID}); err != nil {
		t.Fatal(err)
	}
	again, _ := store.SessionFindings(ctx, batch.Session.ID)
	if !reflect.DeepEqual(first.Assessments, again.Assessments) {
		t.Fatal("unstable rebuild")
	}
	claim, ok, err := func() (projection.Claim, bool, error) {
		_ = store.Invalidate(ctx, batch.Session.ID, &kind)
		return store.Claim(ctx, batch.Session.ID, kind)
	}()
	if err != nil || !ok {
		t.Fatal(err)
	}
	_ = store.Invalidate(ctx, batch.Session.ID, &kind)
	if err := store.PublishFindings(ctx, claim, first.Assessments); err == nil {
		t.Fatal("superseded publication accepted")
	}
}

func TestFindingsClaimValidationCancellationAndRevisionGating(t *testing.T) {
	ctx := context.Background()
	store := openImportStore(t)
	code := 1
	records := []commandRecord{{invocation: "a", phase: model.CommandStarted, command: "false", dir: "/workspace"}, {invocation: "a", phase: model.CommandCompleted, command: "false", dir: "/workspace", exit: &code}, {invocation: "b", phase: model.CommandStarted, command: "false", dir: "/workspace"}, {invocation: "b", phase: model.CommandCompleted, command: "false", dir: "/workspace", exit: &code}}
	batch := commandBatch(records)
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	builder, _ := analysis.NewBuilder(store)
	claim, ok, err := store.Claim(ctx, batch.Session.ID, projection.KindFindings)
	if err != nil || !ok {
		t.Fatal(err)
	}
	request := projection.BuildRequest{SessionID: claim.SessionID, CanonicalRevision: claim.Revision, Version: claim.Version, BuildToken: claim.RunToken, Reader: store}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := builder.Build(canceled, request); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled builder = %v", err)
	}
	pending, _ := store.SessionFindings(ctx, batch.Session.ID)
	if pending.Usable || pending.Status != "running" {
		t.Fatalf("running output = %#v", pending)
	}
	f, err := analysis.AssessRepeatedFailures(ctx, batch.Session.ID, store)
	if err != nil {
		t.Fatal(err)
	}
	v, err := analysis.AssessUnresolvedVerification(ctx, batch.Session.ID, store)
	if err != nil {
		t.Fatal(err)
	}
	foreign := v
	foreign.EventIDs = []model.EventID{model.EventID("evt_" + strings.Repeat("f", 64))}
	if err := store.PublishFindings(ctx, claim, []model.Finding{f, foreign}); err == nil {
		t.Fatal("unowned evidence accepted")
	}
	if err := builder.Build(ctx, request); err != nil {
		t.Fatal(err)
	}
	staged, _ := store.SessionFindings(ctx, batch.Session.ID)
	if staged.Usable || len(staged.Assessments) != 0 {
		t.Fatal("uncompleted output exposed")
	}
	if completed, err := store.Complete(ctx, claim); err != nil || !completed {
		t.Fatal("complete:", err)
	}
	ready, _ := store.SessionFindings(ctx, batch.Session.ID)
	if !ready.Usable || ready.Assessments[0].State != model.FindingTriggered {
		t.Fatalf("ready=%#v", ready)
	}
	// An append advances authoritative evidence and makes earlier output stale.
	records = append(records, commandRecord{invocation: "c", phase: model.CommandStarted, command: "false", dir: "/workspace"})
	appended := commandBatch(records)
	if err := store.CommitBatch(ctx, appended); err != nil {
		t.Fatal(err)
	}
	stale, _ := store.SessionFindings(ctx, batch.Session.ID)
	if !stale.Stale || stale.Usable || len(stale.Assessments) != 0 {
		t.Fatalf("stale=%#v", stale)
	}
	if err := store.PublishFindings(ctx, claim, []model.Finding{f, v}); err == nil {
		t.Fatal("old revision accepted")
	}
	// Truncation/replacement removes command evidence with its canonical events.
	replacement := testImportBatch()
	replacement.Session.Diagnostics = nil
	replacement.RecordDiagnostics = nil
	if err := reconcileBatches(ctx, store, appended.Checkpoint, replacement); err != nil {
		t.Fatal(err)
	}
	assessment, err := analysis.AssessRepeatedFailures(ctx, batch.Session.ID, store)
	if err != nil || assessment.State != model.FindingNotApplicable {
		t.Fatalf("replacement=%#v, %v", assessment, err)
	}
	deleted, err := store.DeleteSession(ctx, batch.Session.ID)
	if err != nil || !deleted {
		t.Fatal(err)
	}
	absent, err := store.SessionFindings(ctx, batch.Session.ID)
	if err != nil || absent.Found {
		t.Fatalf("deleted snapshot=%#v, %v", absent, err)
	}
}

func TestFindingsFailurePreservesCanonicalImportAndSafeDiagnostics(t *testing.T) {
	ctx := context.Background()
	store := openImportStore(t)
	batch := testImportBatch()
	if err := store.CommitBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	registrations := []projection.Registration{}
	for _, d := range projection.DefaultDefinitions() {
		r := projection.Registration{Definition: d}
		if d.Kind == projection.KindFindings {
			r.Builder = projection.BuilderFunc(func(context.Context, projection.BuildRequest) error {
				return errors.New("sensitive raw command output")
			})
		}
		registrations = append(registrations, r)
	}
	manager, err := projection.NewManager(ctx, store, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Project(ctx, importer.ProjectionRequest{SessionID: batch.Session.ID}); err == nil {
		t.Fatal("failure hidden")
	}
	snapshot, _ := store.SessionFindings(ctx, batch.Session.ID)
	if snapshot.Usable || snapshot.Status != "failed" {
		t.Fatalf("failed snapshot=%#v", snapshot)
	}
	state := projectionState(t, store, batch.Session.ID, projection.KindFindings)
	if state.Diagnostic == nil || strings.Contains(state.Diagnostic.Summary, "sensitive") {
		t.Fatal("unsafe failure diagnostic")
	}
	checkpoint, found, err := store.Checkpoint(ctx, batch.Checkpoint.SourceID)
	if err != nil || !found || !importer.CheckpointEqual(checkpoint, batch.Checkpoint) {
		t.Fatal("canonical import lost")
	}
	builder, _ := analysis.NewBuilder(store)
	for i := range registrations {
		if registrations[i].Definition.Kind == projection.KindFindings {
			registrations[i].Builder = builder
		}
	}
	manager, err = projection.NewManager(ctx, store, store, registrations)
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Retry(ctx, batch.Session.ID); err != nil {
		t.Fatal(err)
	}
	ready, _ := store.SessionFindings(ctx, batch.Session.ID)
	if !ready.Usable {
		t.Fatal("retry failed")
	}
	if err := store.Register(ctx, []projection.Definition{{Kind: projection.KindFindings, Version: "changed-target"}}); err != nil {
		t.Fatal(err)
	}
	changed, _ := store.SessionFindings(ctx, batch.Session.ID)
	if changed.Usable || !changed.Stale || len(changed.Assessments) != 0 {
		t.Fatal("old version output exposed")
	}
}
