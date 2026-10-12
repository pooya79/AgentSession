package tui

import (
	"cmp"
	"context"
	"reflect"
	"slices"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

const timelinePrefetchThreshold = 5

type timelineReadingAnchor struct {
	eventID    model.EventID
	position   int
	header     bool
	occurrence int
}

func (m *Model) captureTimelineAnchor() {
	top := m.timelineState.viewport.YOffset()
	for id, card := range m.timelineState.cachedRanges {
		if top >= card.start && top < card.end && len(card.offsets) > 0 {
			row := min(top-card.start, len(card.offsets)-1)
			first := row
			for first > 1 && card.offsets[first-1] == card.offsets[row] {
				first--
			}
			m.timelineState.anchor = timelineReadingAnchor{eventID: id, position: card.offsets[row], header: row == 0, occurrence: row - first}
			return
		}
	}
}

func (m *Model) restoreTimelineAnchor(ranges map[model.EventID]timelineCardRange) {
	anchor := m.timelineState.anchor
	card, ok := ranges[anchor.eventID]
	if !ok {
		return
	}
	row := 0
	if !anchor.header {
		for i := 1; i < len(card.offsets); i++ {
			if card.offsets[i] > anchor.position {
				break
			}
			if row == 0 || card.offsets[i] != card.offsets[row] {
				row = i
			}
		}
		for range anchor.occurrence {
			if row+1 >= len(card.offsets) || card.offsets[row+1] != card.offsets[row] {
				break
			}
			row++
		}
	}
	m.timelineState.viewport.SetYOffset(card.start + row)
}

// reconcileTimelineAnchor compares retained evidence when a refresh replaces
// it. Identity alone cannot promise the same position after a reimport.
func (m *Model) reconcileTimelineAnchor(previous app.TimelinePage, inspections map[model.EventID]app.UnknownEvidenceInspection) {
	anchor := m.timelineState.anchor
	if anchor.eventID == "" {
		return
	}
	found := false
	for _, event := range m.timelineState.page.Events {
		if event.ID == anchor.eventID {
			found = true
			break
		}
	}
	switch {
	case !found:
		m.timelineState.notice = "Reading anchor removed; showing nearby available evidence."
		sequence := int64(0)
		for _, event := range previous.Events {
			if event.ID == anchor.eventID {
				sequence = event.Sequence
				break
			}
		}
		anchor = timelineReadingAnchor{}
		for _, event := range m.timelineState.page.Events {
			anchor = timelineReadingAnchor{eventID: event.ID, header: true}
			if event.Sequence >= sequence {
				break
			}
		}
	case previous.Payloads[anchor.eventID] != nil && !reflect.DeepEqual(previous.Payloads[anchor.eventID], m.timelineState.page.Payloads[anchor.eventID]) ||
		!reflect.DeepEqual(m.timelineState.inspections[anchor.eventID], inspections[anchor.eventID]):
		m.timelineState.notice = "Reading anchor changed; showing the updated event from its start."
		anchor = timelineReadingAnchor{eventID: anchor.eventID, header: true}
	}
	m.timelineState.anchor = anchor
	for id := range m.timelineState.disclosureAnchors {
		if previous.Payloads[id] != nil && !reflect.DeepEqual(previous.Payloads[id], m.timelineState.page.Payloads[id]) ||
			!reflect.DeepEqual(m.timelineState.inspections[id], inspections[id]) {
			delete(m.timelineState.disclosureAnchors, id)
		}
	}
	// Raw inspections are revalidated by the shared bounded/redacted service
	// before this replacement is published. Failed reads retain the old snapshot.
	if inspections == nil {
		inspections = make(map[model.EventID]app.UnknownEvidenceInspection)
	}
	m.timelineState.inspections = inspections
	m.timelineState.inspectionErrors = make(map[model.EventID]error)
}

func (m *Model) resetTimelineState() {
	m.releaseSavedTimelineContent()
	timelineViewport := m.timelineState.viewport
	timelineViewport.GotoTop()
	m.timelineState = timelineState{
		requestedCursors:  make(map[string]bool),
		expanded:          make(map[model.EventID]bool),
		disclosureAnchors: make(map[model.EventID]timelineReadingAnchor),
		inspections:       make(map[model.EventID]app.UnknownEvidenceInspection),
		inspectionErrors:  make(map[model.EventID]error),
		inspectionLoading: make(map[model.EventID]bool),
		viewport:          timelineViewport,
		renderRevision:    1,
	}
}

func (m *Model) invalidateTimelineRender() {
	m.captureTimelineAnchor()
	m.timelineState.renderRevision++
	m.timelineState.cachedLines = nil
	m.timelineState.cachedRanges = nil
}

func (m *Model) appendTimelinePage(next app.TimelinePage) {
	m.timelineState.page = mergeTimelinePage(m.timelineState.page, next)
	m.invalidateTimelineRender()
}

func mergeTimelinePage(current, next app.TimelinePage) app.TimelinePage {
	rows := make(map[model.EventID]model.EventSummary, len(current.Events)+len(next.Events))
	for _, event := range current.Events {
		rows[event.ID] = event
	}
	for _, event := range next.Events {
		rows[event.ID] = event
	}
	current.Events = make([]model.EventSummary, 0, len(rows))
	for _, event := range rows {
		current.Events = append(current.Events, event)
	}
	slices.SortFunc(current.Events, func(a, b model.EventSummary) int { return cmp.Compare(a.Sequence, b.Sequence) })
	if current.Payloads == nil {
		current.Payloads = make(map[model.EventID]model.NormalizedData)
	}
	for _, event := range next.Events {
		current.Payloads[event.ID] = next.Payloads[event.ID]
	}
	current.NextCursor = next.NextCursor
	current.State = next.State
	current.Diagnostics = next.Diagnostics
	current.Interpretation = next.Interpretation
	return current
}

// clearSupersededReads releases guards for all reads canceled when the shared
// evidence request is replaced.
func (m *Model) clearSupersededReads() {
	// The evidence reads share one cancellation generation. Release every
	// guard owned by that generation, retaining all previously usable data.
	if m.timelineState.loading {
		delete(m.timelineState.requestedCursors, m.timelineState.pendingCursor)
		m.timelineState.loading = false
		m.timelineState.pendingCursor = ""
		m.timelineState.pendingTarget = ""
		m.invalidateTimelineRender()
	}
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	if m.searchState.loading {
		m.searchState.loading = false
		m.searchState.notice = "Search canceled."
	}
	if m.detailState.loading {
		m.detailState.err = inspectionError(context.Canceled)
		m.detailState.loading = false
	}
	m.findingsState.loading = false
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

func (m *Model) revealTimelineSelection(ranges map[model.EventID]timelineCardRange) {
	if m.screen != timelineScreen || m.timelineState.selected == "" {
		return
	}
	card, ok := ranges[m.timelineState.selected]
	if !ok {
		return
	}
	m.timelineState.viewport.SetYOffset(card.start)
}
