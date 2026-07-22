package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
	"github.com/pooya79/AgentSession/internal/model"
)

const DefaultAddress = "127.0.0.1:8080"

//go:embed assets/*
var embeddedAssets embed.FS

// ImportSubscriptionProvider resolves an application-owned subscription for
// one HTTP request. Route-specific source lookup remains outside the renderer.
type ImportSubscriptionProvider func(*http.Request) (*app.ImportSubscription, error)

// NewImportProgressHandler renders a shared import subscription as SSE. A
// disconnected HTTP client detaches only its observer, never the import job.
func NewImportProgressHandler(provider ImportSubscriptionProvider) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if provider == nil {
			http.Error(w, "import progress is unavailable", http.StatusServiceUnavailable)
			return
		}
		subscription, err := provider(r)
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, app.ErrShuttingDown) {
				status = http.StatusServiceUnavailable
			}
			http.Error(w, "failed to observe import", status)
			return
		}
		if subscription == nil {
			http.Error(w, "import progress is unavailable", http.StatusServiceUnavailable)
			return
		}
		defer subscription.Close()

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming is unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		for {
			select {
			case <-r.Context().Done():
				return
			case progress, open := <-subscription.Updates():
				if !open {
					return
				}
				payload, marshalErr := json.Marshal(importProgressJSON(progress))
				if marshalErr != nil {
					return
				}
				event := "progress"
				if progress.Failure != nil {
					event = "failure"
				} else if progress.Complete {
					event = "completion"
				}
				if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}

type importProgressPayload struct {
	RunID               uint64              `json:"run_id"`
	SourceID            model.SourceID      `json:"source"`
	ActiveSourceID      model.SourceID      `json:"active_source"`
	Phase               app.ImportPhase     `json:"phase"`
	RecordsProcessed    int64               `json:"records_processed"`
	EventsProcessed     int64               `json:"events_processed"`
	RecordsCommitted    int64               `json:"records_committed"`
	BatchesCommitted    int64               `json:"batches_committed"`
	DiagnosticsObserved int64               `json:"diagnostics_observed"`
	DiagnosticsOmitted  int64               `json:"diagnostics_omitted"`
	RecentDiagnostics   []diagnosticPayload `json:"recent_diagnostics,omitempty"`
	Complete            bool                `json:"complete"`
	Failure             string              `json:"failure,omitempty"`
}

type diagnosticPayload struct {
	Code         string              `json:"code"`
	Severity     model.Severity      `json:"severity"`
	Message      string              `json:"message"`
	EventIDs     []model.EventID     `json:"event_ids,omitempty"`
	RawRecordIDs []model.RawRecordID `json:"raw_record_ids,omitempty"`
}

func importProgressJSON(progress app.ImportProgress) importProgressPayload {
	payload := importProgressPayload{
		RunID: progress.RunID, SourceID: progress.SourceID, ActiveSourceID: progress.ActiveSourceID,
		Phase: progress.Phase, RecordsProcessed: progress.RecordsProcessed, EventsProcessed: progress.EventsProcessed,
		RecordsCommitted: progress.RecordsCommitted, BatchesCommitted: progress.BatchesCommitted,
		DiagnosticsObserved: progress.DiagnosticsObserved, DiagnosticsOmitted: progress.DiagnosticsOmitted,
		Complete: progress.Complete,
	}
	for _, diagnostic := range progress.RecentDiagnostics {
		payload.RecentDiagnostics = append(payload.RecentDiagnostics, diagnosticPayload{
			Code: diagnostic.Code, Severity: diagnostic.Severity, Message: diagnostic.Message,
			EventIDs: diagnostic.EventIDs, RawRecordIDs: diagnostic.RawRecordIDs,
		})
	}
	if progress.Failure != nil {
		payload.Failure = progress.Failure.Error()
	}
	return payload
}

// NewHandler creates the local web interface handler.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := indexPage().Render(r.Context(), w); err != nil {
			http.Error(w, "failed to render page", http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})

	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("web: embedded assets are unavailable: " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets)))
	return mux
}

// Serve starts the local web interface and gracefully stops when ctx is done.
func Serve(ctx context.Context, addr string) error {
	server := &http.Server{
		Addr:              addr,
		Handler:           NewHandler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-errCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
