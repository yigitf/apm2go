// Package app wires the apm2go subsystems together and owns their lifecycle.
//
// Which subsystems run depends on config.Mode: a normal single-host install
// ("all") runs every one of them, while the phase-3 split runs discovery and
// attach on one host ("agent") and ingest, storage and UI on another ("server").
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/apm2go/apm2go/internal/config"
)

// Component is a subsystem with a blocking Run that returns when ctx is done.
type Component interface {
	// Name identifies the component in logs.
	Name() string
	// Run blocks until ctx is cancelled or the component fails fatally.
	Run(ctx context.Context) error
}

// App owns the configured components and runs them as a group.
type App struct {
	cfg        config.Config
	log        *slog.Logger
	components []Component
}

// New prepares the runtime environment (data directory, logger) but does not
// start anything yet.
func New(cfg config.Config, log *slog.Logger) (*App, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}
	return &App{cfg: cfg, log: log}, nil
}

// Add registers a component to be started by Run.
func (a *App) Add(c Component) { a.components = append(a.components, c) }

// DataPath resolves a path inside the data directory, leaving absolute paths
// untouched.
func (a *App) DataPath(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(a.cfg.DataDir, p)
}

// Run starts every registered component and blocks until ctx is cancelled or a
// component returns a fatal error. The first fatal error cancels the rest, and
// Run waits for all of them to unwind before returning.
func (a *App) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)

	for _, c := range a.components {
		wg.Add(1)
		go func(c Component) {
			defer wg.Done()
			a.log.Info("component starting", "component", c.Name())

			err := c.Run(ctx)
			if err != nil && !errors.Is(err, context.Canceled) {
				a.log.Error("component failed", "component", c.Name(), "error", err)
				mu.Lock()
				errs = append(errs, fmt.Errorf("%s: %w", c.Name(), err))
				mu.Unlock()
				// One component dying takes the process down; systemd restarts it.
				cancel()
				return
			}
			a.log.Info("component stopped", "component", c.Name())
		}(c)
	}

	wg.Wait()
	return errors.Join(errs...)
}
