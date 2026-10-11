package claude

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

// wireRecord retains source-shaped fields as raw JSON where presence, null,
// and type validity affect normalization.
type wireRecord struct {
	CWD           string          `json:"cwd"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	Type          string          `json:"type"`
	UUID          string          `json:"uuid"`
	SessionID     string          `json:"sessionId"`
	AgentID       string          `json:"agentId"`
	Version       string          `json:"version"`
	Timestamp     string          `json:"timestamp"`
	IsSidechain   bool            `json:"isSidechain"`
	Message       json.RawMessage `json:"message"`
	Content       json.RawMessage `json:"content"`
	Error         json.RawMessage `json:"error"`
	Data          json.RawMessage `json:"data"`
	Attachment    json.RawMessage `json:"attachment"`
	Subtype       json.RawMessage `json:"subtype"`
	Operation     json.RawMessage `json:"operation"`
	Summary       string          `json:"summary"`
}

// envelope retains one raw line and attaches normalized events plus bounded,
// evidence-backed diagnostics.
func (p *prepared) envelope(line []byte, offset, sequence int64, session model.Session) (importer.RecordEnvelope, error) {
	seq := sequence
	rangeValue := model.ByteRange{Offset: offset, Length: int64(len(line))}
	hashValue := model.HashRecord(line)
	rawID, err := model.NewRawRecordID(model.RawRecordIDInput{SourceID: p.source.ID, RecordSequence: &seq, ByteRange: &rangeValue, ContentHash: hashValue})
	if err != nil {
		return importer.RecordEnvelope{}, err
	}
	ref := model.RawRecordRef{ID: rawID, SourceID: p.source.ID, RecordSequence: &seq, ByteRange: &rangeValue, ContentHash: hashValue}
	envelope := importer.RecordEnvelope{RawRecord: model.RawRecord{Ref: ref, Content: append([]byte(nil), line...)}}
	var record wireRecord
	if err := json.Unmarshal(trimLineEnding(line), &record); err != nil {
		envelope.Diagnostics = []model.Diagnostic{{
			Code: "claude.record.malformed", Severity: model.SeverityWarning,
			Message:              "A complete Claude Code record is malformed JSON and was retained.",
			InterpretationReason: model.InterpretationStructurallyInvalidKnownRecord,
			RawRecordIDs:         []model.RawRecordID{rawID},
		}}
		return envelope, nil
	}
	if strings.TrimSpace(record.Type) == "" {
		envelope.Diagnostics = []model.Diagnostic{{
			Code: "claude.record.type.missing", Severity: model.SeverityWarning,
			Message:              "The complete Claude record has no type discriminant and was retained.",
			InterpretationReason: model.InterpretationMissingDiscriminant,
			RawRecordIDs:         []model.RawRecordID{rawID},
		}}
		return envelope, nil
	}

	drafts, diagnosticDrafts := normalizeRecord(record)
	drafts = p.evidence.normalize(record, drafts)
	var eventIDs []model.EventID
	for ordinal, draft := range drafts {
		eventID, err := claudeEventID(record.UUID, p.sessionID, uint64(ordinal), len(drafts), ref)
		if err != nil {
			return importer.RecordEnvelope{}, err
		}
		event := model.Event{ID: eventID, SessionID: session.ID, Sequence: p.eventSeq, Kind: draft.kind, Summary: draft.summary, SearchableText: draft.searchable, Data: draft.data, RawRecord: ref}
		p.eventSeq++
		envelope.Events = append(envelope.Events, event)
		eventIDs = append(eventIDs, eventID)
	}
	for _, draft := range boundDiagnosticDrafts(diagnosticDrafts) {
		envelope.Diagnostics = append(envelope.Diagnostics, model.Diagnostic{
			Code: draft.code, Severity: model.SeverityWarning,
			Message:              draft.message,
			InterpretationReason: draft.reason,
			EventIDs:             eventIDs,
			RawRecordIDs:         []model.RawRecordID{rawID},
		})
	}
	if value := strings.TrimSpace(record.Timestamp); value != "" {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			diagnostic := model.Diagnostic{Code: "claude.timestamp.invalid", Severity: model.SeverityWarning, Message: "The Claude record timestamp is malformed; source order was preserved.", RawRecordIDs: []model.RawRecordID{rawID}}
			for _, event := range envelope.Events {
				diagnostic.EventIDs = append(diagnostic.EventIDs, event.ID)
			}
			envelope.Diagnostics = append(envelope.Diagnostics, diagnostic)
		} else {
			for i := range envelope.Events {
				envelope.Events[i].Timestamp = &parsed
			}
		}
	}
	envelope.Diagnostics = boundClaudeDiagnostics(envelope.Diagnostics, rawID, eventIDs)
	return envelope, nil
}

func claudeEventID(uuid, sessionID string, ordinal uint64, count int, ref model.RawRecordRef) (model.EventID, error) {
	if uuid = strings.TrimSpace(uuid); uuid != "" {
		if count > 1 {
			uuid = fmt.Sprintf("%s:event:%d", uuid, ordinal)
		}
		return model.NewEventID(model.EventIDInput{Native: &model.NativeEventIdentity{Scope: model.NativeEventIDSession, SessionID: sessionID, EventID: uuid}})
	}
	return model.NewEventID(model.EventIDInput{SourceID: ref.SourceID, RecordSequence: ref.RecordSequence, ByteRange: ref.ByteRange, RecordHash: ref.ContentHash, EventOrdinal: ordinal})
}

func agentSessionID(parentSessionID, agentID string) string {
	sum := sha256.Sum256([]byte("agentsession:claude-agent-session-id:v1\x00" + parentSessionID + "\x00" + agentID))
	return "claude_agent_" + hex.EncodeToString(sum[:])
}

// boundDiagnosticDrafts caps adapter-local diagnostics and reserves the final
// position for an omission marker.
func boundDiagnosticDrafts(values []diagnosticDraft) []diagnosticDraft {
	return boundDiagnostics(values, diagnosticDraft{
		code:    "claude.diagnostics.truncated",
		message: "Additional Claude record diagnostics were omitted.",
	})
}

// boundClaudeDiagnostics caps finalized diagnostics and attaches the omitted
// evidence references to its truncation marker.
func boundClaudeDiagnostics(values []model.Diagnostic, rawID model.RawRecordID, eventIDs []model.EventID) []model.Diagnostic {
	return boundDiagnostics(values, model.Diagnostic{
		Code:         "claude.diagnostics.truncated",
		Severity:     model.SeverityWarning,
		Message:      "Additional Claude record diagnostics were omitted.",
		EventIDs:     append([]model.EventID(nil), eventIDs...),
		RawRecordIDs: []model.RawRecordID{rawID},
	})
}

// boundDiagnostics preserves slices within the cap and copies truncated
// prefixes before appending the supplied marker.
func boundDiagnostics[T any](values []T, marker T) []T {
	if len(values) <= maxRecordDiagnostics {
		return values
	}
	values = append([]T(nil), values[:maxRecordDiagnostics-1]...)
	return append(values, marker)
}
