package container

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// cacheTTL bounds how long a container's metadata is reused.
//
// The data barely changes — a container's name and image are fixed for its
// lifetime — so the cache exists to keep a discovery pass every few seconds
// from becoming a socket call per container per pass.
const cacheTTL = 5 * time.Minute

// lookupTimeout bounds a single resolution, so an unhealthy runtime socket
// slows metadata down rather than stalling discovery.
const lookupTimeout = 3 * time.Second

// Resolver turns container ids into identities, trying each configured source
// in order and caching what it finds.
type Resolver struct {
	runtimes []Runtime
	log      *slog.Logger

	mu    sync.RWMutex
	cache map[string]cacheEntry

	// warnOnce reports the first failure of each source at a level an operator
	// will see. A source that announced itself as available and then never
	// answers would otherwise degrade metadata silently, and "why do my
	// containers have no names" is a question the logs should already answer.
	warnOnce map[string]*sync.Once
}

type cacheEntry struct {
	info      *Info
	expiresAt time.Time
}

// NewResolver builds a Resolver from the available sources.
//
// Sources are ordered richest first: a runtime API knows names, images and
// orchestration labels, while the cgroup path knows only ids. The last source
// is always available, so resolution never fails outright.
func NewResolver(dockerSocket string, log *slog.Logger) *Resolver {
	candidates := []Runtime{
		NewDocker(dockerSocket),
		NewCgroup(),
	}

	var available []Runtime
	for _, runtime := range candidates {
		if !runtime.Available() {
			log.Debug("container metadata source unavailable", "source", runtime.Name())
			continue
		}
		available = append(available, runtime)
	}

	names := make([]string, 0, len(available))
	for _, runtime := range available {
		names = append(names, runtime.Name())
	}
	log.Info("container metadata sources", "sources", names)

	warnOnce := make(map[string]*sync.Once, len(available))
	for _, runtime := range available {
		warnOnce[runtime.Name()] = &sync.Once{}
	}

	return &Resolver{
		runtimes: available,
		log:      log,
		cache:    make(map[string]cacheEntry),
		warnOnce: warnOnce,
	}
}

// Resolve returns what is known about a container id, or nil when the id is
// empty. It never returns an error: missing metadata degrades the display name,
// it does not stop a process being instrumented.
func (r *Resolver) Resolve(containerID string) *Info {
	if r == nil || containerID == "" {
		return nil
	}

	r.mu.RLock()
	entry, cached := r.cache[containerID]
	r.mu.RUnlock()
	if cached && time.Now().Before(entry.expiresAt) {
		return entry.info
	}

	ctx, cancel := context.WithTimeout(context.Background(), lookupTimeout)
	defer cancel()

	var resolved *Info
	for _, runtime := range r.runtimes {
		info, err := runtime.Lookup(ctx, containerID)
		if err != nil {
			r.reportFailure(runtime.Name(), containerID, err)
			continue
		}
		// The first source that yields a usable name wins; a source that
		// answers with only an id is kept as a floor and the next one is tried.
		if resolved == nil {
			resolved = info
		}
		if info.ServiceName() != "" {
			resolved = info
			break
		}
	}

	r.mu.Lock()
	r.cache[containerID] = cacheEntry{info: resolved, expiresAt: time.Now().Add(cacheTTL)}
	r.mu.Unlock()

	return resolved
}

// reportFailure logs a lookup failure: the first from each source at warn
// level, and every one after that at debug, so a persistently broken source is
// visible without filling the log.
func (r *Resolver) reportFailure(source, containerID string, err error) {
	once, known := r.warnOnce[source]
	if known {
		once.Do(func() {
			r.log.Warn("container metadata source is not answering; "+
				"containers will be identified by id only",
				"source", source, "error", err)
		})
	}
	r.log.Debug("container lookup failed",
		"source", source, "container", short(containerID), "error", err)
}

// Sources reports which metadata sources are in use, for self-monitoring.
func (r *Resolver) Sources() []string {
	if r == nil {
		return nil
	}
	names := make([]string, 0, len(r.runtimes))
	for _, runtime := range r.runtimes {
		names = append(names, runtime.Name())
	}
	return names
}
