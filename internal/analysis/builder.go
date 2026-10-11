package analysis

import (
	"context"
	"errors"

	"github.com/pooya79/AgentSession/internal/model"
	"github.com/pooya79/AgentSession/internal/projection"
)

// Store provides authoritative bounded evidence and atomic claim publication.
type Store interface {
	CommandReader
	PublishFindings(context.Context, projection.Claim, []model.Finding) error
}

type Builder struct{ store Store }

func NewBuilder(store Store) (*Builder, error) {
	if store == nil {
		return nil, errors.New("findings builder: store is required")
	}
	return &Builder{store: store}, nil
}
func (b *Builder) Build(ctx context.Context, request projection.BuildRequest) error {
	if request.BuildToken == "" || request.Version != RuleVersion {
		return errors.New("findings builder: invalid claim")
	}
	finding, err := AssessRepeatedFailures(ctx, request.SessionID, b.store)
	if err != nil {
		return err
	}
	verification, err := AssessUnresolvedVerification(ctx, request.SessionID, b.store)
	if err != nil {
		return err
	}
	// The complete bounded result is staged in memory and published in one
	// transaction guarded by the active token, version and canonical revision.
	return b.store.PublishFindings(ctx, projection.Claim{SessionID: request.SessionID, Kind: projection.KindFindings, Version: request.Version, Revision: request.CanonicalRevision, RunToken: request.BuildToken}, []model.Finding{finding, verification})
}
