package web

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/pooya79/AgentSession/internal/app"
)

const (
	// DefaultAddress keeps the operations console bound to localhost unless configured otherwise.
	DefaultAddress = "127.0.0.1:8080"
	// defaultPageLimit bounds initial HTML and service reads.
	defaultPageLimit = 50
	// maximumRequestBody limits mutation forms before parsing untrusted input.
	maximumRequestBody = 8 << 10
)

// embeddedAssets keeps the web interface offline-capable and single-binary compatible.
//
//go:embed assets/*
var embeddedAssets embed.FS

// handler binds shared application services to HTTP routes and a process-local CSRF token.
type handler struct {
	services app.Services
	csrf     string
}

// NewHandler creates the local, server-rendered operations console.
func NewHandler(services app.Services) http.Handler {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		panic("web: generate CSRF token: " + err.Error())
	}
	h := &handler{services: services, csrf: base64.RawURLEncoding.EncodeToString(token)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", h.dashboard)
	mux.HandleFunc("GET /healthz", h.health)
	mux.HandleFunc("GET /indexing", h.indexing)
	mux.HandleFunc("POST /indexing/rescan", h.rescan)
	mux.HandleFunc("GET /fragments/index-status", h.indexStatusFragment)
	mux.HandleFunc("GET /fragments/index-strip", h.indexStripFragment)
	mux.HandleFunc("GET /events/{event}", h.eventRedirect)
	mux.HandleFunc("GET /sessions/{session}", h.timeline)
	mux.HandleFunc("GET /sessions/{session}/fragments/events", h.timelineFragment)
	mux.HandleFunc("GET /sessions/{session}/fragments/event/{event}", h.eventFragment)
	mux.HandleFunc("GET /sessions/{session}/fragments/projections", h.projectionFragment)
	mux.HandleFunc("POST /sessions/{session}/projections/retry", h.retryProjections)
	mux.HandleFunc("POST /sessions/{session}/projections/rebuild", h.rebuildProjection)
	mux.HandleFunc("GET /sessions/{session}/projections/rebuild-all", h.confirmRebuildAll)
	mux.HandleFunc("POST /sessions/{session}/projections/rebuild-all", h.rebuildAll)

	assets, err := fs.Sub(embeddedAssets, "assets")
	if err != nil {
		panic("web: embedded assets are unavailable: " + err.Error())
	}
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(assets)))
	return securityHeaders(mux)
}

// Serve starts the local web interface and gracefully stops when ctx is done.
func Serve(ctx context.Context, addr string, services app.Services) error {
	if services == nil {
		return errors.New("web: application services are required")
	}
	server := &http.Server{
		Addr: addr, Handler: NewHandler(services), ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second,
	}
	if _, err := services.StartImportAll(context.Background()); err != nil {
		return fmt.Errorf("web: start automatic import: %w", err)
	}
	errCh := make(chan error, 1)
	go func() { errCh <- server.ListenAndServe() }()
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
