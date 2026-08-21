package ebpf

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// TargetSink receives the current set of non-Java processes worth watching.
// Supervisor is one; a process-level metrics collector that samples CPU,
// memory and disk I/O for the same processes is another — both want the same
// discovered set, and neither needs to know how the other uses it.
type TargetSink interface {
	SetTargets(targets []Target)
}

// Discoverer periodically scans for processes worth watching and keeps every
// registered TargetSink current.
//
// It is a separate component from Supervisor on purpose, mirroring the split
// between internal/discovery (finds JVMs) and internal/inventory (decides what
// to do about them): scanning /proc and deciding what to do with the result
// are different concerns, and keeping them apart is what let Supervisor be
// tested without a real /proc to scan.
type Discoverer struct {
	procRoot string
	interval time.Duration
	sinks    []TargetSink
	log      *slog.Logger
}

// NewDiscoverer returns a Discoverer that feeds every given sink identical
// target sets on every scan.
func NewDiscoverer(procRoot string, interval time.Duration, log *slog.Logger, sinks ...TargetSink) *Discoverer {
	if procRoot == "" {
		procRoot = "/proc"
	}
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &Discoverer{procRoot: procRoot, interval: interval, sinks: sinks, log: log}
}

// Name identifies this component in logs.
func (d *Discoverer) Name() string { return "ebpf-discovery" }

// Run scans on the configured interval until ctx is done.
func (d *Discoverer) Run(ctx context.Context) error {
	ticker := time.NewTicker(d.interval)
	defer ticker.Stop()

	d.scan()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			d.scan()
		}
	}
}

// scan runs one discovery pass and hands the result to every sink. Errors
// are logged rather than propagated: a /proc read racing against an exiting
// process is ordinary, not a reason to stop discovering everything else.
func (d *Discoverer) scan() {
	found, err := Scan(d.procRoot)
	if err != nil {
		d.log.Warn("eBPF process scan failed", "error", err)
		return
	}
	targets := disambiguate(found)
	for _, sink := range d.sinks {
		sink.SetTargets(targets)
	}
}

// disambiguate makes one scan's targets tell each other apart, in the two ways
// they can fail to.
//
// Names first: two unrelated processes can derive the same one — "app.py" is a
// common script name, and two Node services can both be started as "server.js"
// from different directories. Two targets sharing a name collide as a single
// service in every view apm2go has, silently merging their traces and metrics,
// so a colliding name gains the port that already distinguishes them to OBI.
//
// Then ports, which is the subtler half. OBI selects a process by the port it
// listens on and knows nothing about network namespaces, so a port number
// claimed by two containers selects both — and each target's rule then matches
// the other target's process. This is not hypothetical: two web servers behind
// a distribution's packaged configuration each listen on :80 as well as their
// own port, and the first version of this code duly told OBI to instrument
// "nginx" on 80,8100 and "httpd" on 80,8101, whereupon one of them captured
// every process on :80 and the other reported nothing at all, with no error
// anywhere to say so.
func disambiguate(targets []Target) []Target {
	out := make([]Target, len(targets))
	copy(out, targets)
	dropSharedPorts(out)

	counts := make(map[string]int, len(out))
	for _, t := range out {
		counts[t.Name]++
	}
	for i, t := range out {
		if counts[t.Name] > 1 {
			out[i].Name = fmt.Sprintf("%s-%d", t.Name, t.Port())
		}
	}
	return out
}

// dropSharedPorts removes from each target the ports that another target also
// claims, since a port naming two services names neither.
//
// A target left with nothing keeps every port it had. That case is two
// processes whose only port is the same number, where no selector can separate
// them and OBI will merge them whatever it is told — so the choice is between
// merged data and none, and merged data is what apm2go already produced before
// any of this existed. Silently dropping the target instead would turn a
// long-standing imprecision into a service that disappeared.
func dropSharedPorts(targets []Target) {
	claims := make(map[int]int)
	for _, t := range targets {
		// Ports within one target are already deduplicated, so each target
		// contributes at most one claim per port.
		for _, port := range t.Ports {
			claims[port]++
		}
	}

	for i, t := range targets {
		distinct := make([]int, 0, len(t.Ports))
		for _, port := range t.Ports {
			if claims[port] == 1 {
				distinct = append(distinct, port)
			}
		}
		if len(distinct) > 0 && len(distinct) != len(t.Ports) {
			targets[i].Ports = distinct
		}
	}
}
