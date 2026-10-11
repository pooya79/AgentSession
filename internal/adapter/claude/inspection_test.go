package claude

import (
	"context"
	"strings"
	"testing"

	"github.com/pooya79/AgentSession/internal/importer"
)

func TestProbeAndPrepareInspectEightCompleteRecords(t *testing.T) {
	var lines []string
	for i := 0; i < 7; i++ {
		lines = append(lines, `{"type":"file-history-snapshot"}`)
	}
	lines = append(lines, `{"type":"user","sessionId":"window-session","version":"9.8.7","message":{"role":"user","content":"eighth"}}`)
	data := []byte(strings.Join(lines, "\n") + "\n")
	source := bytesSource("window", data)
	probe, err := New().Probe(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Confidence != importer.ProbeCertain || probe.FormatVersion != "claude-code-jsonl-v1+cli-9.8.7" {
		t.Fatalf("Probe() = %#v", probe)
	}
	sink, _ := importSource(t, source, nil, nil)
	if sink.session.ID != "window-session" || sink.session.Import.AdapterName != "claude" || sink.session.Import.AdapterVersion != "1" || sink.session.Import.NormalizationVersion != NormalizationVersion {
		t.Fatalf("session metadata = %#v", sink.session)
	}

	unsupported, err := New().Probe(context.Background(), bytesSource("empty", nil))
	if err != nil || unsupported.Confidence != importer.ProbeUnsupported {
		t.Fatalf("empty probe = %#v, %v", unsupported, err)
	}
	possible, err := New().Probe(context.Background(), bytesSource("generic", []byte("{\"custom\":true}\n")))
	if err != nil || possible.Confidence != importer.ProbePossible || possible.FormatVersion != "claude-code-jsonl-v1+cli-unknown" {
		t.Fatalf("generic probe = %#v, %v", possible, err)
	}
}

// TestProbeAndPrepareSkipMalformedAndSparseLeadingRecords verifies that header
// discovery continues until usable session metadata appears.
func TestProbeAndPrepareSkipMalformedAndSparseLeadingRecords(t *testing.T) {
	data := []byte("{\"type\":\"assistant\"\n" +
		"{\"custom\":true}\n" +
		"{\"type\":\"file-history-snapshot\"}\n" +
		"{\"type\":\"user\",\"sessionId\":\"delayed-session\",\"version\":\"2.1.179\",\"message\":{\"role\":\"user\",\"content\":\"late metadata\"}}\n")
	source := bytesSource("delayed-metadata", data)
	probe, err := New().Probe(context.Background(), source)
	if err != nil || probe.Confidence != importer.ProbeCertain ||
		probe.FormatVersion != "claude-code-jsonl-v1+cli-2.1.179" ||
		len(probe.Diagnostics) != 1 {
		t.Fatalf("Probe() = %#v, %v", probe, err)
	}
	sink, _ := importSource(t, source, nil, nil)
	if sink.session.ID != "delayed-session" ||
		sink.session.Import.FormatVersion != "claude-code-jsonl-v1+cli-2.1.179" ||
		len(sink.records) != 4 || len(events(sink.records)) != 1 {
		t.Fatalf("delayed import = session %#v records %#v", sink.session, sink.records)
	}
}
