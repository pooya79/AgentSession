package model

import (
	"fmt"
	"strings"
)

// Severity expresses the user-facing importance of a diagnostic or finding.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

func (s Severity) valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityError:
		return true
	default:
		return false
	}
}

// Diagnostic records a problem or limitation encountered while processing
// evidence without discarding the otherwise usable result.
type Diagnostic struct {
	// Code is a stable machine-readable identifier; Message is the
	// human-readable explanation.
	Code     string
	Severity Severity
	Message  string

	// Evidence references are optional because a source-level diagnostic may
	// be known before a canonical event can be produced.
	EventIDs     []EventID
	RawRecordIDs []RawRecordID
}

// RecordDiagnostic associates a recoverable normalization diagnostic with one
// retained raw record. Ordinal is the zero-based position among diagnostics
// emitted for that record and provides stable incremental persistence identity.
type RecordDiagnostic struct {
	RawRecordID RawRecordID
	Ordinal     int64
	Diagnostic  Diagnostic
}

// Validate checks the record association and diagnostic structure.
func (d RecordDiagnostic) Validate() error {
	if strings.TrimSpace(string(d.RawRecordID)) == "" {
		return fmt.Errorf("record diagnostic raw record ID is required")
	}
	if d.Ordinal < 0 {
		return fmt.Errorf("record diagnostic ordinal must not be negative")
	}
	if err := d.Diagnostic.Validate(); err != nil {
		return fmt.Errorf("record diagnostic: %w", err)
	}
	for _, rawRecordID := range d.Diagnostic.RawRecordIDs {
		if rawRecordID != d.RawRecordID {
			return fmt.Errorf("record diagnostic references unrelated raw record %q", rawRecordID)
		}
	}
	return nil
}

// Validate checks the structural invariants of a diagnostic.
func (d Diagnostic) Validate() error {
	if strings.TrimSpace(d.Code) == "" {
		return fmt.Errorf("diagnostic code is required")
	}
	if !d.Severity.valid() {
		return fmt.Errorf("unsupported diagnostic severity %q", d.Severity)
	}
	if strings.TrimSpace(d.Message) == "" {
		return fmt.Errorf("diagnostic message is required")
	}
	if err := validateEvidenceIDs(d.EventIDs); err != nil {
		return err
	}
	return validateRawRecordIDs(d.RawRecordIDs)
}

// FindingState distinguishes rule evaluation from applicability and missing
// evidence. NotTriggered is not equivalent to NotApplicable.
type FindingState string

const (
	FindingTriggered            FindingState = "triggered"
	FindingNotTriggered         FindingState = "not_triggered"
	FindingNotApplicable        FindingState = "not_applicable"
	FindingInsufficientEvidence FindingState = "insufficient_evidence"
)

func (s FindingState) valid() bool {
	switch s {
	case FindingTriggered, FindingNotTriggered, FindingNotApplicable, FindingInsufficientEvidence:
		return true
	default:
		return false
	}
}

// Finding is an explainable, versioned result from a deterministic rule.
type Finding struct {
	ID          FindingID
	SessionID   SessionID
	RuleID      string
	RuleVersion Version
	State       FindingState
	// Severity is optional because not-applicable and informational rule
	// results may not have a severity.
	Severity Severity

	// Explanation states why the rule reached State. EventIDs identify the
	// canonical evidence supporting that explanation.
	Explanation string
	EventIDs    []EventID

	// Metadata contains small source-neutral values emitted by the rule. It
	// must not be used to hide source-specific parsing in the analysis layer.
	Metadata map[string]string
}

// Validate checks the structural invariants of a finding.
func (f Finding) Validate() error {
	if strings.TrimSpace(string(f.ID)) == "" {
		return fmt.Errorf("finding ID is required")
	}
	if strings.TrimSpace(string(f.SessionID)) == "" {
		return fmt.Errorf("finding session ID is required")
	}
	if strings.TrimSpace(f.RuleID) == "" {
		return fmt.Errorf("finding rule ID is required")
	}
	if strings.TrimSpace(string(f.RuleVersion)) == "" {
		return fmt.Errorf("finding rule version is required")
	}
	if !f.State.valid() {
		return fmt.Errorf("unsupported finding state %q", f.State)
	}
	if f.Severity != "" && !f.Severity.valid() {
		return fmt.Errorf("unsupported finding severity %q", f.Severity)
	}
	if strings.TrimSpace(f.Explanation) == "" {
		return fmt.Errorf("finding explanation is required")
	}
	return validateEvidenceIDs(f.EventIDs)
}

// Outcome is the conservative session-level classification derived from
// recorded evidence.
type Outcome string

const (
	OutcomeSuccessful          Outcome = "Successful"
	OutcomePartiallySuccessful Outcome = "Partially successful"
	OutcomeFailed              Outcome = "Failed"
	OutcomeAbandoned           Outcome = "Abandoned"
	OutcomeUnknown             Outcome = "Unknown"
)

// Outcomes returns every supported outcome in stable order.
func Outcomes() []Outcome {
	return []Outcome{
		OutcomeSuccessful,
		OutcomePartiallySuccessful,
		OutcomeFailed,
		OutcomeAbandoned,
		OutcomeUnknown,
	}
}

func (o Outcome) valid() bool {
	for _, candidate := range Outcomes() {
		if o == candidate {
			return true
		}
	}
	return false
}

// OutcomeAssessment records the classifier and evidence behind an outcome.
type OutcomeAssessment struct {
	SessionID         SessionID
	Outcome           Outcome
	ClassifierID      string
	ClassifierVersion Version

	// Explanation and EventIDs make the classification inspectable. EventIDs
	// may be empty for Unknown when no reliable canonical evidence is present.
	Explanation string
	EventIDs    []EventID
}

// Validate checks the structural invariants of an outcome assessment.
func (a OutcomeAssessment) Validate() error {
	if strings.TrimSpace(string(a.SessionID)) == "" {
		return fmt.Errorf("outcome session ID is required")
	}
	if !a.Outcome.valid() {
		return fmt.Errorf("unsupported outcome %q", a.Outcome)
	}
	if strings.TrimSpace(a.ClassifierID) == "" {
		return fmt.Errorf("outcome classifier ID is required")
	}
	if strings.TrimSpace(string(a.ClassifierVersion)) == "" {
		return fmt.Errorf("outcome classifier version is required")
	}
	if strings.TrimSpace(a.Explanation) == "" {
		return fmt.Errorf("outcome explanation is required")
	}
	return validateEvidenceIDs(a.EventIDs)
}

func validateEvidenceIDs(ids []EventID) error {
	seen := make(map[EventID]struct{}, len(ids))
	for i, id := range ids {
		if strings.TrimSpace(string(id)) == "" {
			return fmt.Errorf("evidence event ID %d is empty", i)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("evidence event ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateRawRecordIDs(ids []RawRecordID) error {
	seen := make(map[RawRecordID]struct{}, len(ids))
	for i, id := range ids {
		if strings.TrimSpace(string(id)) == "" {
			return fmt.Errorf("diagnostic raw record ID %d is empty", i)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("diagnostic raw record ID %q is duplicated", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
