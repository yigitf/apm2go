package inventory

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/apm2go/apm2go/internal/config"
	"github.com/apm2go/apm2go/internal/container"
	"github.com/apm2go/apm2go/internal/discovery"
	"github.com/apm2go/apm2go/internal/injector"
)

// exitedRetention is how long a dead process stays visible, so an operator who
// notices a service vanish from the UI can still see that apm2go saw it too.
const exitedRetention = 10 * time.Minute

// GatewayListener extends telemetry ingest to a container network.
//
// The receiver implements it. The inventory depends on the narrow interface
// rather than the receiver itself so that an agent-mode process, which has no
// receiver, simply has nothing to call.
type GatewayListener interface {
	// ListenOnGateway makes ingest reachable at a container network's gateway.
	// It must be safe to call repeatedly with the same address.
	ListenOnGateway(host string)
}

// Manager polls the host for JVMs and drives each one to its target state.
//
// It owns a single reconcile loop rather than a goroutine per process: attaches
// are rare, occasionally slow, and must not overlap for the same target.
type Manager struct {
	scanner  *discovery.Scanner
	filter   *discovery.Filter
	injector *injector.Injector
	gateways GatewayListener
	cfg      config.Config
	log      *slog.Logger

	mu      sync.RWMutex
	entries map[string]*Entry

	// events fans out state changes to subscribers such as the UI's SSE stream.
	events *eventBus

	// inflight guards against a manual attach racing the reconcile loop.
	inflight sync.Map // key string -> struct{}
}

// NewManager wires a Manager from configuration. gateways may be nil, in which
// case containerized targets are still instrumented but ingest is not extended
// to their networks.
func NewManager(cfg config.Config, inj *injector.Injector, gateways GatewayListener, log *slog.Logger) *Manager {
	return &Manager{
		scanner: discovery.NewScanner(cfg.Discovery.ProcRoot).
			WithContainers(container.NewResolver(cfg.Discovery.DockerSocket, log)),
		filter:   discovery.NewFilter(cfg.Discovery.Include, cfg.Discovery.Exclude),
		injector: inj,
		gateways: gateways,
		cfg:      cfg,
		log:      log,
		entries:  make(map[string]*Entry),
		events:   newEventBus(),
	}
}

// Name identifies this component in logs.
func (m *Manager) Name() string { return "inventory" }

// Run polls for JVMs until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.cfg.Discovery.Interval)
	defer ticker.Stop()

	// Reconcile immediately so a restart picks up running JVMs without waiting
	// out a full interval.
	m.reconcile(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.reconcile(ctx)
		}
	}
}

// reconcile runs one discovery pass and acts on what it finds.
func (m *Manager) reconcile(ctx context.Context) {
	found, err := m.scanner.Scan()
	if err != nil {
		m.log.Error("JVM discovery failed", "error", err)
		return
	}

	now := time.Now()
	seen := make(map[string]bool, len(found))

	for _, jvm := range found {
		key := jvm.Key()
		seen[key] = true
		entry := m.observe(jvm, now)

		// Ingest has to be reachable before the target is told to export to it,
		// otherwise the agent's first batches go nowhere.
		if m.gateways != nil && jvm.Gateway != "" {
			m.gateways.ListenOnGateway(jvm.Gateway)
		}

		if m.shouldAttach(entry, now) {
			// Injection can take seconds; run it outside the poll so a slow or
			// unresponsive JVM never stalls discovery of the others.
			go m.attach(ctx, key)
		}
	}

	m.reapMissing(seen, now)
}

// observe records a sighting, creating the entry on first contact and
// classifying it. It returns the live entry, already locked and unlocked.
func (m *Manager) observe(jvm *discovery.JVM, now time.Time) *Entry {
	m.mu.Lock()
	defer m.mu.Unlock()

	key := jvm.Key()
	entry, ok := m.entries[key]
	if !ok {
		entry = &Entry{
			JVM:       jvm,
			State:     StateDiscovered,
			FirstSeen: now,
		}
		m.entries[key] = entry
		m.classify(entry, now)

		m.log.Info("JVM discovered",
			"pid", jvm.PID,
			"service", jvm.ServiceName,
			"java", jvm.JavaVersion,
			"user", jvm.User,
			"state", entry.State,
			"reason", entry.Reason)
		m.events.publish(Event{Type: EventDiscovered, Entry: entry.clone()})
	} else {
		// Refresh the metadata: a JVM's service name can change once its perf
		// data becomes readable, shortly after start-up.
		entry.JVM = jvm
		if entry.State == StatePending {
			m.classify(entry, now)
		}
	}
	entry.LastSeen = now
	return entry
}

// classify decides whether a newly seen JVM is a candidate for instrumentation.
func (m *Manager) classify(entry *Entry, now time.Time) {
	jvm := entry.JVM

	if !m.filter.Accept(jvm) {
		entry.State, entry.Reason = StateSkipped, "excluded by discovery filters"
		return
	}
	if ok, reason := jvm.Attachable(); !ok {
		if jvm.InstrumentedByUs {
			// Not a problem: this is a JVM started with our permanent flag.
			entry.State, entry.Reason = StateAttached, "instrumented at start-up via -javaagent"
			entry.AttachedAt = jvm.StartTime
			return
		}
		entry.State, entry.Reason = StateSkipped, reason
		return
	}
	if jvm.AlreadyInstrumented {
		entry.Warnings = append(entry.Warnings,
			"this JVM already runs another Java agent; loading a second one can cause conflicts")
	}

	// A JVM that started moments ago is still loading classes, and attaching
	// mid-startup both misses spans and slows the boot it interrupts.
	if age := now.Sub(jvm.StartTime); jvm.StartTime.IsZero() || age >= m.cfg.Discovery.MinUptime {
		entry.State, entry.Reason = StateDiscovered, ""
	} else {
		entry.State = StatePending
		entry.Reason = fmt.Sprintf("waiting %s for start-up to settle",
			(m.cfg.Discovery.MinUptime - age).Round(time.Second))
	}
}

// shouldAttach reports whether the reconcile loop should inject into an entry.
func (m *Manager) shouldAttach(entry *Entry, now time.Time) bool {
	if !m.cfg.Attach.AutoAttach {
		return false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	if entry.ManualOnly {
		return false
	}
	switch entry.State {
	case StateDiscovered:
		return true
	case StateFailed:
		// Retry on a backoff, up to the configured ceiling.
		return entry.Attempts < m.cfg.Attach.MaxRetries &&
			!entry.NextAttempt.IsZero() && now.After(entry.NextAttempt)
	default:
		return false
	}
}

// Attach instruments a JVM by pid on an operator's explicit request, ignoring
// the auto-attach setting and any exhausted retry budget.
func (m *Manager) Attach(ctx context.Context, pid int) (*Entry, error) {
	key, err := m.keyForPID(pid)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	entry := m.entries[key]
	// A manual attach is a deliberate override, so clear the retry budget that
	// may have been exhausted automatically.
	entry.Attempts = 0
	entry.ManualOnly = false
	m.mu.Unlock()

	if err := m.attach(ctx, key); err != nil {
		return m.Get(pid)
	}
	return m.Get(pid)
}

// SetManualOnly stops the reconcile loop from attaching to a process. It cannot
// remove an agent that is already loaded — the JVM offers no way to unload one —
// so for an instrumented JVM this only prevents re-injection after a restart.
func (m *Manager) SetManualOnly(pid int, manual bool) (*Entry, error) {
	key, err := m.keyForPID(pid)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	entry := m.entries[key]
	entry.ManualOnly = manual
	if manual && entry.State == StateDiscovered {
		entry.State = StateSkipped
		entry.Reason = "auto-attach disabled by operator"
	}
	if !manual && entry.State == StateSkipped && entry.Reason == "auto-attach disabled by operator" {
		entry.State, entry.Reason = StateDiscovered, ""
	}
	clone := entry.clone()
	m.mu.Unlock()

	m.events.publish(Event{Type: EventStateChanged, Entry: clone})
	return clone, nil
}

// attach performs one injection attempt and records the outcome.
func (m *Manager) attach(ctx context.Context, key string) error {
	// Only one attach per process at a time: two concurrent handshakes against
	// the same JVM confuse its attach listener.
	if _, busy := m.inflight.LoadOrStore(key, struct{}{}); busy {
		return fmt.Errorf("an attach is already in progress for this process")
	}
	defer m.inflight.Delete(key)

	m.mu.Lock()
	entry, ok := m.entries[key]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("process is no longer tracked")
	}
	jvm := entry.JVM
	entry.State = StateAttaching
	entry.Attempts++
	attempt := entry.Attempts
	clone := entry.clone()
	m.mu.Unlock()

	m.events.publish(Event{Type: EventStateChanged, Entry: clone})

	res, err := m.injector.Inject(ctx, jvm)

	m.mu.Lock()
	entry, ok = m.entries[key]
	if !ok {
		// The process exited while we were attaching to it.
		m.mu.Unlock()
		return err
	}
	if err != nil {
		entry.State = StateFailed
		entry.Reason = err.Error()
		if attempt < m.cfg.Attach.MaxRetries {
			entry.NextAttempt = time.Now().Add(m.cfg.Attach.RetryBackoff)
		} else {
			entry.NextAttempt = time.Time{}
		}
	} else {
		entry.applyResult(res)
	}
	clone = entry.clone()
	m.mu.Unlock()

	if err != nil {
		m.log.Warn("attach failed",
			"pid", jvm.PID, "service", jvm.ServiceName,
			"attempt", attempt, "max", m.cfg.Attach.MaxRetries, "error", err)
		m.events.publish(Event{Type: EventAttachFailed, Entry: clone})
		return err
	}

	m.events.publish(Event{Type: EventAttached, Entry: clone})
	return nil
}

// reapMissing marks processes that vanished and drops long-dead entries.
func (m *Manager) reapMissing(seen map[string]bool, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for key, entry := range m.entries {
		if seen[key] {
			continue
		}
		if entry.State != StateExited {
			entry.State = StateExited
			entry.ExitedAt = now
			entry.NextAttempt = time.Time{}
			m.log.Info("JVM exited", "pid", entry.JVM.PID, "service", entry.JVM.ServiceName)
			m.events.publish(Event{Type: EventExited, Entry: entry.clone()})
			continue
		}
		if now.Sub(entry.ExitedAt) > exitedRetention {
			delete(m.entries, key)
		}
	}
}

// List returns every tracked entry, newest process first.
func (m *Manager) List() []*Entry {
	m.mu.RLock()
	defer m.mu.RUnlock()

	out := make([]*Entry, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, e.clone())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].State != out[j].State {
			return statePriority(out[i].State) < statePriority(out[j].State)
		}
		return out[i].JVM.StartTime.After(out[j].JVM.StartTime)
	})
	return out
}

// statePriority orders the JVM list so what needs attention floats to the top.
func statePriority(s State) int {
	switch s {
	case StateFailed:
		return 0
	case StateAttaching:
		return 1
	case StateDiscovered, StatePending:
		return 2
	case StateAttached:
		return 3
	case StateSkipped:
		return 4
	default:
		return 5
	}
}

// Get returns the entry for a live pid.
func (m *Manager) Get(pid int) (*Entry, error) {
	key, err := m.keyForPID(pid)
	if err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.entries[key].clone(), nil
}

// Subscribe returns a channel of lifecycle events and a function to stop it.
func (m *Manager) Subscribe() (<-chan Event, func()) { return m.events.subscribe() }

// ContainerSources reports which container metadata sources are in use, so the
// settings page can show whether a runtime socket was found.
func (m *Manager) ContainerSources() []string { return m.scanner.ContainerSources() }

// keyForPID resolves a pid to its tracking key, rejecting processes apm2go has
// not seen. Callers address processes by pid, but entries are keyed by pid and
// start time so that a reused pid is never mistaken for the original process.
func (m *Manager) keyForPID(pid int) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for key, entry := range m.entries {
		if entry.JVM.PID == pid && entry.State != StateExited {
			return key, nil
		}
	}
	return "", fmt.Errorf("no running JVM tracked with pid %d", pid)
}
