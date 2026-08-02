package sqlite

import (
	"context"
	"math"
	"path/filepath"
	"strings"
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
			format_version, model_version, normalization_version, canonical_revision,
			last_activity_at, first_user_message, event_count
		) VALUES
			('session-a', 'Session A', 'Summary A', 'source', 'adapter', '1', '1', '1', '1', 7,
			 '2026-01-04T00:00:00Z', 'First A', 3),
			('session-b', 'Session B', 'Summary B', 'source', 'adapter', '1', '1', '1', '1', 7,
			 '2026-01-03T00:00:00Z', 'First B', 1),
			('session-c', 'Session C', 'Summary C', 'source', 'adapter', '1', '1', '1', '1', 7,
			 NULL, 'First C', 1);
		INSERT INTO session_projection_states (
			session_id, kind, status, target_version, target_revision,
			ready_version, ready_revision, updated_at
		) VALUES
			('session-a', 'search', 'ready', '1', 7, '1', 7, CURRENT_TIMESTAMP),
			('session-b', 'search', 'ready', '1', 7, '1', 7, CURRENT_TIMESTAMP),
			('session-c', 'search', 'ready', '1', 7, '1', 7, CURRENT_TIMESTAMP);
	`); err != nil {
		t.Fatal(err)
	}
	documentsA := []search.Document{
		{SessionID: "session-a", EventID: testSearchEventID("1"), Sequence: 1, Timestamp: "2026-01-03T00:00:00Z", Kind: model.EventKindMessage, Summary: "alpha first", Content: "alpha common", Files: []string{"src/main.go"}, ProjectionVersion: "1", CanonicalRevision: 7},
		{SessionID: "session-a", EventID: testSearchEventID("2"), Sequence: 2, Timestamp: "2026-01-02T00:00:00Z", Kind: model.EventKindCommand, Summary: "alpha second", Content: "alpha common go test", ToolName: "shell", CommandText: "go test ./...", ProjectionVersion: "1", CanonicalRevision: 7},
		{SessionID: "session-a", EventID: testSearchEventID("3"), Sequence: 3, Kind: model.EventKindError, Summary: "alpha third", Content: "alpha common failure", ProjectionVersion: "1", CanonicalRevision: 7},
	}
	documentSets := []struct {
		token     string
		sessionID model.SessionID
		documents []search.Document
	}{
		{token: "token-a", sessionID: "session-a", documents: documentsA},
		{token: "token-b", sessionID: "session-b", documents: []search.Document{{
			SessionID: "session-b", EventID: testSearchEventID("4"), Sequence: 1,
			Timestamp: "2026-01-02T00:00:00Z", Kind: model.EventKindMessage,
			Summary: "alpha B", Content: "alpha common", ProjectionVersion: "1", CanonicalRevision: 7,
		}}},
		{token: "token-c", sessionID: "session-c", documents: []search.Document{{
			SessionID: "session-c", EventID: testSearchEventID("5"), Sequence: 1,
			Kind: model.EventKindMessage, Summary: "alpha C", Content: "alpha common",
			ProjectionVersion: "1", CanonicalRevision: 7,
		}}},
	}
	for _, set := range documentSets {
		if err := store.StageSearchDocuments(ctx, set.token, set.documents); err != nil {
			t.Fatal(err)
		}
		if err := store.PublishSearchDocuments(ctx, set.token, set.sessionID, "1", 7); err != nil {
			t.Fatal(err)
		}
	}

	parsed, err := search.Parse("alpha")
	if err != nil {
		t.Fatal(err)
	}
	textPage, err := store.Search(ctx, parsed, nil, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(textPage.Items) != 3 || textPage.Availability.Usable != 3 {
		t.Fatalf("text page = %#v", textPage)
	}
	var sessionA search.Row
	for _, item := range textPage.Items {
		if item.SessionID == "session-a" {
			sessionA = item
		}
	}
	if sessionA.MatchCount != 3 || sessionA.Title != "Session A" ||
		sessionA.FirstUserMessage != "First A" || sessionA.EventCount != 3 ||
		sessionA.BestMatchSummary == "" || sessionA.Snippet == "" {
		t.Fatalf("grouped session A = %#v", sessionA)
	}
	rankedFirst, err := store.Search(ctx, parsed, nil, 1)
	if err != nil || !rankedFirst.More || len(rankedFirst.Items) != 1 {
		t.Fatalf("first ranked page = (%#v, %v)", rankedFirst, err)
	}
	rankedAnchor := rankedFirst.Items[0]
	rankedSecond, err := store.Search(ctx, parsed, &search.Cursor{
		Ranked: true, Rank: rankedAnchor.Rank, SessionID: rankedAnchor.SessionID,
		Generation: rankedFirst.Availability.Generation,
	}, 1)
	if err != nil || len(rankedSecond.Items) != 1 ||
		rankedSecond.Items[0].SessionID == rankedAnchor.SessionID {
		t.Fatalf("second ranked page = (%#v, %v)", rankedSecond, err)
	}
	rankedPrevious, err := store.Search(ctx, parsed, &search.Cursor{
		Ranked: true, Rank: rankedSecond.Items[0].Rank, SessionID: rankedSecond.Items[0].SessionID,
		Before: true, Generation: rankedSecond.Availability.Generation,
	}, 1)
	if err != nil || len(rankedPrevious.Items) != 1 ||
		rankedPrevious.Items[0].SessionID != rankedAnchor.SessionID {
		t.Fatalf("previous ranked page = (%#v, %v)", rankedPrevious, err)
	}

	filterOnly, err := search.Parse(`kind:message`)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Search(ctx, filterOnly, nil, 1)
	if err != nil || !first.More || len(first.Items) != 1 || first.Items[0].SessionID != "session-a" {
		t.Fatalf("first filter page = (%#v, %v)", first, err)
	}
	last := first.Items[0]
	second, err := store.Search(ctx, filterOnly, &search.Cursor{
		LastActivity: last.LastActivity, SessionID: last.SessionID,
		Generation: first.Availability.Generation,
	}, 1)
	if err != nil || !second.More || len(second.Items) != 1 || second.Items[0].SessionID != "session-b" {
		t.Fatalf("second filter page = (%#v, %v)", second, err)
	}
	previous, err := store.Search(ctx, filterOnly, &search.Cursor{
		LastActivity: second.Items[0].LastActivity, SessionID: second.Items[0].SessionID,
		Before: true, Generation: second.Availability.Generation,
	}, 1)
	if err != nil || len(previous.Items) != 1 || previous.Items[0].SessionID != "session-a" {
		t.Fatalf("previous filter page = (%#v, %v)", previous, err)
	}

	filtered, err := search.Parse(`file:src kind:message`)
	if err != nil {
		t.Fatal(err)
	}
	filterPage, err := store.Search(ctx, filtered, nil, 10)
	if err != nil || len(filterPage.Items) != 1 || filterPage.Items[0].SessionID != "session-a" {
		t.Fatalf("filtered page = (%#v, %v)", filterPage, err)
	}
	command, _ := search.Parse(`tool:shell command:"TEST ./"`)
	commandPage, err := store.Search(ctx, command, nil, 10)
	if err != nil || len(commandPage.Items) != 1 || commandPage.Items[0].SessionID != "session-a" {
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

func TestSearchTextRankingBlendsBM25WithMatchingEventRecency(t *testing.T) {
	t.Parallel()

	t.Run("bounded linear decay", func(t *testing.T) {
		rows := searchRankedFixtures(t, []searchRankFixture{
			{sessionID: "recent", timestamp: "2026-08-01T00:00:00Z", content: "alpha"},
			{sessionID: "midpoint", timestamp: "2026-06-17T00:00:00Z", content: "alpha"},
			{sessionID: "expired", timestamp: "2026-05-03T00:00:00Z", content: "alpha"},
			{sessionID: "missing", content: "alpha"},
		})
		ranks := make(map[model.SessionID]float64, len(rows))
		for _, row := range rows {
			ranks[row.SessionID] = row.Rank
		}
		if len(ranks) != 4 {
			t.Fatalf("ranked rows = %#v", rows)
		}
		assertClose(t, ranks["recent"]/ranks["expired"], 1.35)
		assertClose(t, ranks["midpoint"]/ranks["expired"], 1.175)
		assertClose(t, ranks["missing"]/ranks["expired"], 1.0)
		if rows[0].SessionID != "recent" || rows[1].SessionID != "midpoint" {
			t.Fatalf("recency order = %#v", rows)
		}
	})

	t.Run("recent match beats slightly stronger old match", func(t *testing.T) {
		rows := searchRankedFixtures(t, []searchRankFixture{
			{sessionID: "recent", timestamp: "2026-08-01T00:00:00Z", content: "alpha"},
			{sessionID: "slightly-stronger-old", timestamp: "2026-05-03T00:00:00Z", content: "alpha alpha"},
		})
		if len(rows) != 2 || rows[0].SessionID != "recent" {
			t.Fatalf("balanced ranking order = %#v", rows)
		}
	})

	t.Run("future timestamp is capped at the maximum boost", func(t *testing.T) {
		rows := searchRankedFixtures(t, []searchRankFixture{
			{sessionID: "future", timestamp: "2099-01-01T00:00:00Z", content: "alpha"},
			{sessionID: "missing", content: "alpha"},
		})
		if len(rows) != 2 || rows[0].SessionID != "future" {
			t.Fatalf("future timestamp order = %#v", rows)
		}
		assertClose(t, rows[0].Rank/rows[1].Rank, 1.35)
	})

	t.Run("substantially stronger old match keeps relevance priority", func(t *testing.T) {
		rows := searchRankedFixtures(t, []searchRankFixture{
			{
				sessionID: "recent", timestamp: "2026-08-01T00:00:00Z",
				content: "alpha " + strings.Repeat("unrelated ", 100),
			},
			{
				sessionID: "strong-old", timestamp: "2026-05-03T00:00:00Z",
				content: strings.Repeat("alpha ", 8),
			},
		})
		if len(rows) != 2 || rows[0].SessionID != "strong-old" {
			t.Fatalf("relevance-first ranking order = %#v", rows)
		}
	})
}

type searchRankFixture struct {
	sessionID model.SessionID
	timestamp string
	content   string
}

func searchRankedFixtures(t *testing.T, fixtures []searchRankFixture) []search.Row {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "ranking.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store, err := NewImportStore(db)
	if err != nil {
		t.Fatal(err)
	}
	for index, fixture := range fixtures {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO sessions (
				id, title, summary, source_id, adapter_name, adapter_version,
				format_version, model_version, normalization_version, canonical_revision
			) VALUES (?, '', '', 'source', 'adapter', '1', '1', '1', '1', 1);
			INSERT INTO session_projection_states (
				session_id, kind, status, target_version, target_revision,
				ready_version, ready_revision, updated_at
			) VALUES (?, 'search', 'ready', '1', 1, '1', 1, CURRENT_TIMESTAMP);
		`, fixture.sessionID, fixture.sessionID); err != nil {
			t.Fatal(err)
		}
		document := search.Document{
			SessionID: fixture.sessionID, EventID: testSearchEventID(string(rune('a' + index))),
			Sequence: 1, Timestamp: fixture.timestamp, Kind: model.EventKindMessage,
			Summary: "alpha", Content: fixture.content,
			ProjectionVersion: "1", CanonicalRevision: 1,
		}
		token := "token-" + string(rune('a'+index))
		if err := store.StageSearchDocuments(ctx, token, []search.Document{document}); err != nil {
			t.Fatal(err)
		}
		if err := store.PublishSearchDocuments(ctx, token, fixture.sessionID, "1", 1); err != nil {
			t.Fatal(err)
		}
	}
	query, err := search.Parse("alpha")
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Search(ctx, query, nil, len(fixtures))
	if err != nil {
		t.Fatal(err)
	}
	return page.Items
}

func assertClose(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("value = %.12f, want %.12f", got, want)
	}
}

func testSearchEventID(suffix string) model.EventID {
	return model.EventID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + suffix)
}
