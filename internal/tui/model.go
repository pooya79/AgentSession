package tui

import (
	"context"
	"errors"
	"fmt"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

// screen identifies the active presentation without encoding navigation or
// business logic into the application service layer.
type screen uint8

const (
	sessionsScreen screen = iota
	indexingScreen
	timelineScreen
	detailScreen
	projectionsScreen
	searchScreen
)

// Model is the sessions-first AgentSession terminal interface.
type Model struct {
	// ctx owns presentation requests only. Starting import-all deliberately
	// uses an independent context because navigation must not cancel indexing.
	ctx      context.Context
	services app.Services

	width  int
	height int
	screen screen

	// Requests and import observation have independent lifetimes. Generations
	// reject responses from commands canceled by later navigation or refreshes.
	requestGeneration uint64
	requestCtx        context.Context
	requestCancel     context.CancelFunc
	observeGeneration uint64
	observeCtx        context.Context
	observeCancel     context.CancelFunc

	// Screen states own their page, selection, errors, and viewport. They are
	// embedded so the root coordinator remains concise while retaining one
	// Bubble Tea model and one explicit navigation graph.
	sessionsState
	timelineState
	detailState
	indexingState
	projectionsState
	searchState

	theme         theme
	helpOpen      bool
	helpViewport  viewport.Model
	spinner       spinner.Model
	spinnerActive bool
}

// New creates a terminal model over the shared application services.
func New(ctx context.Context, services app.Services) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx, requestCancel := context.WithCancel(ctx)
	observeCtx, observeCancel := context.WithCancel(ctx)
	detailViewport := viewport.New()
	detailViewport.SoftWrap = true
	indexViewport := viewport.New()
	indexViewport.SoftWrap = true
	helpViewport := viewport.New()
	helpViewport.SoftWrap = true
	timelineViewport := viewport.New()
	return Model{
		ctx:               ctx,
		services:          services,
		requestGeneration: 1,
		requestCtx:        requestCtx,
		requestCancel:     requestCancel,
		observeGeneration: 1,
		observeCtx:        observeCtx,
		observeCancel:     observeCancel,
		sessionsState: sessionsState{
			loading:         services != nil,
			overviewLoading: services != nil,
			cursors:         []string{""},
		},
		timelineState: timelineState{
			requestedCursors:  make(map[string]bool),
			expanded:          make(map[model.EventID]bool),
			inspections:       make(map[model.EventID]app.UnknownEvidenceInspection),
			inspectionErrors:  make(map[model.EventID]error),
			inspectionLoading: make(map[model.EventID]bool),
			viewport:          timelineViewport,
		},
		detailState: detailState{viewport: detailViewport},
		indexingState: indexingState{
			status:   app.ImportAllStatus{Phase: app.ImportAllUnavailable},
			viewport: indexViewport,
		},
		projectionsState: projectionsState{generation: 1},
		theme:            newTheme(true),
		helpViewport:     helpViewport,
		spinner:          spinner.New(spinner.WithSpinner(spinner.MiniDot)),
		spinnerActive:    services != nil,
	}
}

// Init starts or joins application-owned indexing while independently loading
// the first page of already committed sessions.
func (m Model) Init() tea.Cmd {
	if m.services == nil {
		return tea.RequestBackgroundColor
	}
	return tea.Batch(
		loadSessions(m.requestCtx, m.services, m.requestGeneration, ""),
		loadOverview(m.requestCtx, m.services, m.requestGeneration),
		startImportAll(m.services, m.observeGeneration),
		tea.RequestBackgroundColor,
		m.spinner.Tick,
	)
}

// busy reports whether any visible request or application-owned workflow needs
// spinner animation.
func (m Model) busy() bool {
	return m.sessionsState.loading || m.sessionsState.overviewLoading || m.timelineState.loading || m.detailState.loading || m.detailState.inspectionLoading ||
		m.projectionsState.loading || m.projectionsState.status.Active || m.indexingState.status.Active || m.searchState.loading
}

// visibleError suppresses expected cancellation from superseded presentation
// requests while retaining actionable service failures.
func visibleError(err error) error {
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}

// restoreSessionSelection prefers stable session identity over row position
// when a refreshed page still contains the previously selected session.
func (m *Model) restoreSessionSelection() {
	if len(m.sessionsState.page.Sessions) == 0 {
		m.sessionsState.cursor = 0
		return
	}
	if m.sessionsState.selected != "" {
		for i, session := range m.sessionsState.page.Sessions {
			if session.ID == m.sessionsState.selected {
				m.sessionsState.cursor = i
				return
			}
		}
	}
	if m.sessionsState.cursor >= len(m.sessionsState.page.Sessions) {
		m.sessionsState.cursor = len(m.sessionsState.page.Sessions) - 1
	}
	m.sessionsState.selected = m.sessionsState.page.Sessions[m.sessionsState.cursor].ID
}

// Run opens the interactive terminal interface over application-owned
// services. The command context controls presentation lifetime only.
func Run(ctx context.Context, services app.Services) error {
	if services == nil {
		return fmt.Errorf("tui: application services are required")
	}
	if ctx == nil {
		return fmt.Errorf("tui: context is required")
	}
	_, err := tea.NewProgram(New(ctx, services), tea.WithContext(ctx)).Run()
	return err
}
