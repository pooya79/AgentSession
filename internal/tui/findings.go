package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type findingsState struct {
	sessionID         model.SessionID
	refreshIncomplete bool
	page              app.FindingsPage
	err               error
	loading           bool
	evidenceCursor    int
}
type findingsLoadedMsg struct {
	generation uint64
	page       app.FindingsPage
	err        error
}

func (m *Model) loadFindings() tea.Cmd {
	ctx := m.replaceRequest()
	if m.findingsState.sessionID != m.sessionsState.selected {
		m.findingsState = findingsState{sessionID: m.sessionsState.selected}
	}
	m.findingsState.refreshIncomplete = m.findingsState.page.State == app.EvidenceComplete
	m.findingsState.loading = true
	m.findingsState.err = nil
	// Retain usable assessments with an explicit previous-snapshot label while
	// refreshing, so cancellation or failure does not discard evidence.
	generation, session, services := m.requestGeneration, m.sessionsState.selected, m.services
	return m.startSpinner(func() tea.Msg {
		page, err := services.SessionFindings(ctx, session)
		return findingsLoadedMsg{generation: generation, page: page, err: err}
	})
}
func (m Model) findingsContentLines() []string {
	lines := []string{"Rule assessments · session " + string(m.sessionsState.selected)}
	page := m.findingsState.page
	if m.findingsState.loading {
		lines = append(lines, "Loading assessments…")
		if page.State != app.EvidenceComplete {
			return lines
		}
	}
	if m.findingsState.err != nil {
		lines = append(lines, "Could not refresh assessments: "+safeInline(m.findingsState.err.Error()), "Press r to retry.")
	}
	if page.State != app.EvidenceComplete {
		return append(lines, fmt.Sprintf("Assessments unavailable · %s · stale: %t", page.Status, page.Stale), "Use x from the timeline for projection readiness, retry and rebuild.")
	}
	if m.findingsState.refreshIncomplete {
		lines = append(lines, "Previous snapshot · freshness has not been confirmed.")
	}
	for _, f := range page.Assessments {
		lines = append(lines, "", fmt.Sprintf("%s · %s · version %s", f.RuleID, f.State, f.RuleVersion), "Assessment "+string(f.ID), f.Explanation)
		if f.Metadata["applicable_changes"] != "" {
			lines = append(lines, fmt.Sprintf("Confirmed paths %s · applicable changes %s · verified changes %s · missing changes %s · documentation exemptions %s", f.Metadata["confirmed_changes"], f.Metadata["applicable_changes"], f.Metadata["verified_changes"], f.Metadata["missing_changes"], f.Metadata["documentation_changes"]))
		} else {
			lines = append(lines, fmt.Sprintf("Invocations %s · failures %s · recovered %s", f.Metadata["invocations"], f.Metadata["failures"], f.Metadata["recovered"]))
		}
		lines = append(lines, fmt.Sprintf("uncertainties %s · omitted references %s", f.Metadata["uncertainties"], f.Metadata["omitted_references"]))
		if f.Metadata["sequences"] != "" {
			lines = append(lines, "Repeated failure sequences "+f.Metadata["sequences"])
		}
		if f.Metadata["unresolved"] != "" {
			lines = append(lines, fmt.Sprintf("Conclusively unresolved %s · tests %s · static checks %s · builds %s", f.Metadata["unresolved"], f.Metadata["tests"], f.Metadata["static_checks"], f.Metadata["builds"]))
		}
	}
	lines = append(lines, "", "No severity grade or session outcome is assigned.", "Supporting events · [ / ] select · Enter opens timeline context")
	for i, id := range m.findingReferences() {
		marker := " "
		if i == m.findingsState.evidenceCursor {
			marker = ">"
		}
		lines = append(lines, marker+" "+string(id))
	}
	return lines
}
func (m Model) findingReferences() []model.EventID {
	var refs []model.EventID
	for _, f := range m.findingsState.page.Assessments {
		refs = append(refs, f.EventIDs...)
	}
	return refs
}
func (m *Model) openFindingEvidence() (tea.Model, tea.Cmd) {
	refs := m.findingReferences()
	if m.findingsState.page.State != app.EvidenceComplete || len(refs) == 0 {
		return m, nil
	}
	id := refs[clamp(m.findingsState.evidenceCursor, 0, len(refs)-1)]
	m.pushNavigation()
	m.screen = timelineScreen
	m.resetTimelineState()
	m.timelineState.selected = id
	ctx := m.replaceRequest()
	m.timelineState.loading = true
	generation, services, session := m.requestGeneration, m.services, m.sessionsState.selected
	return m, m.startSpinner(func() tea.Msg {
		page, err := services.Timeline(ctx, app.TimelineRequest{SessionID: session, FocusedEvent: id, Limit: pageSize, IncludePayloads: true})
		return timelineLoadedMsg{generation: generation, page: page, err: err}
	})
}
