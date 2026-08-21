package pipeline

import (
	"sync"
	"time"
)

// rateLimiter is a token bucket bounding how many spans enter the queue per
// second.
//
// The ceiling exists to protect the host, not to be fair: when an application
// suddenly emits ten times its usual volume, apm2go keeps a steady sample of it
// rather than letting the burst evict everything else from the queue.
type rateLimiter struct {
	// limit of zero disables the limiter entirely.
	limit int

	mu       sync.Mutex
	tokens   float64
	lastFill time.Time
}

// newRateLimiter returns a limiter allowing perSecond spans, or an unlimited
// one when perSecond is zero or negative.
func newRateLimiter(perSecond int) *rateLimiter {
	return &rateLimiter{
		limit:    perSecond,
		tokens:   float64(perSecond),
		lastFill: time.Now(),
	}
}

// allow reports whether one more span may be admitted.
func (r *rateLimiter) allow() bool {
	if r.limit <= 0 {
		return true
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(r.lastFill).Seconds()
	r.lastFill = now

	// Refill, capped at one second's worth so an idle period cannot bank
	// enough credit to admit an unbounded burst later.
	r.tokens += elapsed * float64(r.limit)
	if r.tokens > float64(r.limit) {
		r.tokens = float64(r.limit)
	}

	if r.tokens < 1 {
		return false
	}
	r.tokens--
	return true
}

// overflowLabel is the bucket that absorbs everything past the cardinality
// ceiling. It is deliberately conspicuous: an operator seeing it in the UI
// should immediately understand that names were collapsed.
const overflowLabel = "__other__"

// cardinalityGuard caps how many distinct service and operation names are
// stored.
//
// Unbounded names are the classic way an APM's storage explodes: one service
// that puts a request id in its span names can produce millions of operations
// in an hour. Past the ceiling, new names collapse into a single bucket, which
// keeps queries fast and makes the problem visible instead of fatal.
type cardinalityGuard struct {
	maxServices   int
	maxOperations int

	mu         sync.RWMutex
	services   map[string]struct{}
	operations map[string]struct{}
}

func newCardinalityGuard(maxServices, maxOperations int) *cardinalityGuard {
	return &cardinalityGuard{
		maxServices:   maxServices,
		maxOperations: maxOperations,
		services:      make(map[string]struct{}),
		operations:    make(map[string]struct{}),
	}
}

// service returns the name to store for a service.
func (g *cardinalityGuard) service(name string) string {
	if name == "" {
		return overflowLabel
	}
	return g.admit(g.services, name, g.maxServices)
}

// operation returns the name to store for an operation. Operations are counted
// per service so one noisy service cannot exhaust the budget for the others.
func (g *cardinalityGuard) operation(service, name string) string {
	if name == "" {
		return overflowLabel
	}
	key := service + "\x00" + name
	if g.admit(g.operations, key, g.maxOperations) == overflowLabel {
		return overflowLabel
	}
	return name
}

// admit records a name if there is room, and returns either the name or the
// overflow label.
func (g *cardinalityGuard) admit(set map[string]struct{}, key string, max int) string {
	g.mu.RLock()
	_, known := set[key]
	full := max > 0 && len(set) >= max
	g.mu.RUnlock()

	if known {
		// The key is stored with a service prefix for operations, so return the
		// caller's original name rather than the key.
		return trimServicePrefix(key)
	}
	if full {
		return overflowLabel
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	// Re-check under the write lock: another goroutine may have filled the last
	// slot between the two locks.
	if max > 0 && len(set) >= max {
		if _, known := set[key]; !known {
			return overflowLabel
		}
	}
	set[key] = struct{}{}
	return trimServicePrefix(key)
}

// trimServicePrefix undoes the service prefix used to key operations.
func trimServicePrefix(key string) string {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[i+1:]
		}
	}
	return key
}

// counts reports how much of each budget is in use.
func (g *cardinalityGuard) counts() (services, operations int) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.services), len(g.operations)
}
