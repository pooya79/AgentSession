package opencode

import (
	"context"
	"testing"
)

func TestPreparedSnapshotRejectsWrites(t *testing.T) {
	ctx := context.Background()
	containerView, err := New().PrepareContainer(ctx, sourceFor(createFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	defer containerView.Close()
	prepared := containerView.(*container)
	if _, err := prepared.snapshot.tx.ExecContext(ctx, `INSERT INTO unrelated VALUES ('write')`); err == nil {
		t.Fatal("query-only OpenCode snapshot accepted a write")
	}
}
