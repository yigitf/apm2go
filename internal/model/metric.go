package model

import (
	"encoding/json"
	"math"
	"time"
)

// MetricKind is how a measurement relates to time, which decides how it may be
// aggregated. Getting this wrong is the classic way a metrics UI lies: summing
// gauges across processes is meaningless, and averaging a counter is worse.
type MetricKind int8

const (
	// KindGauge is a value sampled at a moment: heap in use, CPU percent, open
	// connections. Averaged over time, summed across sources.
	KindGauge MetricKind = iota
	// KindSum is a running total the source keeps: bytes written, GC
	// collections. Its rate of change is what is interesting, so consecutive
	// points are differenced rather than averaged.
	KindSum
	// KindHistogram is a distribution: GC pause durations, request sizes.
	// Stored as bucket counts so percentiles remain computable over any range,
	// for the same reason spans are.
	KindHistogram
)

// String renders a kind for display and for API responses.
func (k MetricKind) String() string {
	switch k {
	case KindSum:
		return "sum"
	case KindHistogram:
		return "histogram"
	default:
		return "gauge"
	}
}

// Metric is one measurement, flattened for storage.
//
// The shape mirrors Span deliberately: the fields anything filters or groups on
// are real columns, and the rest stays in an attribute bag. That keeps the
// query path free of JSON extraction, and lets a metric be joined to the traces
// from the same process by service, host and pid.
type Metric struct {
	// Timestamp is when the measurement was taken.
	Timestamp time.Time `json:"timestamp"`
	// Name is the instrument name, e.g. "jvm.memory.used".
	Name string     `json:"name"`
	Kind MetricKind `json:"kind"`

	// Service, HostName and PID identify the source, and are what tie a metric
	// to the traces emitted alongside it.
	Service  string `json:"service"`
	HostName string `json:"host_name,omitempty"`
	PID      int    `json:"pid,omitempty"`

	// Value carries gauges and sums. For a sum it is the cumulative total as
	// reported; differencing happens at query time, where the neighbouring
	// point is known.
	Value float64 `json:"value"`

	// Count and Sum describe a histogram; Buckets holds its distribution.
	Count   uint64   `json:"count,omitempty"`
	Sum     float64  `json:"sum,omitempty"`
	Buckets []Bucket `json:"buckets,omitempty"`

	// Unit is the instrument's unit, e.g. "By" or "ms", carried so the UI can
	// format a value without guessing from its name.
	Unit string `json:"unit,omitempty"`

	// Attributes distinguish series sharing a name: which memory pool, which
	// GC, which disk. They are the metric equivalent of span attributes.
	Attributes map[string]string `json:"attributes,omitempty"`
}

// Bucket is one bar of a histogram: how many observations fell at or below
// UpperBound. The last bucket of an OTLP histogram is unbounded, which is
// represented as an infinite upper bound.
type Bucket struct {
	UpperBound float64 `json:"-"`
	Count      uint64  `json:"count"`
}

// bucketJSON is the wire form. The unbounded bucket is written as a null bound
// rather than a number, because JSON has no way to spell infinity: encoding it
// fails outright, and a histogram that cannot be encoded would take every
// metric batched with it down as well.
type bucketJSON struct {
	UpperBound *float64 `json:"upper_bound"`
	Count      uint64   `json:"count"`
}

// MarshalJSON writes an unbounded upper bound as null.
func (b Bucket) MarshalJSON() ([]byte, error) {
	out := bucketJSON{Count: b.Count}
	if !math.IsInf(b.UpperBound, 1) {
		bound := b.UpperBound
		out.UpperBound = &bound
	}
	return json.Marshal(out)
}

// UnmarshalJSON reads a null upper bound back as unbounded.
func (b *Bucket) UnmarshalJSON(data []byte) error {
	var in bucketJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	b.Count = in.Count
	if in.UpperBound == nil {
		b.UpperBound = math.Inf(1)
	} else {
		b.UpperBound = *in.UpperBound
	}
	return nil
}

// IsUnbounded reports whether this is the overflow bucket, which holds
// everything above the highest explicit bound.
func (b Bucket) IsUnbounded() bool { return math.IsInf(b.UpperBound, 1) }

// IsZero reports whether a metric carries no measurement, which is how a
// malformed or empty OTLP point arrives.
func (m *Metric) IsZero() bool {
	return m.Name == "" || m.Timestamp.IsZero()
}
