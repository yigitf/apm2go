package ebpf

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestSameTargets(t *testing.T) {
	a := []Target{{Name: "x", Ports: []int{1}}, {Name: "y", Ports: []int{2}}}
	b := []Target{{Name: "y", Ports: []int{2}}, {Name: "x", Ports: []int{1}}} // reordered
	c := []Target{{Name: "x", Ports: []int{1}}}
	d := []Target{{Name: "x", Ports: []int{9}}} // same name, different port

	if !sameTargets(a, b) {
		t.Error("sameTargets should ignore order")
	}
	if sameTargets(a, c) {
		t.Error("sameTargets should notice a missing target")
	}
	if sameTargets(a, d) {
		t.Error("sameTargets should notice a changed port")
	}
	if !sameTargets(nil, nil) {
		t.Error("two empty sets should be the same")
	}
}

// A SetTargets call whose set has not actually changed must be a no-op: it
// must not wake Run, which would otherwise restart OBI on every discovery
// tick even when nothing about what to instrument changed.
func TestSetTargetsDedupesUnchangedSet(t *testing.T) {
	s := NewSupervisor(t.TempDir(), "http://127.0.0.1:4317", "tok", false, discardLogger())

	s.SetTargets([]Target{{Name: "a", Ports: []int{1}}})
	select {
	case <-s.changed:
	default:
		t.Fatal("first SetTargets call did not signal a change")
	}

	s.SetTargets([]Target{{Name: "a", Ports: []int{1}}})
	select {
	case <-s.changed:
		t.Fatal("SetTargets signalled a change for an identical target set")
	default:
	}
}

func TestSetTargetsCoalescesRapidChanges(t *testing.T) {
	s := NewSupervisor(t.TempDir(), "http://127.0.0.1:4317", "tok", false, discardLogger())

	s.SetTargets([]Target{{Name: "a", Ports: []int{1}}})
	s.SetTargets([]Target{{Name: "a", Ports: []int{1}}, {Name: "b", Ports: []int{2}}})
	s.SetTargets([]Target{{Name: "a", Ports: []int{1}}, {Name: "b", Ports: []int{2}}, {Name: "c", Ports: []int{3}}})

	// Exactly one pending signal regardless of how many calls preceded it —
	// the channel is buffered by one on purpose, and a reader is expected to
	// re-read the current target set rather than trust a queued value.
	select {
	case <-s.changed:
	default:
		t.Fatal("expected a pending change signal")
	}
	if got := s.snapshot(); len(got) != 3 {
		t.Errorf("snapshot has %d targets, want the latest set of 3", len(got))
	}
}

// On a platform (or a build) without eBPF support, Run must return promptly
// and without error once its context is cancelled — never treat "unavailable"
// as a fatal condition, since a Component returning an error takes the whole
// apm2go process down.
func TestRunReturnsCleanlyWithoutEBPFSupport(t *testing.T) {
	if Available() {
		t.Skip("this build embeds OBI; the unavailable path is not exercised here")
	}

	s := NewSupervisor(t.TempDir(), "http://127.0.0.1:4317", "tok", false, discardLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case err := <-done:
		// context.DeadlineExceeded here, mirroring every other Component in
		// this codebase: app.Run treats it as a clean stop, not a failure.
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Run returned %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
