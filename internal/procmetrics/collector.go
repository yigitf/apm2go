// Package procmetrics measures CPU, memory and disk I/O for the non-Java
// processes apm2go's eBPF discovery finds — the OS-level counterpart to what
// the OpenTelemetry Java agent already reports for a JVM (heap, GC, CPU),
// gathered the same way internal/hostmetrics measures the whole machine, just
// narrowed to one pid.
//
// This exists because OBI does not fill that gap itself: measured directly,
// enabling every metric feature it offers still produced only HTTP request
// counters for a Node process, none of the runtime figures (event loop delay,
// heap, GC) an official per-language SDK would give — and a per-language SDK
// needs the process restarted with it loaded, which is exactly the manual
// step apm2go exists to avoid. CPU, memory and disk I/O do not have that
// problem: the kernel already accounts for them per pid, for any process,
// language notwithstanding.
//
// Network I/O is deliberately not attempted here. Linux has no per-process
// byte counter the way it has one for disk: only a socket table and a
// process's open file descriptors, and turning those into a throughput figure
// needs the kind of packet-level accounting eBPF does — which is a real
// option, but a distinct piece of work from reading /proc, not a small
// addition to it.
package procmetrics

import (
	"context"
	"log/slog"
	"os"
	"runtime"
	"sync"
	"time"

	gopsprocess "github.com/shirou/gopsutil/v4/process"

	"github.com/yigitf/apm2go/internal/ebpf"
	"github.com/yigitf/apm2go/internal/model"
)

// Consumer receives collected metrics. The pipeline implements it.
type Consumer interface {
	ConsumeMetrics(ctx context.Context, metrics []*model.Metric) error
}

// Collector samples every currently known target on an interval.
type Collector struct {
	interval time.Duration
	consumer Consumer
	log      *slog.Logger
	hostName string
	numCPU   float64

	mu      sync.Mutex
	targets []ebpf.Target
	// prior holds the last CPU sample per pid, which is what turns a single
	// cumulative CPU-seconds reading into a recent utilization figure. A pid
	// missing here on a given tick is treated as newly seen, matching how
	// internal/hostmetrics treats its own first sample: skipped rather than
	// reported as a nonsensical instantaneous 0.
	prior map[int]cpuSample
}

type cpuSample struct {
	cpuSeconds float64
	at         time.Time
}

// NewCollector returns a Collector sampling every interval.
func NewCollector(interval time.Duration, consumer Consumer, log *slog.Logger) *Collector {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	hostName, err := os.Hostname()
	if err != nil {
		hostName = "unknown"
	}
	return &Collector{
		interval: interval,
		consumer: consumer,
		log:      log,
		hostName: hostName,
		numCPU:   float64(runtime.NumCPU()),
		prior:    make(map[int]cpuSample),
	}
}

// Name identifies this component in logs.
func (c *Collector) Name() string { return "process-metrics" }

// SetTargets replaces the set of processes to sample. Implements
// ebpf.TargetSink, so the same discovery pass that feeds OBI feeds this too —
// one scan of /proc, two consumers of its result.
func (c *Collector) SetTargets(targets []ebpf.Target) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.targets = append([]ebpf.Target(nil), targets...)
}

func (c *Collector) snapshot() []ebpf.Target {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ebpf.Target(nil), c.targets...)
}

// Run samples until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

// collect samples every current target and forwards whatever it could read.
//
// A target that has exited between discovery and this tick is common — a
// short-lived worker, a process that crashed — and is skipped rather than
// treated as a reason to drop the rest of the batch.
func (c *Collector) collect(ctx context.Context) {
	targets := c.snapshot()

	now := time.Now().UTC()
	seen := make(map[int]bool, len(targets))
	var metrics []*model.Metric

	for _, t := range targets {
		seen[t.PID] = true
		metrics = append(metrics, c.sampleTarget(t, now)...)
	}

	// Drop CPU history for anything no longer discovered, so a pid reused by
	// an unrelated process never inherits a stale baseline.
	for pid := range c.prior {
		if !seen[pid] {
			delete(c.prior, pid)
		}
	}

	if len(metrics) == 0 {
		return
	}
	if err := c.consumer.ConsumeMetrics(ctx, metrics); err != nil {
		c.log.Warn("could not record process metrics", "error", err)
	}
}

func (c *Collector) sampleTarget(t ebpf.Target, now time.Time) []*model.Metric {
	proc, err := gopsprocess.NewProcess(int32(t.PID))
	if err != nil {
		// Exited between discovery and this tick.
		return nil
	}

	var metrics []*model.Metric
	metrics = append(metrics, c.cpuMetric(t, proc, now)...)
	metrics = append(metrics, c.memoryMetrics(t, proc, now)...)
	metrics = append(metrics, c.diskMetrics(t, proc, now)...)
	return metrics
}

// cpuMetric reports the fraction of total available CPU capacity this process
// used since the last sample — the same "recent utilization normalized by CPU
// count" shape the OpenTelemetry Java agent reports as jvm.cpu.recent_utilization,
// so the two chart on comparable axes.
func (c *Collector) cpuMetric(t ebpf.Target, proc *gopsprocess.Process, now time.Time) []*model.Metric {
	times, err := proc.Times()
	if err != nil {
		return nil
	}
	cpuSeconds := times.Total()

	prev, ok := c.prior[t.PID]
	c.prior[t.PID] = cpuSample{cpuSeconds: cpuSeconds, at: now}
	if !ok {
		// First sample for this pid: nothing to diff against yet.
		return nil
	}

	elapsed := now.Sub(prev.at).Seconds()
	if elapsed <= 0 {
		return nil
	}
	utilization := (cpuSeconds - prev.cpuSeconds) / elapsed / c.numCPU
	if utilization < 0 {
		// A pid reused by a different process between samples would otherwise
		// show as negative CPU usage.
		return nil
	}

	return []*model.Metric{c.gauge(t, now, "process.cpu.utilization", utilization, "1", nil)}
}

func (c *Collector) memoryMetrics(t ebpf.Target, proc *gopsprocess.Process, now time.Time) []*model.Metric {
	info, err := proc.MemoryInfo()
	if err != nil {
		return nil
	}
	metrics := []*model.Metric{
		c.gauge(t, now, "process.memory.usage", float64(info.RSS), "By", nil),
	}
	if percent, err := proc.MemoryPercent(); err == nil {
		metrics = append(metrics, c.gauge(t, now, "process.memory.utilization", float64(percent)/100, "1", nil))
	}
	return metrics
}

func (c *Collector) diskMetrics(t ebpf.Target, proc *gopsprocess.Process, now time.Time) []*model.Metric {
	counters, err := proc.IOCounters()
	if err != nil {
		// Not every platform or permission set exposes this; a host that
		// cannot report it still gets CPU and memory.
		return nil
	}
	return []*model.Metric{
		c.sum(t, now, "process.disk.io", float64(counters.ReadBytes), "By", map[string]string{"direction": "read"}),
		c.sum(t, now, "process.disk.io", float64(counters.WriteBytes), "By", map[string]string{"direction": "write"}),
	}
}

func (c *Collector) gauge(t ebpf.Target, ts time.Time, name string, value float64, unit string, attrs map[string]string) *model.Metric {
	return &model.Metric{
		Timestamp:  ts,
		Name:       name,
		Kind:       model.KindGauge,
		Service:    t.Name,
		HostName:   c.hostName,
		PID:        t.PID,
		Value:      value,
		Unit:       unit,
		Attributes: attrs,
	}
}

// sum builds a process counter, whose rate of change is the interesting part
// — matching how internal/hostmetrics reports host-wide disk and network I/O.
func (c *Collector) sum(t ebpf.Target, ts time.Time, name string, value float64, unit string, attrs map[string]string) *model.Metric {
	m := c.gauge(t, ts, name, value, unit, attrs)
	m.Kind = model.KindSum
	return m
}
