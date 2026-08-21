package ebpf

import (
	"sort"
	"sync"
	"time"
)

// maxRemembered bounds the registry. Names come from discovery, not from the
// network, so the realistic ceiling is however many services a host runs — but
// a host that churns through short-lived named processes would otherwise grow
// this map for the life of the process.
const maxRemembered = 4096

// Registry remembers which runtime each service name belongs to.
//
// It exists because the language attribute has to come from somewhere for
// services apm2go instruments through eBPF rather than through an in-process
// agent. OBI reports one for the runtimes it can identify from a process's own
// symbols — Go, Node.js, Python — and nothing useful for a native binary: an
// nginx worker is a C program, and "c" is both true and useless next to a
// service named nginx. apm2go already established what it was, from the
// executable, at the moment it decided to instrument it; this carries that
// forward to the point where telemetry is stored.
//
// Names are never forgotten while apm2go runs. A process that exits stops
// being a target, but the spans it emitted seconds earlier are still arriving,
// and dropping the mapping the moment the process dies would leave the tail of
// a restarting service unlabelled while the rest of it is labelled.
type Registry struct {
	mu       sync.RWMutex
	runtimes map[string]Runtime
	// current is the last scan's target set, which is what answers "what is
	// apm2go watching right now" — a different question from "what has
	// reported telemetry", and the one the trace views cannot answer. A service
	// that is being watched and has simply had no requests is invisible in
	// every span-derived view, which reads as apm2go having missed it.
	current []Target
	since   map[string]time.Time
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		runtimes: make(map[string]Runtime),
		since:    make(map[string]time.Time),
	}
}

// SetTargets records the runtime of every current target. It makes Registry a
// TargetSink, so it is fed by the same discovery pass that feeds the
// supervisor and the process-metrics collector.
func (r *Registry) SetTargets(targets []Target) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.current = append(r.current[:0], targets...)
	now := time.Now().UTC()

	for _, target := range targets {
		if target.Name != "" {
			if _, seen := r.since[target.Name]; !seen && len(r.since) < maxRemembered {
				r.since[target.Name] = now
			}
		}
		if target.Name == "" || target.Runtime == "" {
			continue
		}
		if _, known := r.runtimes[target.Name]; !known && len(r.runtimes) >= maxRemembered {
			continue
		}
		r.runtimes[target.Name] = target.Runtime
	}
}

// Watched is one process apm2go is currently handing to eBPF instrumentation.
type Watched struct {
	Name        string    `json:"service"`
	Runtime     string    `json:"runtime,omitempty"`
	PID         int       `json:"pid"`
	Ports       []int     `json:"ports,omitempty"`
	ContainerID string    `json:"container_id,omitempty"`
	FirstSeen   time.Time `json:"first_seen"`
}

// Snapshot returns the processes currently being watched.
func (r *Registry) Snapshot() []Watched {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Watched, 0, len(r.current))
	for _, target := range r.current {
		out = append(out, Watched{
			Name:        target.Name,
			Runtime:     string(target.Runtime),
			PID:         target.PID,
			Ports:       append([]int(nil), target.Ports...),
			ContainerID: target.ContainerID,
			FirstSeen:   r.since[target.Name],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// RuntimeFor names the runtime a service runs, or "" when apm2go never
// discovered a process under that name — a service reporting from another host,
// or one instrumented some way other than eBPF.
func (r *Registry) RuntimeFor(service string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return string(r.runtimes[service])
}
