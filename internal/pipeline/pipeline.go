// Package pipeline sits between the OTLP receiver and the store. It absorbs
// ingest bursts, enforces the limits that keep apm2go from destabilising the
// host it monitors, and batches writes so the store is not asked to commit a
// row at a time.
//
// The ordering of the stages matters. Normalization runs on the receiver's
// goroutine so malformed data is corrected before it occupies queue space;
// rate limiting and batching run on a single writer goroutine, because the
// embedded store admits one writer at a time anyway.
package pipeline

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yigitf/apm2go/internal/config"
	"github.com/yigitf/apm2go/internal/model"
)

// Writer is the store's ingest side.
type Writer interface {
	// WriteSpans persists a batch. It is only ever called from the pipeline's
	// single writer goroutine.
	WriteSpans(ctx context.Context, spans []*model.Span) error
	// WriteMetrics does the same for metrics.
	WriteMetrics(ctx context.Context, metrics []*model.Metric) error
}

// Stats reports what the pipeline has done, for the self-monitoring endpoint.
type Stats struct {
	Queued         int64 `json:"queued"`
	QueueCapacity  int   `json:"queue_capacity"`
	QueueDepth     int   `json:"queue_depth"`
	Written        int64 `json:"written"`
	DroppedQueue   int64 `json:"dropped_queue_full"`
	DroppedRate    int64 `json:"dropped_rate_limit"`
	WriteErrors    int64 `json:"write_errors"`
	Batches        int64 `json:"batches"`
	MetricsWritten int64 `json:"metrics_written"`
	MetricsDropped int64 `json:"metrics_dropped"`
	LastFlushAt    int64 `json:"last_flush_at"`
	LastFlushMs    int64 `json:"last_flush_ms"`
}

// Pipeline batches spans from the receiver into the store.
// RuntimeResolver names the runtime a service runs, for telemetry that did not
// carry one of its own. internal/ebpf.Registry is the implementation.
type RuntimeResolver interface {
	RuntimeFor(service string) string
}

type Pipeline struct {
	cfg    config.PipelineConfig
	writer Writer
	log    *slog.Logger

	// runtimes fills in a language the producer did not report. It is set once
	// during wiring, before Run, and only read afterwards — a mutex here would
	// guard against a caller that does not exist.
	runtimes RuntimeResolver

	queue chan *model.Span
	// Metrics get their own queue. Sharing one with spans would let a burst of
	// either starve the other, and they are flushed on different terms:
	// metrics arrive on a fixed collection interval and are far fewer.
	metricQueue chan *model.Metric
	limiter     *rateLimiter
	guard       *cardinalityGuard

	// Counters are atomic because the receiver increments them from many
	// goroutines while the writer reads them.
	queued         atomic.Int64
	written        atomic.Int64
	metricsWritten atomic.Int64
	metricsDropped atomic.Int64
	droppedQueue   atomic.Int64
	droppedRate    atomic.Int64
	writeErrors    atomic.Int64
	batches        atomic.Int64
	lastFlushAt    atomic.Int64
	lastFlushMs    atomic.Int64

	// warnOnce keeps a sustained overload from filling the log with one line
	// per dropped span.
	warnOnce sync.Once
}

// New returns a Pipeline writing into writer.
func New(cfg config.PipelineConfig, writer Writer, log *slog.Logger) *Pipeline {
	return &Pipeline{
		cfg:    cfg,
		writer: writer,
		log:    log,
		queue:  make(chan *model.Span, cfg.QueueSize),
		// A metric queue proportional to the span queue: metrics are a small
		// fraction of the volume, and an oversized buffer would only delay
		// noticing that writes have stalled.
		metricQueue: make(chan *model.Metric, cfg.QueueSize/10+1000),
		limiter:     newRateLimiter(cfg.MaxSpansPerSecond),
		guard:       newCardinalityGuard(cfg.MaxServices, cfg.MaxOperations),
	}
}

// SetRuntimeResolver attaches the source of runtimes for services whose own
// telemetry does not name one. Call it during wiring, before Run.
func (p *Pipeline) SetRuntimeResolver(resolver RuntimeResolver) { p.runtimes = resolver }

// Name identifies this component in logs.
func (p *Pipeline) Name() string { return "pipeline" }

// Consume accepts spans from the receiver.
//
// It never blocks. When the queue is full the spans are dropped and counted:
// blocking here would push backpressure into the monitored application's
// export thread, which is exactly the kind of harm an APM must not do.
func (p *Pipeline) Consume(ctx context.Context, spans []*model.Span) error {
	for _, span := range spans {
		if !p.limiter.allow() {
			p.droppedRate.Add(1)
			continue
		}
		p.normalize(span)

		select {
		case p.queue <- span:
			p.queued.Add(1)
		case <-ctx.Done():
			return ctx.Err()
		default:
			p.droppedQueue.Add(1)
			p.warnOverload()
		}
	}
	return nil
}

// warnOverload reports sustained shedding once, pointing at the settings that
// control it.
func (p *Pipeline) warnOverload() {
	p.warnOnce.Do(func() {
		p.log.Warn("ingest queue is full, spans are being dropped",
			"queue_size", p.cfg.QueueSize,
			"hint", "raise pipeline.queue_size, lower attach.sample_ratio, or check why writes are slow")
	})
}

// ConsumeMetrics accepts metrics from the receiver.
//
// Like Consume it never blocks: a full queue drops rather than pushing
// backpressure into the monitored application's export thread.
func (p *Pipeline) ConsumeMetrics(ctx context.Context, metrics []*model.Metric) error {
	for _, metric := range metrics {
		// Metric names are bounded by the instruments an SDK defines, but their
		// attributes are not, so the same service ceiling applies.
		metric.Service = p.guard.service(metric.Service)

		select {
		case p.metricQueue <- metric:
		case <-ctx.Done():
			return ctx.Err()
		default:
			p.metricsDropped.Add(1)
		}
	}
	return nil
}

// Run drains the queue into the store until ctx is cancelled.
func (p *Pipeline) Run(ctx context.Context) error {
	ticker := time.NewTicker(p.cfg.BatchTimeout)
	defer ticker.Stop()

	batch := make([]*model.Span, 0, p.cfg.BatchSize)
	metricBatch := make([]*model.Metric, 0, p.cfg.BatchSize)

	// flush is a closure so both the size trigger and the timer trigger share
	// exactly one code path.
	flush := func(reason string) {
		if len(batch) == 0 {
			return
		}
		p.write(ctx, batch, reason)
		batch = batch[:0]
	}

	flushMetrics := func(reason string) {
		if len(metricBatch) == 0 {
			return
		}
		p.writeMetrics(ctx, metricBatch, reason)
		metricBatch = metricBatch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			// Drain whatever is already queued so a clean shutdown does not
			// lose the last few seconds of telemetry.
			p.drain(&batch)
			p.drainMetrics(&metricBatch)
			flush("shutdown")
			flushMetrics("shutdown")
			return ctx.Err()

		case span := <-p.queue:
			batch = append(batch, span)
			if len(batch) >= p.cfg.BatchSize {
				flush("batch full")
			}

		case metric := <-p.metricQueue:
			metricBatch = append(metricBatch, metric)
			if len(metricBatch) >= p.cfg.BatchSize {
				flushMetrics("batch full")
			}

		case <-ticker.C:
			flush("batch timeout")
			flushMetrics("batch timeout")
		}
	}
}

// drain moves everything currently queued into batch without blocking.
func (p *Pipeline) drain(batch *[]*model.Span) {
	for {
		select {
		case span := <-p.queue:
			*batch = append(*batch, span)
		default:
			return
		}
	}
}

// drainMetrics moves everything currently queued into batch without blocking.
func (p *Pipeline) drainMetrics(batch *[]*model.Metric) {
	for {
		select {
		case metric := <-p.metricQueue:
			*batch = append(*batch, metric)
		default:
			return
		}
	}
}

// writeMetrics persists one metric batch.
func (p *Pipeline) writeMetrics(ctx context.Context, batch []*model.Metric, reason string) {
	writeCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}

	if err := p.writer.WriteMetrics(writeCtx, batch); err != nil {
		p.writeErrors.Add(1)
		p.log.Error("failed to write metric batch",
			"metrics", len(batch), "reason", reason, "error", err)
		return
	}
	p.metricsWritten.Add(int64(len(batch)))
}

// write persists one batch, recording timing and errors.
//
// A failed write loses the batch rather than retrying: the data is already
// seconds old, the store is the only place it could go, and blocking here would
// back the queue up into the applications being monitored.
func (p *Pipeline) write(ctx context.Context, batch []*model.Span, reason string) {
	start := time.Now()

	// Shutdown must still be able to flush, so the write gets its own context
	// rather than inheriting a cancelled one.
	writeCtx := ctx
	if ctx.Err() != nil {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
	}

	if err := p.writer.WriteSpans(writeCtx, batch); err != nil {
		p.writeErrors.Add(1)
		p.log.Error("failed to write span batch",
			"spans", len(batch), "reason", reason, "error", err)
		return
	}

	elapsed := time.Since(start)
	p.written.Add(int64(len(batch)))
	p.batches.Add(1)
	p.lastFlushAt.Store(time.Now().Unix())
	p.lastFlushMs.Store(elapsed.Milliseconds())

	p.log.Debug("wrote span batch",
		"spans", len(batch), "reason", reason, "took", elapsed.Round(time.Millisecond))
}

// normalize applies the limits that protect the store from any single
// misbehaving application.
func (p *Pipeline) normalize(span *model.Span) {
	span.Service = p.guard.service(span.Service)
	span.Operation = p.guard.operation(span.Service, span.Operation)

	// Only when the producer said nothing. A runtime that arrived in the
	// telemetry came from the process itself and is the better answer; this is
	// for the native binaries — nginx, httpd — that OBI can watch but cannot
	// introspect, where the only thing that ever knew what they were is the
	// discovery pass that decided to instrument them.
	if span.Runtime == "" && p.runtimes != nil {
		span.Runtime = p.runtimes.RuntimeFor(span.Service)
	}

	span.DBStatement = truncate(span.DBStatement, p.cfg.MaxAttrBytes)
	span.StatusMessage = truncate(span.StatusMessage, p.cfg.MaxAttrBytes)

	for k, v := range span.Attributes {
		if len(v) > p.cfg.MaxAttrBytes {
			span.Attributes[k] = truncate(v, p.cfg.MaxAttrBytes)
		}
	}
	for i := range span.Events {
		for k, v := range span.Events[i].Attributes {
			if len(v) > p.cfg.MaxAttrBytes {
				span.Events[i].Attributes[k] = truncate(v, p.cfg.MaxAttrBytes)
			}
		}
	}
}

// truncate shortens a value and marks that it was shortened, so nobody debugs a
// query that was merely cut off.
func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	const marker = "...[truncated]"
	if max <= len(marker) {
		return s[:max]
	}
	return s[:max-len(marker)] + marker
}

// Stats returns a snapshot of the pipeline counters.
func (p *Pipeline) Stats() Stats {
	return Stats{
		Queued:         p.queued.Load(),
		QueueCapacity:  p.cfg.QueueSize,
		QueueDepth:     len(p.queue),
		Written:        p.written.Load(),
		DroppedQueue:   p.droppedQueue.Load(),
		DroppedRate:    p.droppedRate.Load(),
		WriteErrors:    p.writeErrors.Load(),
		Batches:        p.batches.Load(),
		MetricsWritten: p.metricsWritten.Load(),
		MetricsDropped: p.metricsDropped.Load(),
		LastFlushAt:    p.lastFlushAt.Load(),
		LastFlushMs:    p.lastFlushMs.Load(),
	}
}

// Cardinality returns the current service and operation counts.
func (p *Pipeline) Cardinality() (services, operations int) { return p.guard.counts() }
