package receiver

import (
	"fmt"
	"net"
	"sort"
	"sync"

	"github.com/yigitf/apm2go/internal/config"
)

// A containerized application reaches this host at its network's gateway, so
// the receiver has to be listening there. Which addresses those are is not
// known at start-up — they are discovered as containers are found — so the
// listener set grows at runtime rather than being fixed by configuration.
//
// The alternative, binding every interface, would expose ingest far wider than
// the container networks that actually need it. Binding the gateways instead
// opens exactly the networks apm2go is monitoring and nothing else.

// binder owns the set of addresses the receiver listens on and can extend it.
type binder struct {
	mode config.ContainerBind
	// port is taken from the configured address; every extra listener uses the
	// same port, so one endpoint rewrite works for all of them.
	port string

	mu sync.Mutex
	// bound maps "host:port" to its listener, so an address is never bound
	// twice and every listener can be closed on shutdown.
	bound map[string]net.Listener
	// onNew is called with each newly bound address, for logging.
	onNew func(addr string)
}

// newBinder returns a binder for one transport, listening first on addr.
func newBinder(mode config.ContainerBind, addr string, onNew func(string)) (*binder, net.Listener, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, fmt.Errorf("parse listen address %q: %w", addr, err)
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on %s: %w", addr, err)
	}

	b := &binder{
		mode:  mode,
		port:  port,
		bound: map[string]net.Listener{addr: listener},
		onNew: onNew,
	}
	return b, listener, nil
}

// listenOn binds an additional address, returning the new listener so the
// caller can start serving on it. It returns nil when the address is already
// bound or when the mode does not permit extending.
//
// Binding "all" already covers every address, and "off" declines by design, so
// only "auto" ever grows.
func (b *binder) listenOn(host string) (net.Listener, error) {
	if b.mode != config.ContainerBindAuto || host == "" {
		return nil, nil
	}

	addr := net.JoinHostPort(host, b.port)

	b.mu.Lock()
	defer b.mu.Unlock()

	if _, exists := b.bound[addr]; exists {
		return nil, nil
	}

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		// A gateway apm2go cannot bind is not fatal: the address may belong to
		// another host entirely, or be held by something else. The attempt is
		// recorded so it is not retried on every discovery pass.
		b.bound[addr] = nil
		return nil, fmt.Errorf("listen on container gateway %s: %w", addr, err)
	}

	b.bound[addr] = listener
	if b.onNew != nil {
		b.onNew(addr)
	}
	return listener, nil
}

// addresses reports every address currently bound, for self-monitoring.
func (b *binder) addresses() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]string, 0, len(b.bound))
	for addr, listener := range b.bound {
		if listener != nil {
			out = append(out, addr)
		}
	}
	sort.Strings(out)
	return out
}

// close shuts every listener down.
func (b *binder) close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	for _, listener := range b.bound {
		if listener != nil {
			_ = listener.Close()
		}
	}
}

// resolveListenAddr expands the configured address for the chosen mode. In
// "all" mode the host part is replaced with the wildcard, so the single
// listener already covers every container network and nothing has to grow.
func resolveListenAddr(mode config.ContainerBind, addr string) (string, error) {
	if mode != config.ContainerBindAll {
		return addr, nil
	}
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("parse listen address %q: %w", addr, err)
	}
	return net.JoinHostPort("0.0.0.0", port), nil
}
