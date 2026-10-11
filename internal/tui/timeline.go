package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

const timelinePrefetchThreshold = 5

func (m *Model) resetTimelineState() {
	timelineViewport := m.timelineState.viewport
	timelineViewport.GotoTop()
	m.timelineState = timelineState{
		requestedCursors:  make(map[string]bool),
		expanded:          make(map[model.EventID]bool),
		inspections:       make(map[model.EventID]app.UnknownEvidenceInspection),
		inspectionErrors:  make(map[model.EventID]error),
		inspectionLoading: make(map[model.EventID]bool),
		viewport:          timelineViewport,
		renderRevision:    1,
	}
}

func (m *Model) invalidateTimelineRender() {
	m.timelineState.renderRevision++
	m.timelineState.cachedLines = nil
	m.timelineState.cachedRanges = nil
}

func (m *Model) appendTimelinePage(next app.TimelinePage) {
	seen := make(map[model.EventID]bool, len(m.timelineState.page.Events))
	var lastSequence int64 = -1
	for _, event := range m.timelineState.page.Events {
		seen[event.ID] = true
		if event.Sequence > lastSequence {
			lastSequence = event.Sequence
		}
	}
	for _, event := range next.Events {
		if seen[event.ID] || event.Sequence <= lastSequence {
			continue
		}
		m.timelineState.page.Events = append(m.timelineState.page.Events, event)
		seen[event.ID] = true
		lastSequence = event.Sequence
	}
	if m.timelineState.page.Payloads == nil {
		m.timelineState.page.Payloads = make(map[model.EventID]model.NormalizedData)
	}
	for eventID, payload := range next.Payloads {
		if seen[eventID] {
			m.timelineState.page.Payloads[eventID] = payload
		}
	}
	m.timelineState.page.NextCursor = next.NextCursor
	m.timelineState.page.State = next.State
	m.timelineState.page.Diagnostics = next.Diagnostics
	m.timelineState.page.Interpretation = next.Interpretation
	m.invalidateTimelineRender()
}

func (m *Model) prefetchTimelineIfNeeded() tea.Cmd {
	if m.screen != timelineScreen || m.timelineState.loading || m.services == nil {
		return nil
	}
	next := m.timelineState.page.NextCursor
	if next == "" || m.timelineState.requestedCursors[next] {
		return nil
	}
	nearSelection := len(m.timelineState.page.Events)-m.timelineState.cursor <= timelinePrefetchThreshold
	if !nearSelection && !m.timelineState.viewport.AtBottom() {
		return nil
	}
	ctx := m.replaceRequest()
	m.timelineState.loading = true
	m.timelineState.err = nil
	m.timelineState.pendingCursor = next
	m.timelineState.requestedCursors[next] = true
	m.invalidateTimelineRender()
	return m.startSpinner(loadTimeline(
		ctx, m.services, m.requestGeneration, m.sessionsState.selected, next,
	))
}

// clearSupersededInspections releases retry guards for inspection reads
// canceled whenever the shared evidence request is replaced.
func (m *Model) clearSupersededInspections() {
	changed := false
	for eventID, loading := range m.timelineState.inspectionLoading {
		if loading {
			delete(m.timelineState.inspectionLoading, eventID)
			changed = true
		}
	}
	if changed {
		m.invalidateTimelineRender()
	}
	m.detailState.inspectionLoading = false
}

func (m *Model) anchorTimelineSelection(ranges map[model.EventID]timelineCardRange) {
	if m.screen != timelineScreen || m.timelineState.selected == "" {
		return
	}
	card, ok := ranges[m.timelineState.selected]
	if !ok {
		return
	}
	top := m.timelineState.viewport.YOffset()
	height := max(1, m.timelineState.viewport.Height())
	switch {
	case card.start < top:
		m.timelineState.viewport.SetYOffset(card.start)
	case card.end-card.start >= height && card.start >= top+height:
		m.timelineState.viewport.SetYOffset(card.start)
	case card.end-card.start >= height:
		// Keep the selected card's header anchored while expanded evidence
		// extends below the viewport.
		return
	case card.end > top+height:
		m.timelineState.viewport.SetYOffset(max(card.start, card.end-height))
	}
}
