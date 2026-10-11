package opencode

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pooya79/AgentSession/internal/adaptertest"
	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

func createFixture(t *testing.T) string {
	return createNamedFixture(t, "valid_multi_session.sql")
}

func createNamedFixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	fixture := adaptertest.LoadSanitizedFixture(t, filepath.Join("testdata", name), "private-project")
	if _, err := db.Exec(string(fixture)); err != nil {
		t.Fatalf("execute fixture: %v", err)
	}
	return path
}

func equalKinds(left, right []model.EventKind) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func sourceFor(path string) importer.Source {
	return importer.Source{ID: "physical-opencode", LocalPath: path, Hint: "opencode"}
}
