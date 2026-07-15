package model

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSessionValidate(t *testing.T) {
	started := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	ended := started.Add(-time.Hour)
	session := validSession()
	session.StartedAt = &started
	session.EndedAt = &ended
	if err := session.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want reverse timestamps to remain valid evidence", err)
	}

	tests := []struct {
		name   string
		mutate func(*Session)
	}{
		{name: "missing session ID", mutate: func(s *Session) { s.ID = "" }},
		{name: "missing source ID", mutate: func(s *Session) { s.Import.SourceID = "" }},
		{name: "missing adapter", mutate: func(s *Session) { s.Import.AdapterName = "" }},
		{name: "missing adapter version", mutate: func(s *Session) { s.Import.AdapterVersion = "" }},
		{name: "missing format version", mutate: func(s *Session) { s.Import.FormatVersion = "" }},
		{name: "missing model version", mutate: func(s *Session) { s.Import.ModelVersion = "" }},
		{name: "missing normalization version", mutate: func(s *Session) { s.Import.NormalizationVersion = "" }},
		{name: "invalid diagnostic", mutate: func(s *Session) {
			s.Diagnostics = []Diagnostic{{Severity: SeverityWarning, Message: "partial record"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := validSession()
			tt.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want structural validation error")
			}
		})
	}
}

func TestEventKindsAndPayloadValidation(t *testing.T) {
	payloads := []NormalizedData{
		MessageData{Role: MessageRoleUser, Text: "please inspect"},
		ToolCallData{},
		ToolResultData{},
		CommandData{},
		FileReadData{},
		FileMutationData{Operation: FileMutationUnknown},
		PatchData{},
		UsageData{},
		ErrorData{},
		SummaryData{},
		UnknownData{},
	}
	kinds := EventKinds()
	if len(kinds) != len(payloads) {
		t.Fatalf("len(EventKinds()) = %d, want %d", len(kinds), len(payloads))
	}
	for i, payload := range payloads {
		event := validEvent(EventID("event-"+kinds[i]), int64(i), kinds[i], payload)
		if err := event.Validate(); err != nil {
			t.Errorf("%s event Validate() error = %v", kinds[i], err)
		}
	}

	unknown := validEvent("unknown-event", 0, EventKindUnknown, UnknownData{})
	unknown.Timestamp = nil
	unknown.SearchableText = ""
	if err := unknown.Validate(); err != nil {
		t.Fatalf("incomplete unknown event Validate() error = %v", err)
	}
}

func TestEventValidateRejectsInvalidStructure(t *testing.T) {
	negative := int64(-1)
	zero := int64(0)
	tests := []struct {
		name   string
		mutate func(*Event)
	}{
		{name: "missing ID", mutate: func(e *Event) { e.ID = "" }},
		{name: "missing session ID", mutate: func(e *Event) { e.SessionID = "" }},
		{name: "negative sequence", mutate: func(e *Event) { e.Sequence = -1 }},
		{name: "unknown kind", mutate: func(e *Event) { e.Kind = "native_message" }},
		{name: "missing summary", mutate: func(e *Event) { e.Summary = "" }},
		{name: "missing data", mutate: func(e *Event) { e.Data = nil }},
		{name: "kind mismatch", mutate: func(e *Event) { e.Kind = EventKindCommand }},
		{name: "bad role", mutate: func(e *Event) { e.Data = MessageData{Role: "developer"} }},
		{name: "negative usage", mutate: func(e *Event) {
			e.Kind = EventKindUsage
			e.Data = UsageData{InputTokens: &negative}
		}},
		{name: "zero line", mutate: func(e *Event) {
			e.Kind = EventKindFileRead
			e.Data = FileReadData{StartLine: &zero}
		}},
		{name: "invalid raw reference", mutate: func(e *Event) { e.RawRecord.ID = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			event := validEvent("event-1", 0, EventKindMessage, MessageData{Role: MessageRoleUnknown})
			tt.mutate(&event)
			if err := event.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want structural validation error")
			}
		})
	}
}

func TestValidateEventOrderUsesSequenceNotTimestamp(t *testing.T) {
	later := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	earlier := later.Add(-time.Hour)
	events := []Event{
		validEvent("event-1", 2, EventKindSummary, SummaryData{}),
		validEvent("event-2", 7, EventKindSummary, SummaryData{}),
		validEvent("event-3", 11, EventKindSummary, SummaryData{}),
	}
	events[0].Timestamp = &later
	events[1].Timestamp = nil
	events[2].Timestamp = &earlier
	if err := ValidateEventOrder(events); err != nil {
		t.Fatalf("ValidateEventOrder() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func([]Event)
	}{
		{name: "mixed sessions", mutate: func(events []Event) { events[1].SessionID = "other" }},
		{name: "equal sequence", mutate: func(events []Event) { events[1].Sequence = events[0].Sequence }},
		{name: "decreasing sequence", mutate: func(events []Event) { events[2].Sequence = 3 }},
		{name: "negative sequence", mutate: func(events []Event) { events[0].Sequence = -1 }},
		{name: "duplicate ID", mutate: func(events []Event) { events[2].ID = events[0].ID }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := append([]Event(nil), events...)
			tt.mutate(candidate)
			if err := ValidateEventOrder(candidate); err == nil {
				t.Fatal("ValidateEventOrder() error = nil, want ordering error")
			}
		})
	}

	if err := ValidateEventOrder(nil); err != nil {
		t.Fatalf("ValidateEventOrder(nil) error = %v", err)
	}
}

func TestEvidenceValidationAndOutcomes(t *testing.T) {
	wantOutcomes := []Outcome{
		"Successful",
		"Partially successful",
		"Failed",
		"Abandoned",
		"Unknown",
	}
	if got := Outcomes(); !reflect.DeepEqual(got, wantOutcomes) {
		t.Fatalf("Outcomes() = %#v, want %#v", got, wantOutcomes)
	}

	diagnostic := Diagnostic{Code: "record.partial", Severity: SeverityWarning, Message: "record was incomplete"}
	if err := diagnostic.Validate(); err != nil {
		t.Fatalf("Diagnostic.Validate() error = %v", err)
	}
	diagnostic.RawRecordIDs = []RawRecordID{"raw-1", "raw-1"}
	if err := diagnostic.Validate(); err == nil {
		t.Fatal("Diagnostic.Validate() error = nil for duplicate raw-record evidence")
	}
	finding := Finding{
		ID:          "finding-1",
		SessionID:   "session-1",
		RuleID:      "verification.after-change",
		RuleVersion: "1",
		State:       FindingInsufficientEvidence,
		Explanation: "No relevant verification result was recorded.",
	}
	if err := finding.Validate(); err != nil {
		t.Fatalf("Finding.Validate() error = %v", err)
	}
	assessment := OutcomeAssessment{
		SessionID:         "session-1",
		Outcome:           OutcomeUnknown,
		ClassifierID:      "session-outcome",
		ClassifierVersion: "1",
		Explanation:       "The available evidence is incomplete.",
	}
	if err := assessment.Validate(); err != nil {
		t.Fatalf("OutcomeAssessment.Validate() error = %v", err)
	}

	assessment.Outcome = "success"
	if err := assessment.Validate(); err == nil {
		t.Fatal("OutcomeAssessment.Validate() error = nil for unsupported outcome")
	}
	finding.EventIDs = []EventID{"event-1", "event-1"}
	if err := finding.Validate(); err == nil {
		t.Fatal("Finding.Validate() error = nil for duplicate evidence")
	}
}

func validSession() Session {
	return Session{
		ID: "session-1",
		Import: ImportMetadata{
			SourceID:             "source-1",
			AdapterName:          "fixture",
			AdapterVersion:       "1",
			FormatVersion:        "2026-07",
			ModelVersion:         "1",
			NormalizationVersion: "1",
		},
	}
}

func validEvent(id EventID, sequence int64, kind EventKind, data NormalizedData) Event {
	return Event{
		ID:        id,
		SessionID: "session-1",
		Sequence:  sequence,
		Kind:      kind,
		Summary:   strings.ReplaceAll(string(kind), "_", " "),
		Data:      data,
		RawRecord: RawRecordRef{ID: RawRecordID("raw-" + id), SourceID: "source-1"},
	}
}
