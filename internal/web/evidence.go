package web

import (
	"context"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// resolveDiagnostics resolves a bounded, deduplicated set of evidence links.
func (h *handler) resolveDiagnostics(ctx context.Context, expected model.SessionID, diagnostics []model.Diagnostic) map[model.EventID]eventReference {
	ids := make([]model.EventID, 0)
	seen := make(map[model.EventID]struct{})
	for _, diagnostic := range diagnostics {
		for _, id := range diagnostic.EventIDs {
			if _, exists := seen[id]; !exists && len(ids) < app.MaximumPageSize {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	result := make(map[model.EventID]eventReference, len(ids))
	locations, err := h.services.EventLocations(ctx, ids)
	if err != nil {
		return result
	}
	for _, id := range ids {
		location, found := locations[id]
		result[id] = eventReference{Found: found, MatchesSession: found && (expected == "" || location.SessionID == expected), SessionID: location.SessionID}
	}
	return result
}

// resolveImportDiagnostics adapts import diagnostics to the shared evidence resolver.
func (h *handler) resolveImportDiagnostics(ctx context.Context, diagnostics []app.ImportAllDiagnostic) map[model.EventID]eventReference {
	normalized := make([]model.Diagnostic, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		normalized = append(normalized, model.Diagnostic{EventIDs: diagnostic.EventIDs})
	}
	return h.resolveDiagnostics(ctx, "", normalized)
}
