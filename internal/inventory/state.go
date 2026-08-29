// Package inventory tracks every JVM apm2go knows about on this host and drives
// them through the instrumentation lifecycle.
package inventory

import (
	"time"

	"github.com/yigitf/apm2go/internal/discovery"
	"github.com/yigitf/apm2go/internal/injector"
)

// State is where a JVM sits in the instrumentation lifecycle.
type State string

const (
	// StateDiscovered means apm2go has seen the process but not acted on it.
	StateDiscovered State = "discovered"
	// StatePending means the process is waiting out its start-up grace period.
	StatePending State = "pending"
	// StateAttaching means an injection is in flight.
	StateAttaching State = "attaching"
	// StateAttached means the agent is loaded and the JVM should be exporting.
	StateAttached State = "attached"
	// StateFailed means injection failed and retries are exhausted or waiting.
	StateFailed State = "failed"
	// StateSkipped means apm2go will not instrument this JVM: it is filtered
	// out, unsupported, or already carries another agent.
	StateSkipped State = "skipped"
	// StateExited means the process is gone. Entries linger briefly so the UI
	// can explain a service that just disappeared.
	StateExited State = "exited"
)

// Entry is one tracked JVM together with its instrumentation status.
type Entry struct {
	JVM   *discovery.JVM `json:"jvm"`
	State State          `json:"state"`

	// Reason explains a Skipped or Failed state in operator-facing language.
	Reason string `json:"reason,omitempty"`
	// Warnings carries non-fatal notes from the last injection.
	Warnings []string `json:"warnings,omitempty"`

	// FirstSeen and LastSeen bound the process's observed lifetime.
	FirstSeen time.Time `json:"first_seen"`
	LastSeen  time.Time `json:"last_seen"`

	// Attempts counts injections tried; NextAttempt is when the next retry is
	// due, and is zero when none is scheduled.
	Attempts    int       `json:"attempts"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
	AttachedAt  time.Time `json:"attached_at,omitempty"`
	ExitedAt    time.Time `json:"exited_at,omitempty"`

	// Endpoint is the OTLP address this JVM was told to export to.
	Endpoint string `json:"endpoint,omitempty"`

	// ManualOnly is set when an operator disabled auto-attach for this process,
	// so the reconcile loop leaves it alone.
	ManualOnly bool `json:"manual_only"`
}

// clone returns a deep enough copy to hand to API callers without exposing
// the manager's mutable state.
func (e *Entry) clone() *Entry {
	cp := *e
	if e.Warnings != nil {
		cp.Warnings = append([]string(nil), e.Warnings...)
	}
	if e.JVM != nil {
		jvm := *e.JVM
		cp.JVM = &jvm
	}
	return &cp
}

// applyResult folds a successful injection into the entry.
func (e *Entry) applyResult(res *injector.Result) {
	e.State = StateAttached
	e.AttachedAt = time.Now()
	e.Reason = ""
	e.NextAttempt = time.Time{}
	e.Endpoint = res.Endpoint
	e.Warnings = nil
	for _, w := range res.Warnings {
		if w != "" {
			e.Warnings = append(e.Warnings, w)
		}
	}
	if res.AlreadyInstrumented {
		e.Warnings = append(e.Warnings,
			"this JVM was already instrumented by apm2go, so nothing was injected")
	}
}
