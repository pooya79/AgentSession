package codex

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/pooya79/AgentSession/internal/importer"
	"github.com/pooya79/AgentSession/internal/model"
)

func (p *prepared) envelope(line []byte, offset, sequence, eventSequence int64, session model.Session) (importer.RecordEnvelope, error) {
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
			Code: "codex.record.malformed", Severity: model.SeverityWarning,
			Message:              "A complete Codex rollout record is malformed JSON and was retained.",
			InterpretationReason: model.InterpretationStructurallyInvalidKnownRecord,
			RawRecordIDs:         []model.RawRecordID{rawID},
		}}
		return envelope, nil
	}
	normalizedEvents, diagnostics, err := p.normalize(record, ref, eventSequence, session.ID)
	if err != nil {
		return importer.RecordEnvelope{}, err
	}
	envelope.Events = normalizedEvents
	for i := range diagnostics {
		diagnostics[i].RawRecordIDs = []model.RawRecordID{rawID}
		if len(normalizedEvents) > 0 {
			diagnostics[i].EventIDs = make([]model.EventID, 0, len(normalizedEvents))
			for _, event := range normalizedEvents {
				diagnostics[i].EventIDs = append(diagnostics[i].EventIDs, event.ID)
			}
		}
	}
	envelope.Diagnostics = boundDiagnostics(diagnostics)
	return envelope, nil
}

func (p *prepared) normalize(record wireRecord, ref model.RawRecordRef, eventSequence int64, sessionID model.SessionID) ([]model.Event, []model.Diagnostic, error) {
	result, commandRecord := p.commands.normalize(record)
	if record.Type == "session_meta" {
		if _, ok := objectValue(record.Payload); !ok {
			result := invalidKnown()
			return nil, result.diagnostics, nil
		}
		return nil, nil, nil
	}
	if strings.TrimSpace(record.Type) == "" {
		return nil, []model.Diagnostic{{
			Code: "codex.record.type.missing", Severity: model.SeverityWarning,
			Message:              "The complete Codex record has no type discriminant and was retained.",
			InterpretationReason: model.InterpretationMissingDiscriminant,
		}}, nil
	}
	if !commandRecord {
		result = normalizeRecord(record, p.ordinal, string(sessionID))
	}
	if !result.handled {
		label := record.Type
		reason := model.UnknownUnsupportedRecordKind
		if record.Type == "response_item" || record.Type == "event_msg" {
			label += ":" + result.nested
			reason = model.UnknownUnsupportedNestedVariant
		}
		label = model.BoundOriginalKind(label)
		result.drafts = []eventDraft{{
			kind: model.EventKindUnknown, summary: "Unsupported Codex record: " + label,
			searchable: label,
			data: model.UnknownData{
				Reason: reason, OriginalKind: label,
			},
		}}
	}
	events := make([]model.Event, 0, len(result.drafts))
	for ordinal, draft := range result.drafts {
		eventID, err := model.NewEventID(model.EventIDInput{
			Native: draft.native, SourceID: ref.SourceID, RecordSequence: ref.RecordSequence,
			ByteRange: ref.ByteRange, RecordHash: ref.ContentHash, EventOrdinal: uint64(ordinal),
		})
		if err != nil {
			return nil, nil, err
		}
		events = append(events, model.Event{
			ID: eventID, SessionID: sessionID, Sequence: eventSequence + int64(ordinal),
			Kind: draft.kind, Summary: draft.summary, SearchableText: draft.searchable,
			Data: draft.data, RawRecord: ref,
		})
	}
	diagnostics := append([]model.Diagnostic(nil), result.diagnostics...)
	if strings.TrimSpace(record.Timestamp) != "" && len(events) > 0 {
		parsed, err := time.Parse(time.RFC3339Nano, record.Timestamp)
		if err != nil {
			diagnostics = append(diagnostics, model.Diagnostic{
				Code: "codex.timestamp.invalid", Severity: model.SeverityWarning,
				Message: "The rollout timestamp is malformed; source order was preserved.",
			})
		} else {
			for index := range events {
				events[index].Timestamp = &parsed
			}
		}
	}
	return events, diagnostics, nil
}

func boundDiagnostics(diagnostics []model.Diagnostic) []model.Diagnostic {
	if len(diagnostics) <= maxRecordDiagnostics {
		return diagnostics
	}
	result := append([]model.Diagnostic(nil), diagnostics[:maxRecordDiagnostics-1]...)
	result = append(result, model.Diagnostic{
		Code: "codex.record.diagnostics.truncated", Severity: model.SeverityWarning,
		Message:      "Additional Codex record diagnostics were omitted deterministically.",
		RawRecordIDs: append([]model.RawRecordID(nil), diagnostics[0].RawRecordIDs...),
	})
	return result
}
