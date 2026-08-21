package store

import (
	"time"

	"github.com/apm2go/apm2go/internal/model"
)

// TimeRange bounds a query. From is inclusive, To exclusive.
type TimeRange struct {
	From time.Time
	To   time.Time
}

// Duration is the length of the range.
func (r TimeRange) Duration() time.Duration { return r.To.Sub(r.From) }

// ServiceStats are the RED metrics for one service over a time range: rate,
// errors and duration, which is what an operator looks at first.
type ServiceStats struct {
	Service string `json:"service"`
	// SpanCount is the number of entry spans, i.e. requests served.
	SpanCount int64 `json:"span_count"`
	// ErrorCount is how many of those failed.
	ErrorCount int64 `json:"error_count"`
	// ErrorRate is ErrorCount/SpanCount, precomputed so every consumer agrees.
	ErrorRate float64 `json:"error_rate"`
	// RequestsPerSecond is SpanCount over the range length.
	RequestsPerSecond float64 `json:"requests_per_second"`
	// Latencies in nanoseconds.
	AvgLatencyNanos int64 `json:"avg_latency_ns"`
	P50LatencyNanos int64 `json:"p50_latency_ns"`
	P95LatencyNanos int64 `json:"p95_latency_ns"`
	P99LatencyNanos int64 `json:"p99_latency_ns"`
	MaxLatencyNanos int64 `json:"max_latency_ns"`
	// LastSeen is the newest span timestamp for this service.
	LastSeen time.Time `json:"last_seen"`
	// Runtime is the language the service is written in, as its own telemetry
	// reported it. Empty when nothing that reached apm2go said — an SDK that
	// omits the attribute, or a range whose spans predate apm2go recording it —
	// which readers must treat as "not known" rather than as a language.
	Runtime string `json:"runtime,omitempty"`
}

// OperationStats are the same metrics broken down by operation within a service.
type OperationStats struct {
	Operation string `json:"operation"`
	Kind      string `json:"kind"`
	ServiceStats
}

// TraceSummary is one row in the trace list: enough to decide whether a trace
// is worth opening, without loading its spans.
type TraceSummary struct {
	TraceID string `json:"trace_id"`
	// RootService and RootOperation name the entry point of the trace.
	RootService   string    `json:"root_service"`
	RootOperation string    `json:"root_operation"`
	StartTime     time.Time `json:"start_time"`
	// DurationNanos is the root span's duration, i.e. the end-to-end time.
	DurationNanos int64  `json:"duration_ns"`
	SpanCount     int64  `json:"span_count"`
	ErrorCount    int64  `json:"error_count"`
	ServiceCount  int64  `json:"service_count"`
	HTTPMethod    string `json:"http_method,omitempty"`
	HTTPRoute     string `json:"http_route,omitempty"`
	HTTPStatus    int    `json:"http_status,omitempty"`
	// ExceptionType names the first recorded exception, which is usually the
	// reason a trace is being looked at.
	ExceptionType string `json:"exception_type,omitempty"`
}

// TraceFilter narrows a trace search. Zero values mean "no constraint".
type TraceFilter struct {
	Range TimeRange
	// Service and Operation restrict to traces containing a matching span.
	Service   string
	Operation string
	// MinDuration and MaxDuration bound the root span's duration.
	MinDuration time.Duration
	MaxDuration time.Duration
	// OnlyErrors keeps traces containing at least one failed span.
	OnlyErrors bool
	// HTTPStatus, when non-zero, matches the root span's response code.
	HTTPStatus int
	// Search matches operation names, routes and SQL statements.
	Search string
	// Limit caps the result set; the store applies a hard ceiling regardless.
	Limit int
}

// maxTraceLimit bounds any single trace search, so a missing or hostile limit
// cannot ask the store for the entire retention window.
const maxTraceLimit = 1000

// limit returns the effective row limit for a filter.
func (f *TraceFilter) limit() int {
	if f.Limit <= 0 || f.Limit > maxTraceLimit {
		return maxTraceLimit
	}
	return f.Limit
}

// Trace is a full trace: every span, ordered so the UI can build a waterfall.
type Trace struct {
	TraceID string        `json:"trace_id"`
	Spans   []*model.Span `json:"spans"`
	// StartTime and DurationNanos bound the whole trace, which is what the
	// waterfall scales against.
	StartTime     time.Time `json:"start_time"`
	DurationNanos int64     `json:"duration_ns"`
	Services      []string  `json:"services"`
}

// Dependency is one edge of the service map.
type Dependency struct {
	Caller     string  `json:"caller"`
	Callee     string  `json:"callee"`
	CallCount  int64   `json:"call_count"`
	ErrorCount int64   `json:"error_count"`
	ErrorRate  float64 `json:"error_rate"`
	AvgLatency int64   `json:"avg_latency_ns"`
}

// TimeSeriesPoint is one bucket of a chart.
type TimeSeriesPoint struct {
	Timestamp  time.Time `json:"timestamp"`
	Count      int64     `json:"count"`
	ErrorCount int64     `json:"error_count"`
	// Rate is Count divided by the bucket width in seconds.
	Rate            float64 `json:"rate"`
	ErrorRate       float64 `json:"error_rate"`
	AvgLatencyNanos int64   `json:"avg_latency_ns"`
	P95LatencyNanos int64   `json:"p95_latency_ns"`
}

// StorageStats describe the database itself, for the settings page.
type StorageStats struct {
	SpanCount   int64     `json:"span_count"`
	RollupCount int64     `json:"rollup_count"`
	OldestSpan  time.Time `json:"oldest_span,omitempty"`
	NewestSpan  time.Time `json:"newest_span,omitempty"`
	SizeBytes   int64     `json:"size_bytes"`
	Services    int64     `json:"services"`
}
