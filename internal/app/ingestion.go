package app

import (
	"context"
	"fmt"

	"github.com/pooya79/AgentSession/internal/model"
)

type SourceSummary struct {
	ID     model.SourceID
	Kind   string
	Path   string
	Origin string
}

type DiscoveryDiagnostic struct {
	Code     string
	Severity model.Severity
	Kind     string
	Path     string
	Message  string
}

type SourceDiscovery struct {
	State       EvidenceState
	Sources     []SourceSummary
	Diagnostics []DiscoveryDiagnostic
}

type ImportStart struct {
	State        EvidenceState
	Subscription *ImportSubscription
	Joined       bool
}

type Ingestion interface {
	DiscoverSources(context.Context) (SourceDiscovery, error)
	StartImport(context.Context, model.SourceID) (ImportStart, error)
}

// Services is the complete, presentation-neutral boundary shared by TUI and web.
type Services interface {
	Explorer
	Ingestion
}

func (r *Runtime) DiscoverSources(ctx context.Context) (SourceDiscovery, error) {
	result, err := r.Discover(ctx)
	if err != nil {
		return SourceDiscovery{}, err
	}
	discovery := SourceDiscovery{State: EvidenceComplete, Sources: make([]SourceSummary, 0, len(result.Sources))}
	for _, source := range result.Sources {
		discovery.Sources = append(discovery.Sources, SourceSummary{ID: source.ID, Kind: string(source.Kind), Path: source.Path, Origin: string(source.Origin)})
	}
	for _, diagnostic := range result.Diagnostics {
		discovery.Diagnostics = append(discovery.Diagnostics, DiscoveryDiagnostic{
			Code: string(diagnostic.Code), Severity: diagnostic.Severity, Kind: string(diagnostic.Kind),
			Path: diagnostic.Path, Message: diagnostic.Message,
		})
	}
	if len(discovery.Diagnostics) > 0 {
		discovery.State = EvidencePartial
		if len(discovery.Sources) == 0 {
			discovery.State = EvidenceUnavailable
		}
	}
	return discovery, nil
}

func (r *Runtime) StartImport(ctx context.Context, sourceID model.SourceID) (ImportStart, error) {
	if err := ctx.Err(); err != nil {
		return ImportStart{}, err
	}
	if err := validateIdentifier("source", string(sourceID)); err != nil {
		return ImportStart{}, err
	}
	subscription, joined, err := r.RequestImport(sourceID)
	if err != nil {
		if _, found := r.DiscoveredSource(sourceID); !found {
			return ImportStart{State: EvidenceNotFound}, nil
		}
		return ImportStart{}, fmt.Errorf("start import for source %q: %w", sourceID, err)
	}
	return ImportStart{State: EvidenceComplete, Subscription: subscription, Joined: joined}, nil
}
