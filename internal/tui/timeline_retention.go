package tui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// These are accounted working-set budgets, not a process heap ceiling. A single
// reading event is always kept in full even when it exceeds either byte budget.
const (
	timelineEventBudget   = 150
	timelinePayloadBudget = 2 << 20
	timelineRenderBudget  = 4 << 20
)

type timelinePayloadMetadata struct {
	digest [32]byte
	bytes  int
}

func (m *Model) rememberTimelinePayloads(page app.TimelinePage) {
	if m.timelineState.payloadMetadata == nil {
		m.timelineState.payloadMetadata = make(map[model.EventID]timelinePayloadMetadata)
	}
	for _, e := range page.Events {
		encoded, _ := json.Marshal(page.Payloads[e.ID]) // Canonical data contains only JSON-compatible fields.
		next := timelinePayloadMetadata{digest: sha256.Sum256(encoded), bytes: len(encoded) + 64}
		if old, ok := m.timelineState.payloadMetadata[e.ID]; ok && old.digest != next.digest {
			delete(m.timelineState.disclosureAnchors, e.ID)
			if m.timelineState.anchor.eventID == e.ID {
				m.timelineState.notice = "Reading anchor changed; showing the updated event from its start."
				m.timelineState.anchor = timelineReadingAnchor{eventID: e.ID, header: true}
			}
		}
		m.timelineState.payloadMetadata[e.ID] = next
	}
}

func (m *Model) timelineEventIndex(id model.EventID) int {
	for i, e := range m.timelineState.page.Events {
		if e.ID == id {
			return i
		}
	}
	return -1
}

// boundTimeline retains a contiguous neighborhood of the reading anchor. Old
// summaries and payload digests remain lightweight navigation/revalidation data.
func (m *Model) boundTimeline() bool {
	state := &m.timelineState
	events := state.page.Events
	if len(events) == 0 {
		return false
	}
	center := m.timelineEventIndex(state.anchor.eventID)
	if center < 0 {
		center = clamp(state.cursor, 0, len(events)-1)
	}
	if _, ok := state.page.Payloads[events[center].ID]; !ok {
		return false
	}
	cost := func(i int) (int, int) {
		id := events[i].ID
		return state.payloadMetadata[id].bytes + len(state.inspections[id].Text), state.cards[id].bytes
	}
	lo, hi := center, center+1
	payload, rendered := cost(center)
	for hi-lo < timelineEventBudget {
		added := false
		for _, i := range []int{lo - 1, hi} {
			if i < 0 || i >= len(events) {
				continue
			}
			if _, ok := state.page.Payloads[events[i].ID]; !ok {
				continue
			}
			p, r := cost(i)
			if payload+p > timelinePayloadBudget || rendered+r > timelineRenderBudget {
				oversized := payload > timelinePayloadBudget || rendered > timelineRenderBudget || p > timelinePayloadBudget || r > timelineRenderBudget
				boundary := i == hi && state.scrollDirection >= 0 && state.viewport.AtBottom() || i < lo && state.scrollDirection < 0 && state.viewport.AtTop()
				// Keep one boundary neighbor while crossing an oversized event. Otherwise
				// eviction could make that evidence impossible to reach by text scrolling.
				if !oversized || !boundary || hi-lo != 1 {
					continue
				}
			}
			payload += p
			rendered += r
			if i < lo {
				lo = i
			} else {
				hi = i + 1
			}
			added = true
			if hi-lo == timelineEventBudget {
				break
			}
		}
		if !added {
			break
		}
	}
	changed := lo != state.residentStart || hi != state.residentEnd
	keep := make(map[model.EventID]bool, hi-lo)
	for _, e := range events[lo:hi] {
		keep[e.ID] = true
	}
	for id := range state.page.Payloads {
		if !keep[id] {
			delete(state.page.Payloads, id)
			delete(state.cards, id)
			delete(state.inspections, id)
			delete(state.inspectionErrors, id)
			delete(state.inspectionLoading, id)
		}
	}
	state.residentStart, state.residentEnd = lo, hi
	if changed {
		m.invalidateTimelineRender()
	}
	return changed
}

func (m *Model) requestTimelineHistory() tea.Cmd {
	state := &m.timelineState
	if m.screen != timelineScreen || m.services == nil || len(state.page.Events) == 0 {
		return nil
	}
	// A selection in evicted history is a request, never an unavailable payload.
	_, selectedResident := state.page.Payloads[state.selected]
	if state.loading {
		if !state.selectionRequested || selectedResident || state.pendingTarget == state.selected {
			return nil
		}
		// Repeated event jumps may supersede a delayed local read.
	}
	if selectedResident {
		state.selectionRequested = false
	}
	m.captureTimelineAnchor()
	start, end := state.residentStart, state.residentEnd
	if end == 0 {
		end = len(state.page.Events)
	}
	target := model.EventID("")
	focused := model.EventID("")
	earlier := false
	cursor := ""
	switch {
	case state.selectionRequested && !selectedResident && state.cursor >= 0:
		target = state.selected
		focused = state.page.Events[min(len(state.page.Events)-1, state.cursor+pageSize/2)].ID
	case state.scrollDirection < 0 && state.viewport.AtTop() && (start > 0 || !state.earlierExhausted):
		earlier = true
		focused = state.page.Events[max(0, start-1)].ID
	case state.scrollDirection >= 0 && state.viewport.AtBottom() && end < len(state.page.Events):
		focused = state.page.Events[min(len(state.page.Events)-1, end+pageSize-1)].ID
	case len(state.page.Events)-state.cursor <= timelinePrefetchThreshold || state.viewport.AtBottom():
		cursor = state.page.NextCursor
		if cursor == "" || state.requestedCursors[cursor] {
			return nil
		}
	default:
		return nil
	}
	ctx := m.replaceRequest()
	state.loading = true
	state.err = nil
	state.pendingCursor = cursor
	state.pendingTarget = target
	if cursor != "" {
		state.requestedCursors[cursor] = true
	}
	m.invalidateTimelineRender()
	generation, services, session := m.requestGeneration, m.services, m.sessionsState.selected
	if focused == "" {
		return m.startSpinner(loadTimeline(ctx, services, generation, session, cursor))
	}
	return m.startSpinner(func() tea.Msg {
		page, err := services.Timeline(ctx, app.TimelineRequest{SessionID: session, FocusedEvent: focused, Limit: pageSize, IncludePayloads: true})
		if err == nil && target != "" {
			found := false
			for _, event := range page.Events {
				if event.ID == target {
					found = true
					break
				}
			}
			// A reimport may move/remove the speculative neighboring anchor while
			// leaving the requested stable identity available. Verify that identity.
			if !found {
				page, err = services.Timeline(ctx, app.TimelineRequest{SessionID: session, FocusedEvent: target, Limit: pageSize, IncludePayloads: true})
			}
		}
		if err == nil && page.State == app.EvidenceNotFound {
			err = fmt.Errorf("history anchor is no longer available after reimport; r to refresh nearby evidence")
		}
		if err == nil {
			err = ctx.Err()
		}
		return timelineLoadedMsg{generation: generation, page: page, err: err, history: true, target: target, earlier: earlier}
	})
}

// loadTimelineWindow refreshes only the current working set, never all visited
// payloads. Each individual read remains bounded and cancellation is explicit.
func loadTimelineWindow(ctx context.Context, services app.Services, session model.SessionID, first, anchor, last model.EventID, through int64) (app.TimelinePage, error) {
	request := app.TimelineRequest{SessionID: session, FocusedEvent: first, Limit: pageSize, IncludePayloads: true}
	page, err := services.Timeline(ctx, request)
	tried := map[model.EventID]bool{first: true}
	for _, fallback := range []model.EventID{anchor, last, ""} {
		if err != nil || page.State != app.EvidenceNotFound {
			break
		}
		if tried[fallback] {
			continue
		}
		tried[fallback] = true
		if err = ctx.Err(); err != nil {
			break
		}
		request.FocusedEvent = fallback
		page, err = services.Timeline(ctx, request)
	}
	seen := make(map[string]bool)
	for err == nil && page.NextCursor != "" && (len(page.Events) == 0 || page.Events[len(page.Events)-1].Sequence < through) {
		if err = ctx.Err(); err != nil {
			break
		}
		cursor := page.NextCursor
		if seen[cursor] {
			return app.TimelinePage{}, fmt.Errorf("refresh timeline: continuation did not advance")
		}
		seen[cursor] = true
		request.FocusedEvent, request.Cursor = "", cursor
		var next app.TimelinePage
		next, err = services.Timeline(ctx, request)
		if err == nil {
			page = mergeTimelinePage(page, next)
		}
		if len(page.Events) >= timelineEventBudget+pageSize {
			break
		}
	}
	if err == nil {
		err = ctx.Err()
	}
	return page, err
}

// A focused read is authoritative only within its returned sequence interval.
// Reimport may remove or replace previously visited rows inside that interval.
func (m *Model) reconcileHistoryWindow(page app.TimelinePage) {
	if len(page.Events) == 0 {
		return
	}
	first, last := page.Events[0].Sequence, page.Events[len(page.Events)-1].Sequence
	returned := make(map[model.EventID]bool, len(page.Events))
	for _, e := range page.Events {
		returned[e.ID] = true
	}
	rows := make([]model.EventSummary, 0, len(m.timelineState.page.Events))
	for _, e := range m.timelineState.page.Events {
		if e.Sequence >= first && e.Sequence <= last && !returned[e.ID] {
			delete(m.timelineState.page.Payloads, e.ID)
			delete(m.timelineState.cards, e.ID)
			delete(m.timelineState.inspections, e.ID)
			delete(m.timelineState.disclosureAnchors, e.ID)
			if e.ID == m.timelineState.anchor.eventID {
				m.timelineState.notice = "Reading anchor removed; showing nearby available evidence."
				m.timelineState.anchor = timelineReadingAnchor{eventID: page.Events[0].ID, header: true}
			}
			continue
		}
		rows = append(rows, e)
	}
	m.timelineState.page.Events = rows
}

// A nested search/assessment timeline must not retain one heavy window per
// navigation frame. Saved frames retain identities, disclosure and anchors;
// returning revalidates their bounded neighborhood through the same services.
func (m *Model) releaseSavedTimelineContent() {
	for i := range m.navigation {
		saved := &m.navigation[i].timeline
		if _, inspected := saved.inspections[saved.anchor.eventID]; inspected {
			saved.notice = "Raw inspection evicted; press u to inspect retained evidence again."
			saved.anchor = timelineReadingAnchor{eventID: saved.anchor.eventID, header: true}
		}
		saved.page.Payloads = nil
		saved.cards = nil
		saved.cachedLines = nil
		saved.cachedRanges = nil
		saved.inspections = nil
		saved.inspectionErrors = make(map[model.EventID]error)
		saved.inspectionLoading = make(map[model.EventID]bool)
		saved.viewport = viewport.New()
	}
}
