package ebpf

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// restartBackoff bounds how fast the supervisor retries a target set that
// keeps failing to start. OBI is a v0 dependency that has not reached a stable
// release; a tight crash loop must not be allowed to spin the host.
const restartBackoff = 5 * time.Second

// Supervisor runs OBI as a child process and keeps its target list current.
//
// It never returns a non-nil error from Run for anything short of the context
// being cancelled — an app.Component's Run failing takes the whole apm2go
// process down, and OBI is exactly the dependency this must not be true for:
// v0, embedded for its own sake, with the Java side of apm2go owing it
// nothing. A crashing or missing OBI binary is a degraded capability, logged
// and retried, never a reason to stop injecting Java agents.
type Supervisor struct {
	dataDir      string
	otlpEndpoint string
	token        string
	withMetrics  bool
	log          *slog.Logger

	mu      sync.Mutex
	targets []Target
	// changed signals Run that s.targets has a new value worth reacting to.
	// Buffered by one: several SetTargets calls between two reads of the
	// channel coalesce into a single wake-up, and Run always acts on the
	// latest targets by re-reading s.targets rather than on a queued value.
	changed chan struct{}
}

// NewSupervisor returns a Supervisor. token is the single ingest credential
// OBI exports under, covering every target it instruments — the receiver
// validates a token against the registry it issued, not against which service
// claims to be sending, so one shared credential for the whole OBI process is
// consistent with how apm2go already authenticates ingest.
func NewSupervisor(dataDir, otlpEndpoint, token string, withMetrics bool, log *slog.Logger) *Supervisor {
	return &Supervisor{
		dataDir:      dataDir,
		otlpEndpoint: otlpEndpoint,
		token:        token,
		withMetrics:  withMetrics,
		log:          log,
		changed:      make(chan struct{}, 1),
	}
}

// Name identifies this component in logs.
func (s *Supervisor) Name() string { return "ebpf" }

// SetTargets replaces the set of processes OBI should instrument. Called
// repeatedly as discovery finds new processes or loses old ones; a call whose
// set is unchanged from the last one is a no-op; it does not trigger a
// restart.
func (s *Supervisor) SetTargets(targets []Target) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if sameTargets(s.targets, targets) {
		return
	}
	s.targets = append([]Target(nil), targets...)

	select {
	case s.changed <- struct{}{}:
	default:
		// A signal is already pending; Run will pick up the latest targets
		// when it gets to it, so there is nothing more to do here.
	}
}

func (s *Supervisor) snapshot() []Target {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Target(nil), s.targets...)
}

// Run drives OBI for as long as ctx is live. See the type doc: the only error
// this ever returns is ctx's own — app.Run treats context.Canceled as a clean
// stop, not a fatal component failure, which is the established convention
// every other Component in this codebase follows.
func (s *Supervisor) Run(ctx context.Context) error {
	capability := Detect()
	if !capability.Spans {
		s.log.Info("eBPF instrumentation is not available on this host; " +
			"Java instrumentation is unaffected")
		if capability.Reason != "" {
			s.log.Info("eBPF unavailable", "reason", capability.Reason)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	if !capability.ContextPropagation {
		s.log.Warn("eBPF instrumentation can measure processes on this host but cannot " +
			"join their traces to the ones they call; " + capability.PropagationReason)
	}

	binPath, err := s.materialize()
	if err != nil {
		s.log.Error("could not stage the OBI binary; eBPF instrumentation is disabled, "+
			"Java instrumentation is unaffected", "error", err)
		<-ctx.Done()
		return ctx.Err()
	}

	for {
		targets := s.snapshot()

		if len(targets) == 0 {
			// Nothing to instrument yet. Wait for either a target set or
			// shutdown, rather than busy-looping in between.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-s.changed:
				continue
			}
		}

		if err := s.runUntilChangedOrFailed(ctx, binPath, targets); err != nil {
			s.log.Error("OBI exited", "error", err, "targets", len(targets))
			if !s.sleepOrDone(ctx, restartBackoff) {
				return ctx.Err()
			}
			continue
		}
		// runUntilChangedOrFailed only returns nil when ctx is done or the
		// target set changed. Either way the top of the loop is where to go
		// next: it re-reads ctx.Err() implicitly by re-snapshotting, and a
		// changed set is already sitting in s.targets for it to pick up.
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

// runUntilChangedOrFailed starts OBI for one target set and blocks until the
// context is cancelled, the target set changes, or the process exits on its
// own (in which case it returns that as an error for the caller to log and
// back off from).
func (s *Supervisor) runUntilChangedOrFailed(ctx context.Context, binPath string, targets []Target) error {
	configDir := filepath.Join(s.dataDir, "ebpf")
	configPath, err := writeConfig(configDir, targets, s.otlpEndpoint, s.withMetrics)
	if err != nil {
		return fmt.Errorf("write OBI config: %w", err)
	}

	runCtx, stop := context.WithCancel(ctx)
	defer stop()

	cmd := exec.CommandContext(runCtx, binPath, "-config", configPath)
	// OBI needs no ambient configuration beyond what the config file and these
	// two variables carry; a stray OTEL_* in apm2go's own environment must not
	// leak into it and silently change what it exports.
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS=" + ingestHeader(s.token),
	}
	if s.withMetrics {
		cmd.Env = append(cmd.Env, "OTEL_EXPORTER_OTLP_METRICS_HEADERS="+ingestHeader(s.token))
	}

	var stderr bytes.Buffer
	cmd.Stdout = &stderr
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start OBI: %w", err)
	}
	s.log.Info("OBI started", "pid", cmd.Process.Pid, "targets", len(targets))

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case <-ctx.Done():
		stop()
		<-exited
		return nil
	case <-s.changed:
		s.log.Info("instrumentation targets changed, restarting OBI")
		stop()
		<-exited
		return nil
	case err := <-exited:
		if ctx.Err() != nil {
			// Cancelled and exited in the same moment; not a failure.
			return nil
		}
		msg := trimmed(stderr.String())
		if err == nil {
			return fmt.Errorf("OBI exited unexpectedly (no error, last output: %s)", msg)
		}
		if msg == "" {
			return fmt.Errorf("OBI exited: %w", err)
		}
		return fmt.Errorf("OBI exited: %w: %s", err, msg)
	}
}

// materialize writes the embedded OBI binary to disk once, skipping the write
// if an identical file is already there.
func (s *Supervisor) materialize() (string, error) {
	dir := filepath.Join(s.dataDir, "ebpf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "obi")
	data := binary()

	if fi, err := os.Stat(path); err == nil && fi.Size() == int64(len(data)) {
		return path, nil
	}

	tmp, err := os.CreateTemp(dir, "obi.tmp*")
	if err != nil {
		return "", fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		return "", fmt.Errorf("chmod %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("rename %s to %s: %w", tmpPath, path, err)
	}
	return path, nil
}

// sleepOrDone waits out d, returning false early (and not waiting) if ctx is
// cancelled first.
func (s *Supervisor) sleepOrDone(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// ingestHeader renders the token as the OTLP header apm2go's receiver checks.
func ingestHeader(token string) string {
	return "x-apm2go-token=" + token
}

// sameTargets reports whether two target sets are the same set, independent
// of order — the order processes were discovered in must not itself trigger a
// restart.
func sameTargets(a, b []Target) bool {
	if len(a) != len(b) {
		return false
	}
	// Keyed on a rendering of the target rather than on the target itself: a
	// Target carries a slice of ports now, which makes it uncomparable and so
	// unusable as a map key. The rendering has to include every field that
	// changes what OBI is told, or a real change would be diffed away as no
	// change and OBI would keep running against a stale config.
	index := make(map[string]bool, len(a))
	for _, t := range a {
		index[targetKey(t)] = true
	}
	for _, t := range b {
		if !index[targetKey(t)] {
			return false
		}
	}
	return true
}

// targetKey renders a target as a comparable string.
func targetKey(t Target) string {
	return fmt.Sprintf("%s|%s|%d|%v", t.Name, t.Runtime, t.PID, t.Ports)
}

// trimmed returns the tail of s, trimmed of trailing whitespace and bounded to
// a log-friendly length — OBI's own crash output, not the whole session.
func trimmed(s string) string {
	const maxLen = 500
	s = strings.TrimRight(s, "\n \t")
	if len(s) > maxLen {
		s = "…" + s[len(s)-maxLen:]
	}
	return s
}
