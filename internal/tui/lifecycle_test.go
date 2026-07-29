package tui

import (
	"context"
	"strings"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/pooya79/AgentSession/internal/app"
)

func TestInitStartsImportAllAndLoadsSessions(t *testing.T) {
	services := &servicesStub{
		start: app.ImportAllStart{Status: app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}},
		sessionPage: app.SessionPage{
			State: app.EvidenceComplete, Sessions: []app.SessionSummary{testSession("session-1")},
		},
	}
	m := New(context.Background(), services)
	msg, ok := m.Init()().(tea.BatchMsg)
	if !ok || len(msg) != 5 {
		t.Fatalf("Init() message = %T %#v, want sessions, overview, import, theme, and spinner commands", msg, msg)
	}
	for _, cmd := range msg {
		switch result := cmd().(type) {
		case importStartedMsg:
			m, _ = updateModel(t, m, result)
		case sessionsLoadedMsg:
			m, _ = updateModel(t, m, result)
		}
	}
	if services.startCalls != 1 || len(services.sessionCalls) != 1 {
		t.Fatalf("startup calls = start %d, sessions %d", services.startCalls, len(services.sessionCalls))
	}
	if got := m.View().Content; !strings.Contains(got, "Sessions dashboard") || strings.Contains(got, "Select source") {
		t.Fatalf("startup view = %q", got)
	}
}

func TestReloadSessionsRestartsStoppedSpinnerWithoutDuplicatingTickChain(t *testing.T) {
	m := New(context.Background(), &servicesStub{})
	m.sessionsState.loading = false
	m.sessionsState.overviewLoading = false
	m.spinnerActive = true
	m, cmd := updateModel(t, m, spinner.TickMsg{})
	if cmd != nil || m.spinnerActive {
		t.Fatalf("idle tick = (cmd %v, active %v), want stopped chain", cmd != nil, m.spinnerActive)
	}

	cmd = m.reloadSessions()
	if !m.spinnerActive {
		t.Fatal("reloadSessions left spinner inactive")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("reloadSessions command = %T with %d children, want work plus one tick", batch, len(batch))
	}
	if _, ok := batch[0]().(tea.BatchMsg); !ok {
		t.Fatalf("restarted spinner first command = %T, want batched reload work", batch[0]())
	}

	cmd = m.reloadSessions()
	batch, ok = cmd().(tea.BatchMsg)
	if !ok || len(batch) != 2 {
		t.Fatalf("active reload command = %T with %d children, want only session and overview work", batch, len(batch))
	}
	if _, ok := batch[0]().(sessionsLoadedMsg); !ok {
		t.Fatalf("active spinner first command = %T, want session load without another tick wrapper", batch[0]())
	}
}

func TestImportCompletionRefreshesSessions(t *testing.T) {
	services := &servicesStub{}
	m := New(context.Background(), services)
	m.indexingState.status = app.ImportAllStatus{Active: true, Phase: app.ImportAllIndexing}
	m.observeGeneration = 7

	updated, cmd := updateModel(t, m, importStatusMsg{
		generation: 7,
		status: app.ImportAllStatus{
			Phase: app.ImportAllIssues, SourcesDiscovered: 2, SourcesCompleted: 2,
			SourcesFailed: 1, DiagnosticsTotal: 1,
		},
	})
	if cmd == nil || !updated.sessionsState.loading {
		t.Fatal("terminal import status did not trigger a sessions refresh")
	}
	if got := updated.View().Content; !strings.Contains(got, "COMPLETED WITH ISSUES") {
		t.Fatalf("completion view = %q", got)
	}
}

func TestPollImportObservationLifecycle(t *testing.T) {
	t.Run("current poll replaces observer context", func(t *testing.T) {
		services := &servicesStub{}
		m := New(context.Background(), services)
		m.observeGeneration = 7
		previousCtx := m.observeCtx

		updated, cmd := updateModel(t, m, pollImportMsg{generation: 7})
		if cmd == nil || updated.observeCtx == previousCtx {
			t.Fatal("current poll did not replace the observer context")
		}
		select {
		case <-previousCtx.Done():
		default:
			t.Fatal("replaced observer context was not canceled")
		}

		_ = cmd()
		if services.statusCtx != updated.observeCtx {
			t.Fatal("status read did not use the replacement observer context")
		}
		select {
		case <-services.statusCtx.Done():
			t.Fatal("replacement observer context was unexpectedly canceled")
		default:
		}
	})

	t.Run("stale and stopped polls do not restart observation", func(t *testing.T) {
		services := &servicesStub{}
		m := New(context.Background(), services)
		observeCtx := m.observeCtx
		generation := m.observeGeneration

		updated, cmd := updateModel(t, m, pollImportMsg{generation: generation - 1})
		if cmd != nil || updated.observeCtx != observeCtx {
			t.Fatal("stale poll replaced or restarted the observer")
		}

		updated.stopObservation()
		stoppedGeneration := updated.observeGeneration
		updated, cmd = updateModel(t, updated, pollImportMsg{generation: stoppedGeneration})
		if cmd != nil || updated.observeCancel != nil || updated.observeGeneration != stoppedGeneration {
			t.Fatal("poll after stopObservation restarted observation")
		}
		if services.statusCtx != nil {
			t.Fatal("ignored polls read import status")
		}
	})
}
