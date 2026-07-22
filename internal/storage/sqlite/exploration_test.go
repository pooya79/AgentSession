package sqlite

import (
	"context"
	"testing"

	"github.com/pooya79/AgentSession/internal/model"
	storagecontract "github.com/pooya79/AgentSession/internal/storage"
)

func TestListSessionsOrdersVariablePrecisionTimestampsChronologicallyAcrossPages(t *testing.T) {
	t.Parallel()

	store := openImportStore(t)
	ctx := context.Background()
	rows := []struct {
		id        model.SessionID
		startedAt any
	}{
		{"fractional-later", "2026-07-22T12:00:00.1Z"},
		{"exact-second", "2026-07-22T12:00:00Z"},
		{"previous-second", "2026-07-22T11:59:59.9Z"},
		{"unknown-time", nil},
	}
	for _, row := range rows {
		_, err := store.db.ExecContext(ctx, `
			INSERT INTO sessions (
				id, title, summary, started_at, source_id, adapter_name,
				adapter_version, format_version, model_version, normalization_version
			) VALUES (?, '', '', ?, 'source', 'adapter', '1', '1', '1', '1')
		`, row.id, row.startedAt)
		if err != nil {
			t.Fatalf("insert session %q: %v", row.id, err)
		}
	}

	first, hasMore, err := store.ListSessions(ctx, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore || len(first) != 2 || first[0].ID != "fractional-later" || first[1].ID != "exact-second" {
		t.Fatalf("first page = (%v, hasMore %v), want fractional-later then exact-second", sessionSummaryIDs(first), hasMore)
	}

	cursor := storagecontract.SessionCursor{StartedAt: first[1].StartedAt, ID: first[1].ID}
	second, hasMore, err := store.ListSessions(ctx, &cursor, 2)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore || len(second) != 2 || second[0].ID != "previous-second" || second[1].ID != "unknown-time" {
		t.Fatalf("second page = (%v, hasMore %v), want previous-second then unknown-time", sessionSummaryIDs(second), hasMore)
	}
}

func sessionSummaryIDs(summaries []storagecontract.SessionSummary) []model.SessionID {
	ids := make([]model.SessionID, len(summaries))
	for i, summary := range summaries {
		ids[i] = summary.ID
	}
	return ids
}
