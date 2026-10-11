package importer

import (
	"github.com/pooya79/AgentSession/internal/model"
)

func cloneRecordEnvelope(envelope RecordEnvelope) RecordEnvelope {
	clone := envelope
	clone.RawRecord.Content = append([]byte(nil), envelope.RawRecord.Content...)
	clone.RawRecord.Ref = cloneRawRecordRef(envelope.RawRecord.Ref)
	clone.Events = make([]model.Event, len(envelope.Events))
	for i, event := range envelope.Events {
		clone.Events[i] = event
		if event.Timestamp != nil {
			timestamp := *event.Timestamp
			clone.Events[i].Timestamp = &timestamp
		}
		clone.Events[i].RawRecord = cloneRawRecordRef(event.RawRecord)
		clone.Events[i].Data = cloneNormalizedData(event.Data)
	}
	clone.Diagnostics = make([]model.Diagnostic, len(envelope.Diagnostics))
	for i, diagnostic := range envelope.Diagnostics {
		clone.Diagnostics[i] = diagnostic
		clone.Diagnostics[i].EventIDs = append([]model.EventID(nil), diagnostic.EventIDs...)
		clone.Diagnostics[i].RawRecordIDs = append([]model.RawRecordID(nil), diagnostic.RawRecordIDs...)
	}
	return clone
}

func cloneSession(session model.Session) model.Session {
	clone := session
	if session.StartedAt != nil {
		startedAt := *session.StartedAt
		clone.StartedAt = &startedAt
	}
	if session.EndedAt != nil {
		endedAt := *session.EndedAt
		clone.EndedAt = &endedAt
	}
	if session.Diagnostics != nil {
		clone.Diagnostics = make([]model.Diagnostic, len(session.Diagnostics))
		for i, diagnostic := range session.Diagnostics {
			clone.Diagnostics[i] = diagnostic
			clone.Diagnostics[i].EventIDs = append([]model.EventID(nil), diagnostic.EventIDs...)
			clone.Diagnostics[i].RawRecordIDs = append([]model.RawRecordID(nil), diagnostic.RawRecordIDs...)
		}
	}
	return clone
}

func cloneSourceState(state SourceState) SourceState {
	clone := state
	clone.Session = cloneSession(state.Session)
	clone.Checkpoint = cloneCheckpoint(state.Checkpoint)
	if state.LastEventSequence != nil {
		sequence := *state.LastEventSequence
		clone.LastEventSequence = &sequence
	}
	return clone
}

func cloneRawRecordRef(ref model.RawRecordRef) model.RawRecordRef {
	clone := ref
	if ref.RecordSequence != nil {
		sequence := *ref.RecordSequence
		clone.RecordSequence = &sequence
	}
	if ref.ByteRange != nil {
		byteRange := *ref.ByteRange
		clone.ByteRange = &byteRange
	}
	return clone
}

func cloneNormalizedData(data model.NormalizedData) model.NormalizedData {
	switch value := data.(type) {
	case model.ToolResultData:
		if value.IsError != nil {
			copied := *value.IsError
			value.IsError = &copied
		}
		return value
	case model.CommandData:
		if value.ExitCode != nil {
			copied := *value.ExitCode
			value.ExitCode = &copied
		}
		return value
	case model.FileReadData:
		if value.StartLine != nil {
			copied := *value.StartLine
			value.StartLine = &copied
		}
		if value.EndLine != nil {
			copied := *value.EndLine
			value.EndLine = &copied
		}
		return value
	case model.PatchData:
		value.Paths = append([]string(nil), value.Paths...)
		return value
	case model.UsageData:
		value.InputTokens = cloneInt64(value.InputTokens)
		value.OutputTokens = cloneInt64(value.OutputTokens)
		value.CacheReadTokens = cloneInt64(value.CacheReadTokens)
		value.CacheWriteTokens = cloneInt64(value.CacheWriteTokens)
		return value
	default:
		return data
	}
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}
