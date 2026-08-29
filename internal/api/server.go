// Package api serves apm2go's HTTP interface: a JSON API over the stored traces
// and the JVM inventory, plus the embedded web UI that consumes it.
package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/yigitf/apm2go/internal/config"
)

// Server hosts the API and the UI on one port, so an install exposes a single
// address to reach apm2go.
type Server struct {
	cfg      config.APIConfig
	log      *slog.Logger
	handlers *Handlers
}

// NewServer returns an API server.
func NewServer(cfg config.APIConfig, handlers *Handlers, log *slog.Logger) *Server {
	return &Server{cfg: cfg, log: log, handlers: handlers}
}

// Name identifies this component in logs.
func (s *Server) Name() string { return "api" }

// Run serves until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr, err)
	}

	server := &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: the event stream is a long-lived response and any
		// deadline here would sever it.
		IdleTimeout: 120 * time.Second,
	}

	s.log.Info("web interface listening", "addr", s.cfg.Addr, "url", "http://"+displayAddr(s.cfg.Addr))

	errCh := make(chan error, 1)
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = server.Shutdown(shutdownCtx)
	return ctx.Err()
}

// routes builds the mux: the JSON API under /api/v1 and the UI everywhere else.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	// Read endpoints.
	mux.HandleFunc("GET /api/v1/health", s.handlers.Health)
	mux.HandleFunc("GET /api/v1/self", s.handlers.Self)
	mux.HandleFunc("GET /api/v1/jvms", s.handlers.ListJVMs)
	mux.HandleFunc("GET /api/v1/processes", s.handlers.ListProcesses)
	mux.HandleFunc("GET /api/v1/jvms/{pid}", s.handlers.GetJVM)
	mux.HandleFunc("GET /api/v1/jvms/{pid}/diagnostics", s.handlers.ListDiagnostics)
	mux.HandleFunc("GET /api/v1/diagnostics/compare", s.handlers.CompareDiagnostics)
	mux.HandleFunc("GET /api/v1/diagnostics/{id}", s.handlers.GetDiagnostic)
	mux.HandleFunc("GET /api/v1/diagnostics/{id}/raw", s.handlers.GetDiagnosticRaw)
	mux.HandleFunc("GET /api/v1/services", s.handlers.ListServices)
	mux.HandleFunc("GET /api/v1/services/{service}/operations", s.handlers.ListOperations)
	mux.HandleFunc("GET /api/v1/services/{service}/timeseries", s.handlers.ServiceTimeSeries)
	mux.HandleFunc("GET /api/v1/timeseries", s.handlers.TimeSeries)
	mux.HandleFunc("GET /api/v1/metrics", s.handlers.ListMetricNames)
	mux.HandleFunc("GET /api/v1/metrics/query", s.handlers.QueryMetric)
	mux.HandleFunc("GET /api/v1/traces", s.handlers.SearchTraces)
	mux.HandleFunc("GET /api/v1/traces/{traceID}", s.handlers.GetTrace)
	mux.HandleFunc("GET /api/v1/dependencies", s.handlers.Dependencies)
	mux.HandleFunc("GET /api/v1/events", s.handlers.Events)

	// Write endpoints. Read-only mode is for installs where the UI is exposed
	// more widely than the people allowed to change what is instrumented.
	if !s.cfg.ReadOnly {
		mux.HandleFunc("POST /api/v1/jvms/{pid}/attach", s.handlers.AttachJVM)
		mux.HandleFunc("POST /api/v1/jvms/{pid}/disable", s.handlers.DisableJVM)
		mux.HandleFunc("POST /api/v1/jvms/{pid}/enable", s.handlers.EnableJVM)
		// Collecting a diagnostic pauses the target at a safepoint, so it is a
		// write even though it changes nothing: a read-only install must not be
		// able to stop a production JVM.
		mux.HandleFunc("POST /api/v1/jvms/{pid}/diagnostics/{kind}", s.handlers.CollectDiagnostic)
	}

	mux.Handle("/", s.uiHandler())

	return withRecovery(withLogging(s.log, mux))
}

// displayAddr turns a wildcard bind address into something clickable in a log.
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "localhost:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
