package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

func TestInspectUnknownEvidenceIsExplicitBoundedRedactedAndSessionScoped(t *testing.T) {
	eventID := model.EventID("evt_" + strings.Repeat("b", 64))
	rawID := model.RawRecordID("raw_" + strings.Repeat("c", 64))
	content := append([]byte("Authorization: Bearer visible-secret\n"), 0xff)
	stub := &explorationReaderStub{
		envelope: storage.EventEnvelope{
			EventSummary: model.EventSummary{ID: eventID, SessionID: "session", Kind: model.EventKindUnknown, Summary: "unknown"},
			RawRecord:    model.RawRecordRef{ID: rawID, SourceID: "source", ContentHash: "sha256:test"},
		},
		rawPrefix: storage.RawRecordPrefix{Content: content, OriginalSize: int64(len(content) + 10)},
		rawFound:  true,
	}
	explorer, _ := NewExplorer(stub)
	if _, err := explorer.EventDetail(context.Background(), EventDetailRequest{SessionID: "session", EventID: eventID}); err != nil || stub.rawReads != 0 {
		t.Fatalf("ordinary detail read raw evidence: error=%v reads=%d", err, stub.rawReads)
	}
	inspection, err := explorer.InspectUnknownEvidence(context.Background(), "session", eventID)
	if err != nil || inspection.State != EvidenceComplete || !inspection.Truncated || inspection.RedactionCount != 1 ||
		inspection.ReturnedSize != int64(len(inspection.Text)) || !utf8.ValidString(inspection.Text) ||
		strings.Contains(inspection.Text, "visible-secret") || stub.rawReads != 1 {
		t.Fatalf("InspectUnknownEvidence() = (%#v, %v), reads=%d", inspection, err, stub.rawReads)
	}
	stub.envelope.Kind = model.EventKindMessage
	if _, err := explorer.InspectUnknownEvidence(context.Background(), "session", eventID); !errors.Is(err, ErrInvalidRequest) || stub.rawReads != 1 {
		t.Fatalf("typed inspection error=%v reads=%d", err, stub.rawReads)
	}
	stub.envelope.Kind = model.EventKindUnknown
	if result, err := explorer.InspectUnknownEvidence(context.Background(), "other", eventID); err != nil || result.State != EvidenceNotFound || stub.rawReads != 1 {
		t.Fatalf("cross-session inspection=(%#v, %v) reads=%d", result, err, stub.rawReads)
	}
}
