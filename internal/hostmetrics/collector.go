// Package hostmetrics measures the machine apm2go runs on.
//
// It answers the question a trace cannot: whether a service got slower because
// of its own code or because the box it sits on ran out of something. Those two
// look identical in a latency chart and are told apart by putting host CPU,
// memory, disk and network on the same time axis as the traces.
//
// The measurements go through the same pipeline as everything else, so they are
// stored, rolled up and expired by the machinery that already exists.
package hostmetrics

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/apm2go/apm2go/internal/model"
)

// hostService is the service name host metrics are filed under. It is not a
// real service, and is named so that it cannot collide with one.
const hostService = "__host__"

// Consumer receives collected metrics. The pipeline implements it.
type Consumer interface {
	ConsumeMetrics(ctx context.Context, metrics []*model.Metric) error
}

// Collector samples the host on an interval.
type Collector struct {
	interval time.Duration
	consumer Consumer
	log      *slog.Logger
	hostName string
}

// New returns a Collector sampling every interval.
func New(interval time.Duration, consumer Consumer, log *slog.Logger) *Collector {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	hostName, err := os.Hostname()
	if err != nil {
		hostName = "unknown"
	}
	return &Collector{interval: interval, consumer: consumer, log: log, hostName: hostName}
}

// Name identifies this component in logs.
func (c *Collector) Name() string { return "host-metrics" }

// Run samples until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) error {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// CPU percentage needs two samples to mean anything, so the first tick is
	// what produces the first usable number rather than start-up.
	c.collect(ctx)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.collect(ctx)
		}
	}
}

// collect takes one sample of everything and forwards it.
//
// A failure in one subsystem is logged and skipped rather than abandoning the
// sample: a host with an unreadable disk should still report its memory.
func (c *Collector) collect(ctx context.Context) {
	now := time.Now().UTC()
	var metrics []*model.Metric

	metrics = append(metrics, c.cpuMetrics(now)...)
	metrics = append(metrics, c.memoryMetrics(now)...)
	metrics = append(metrics, c.diskMetrics(now)...)
	metrics = append(metrics, c.networkMetrics(now)...)
	metrics = append(metrics, c.loadMetrics(now)...)

	if len(metrics) == 0 {
		return
	}
	if err := c.consumer.ConsumeMetrics(ctx, metrics); err != nil {
		c.log.Warn("could not record host metrics", "error", err)
	}
}

// gauge builds a host gauge with the shared identifying fields filled in.
func (c *Collector) gauge(ts time.Time, name string, value float64, unit string, attrs map[string]string) *model.Metric {
	return &model.Metric{
		Timestamp:  ts,
		Name:       name,
		Kind:       model.KindGauge,
		Service:    hostService,
		HostName:   c.hostName,
		Value:      value,
		Unit:       unit,
		Attributes: attrs,
	}
}

// sum builds a host counter, whose rate of change is the interesting part.
func (c *Collector) sum(ts time.Time, name string, value float64, unit string, attrs map[string]string) *model.Metric {
	m := c.gauge(ts, name, value, unit, attrs)
	m.Kind = model.KindSum
	return m
}

func (c *Collector) cpuMetrics(ts time.Time) []*model.Metric {
	// Zero interval means "since the previous call", which is exactly the
	// sampling period and avoids blocking the collector for a measurement window.
	percents, err := cpu.Percent(0, false)
	if err != nil || len(percents) == 0 {
		c.log.Debug("cpu sample failed", "error", err)
		return nil
	}
	return []*model.Metric{
		c.gauge(ts, "system.cpu.utilization", percents[0]/100, "1", nil),
	}
}

func (c *Collector) memoryMetrics(ts time.Time) []*model.Metric {
	virtual, err := mem.VirtualMemory()
	if err != nil {
		c.log.Debug("memory sample failed", "error", err)
		return nil
	}

	metrics := []*model.Metric{
		c.gauge(ts, "system.memory.usage", float64(virtual.Used), "By", map[string]string{"state": "used"}),
		c.gauge(ts, "system.memory.usage", float64(virtual.Available), "By", map[string]string{"state": "available"}),
		// UsedPercent already accounts for cache and buffers, which is what
		// "how full is this machine" actually means on Linux.
		c.gauge(ts, "system.memory.utilization", virtual.UsedPercent/100, "1", nil),
	}

	if swap, err := mem.SwapMemory(); err == nil && swap.Total > 0 {
		metrics = append(metrics,
			c.gauge(ts, "system.swap.usage", float64(swap.Used), "By", nil))
	}
	return metrics
}

func (c *Collector) diskMetrics(ts time.Time) []*model.Metric {
	var metrics []*model.Metric

	// Only real filesystems: the pseudo ones would multiply series without
	// describing any storage.
	partitions, err := disk.Partitions(false)
	if err != nil {
		c.log.Debug("disk partitions unavailable", "error", err)
		return nil
	}
	for _, partition := range partitions {
		usage, err := disk.Usage(partition.Mountpoint)
		if err != nil {
			continue
		}
		attrs := map[string]string{"mountpoint": partition.Mountpoint}
		metrics = append(metrics,
			c.gauge(ts, "system.filesystem.usage", float64(usage.Used), "By", attrs),
			c.gauge(ts, "system.filesystem.utilization", usage.UsedPercent/100, "1", attrs),
		)
	}

	if counters, err := disk.IOCounters(); err == nil {
		for device, counter := range counters {
			metrics = append(metrics,
				c.sum(ts, "system.disk.io", float64(counter.ReadBytes), "By",
					map[string]string{"device": device, "direction": "read"}),
				c.sum(ts, "system.disk.io", float64(counter.WriteBytes), "By",
					map[string]string{"device": device, "direction": "write"}),
			)
		}
	}
	return metrics
}

func (c *Collector) networkMetrics(ts time.Time) []*model.Metric {
	// Aggregated across interfaces: per-interface counters multiply series on
	// hosts with many virtual devices, and the total is what indicates load.
	counters, err := net.IOCounters(false)
	if err != nil || len(counters) == 0 {
		c.log.Debug("network sample failed", "error", err)
		return nil
	}
	counter := counters[0]
	return []*model.Metric{
		c.sum(ts, "system.network.io", float64(counter.BytesRecv), "By", map[string]string{"direction": "receive"}),
		c.sum(ts, "system.network.io", float64(counter.BytesSent), "By", map[string]string{"direction": "transmit"}),
	}
}

func (c *Collector) loadMetrics(ts time.Time) []*model.Metric {
	averages, err := load.Avg()
	if err != nil {
		// Load average does not exist on every platform apm2go compiles for.
		c.log.Debug("load average unavailable", "error", err)
		return nil
	}
	return []*model.Metric{
		c.gauge(ts, "system.cpu.load_average.1m", averages.Load1, "1", nil),
		c.gauge(ts, "system.cpu.load_average.5m", averages.Load5, "1", nil),
		c.gauge(ts, "system.cpu.load_average.15m", averages.Load15, "1", nil),
	}
}

// HostService is the name host metrics are stored under, exported so queries
// and the UI can ask for them without repeating the literal.
const HostService = hostService
