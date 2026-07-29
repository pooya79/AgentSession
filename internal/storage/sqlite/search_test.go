package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/search"
)

func TestSearchProjectionQueryAndPagination(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "search.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewImportStore(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (
			id, title, summary, source_id, adapter_name, adapter_version,
			format_version, model_version, normalization_version, canonical_revision
		) VALUES ('session-a', '', '', 'source', 'adapter', '1', '1', '1', '1', 7);
		INSERT INTO session_projection_states (
			session_id, kind, status, target_version, target_revision,
			ready_version, ready_revision, updated_at
		) VALUES ('session-a', 'search', 'ready', '1', 7, '1', 7, CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatal(err)
	}
	documents := []search.Document{
		{SessionID: "session-a", EventID: testSearchEventID("1"), Sequence: 1, Timestamp: "2026-01-03T00:00:00Z", Kind: model.EventKindMessage, Summary: "alpha first", Content: "alpha common", Files: []string{"src/main.go"}, ProjectionVersion: "1", CanonicalRevision: 7},
		{SessionID: "session-a", EventID: testSearchEventID("2"), Sequence: 2, Timestamp: "2026-01-02T00:00:00Z", Kind: model.EventKindCommand, Summary: "alpha second", Content: "alpha common go test", ToolName: "shell", CommandText: "go test ./...", ProjectionVersion: "1", CanonicalRevision: 7},
		{SessionID: "session-a", EventID: testSearchEventID("3"), Sequence: 3, Kind: model.EventKindError, Summary: "alpha third", Content: "alpha common failure", ProjectionVersion: "1", CanonicalRevision: 7},
	}
	if err := store.StageSearchDocuments(ctx, "token", documents); err != nil {
		t.Fatal(err)
	}
	if err := store.PublishSearchDocuments(ctx, "token", "session-a", "1", 7); err != nil {
		t.Fatal(err)
	}

	parsed, err := search.Parse("alpha")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Search(ctx, parsed, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !first.More || len(first.Items) != 2 || first.Availability.Usable != 1 ||
		first.Items[0].EventID >= first.Items[1].EventID {
		t.Fatalf("first page = %#v", first)
	}
	last := first.Items[1]
	second, err := store.Search(ctx, parsed, &search.Cursor{
		Ranked: true, Rank: last.Rank, EventID: last.EventID,
		Generation: first.Availability.Generation,
	}, 2)
	if err != nil || len(second.Items) != 1 || second.Items[0].EventID == last.EventID {
		t.Fatalf("second page = (%#v, %v)", second, err)
	}

	filtered, err := search.Parse(`file:src kind:message`)
	if err != nil {
		t.Fatal(err)
	}
	filterPage, err := store.Search(ctx, filtered, nil, 10)
	if err != nil || len(filterPage.Items) != 1 || filterPage.Items[0].EventID != documents[0].EventID {
		t.Fatalf("filtered page = (%#v, %v)", filterPage, err)
	}
	command, _ := search.Parse(`tool:shell command:"TEST ./"`)
	commandPage, err := store.Search(ctx, command, nil, 10)
	if err != nil || len(commandPage.Items) != 1 || commandPage.Items[0].EventID != documents[1].EventID {
		t.Fatalf("command page = (%#v, %v)", commandPage, err)
	}
	hostile, err := search.Parse(`"' OR 1=1 --"`)
	if err != nil {
		t.Fatal(err)
	}
	hostilePage, err := store.Search(ctx, hostile, nil, 10)
	if err != nil || len(hostilePage.Items) != 0 {
		t.Fatalf("hostile query = (%#v, %v)", hostilePage, err)
	}
}

func TestSearchExcludesStaleProjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "stale.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, _ := NewImportStore(db)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sessions (
			id, title, summary, source_id, adapter_name, adapter_version,
			format_version, model_version, normalization_version, canonical_revision
		) VALUES ('stale', '', '', 'source', 'adapter', '1', '1', '1', '1', 2);
		INSERT INTO session_projection_states (
			session_id, kind, status, target_version, target_revision,
			ready_version, ready_revision, updated_at
		) VALUES ('stale', 'search', 'pending', '1', 2, '1', 1, CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatal(err)
	}
	page, err := store.Search(ctx, search.Query{}, nil, 10)
	if err != nil || page.Availability.Usable != 0 || page.Availability.Stale != 1 || len(page.Items) != 0 {
		t.Fatalf("stale availability = (%#v, %v)", page, err)
	}
}

func testSearchEventID(suffix string) model.EventID {
	return model.EventID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + suffix)
}
