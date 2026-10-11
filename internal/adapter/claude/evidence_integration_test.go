package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage/sqlite"
)

func TestOrdinaryEvidenceSQLiteResumesAndPreservesRawRecords(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "evidence.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store, err := sqlite.NewImportStore(db)
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := importer.NewCoordinator(store, []importer.Adapter{New()}, nil, importer.Options{BatchRecords: 1})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("testdata/ordinary_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	var session model.SessionID
	for end := 1; end <= 10; end++ {
		content := bytes.Join(lines[:end], nil)
		// A partial following line must not advance either evidence or checkpoint.
		if end < 10 {
			content = append(content, lines[end][:len(lines[end])/2]...)
		}
		result, err := coordinator.Import(ctx, bytesSource("ordinary", content))
		if err != nil {
			t.Fatal(err)
		}
		session = result.SessionID
		got, err := store.Events(ctx, session)
		if err != nil || len(got) != end {
			t.Fatalf("prefix %d events=%d err=%v", end, len(got), err)
		}
		for i, e := range got {
			if e.Sequence != int64(i) {
				t.Fatalf("sequence=%d", e.Sequence)
			}
			raw, found, err := store.RawRecord(ctx, e.RawRecord.ID)
			if err != nil || !found || !bytes.Equal(raw.Content, lines[i]) {
				t.Fatalf("raw %d retained=%v err=%v", i, found, err)
			}
		}
	}
	before, err := store.Events(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.Import(ctx, bytesSource("ordinary", data))
	if err != nil || result.RecordsCommitted != 0 || result.Change != importer.SourceUnchanged {
		t.Fatalf("idempotency=%#v, %v", result, err)
	}
	fresh, _ := importSource(t, bytesSource("ordinary", data), nil, nil)
	if !reflect.DeepEqual(before, events(fresh.records)) {
		t.Fatal("incremental canonical evidence differs from full import")
	}
	// An oversized structured write stays complete in canonical/raw storage; it
	// is not silently sampled by normalization.
	large := strings.Repeat("sanitized line\n", 20000)
	var request, resultRecord map[string]any
	_ = json.Unmarshal(lines[8], &request)
	_ = json.Unmarshal(lines[9], &resultRecord)
	request["uuid"] = "large-request"
	request["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["input"].(map[string]any)["content"] = large
	resultRecord["uuid"] = "large-result"
	resultRecord["toolUseResult"].(map[string]any)["content"] = large
	requestBytes, _ := json.Marshal(request)
	resultBytes, _ := json.Marshal(resultRecord)
	largeData := append(append(requestBytes, '\n'), append(resultBytes, '\n')...)
	_, err = coordinator.Import(ctx, bytesSource("ordinary", largeData))
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Events(ctx, session)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, e := range got {
		if p, ok := e.Data.(model.PatchData); ok && p.Status == model.ChangeConfirmed && strings.Contains(p.Text, `sanitized line\n`) {
			raw, ok, err := store.RawRecord(ctx, e.RawRecord.ID)
			if err != nil || !ok || !bytes.Equal(raw.Content, append(resultBytes, '\n')) {
				t.Fatal("large result raw changed")
			}
			found = true
		}
	}
	if !found {
		t.Fatal("large change evidence missing")
	}
}

func TestOrdinaryLinkageBoundLeavesEvictedResultsUncertain(t *testing.T) {
	data, err := os.ReadFile("testdata/ordinary_tools.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	var stream bytes.Buffer
	stream.Write(lines[0])
	for i := 0; i < maxEvidenceCalls; i++ {
		fmt.Fprintf(&stream, "{\"type\":\"assistant\",\"sessionId\":\"claude-findings\",\"uuid\":\"call-%d\",\"message\":{\"role\":\"assistant\",\"content\":[{\"type\":\"tool_use\",\"id\":\"call-%d\",\"name\":\"Read\",\"input\":{}}]}}\n", i, i)
	}
	stream.Write(lines[1])
	sink, _ := importSource(t, bytesSource("bounded", stream.Bytes()), nil, nil)
	got := events(sink.records)
	if _, ok := got[len(got)-1].Data.(model.ToolResultData); !ok {
		t.Fatal("evicted linkage fabricated confirmation")
	}
}
