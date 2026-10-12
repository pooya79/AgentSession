package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

type findingsState struct {
	showDetails       bool
	sessionID         model.SessionID
	refreshIncomplete bool
	page              app.FindingsPage
	err               error
	loading           bool
	evidenceCursor    int
	notice            string
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
	lines := []string{"Outcome: Unknown (not assessed)", ""}
	page := m.findingsState.page
	if m.findingsState.notice != "" {
		lines = append(lines, m.findingsState.notice)
	}
	if m.findingsState.loading {
		lines = append(lines, "Loading assessments…")
		if page.State != app.EvidenceComplete {
			return lines
		}
	}
	if m.findingsState.err != nil {
		lines = append(lines, "Could not refresh assessments: "+safeInline(m.findingsState.err.Error()), "Press r to retry.")
	}
	if page.State == app.EvidenceNotFound {
		return append(lines, "This session is no longer available.")
	}
	if page.State != app.EvidenceComplete {
		return append(lines, fmt.Sprintf("Assessments unavailable · %s · stale: %t", page.Status, page.Stale), "Use x from the timeline for projection readiness, retry and rebuild.")
	}
	if m.findingsState.refreshIncomplete {
		lines = append(lines, "Previous snapshot · freshness has not been confirmed.")
	}
	if len(page.Assessments) == 0 {
		lines = append(lines, "No rule assessments returned; this does not establish success.")
	}
	for _, f := range page.Assessments {
		if !m.findingsState.showDetails {
			state := strings.ReplaceAll(string(f.State), "_", " ")
			if f.State == model.FindingInsufficientEvidence {
				state = "not enough evidence"
			}
			lines = append(lines, findingLabel(f.RuleID)+" · "+state)
			continue
		}
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
	if m.findingsState.showDetails {
		lines = append(lines, "", "No severity grade or session outcome is assigned.")
	} else {
		lines = append(lines, "", "v explanations")
	}
	lines = append(lines, "", "Supporting events")
	for i, id := range m.findingReferences() {
		marker := " "
		if i == m.findingsState.evidenceCursor {
			marker = ">"
		}
		label := fmt.Sprintf("Supporting event %d", i+1)
		for _, event := range m.timelineState.page.Events {
			if event.ID == id {
				label = fmt.Sprintf("#%d · %s", event.Sequence, safeInline(event.Summary))
				break
			}
		}
		if m.findingsState.showDetails {
			label += " · " + string(id)
		}
		lines = append(lines, marker+" "+label)
	}
	return lines
}

func findingLabel(rule string) string {
	switch rule {
	case "repeated_command_failures":
		return "Repeated failures"
	case "unresolved_failed_verification":
		return "Unresolved checks"
	case "missing_verification":
		return "Missing checks"
	default:
		return safeInline(strings.ReplaceAll(rule, "_", " "))
	}
}
func (m Model) findingReferences() []model.EventID {
	var refs []model.EventID
	seen := make(map[model.EventID]bool)
	for _, f := range m.findingsState.page.Assessments {
		for _, id := range f.EventIDs {
			if !seen[id] {
				refs = append(refs, id)
				seen[id] = true
			}
		}
	}
	return refs
}

func (m *Model) revealFindingReference() {
	row := 0
	for _, line := range m.findingsContentLines() {
		if strings.HasPrefix(line, "> ") {
			m.detailState.viewport.SetYOffset(row)
			return
		}
		row += len(wrapIndented(terminalText(line), m.renderWidth(), true))
	}
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
