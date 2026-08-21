package procmetrics

import (
	"context"
	"io"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/apm2go/apm2go/internal/ebpf"
	"github.com/apm2go/apm2go/internal/model"
)

// fakeConsumer records every batch it receives.
type fakeConsumer struct {
	mu      sync.Mutex
	batches [][]*model.Metric
}

func (f *fakeConsumer) ConsumeMetrics(_ context.Context, metrics []*model.Metric) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.batches = append(f.batches, metrics)
	return nil
}

func (f *fakeConsumer) all() []*model.Metric {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []*model.Metric
	for _, b := range f.batches {
		out = append(out, b...)
	}
	return out
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func hasMetric(metrics []*model.Metric, name string) bool {
	for _, m := range metrics {
		if m.Name == name {
			return true
		}
	}
	return false
}

// collect with no targets set must do nothing — not even call the consumer
// with an empty batch, which would otherwise show up as a phantom write in
// the pipeline's own metrics.
func TestCollectWithNoTargetsIsANoop(t *testing.T) {
	consumer := &fakeConsumer{}
	c := NewCollector(time.Second, consumer, discardLogger())

	c.collect(context.Background())

	if len(consumer.batches) != 0 {
		t.Errorf("consumer received %d batches with no targets set, want 0", len(consumer.batches))
	}
}

// CPU utilization needs two samples to mean anything — the first tick for a
// newly discovered pid must not report a fabricated number.
func TestFirstSampleReportsNoCPU(t *testing.T) {
	consumer := &fakeConsumer{}
	c := NewCollector(time.Second, consumer, discardLogger())
	c.SetTargets([]ebpf.Target{{PID: os.Getpid(), Name: "self"}})

	c.collect(context.Background())

	got := consumer.all()
	if hasMetric(got, "process.cpu.utilization") {
		t.Error("first sample reported process.cpu.utilization; it has nothing to diff against yet")
	}
	// Memory and disk do not need a second sample and should appear immediately.
	if !hasMetric(got, "process.memory.usage") {
		t.Error("first sample did not report process.memory.usage")
	}
}

// A second sample, taken after real wall-clock time has passed, produces a
// CPU utilization figure — this is the actual, non-mocked measurement path,
// run against this test binary's own process so it needs no fixture.
func TestSecondSampleReportsCPU(t *testing.T) {
	consumer := &fakeConsumer{}
	c := NewCollector(time.Second, consumer, discardLogger())
	target := []ebpf.Target{{PID: os.Getpid(), Name: "self"}}
	c.SetTargets(target)

	c.collect(context.Background())
	time.Sleep(50 * time.Millisecond)
	c.collect(context.Background())

	got := consumer.all()
	if !hasMetric(got, "process.cpu.utilization") {
		t.Error("second sample did not report process.cpu.utilization")
	}
	for _, m := range got {
		if m.Name != "process.cpu.utilization" {
			continue
		}
		if m.Value < 0 {
			t.Errorf("process.cpu.utilization = %v, want >= 0", m.Value)
		}
		if m.Service != "self" || m.PID != os.Getpid() {
			t.Errorf("metric identity = service %q pid %d, want %q %d", m.Service, m.PID, "self", os.Getpid())
		}
	}
}

// A pid that disappears between two scans must not leave its CPU baseline
// behind: a different process reusing that pid later would otherwise inherit
// a stale sample and report a nonsensical utilization on its very first tick.
func TestStalePIDsAreForgotten(t *testing.T) {
	consumer := &fakeConsumer{}
	c := NewCollector(time.Second, consumer, discardLogger())

	c.SetTargets([]ebpf.Target{{PID: os.Getpid(), Name: "self"}})
	c.collect(context.Background())
	if _, ok := c.prior[os.Getpid()]; !ok {
		t.Fatal("no CPU baseline recorded after the first sample")
	}

	c.SetTargets(nil)
	c.collect(context.Background())
	if _, ok := c.prior[os.Getpid()]; ok {
		t.Error("CPU baseline for a pid no longer discovered was not cleared")
	}
}

// A pid for a process that has already exited must not break the batch for
// every other target — matching the "one malformed item must not lose a
// batch" rule the rest of apm2go's ingest path follows.
func TestExitedPIDIsSkippedNotFatal(t *testing.T) {
	consumer := &fakeConsumer{}
	c := NewCollector(time.Second, consumer, discardLogger())

	const implausiblePID = 1 << 30
	c.SetTargets([]ebpf.Target{
		{PID: implausiblePID, Name: "gone"},
		{PID: os.Getpid(), Name: "self"},
	})

	c.collect(context.Background())

	got := consumer.all()
	for _, m := range got {
		if m.Service == "gone" {
			t.Errorf("metric reported for a pid that cannot exist: %+v", m)
		}
	}
	if !hasMetric(got, "process.memory.usage") {
		t.Error("the live target's metrics were lost alongside the dead one's")
	}
}
