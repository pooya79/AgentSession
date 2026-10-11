package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/storage"
)

// FindingsPage distinguishes unavailable derived output from evaluated rules.
// Only implemented rules appear; missing rules are never implied to pass.
type FindingsPage struct {
	SessionID   model.SessionID
	State       EvidenceState
	Status      string
	Stale       bool
	Assessments []model.Finding
}

type Assessor interface {
	SessionFindings(context.Context, model.SessionID) (FindingsPage, error)
}

type assessmentService struct{ reader storage.FindingsReader }

func NewAssessor(reader storage.FindingsReader) (Assessor, error) {
	if reader == nil {
		return nil, errors.New("application findings: reader is required")
	}
	return &assessmentService{reader: reader}, nil
}
func (s *assessmentService) SessionFindings(ctx context.Context, session model.SessionID) (FindingsPage, error) {
	page := FindingsPage{SessionID: session, State: EvidenceUnavailable}
	if err := validateIdentifier("session", string(session)); err != nil {
		return page, err
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	snapshot, err := s.reader.SessionFindings(ctx, session)
	if err != nil {
		return page, fmt.Errorf("application findings: read assessment: %w", err)
	}
	if !snapshot.Found {
		page.State = EvidenceNotFound
		return page, nil
	}
	page.Status = snapshot.Status
	page.Stale = snapshot.Stale
	if snapshot.Usable {
		// Storage enforces evidence ownership on atomic publication; validate the
		// navigation identifiers before exposing the complete bounded snapshot.
		for _, f := range snapshot.Assessments {
			if f.SessionID != session {
				return page, errors.New("application findings: invalid evidence ownership")
			}
			for _, id := range f.EventIDs {
				if err := validateEventID(id); err != nil {
					return page, errors.New("application findings: invalid evidence reference")
				}
			}
		}
		page.State = EvidenceComplete
		page.Assessments = snapshot.Assessments
		sort.SliceStable(page.Assessments, func(i, j int) bool {
			return page.Assessments[i].State == model.FindingTriggered && page.Assessments[j].State != model.FindingTriggered
		})
	}
	return page, nil
}
func (r *Runtime) SessionFindings(ctx context.Context, session model.SessionID) (FindingsPage, error) {
	return r.assessor.SessionFindings(ctx, session)
}
