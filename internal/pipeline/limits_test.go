package pipeline

import (
	"testing"
	"time"
)

func TestRateLimiterCapsAdmissions(t *testing.T) {
	limiter := newRateLimiter(10)

	admitted := 0
	for i := 0; i < 100; i++ {
		if limiter.allow() {
			admitted++
		}
	}
	// The bucket starts full and barely refills within a tight loop.
	if admitted > 12 {
		t.Errorf("admitted %d spans against a limit of 10/s", admitted)
	}
	if admitted < 5 {
		t.Errorf("admitted only %d spans; the initial bucket should be full", admitted)
	}
}

func TestRateLimiterZeroMeansUnlimited(t *testing.T) {
	limiter := newRateLimiter(0)
	for i := 0; i < 1000; i++ {
		if !limiter.allow() {
			t.Fatal("a limit of 0 must admit everything")
		}
	}
}

func TestRateLimiterDoesNotBankUnboundedCredit(t *testing.T) {
	limiter := newRateLimiter(10)
	// Drain the bucket, then pretend a long idle period passed. Refill must be
	// capped at one second's worth, or a quiet night would let the next burst
	// through unbounded.
	for i := 0; i < 20; i++ {
		limiter.allow()
	}
	limiter.mu.Lock()
	limiter.lastFill = time.Now().Add(-time.Hour)
	limiter.mu.Unlock()

	admitted := 0
	for i := 0; i < 100; i++ {
		if limiter.allow() {
			admitted++
		}
	}
	if admitted > 12 {
		t.Errorf("admitted %d after an idle hour; refill should cap at the per-second limit", admitted)
	}
}

func TestCardinalityGuardCollapsesOverflow(t *testing.T) {
	guard := newCardinalityGuard(3, 3)

	for _, name := range []string{"a", "b", "c"} {
		if got := guard.service(name); got != name {
			t.Errorf("service(%q) = %q, want it admitted", name, got)
		}
	}
	// The budget is spent; a new name must collapse rather than be stored.
	if got := guard.service("d"); got != overflowLabel {
		t.Errorf("service(\"d\") = %q, want %q", got, overflowLabel)
	}
	// An already-admitted name keeps working after the budget is spent.
	if got := guard.service("a"); got != "a" {
		t.Errorf("service(\"a\") = %q, want it still admitted", got)
	}
}

func TestCardinalityGuardBudgetsOperationsPerService(t *testing.T) {
	// Operations are keyed per service, so a noisy service must not exhaust
	// the namespace for a quiet one... but the total budget is still shared,
	// which is what keeps storage bounded.
	guard := newCardinalityGuard(10, 2)

	if got := guard.operation("svc-a", "op1"); got != "op1" {
		t.Errorf("operation = %q, want %q", got, "op1")
	}
	if got := guard.operation("svc-b", "op1"); got != "op1" {
		t.Errorf("the same operation name under another service should be admitted separately, got %q", got)
	}
	if got := guard.operation("svc-a", "op2"); got != overflowLabel {
		t.Errorf("operation = %q, want %q once the budget is spent", got, overflowLabel)
	}
}

func TestTruncateMarksShortenedValues(t *testing.T) {
	long := make([]byte, 100)
	for i := range long {
		long[i] = 'x'
	}

	got := truncate(string(long), 40)
	if len(got) != 40 {
		t.Errorf("truncated length = %d, want 40", len(got))
	}
	// A silently cut value would be debugged as if it were complete.
	if got[len(got)-len("...[truncated]"):] != "...[truncated]" {
		t.Errorf("truncated value = %q, want a truncation marker", got)
	}

	if got := truncate("short", 40); got != "short" {
		t.Errorf("truncate left a short value alone incorrectly: %q", got)
	}
}
