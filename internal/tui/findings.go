package tui

import (
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type findingsState struct {
	page           app.FindingsPage
	err            error
	loading        bool
	evidenceCursor int
}
type findingsLoadedMsg struct {
	generation uint64
	page       app.FindingsPage
	err        error
}

func (m *Model) loadFindings() tea.Cmd {
	ctx := m.replaceRequest()
	m.findingsState.loading = true
	m.findingsState.err = nil
	// Clear previously usable conclusions while a fresh snapshot is requested.
	m.findingsState.page = app.FindingsPage{State: app.EvidenceUnavailable}
	generation, session, services := m.requestGeneration, m.sessionsState.selected, m.services
	return m.startSpinner(func() tea.Msg {
		page, err := services.SessionFindings(ctx, session)
		return findingsLoadedMsg{generation: generation, page: page, err: err}
	})
}
func (m Model) findingsContentLines() []string {
	lines := []string{"Rule assessments · session " + string(m.sessionsState.selected)}
	if m.findingsState.loading {
		return append(lines, "Loading assessments…")
	}
	page := m.findingsState.page
	if m.findingsState.err != nil || page.State != app.EvidenceComplete {
		return append(lines, fmt.Sprintf("Assessments unavailable · %s · stale: %t", page.Status, page.Stale), "Use x from the timeline for projection readiness, retry and rebuild.")
	}
	for _, f := range page.Assessments {
		lines = append(lines, "", fmt.Sprintf("%s · %s · version %s", f.RuleID, f.State, f.RuleVersion), "Assessment "+string(f.ID), f.Explanation,
			fmt.Sprintf("Invocations %s · failures %s · recovered %s · uncertainties %s · omitted references %s", f.Metadata["invocations"], f.Metadata["failures"], f.Metadata["recovered"], f.Metadata["uncertainties"], f.Metadata["omitted_references"]))
		if f.Metadata["sequences"] != "" {
			lines = append(lines, "Repeated failure sequences "+f.Metadata["sequences"])
		}
		if f.Metadata["unresolved"] != "" {
			lines = append(lines, fmt.Sprintf("Conclusively unresolved %s · tests %s · static checks %s · builds %s", f.Metadata["unresolved"], f.Metadata["tests"], f.Metadata["static_checks"], f.Metadata["builds"]))
		}
	}
	lines = append(lines, "", "Missing verification has not been assessed.", "Supporting events · [ / ] select · Enter opens timeline context")
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
	m.screen = timelineScreen
	m.resetTimelineState()
	m.timelineState.loading = true
	m.timelineState.selected = id
	ctx := m.replaceRequest()
	generation, services, session := m.requestGeneration, m.services, m.sessionsState.selected
	return m, m.startSpinner(func() tea.Msg {
		page, err := services.Timeline(ctx, app.TimelineRequest{SessionID: session, FocusedEvent: id, Limit: pageSize, IncludePayloads: true})
		return timelineLoadedMsg{generation: generation, page: page, err: err}
	})
}
