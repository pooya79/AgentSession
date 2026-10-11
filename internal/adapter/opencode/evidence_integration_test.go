package opencode

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	storageSQLite "github.com/pooya79/AgentSession/internal/storage/sqlite"
)

func TestFindingsEvidenceAcrossSelectedGenerations(t *testing.T) {
	ctx := context.Background()
	db, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := storageSQLite.NewImportStore(db)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{BatchRecords: 2})
	if err != nil {
		t.Fatal(err)
	}
	source := sourceFor(createNamedFixture(t, "findings_evidence_v1.sql"))
	results, err := coordinator.ImportAll(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 4 {
		t.Fatalf("logical sessions = %d", len(results))
	}
	var original []model.Event
	for _, result := range results {
		session, _, err := store.Session(ctx, result.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		summaries, err := store.EventSummaries(ctx, result.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		commands, confirmed := 0, 0
		for i, summary := range summaries {
			event, found, err := store.Event(ctx, summary.ID)
			if err != nil || !found {
				t.Fatalf("event = %#v, %v", event, err)
			}
			if event.Sequence != int64(i) {
				t.Fatalf("source order = %d, want %d", event.Sequence, i)
			}
			raw, found, err := store.RawRecord(ctx, event.RawRecord.ID)
			if err != nil || !found || len(raw.Content) == 0 {
				t.Fatalf("raw = %#v, %v", raw, err)
			}
			switch data := event.Data.(type) {
			case model.CommandData:
				commands++
				if data.InvocationID == "" {
					t.Fatal("lost invocation linkage")
				}
				if session.Title == "durable" && data.Phase == model.CommandCompleted && data.ExitCode == nil {
					t.Fatal("lost explicit process result")
				}
				if session.Title == "legacy" && (data.ExitCode != nil || data.WorkingDirectory != "") {
					t.Fatal("fabricated legacy exit or directory")
				}
				if session.Title == "sequenced" && data.Phase == model.CommandStarted {
					t.Fatal("fabricated ordered start from final snapshot")
				}
				if session.Title == "uncertain" && data.ExitCode != nil {
					t.Fatal("timeout became process success")
				}
			case model.PatchData:
				if data.Status == model.ChangeConfirmed {
					confirmed++
				}
				if strings.Contains(strings.Join(data.Paths, " "), "duplicate") {
					t.Fatal("combined alternative histories")
				}
			}
			if session.Title == "durable" {
				if !strings.Contains(string(raw.Content), `"base64":"AP8="`) {
					t.Fatal("lost exact unknown BLOB column")
				}
				original = append(original, event)
			}
		}
		switch session.Title {
		case "durable":
			if commands != 8 || confirmed != 3 || len(summaries) != 14 {
				t.Fatalf("durable commands=%d confirmed=%d events=%d", commands, confirmed, len(summaries))
			}
		case "sequenced":
			if commands != 2 || confirmed != 1 {
				t.Fatalf("sequenced commands=%d confirmed=%d", commands, confirmed)
			}
		case "legacy":
			if commands != 2 || confirmed != 0 {
				t.Fatalf("legacy commands=%d confirmed=%d", commands, confirmed)
			}
		}
	}
	again, err := coordinator.ImportAll(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range again {
		if result.CanonicalChanged || result.Change != importer.SourceUnchanged {
			t.Fatalf("not idempotent: %#v", result)
		}
	}
	for _, before := range original {
		after, _, err := store.Event(ctx, before.ID)
		if err != nil || !reflect.DeepEqual(before, after) {
			t.Fatalf("stable event changed: %v", err)
		}
	}
}

func TestModernMutationApplicationConfirmation(t *testing.T) {
	cases := []struct {
		name, tool, fields string
		confirmed          bool
		operations         []model.FileMutationOperation
	}{
		{"edit", "edit", `{"files":[{"file":"/workspace/example/edit.go","status":"modified"}]}`, true, []model.FileMutationOperation{model.FileMutationUpdate}},
		{"applied patch", "apply_patch", `{"applied":[{"target":"/workspace/example/add.go","type":"add"},{"target":"/workspace/example/delete.go","type":"delete"}]}`, true, []model.FileMutationOperation{model.FileMutationCreate, model.FileMutationDelete}},
		{"patch preview", "apply_patch", `{"files":[{"file":"/workspace/example/add.go","status":"added"}]}`, false, nil},
		{"relative effect", "edit", `{"files":[{"file":"edit.go","status":"modified"}]}`, false, nil},
		{"unknown operation", "edit", `{"files":[{"file":"/workspace/example/edit.go","status":"future"}]}`, false, nil},
		{"partial malformed effect", "apply_patch", `{"applied":[{"target":"/workspace/example/add.go","type":"add"},{"target":null,"type":"delete"}]}`, false, nil},
		{"null effects", "edit", `{"files":null}`, false, nil},
		{"empty edit effects", "edit", `{"files":[]}`, false, nil},
		{"foreign edit operation", "edit", `{"files":[{"file":"/workspace/example/edit.go","status":"add"}]}`, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			sourcePath := createNamedFixture(t, "modern_mutations_v1.sql")
			sourceDB, err := sql.Open("sqlite", sourcePath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := sourceDB.Exec(`DELETE FROM event WHERE seq>2; UPDATE event_sequence SET seq=2; UPDATE event SET data=json_object('callID','edit','tool',?,'input',json_object('path','/workspace/example/edit.go')) WHERE seq=1`, tc.tool); err != nil {
				t.Fatal(err)
			}
			data := `{"callID":"edit","content":[],"structured":` + tc.fields + `}`
			if _, err := sourceDB.Exec(`UPDATE event SET data=? WHERE seq=2`, data); err != nil {
				t.Fatal(err)
			}
			if err := sourceDB.Close(); err != nil {
				t.Fatal(err)
			}
			indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer indexDB.Close()
			store, err := storageSQLite.NewImportStore(indexDB)
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{})
			if err != nil {
				t.Fatal(err)
			}
			results, err := coordinator.ImportAll(ctx, sourceFor(sourcePath))
			if err != nil {
				t.Fatal(err)
			}
			summaries, err := store.EventSummaries(ctx, results[0].SessionID)
			if err != nil {
				t.Fatal(err)
			}
			event, found, err := store.Event(ctx, summaries[len(summaries)-1].ID)
			if err != nil || !found {
				t.Fatal(err)
			}
			p, ok := event.Data.(model.PatchData)
			if !ok || (p.Status == model.ChangeConfirmed) != tc.confirmed || (!tc.confirmed && p.Status != model.ChangeUnknown) {
				t.Fatalf("effect = %#v", event.Data)
			}
			var operations []model.FileMutationOperation
			for _, change := range p.Changes {
				operations = append(operations, change.Operation)
			}
			if !reflect.DeepEqual(operations, tc.operations) {
				t.Fatalf("operations = %v", operations)
			}
		})
	}
}

func TestDurableToolAttributionBounds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		extraCalls int
		id         string
	}{
		{"retained", 255, "edit"}, {"evicted", 256, "edit"}, {"oversized identity", 0, strings.Repeat("i", 513)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			source := createNamedFixture(t, "modern_mutations_v1.sql")
			db, err := sql.Open("sqlite", source)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM event WHERE seq>1; UPDATE event SET data=json_object('callID',?,'tool','edit','input',json_object('path','/workspace/example/edit.go'))`, tc.id); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < tc.extraCalls; i++ {
				if _, err := db.Exec(`INSERT INTO event VALUES (?, 'mutations', ?, 'session.next.tool.called', json_object('callID',?,'tool','read','input',json_object('path','README.md')))`, fmt.Sprintf("other_%d", i), i+2, fmt.Sprintf("other_%d", i)); err != nil {
					t.Fatal(err)
				}
			}
			seq := tc.extraCalls + 2
			if _, err := db.Exec(`INSERT INTO event VALUES ('final','mutations',?,'session.next.tool.success',json_object('callID',?,'content',json('[]'),'structured',json('{"files":[{"file":"/workspace/example/edit.go","status":"modified"}]}')))`, seq, tc.id); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`UPDATE event_sequence SET seq=?`, seq); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			indexDB, err := storageSQLite.Open(ctx, filepath.Join(t.TempDir(), "index.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer indexDB.Close()
			store, err := storageSQLite.NewImportStore(indexDB)
			if err != nil {
				t.Fatal(err)
			}
			coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{BatchRecords: 2})
			if err != nil {
				t.Fatal(err)
			}
			results, err := coordinator.ImportAll(ctx, sourceFor(source))
			if err != nil {
				t.Fatal(err)
			}
			summaries, err := store.EventSummaries(ctx, results[0].SessionID)
			if err != nil {
				t.Fatal(err)
			}
			event, found, err := store.Event(ctx, summaries[len(summaries)-1].ID)
			if err != nil || !found {
				t.Fatal(err)
			}
			p, ok := event.Data.(model.PatchData)
			confirmed := ok && p.Status == model.ChangeConfirmed
			if confirmed != (tc.name == "retained") {
				t.Fatalf("attribution = %#v", event.Data)
			}
			if !confirmed {
				if _, ok := event.Data.(model.ToolResultData); !ok {
					t.Fatalf("unattributed result lost: %#v", event.Data)
				}
			}
		})
	}
}
