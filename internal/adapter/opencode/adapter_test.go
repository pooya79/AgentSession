package opencode

import (
	"context"
	"database/sql"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
)

func TestProbeRequiresSchemaAndLocalPath(t *testing.T) {
	ctx := context.Background()
	adapter := New()
	probe, err := adapter.Probe(ctx, sourceFor(createFixture(t)))
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain || probe.FormatVersion != FormatVersion {
		t.Fatalf("probe = %#v", probe)
	}
	jsonl := []byte(`{"type":"message"}`)
	stream := importer.Source{ID: "jsonl", Size: int64(len(jsonl)), OpenAt: func(context.Context, int64) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(string(jsonl))), nil
	}}
	probe, err = adapter.Probe(ctx, stream)
	if err != nil || probe.Confidence != importer.ProbeUnsupported {
		t.Fatalf("JSONL probe = %#v, %v", probe, err)
	}

	unrelatedPath := filepath.Join(t.TempDir(), "other.db")
	db, err := sql.Open("sqlite", unrelatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE other (id TEXT)`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	probe, err = adapter.Probe(ctx, sourceFor(unrelatedPath))
	if err != nil || probe.Confidence != importer.ProbeUnsupported {
		t.Fatalf("unrelated probe = %#v, %v", probe, err)
	}
}
