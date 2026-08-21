// Package model defines apm2go's internal representation of a trace span.
//
// It deliberately sits between the OTLP wire format and the store: OTLP is a
// nested, attribute-bag structure built for transport, while queries want a
// flat row with the handful of fields an APM actually filters and groups on.
// Translating once, at ingest, keeps that cost off the query path.
package model

import (
	"encoding/hex"
	"encoding/json"
	"time"
)

// SpanKind mirrors the OpenTelemetry span kind.
type SpanKind int8

// Span kinds, with values matching the OTLP enum so conversion is a cast.
const (
	KindUnspecified SpanKind = 0
	KindInternal    SpanKind = 1
	KindServer      SpanKind = 2
	KindClient      SpanKind = 3
	KindProducer    SpanKind = 4
	KindConsumer    SpanKind = 5
)

// String renders a kind for display and for API responses.
func (k SpanKind) String() string {
	switch k {
	case KindInternal:
		return "internal"
	case KindServer:
		return "server"
	case KindClient:
		return "client"
	case KindProducer:
		return "producer"
	case KindConsumer:
		return "consumer"
	default:
		return "unspecified"
	}
}

// StatusCode mirrors the OpenTelemetry span status.
type StatusCode int8

// Status codes, with values matching the OTLP enum.
const (
	StatusUnset StatusCode = 0
	StatusOK    StatusCode = 1
	StatusError StatusCode = 2
)

// String renders a status for display.
func (s StatusCode) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusError:
		return "error"
	default:
		return "unset"
	}
}

// TraceID and SpanID are fixed-size identifiers, stored as raw bytes because
// that halves the storage cost against hex and keeps comparisons cheap.
type (
	TraceID [16]byte
	SpanID  [8]byte
)

// String renders an id as lowercase hex, which is how the API and UI show it.
func (t TraceID) String() string { return hex.EncodeToString(t[:]) }

// String renders an id as lowercase hex.
func (s SpanID) String() string { return hex.EncodeToString(s[:]) }

// IsZero reports whether the id is unset, which for a parent id means the span
// is the root of its trace.
func (s SpanID) IsZero() bool { return s == SpanID{} }

// IsZero reports whether the trace id is unset, which makes a span unusable.
func (t TraceID) IsZero() bool { return t == TraceID{} }

// ParseTraceID decodes a 32 character hex trace id.
func ParseTraceID(s string) (TraceID, error) {
	var id TraceID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, &IDError{Kind: "trace", Value: s}
	}
	copy(id[:], b)
	return id, nil
}

// ParseSpanID decodes a 16 character hex span id.
func ParseSpanID(s string) (SpanID, error) {
	var id SpanID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, &IDError{Kind: "span", Value: s}
	}
	copy(id[:], b)
	return id, nil
}

// IDError reports a malformed identifier.
type IDError struct {
	Kind  string
	Value string
}

func (e *IDError) Error() string {
	return "invalid " + e.Kind + " id: " + e.Value
}

// Span is one unit of work, flattened for storage and querying.
//
// The promoted fields (HTTP, database, messaging) are the ones the UI filters
// and groups on; everything else stays in Attributes and is only read when a
// single trace is opened.
type Span struct {
	// Timestamp is when the span started.
	Timestamp time.Time `json:"timestamp"`
	// Duration is the span's wall clock length.
	Duration time.Duration `json:"duration"`

	TraceID      TraceID `json:"trace_id"`
	SpanID       SpanID  `json:"span_id"`
	ParentSpanID SpanID  `json:"parent_span_id"`

	// Service is the emitting service, and Operation the span name.
	Service   string `json:"service"`
	Operation string `json:"operation"`

	Kind          SpanKind   `json:"kind"`
	Status        StatusCode `json:"status"`
	StatusMessage string     `json:"status_message,omitempty"`

	// HTTP fields, populated for client and server HTTP spans.
	HTTPMethod string `json:"http_method,omitempty"`
	// HTTPRoute is the templated path such as "/orders/{id}". Falling back to a
	// raw URL would make every request its own operation, so it is normalized
	// at ingest when the instrumentation did not supply one.
	HTTPRoute  string `json:"http_route,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`

	// Database fields, populated for client database spans.
	DBSystem    string `json:"db_system,omitempty"`
	DBStatement string `json:"db_statement,omitempty"`
	DBName      string `json:"db_name,omitempty"`

	// PeerService names the remote side of a client span, which is what draws
	// the edges on the service map.
	PeerService string `json:"peer_service,omitempty"`

	// HostName and PID identify the process the span came from.
	HostName string `json:"host_name,omitempty"`
	PID      int    `json:"pid,omitempty"`

	// Runtime is the language the emitting process runs — "java", "go",
	// "nodejs", "python". It comes from the telemetry itself rather than from
	// apm2go's own process inventory, because the inventory only knows about
	// processes alive right now: a service that exited an hour ago is still in
	// every chart covering that hour, and should still say what it was.
	Runtime string `json:"runtime,omitempty"`

	// Attributes holds everything not promoted to a column.
	Attributes map[string]string `json:"attributes,omitempty"`

	// Events carries span events, most importantly recorded exceptions.
	Events []Event `json:"events,omitempty"`
}

// Event is a timestamped annotation on a span.
type Event struct {
	Timestamp  time.Time         `json:"timestamp"`
	Name       string            `json:"name"`
	Attributes map[string]string `json:"attributes,omitempty"`
}

// IsRoot reports whether the span starts its trace.
func (s *Span) IsRoot() bool { return s.ParentSpanID.IsZero() }

// IsError reports whether the span should count against the error rate. An
// explicit error status wins; otherwise a 5xx response is treated as a failure,
// while 4xx is not, since client mistakes are not the service's errors.
func (s *Span) IsError() bool {
	if s.Status == StatusError {
		return true
	}
	return s.HTTPStatus >= 500
}

// EndTime is when the span finished.
func (s *Span) EndTime() time.Time { return s.Timestamp.Add(s.Duration) }

// ExceptionType returns the type of the first recorded exception, which is what
// the trace list shows as the reason a request failed.
func (s *Span) ExceptionType() string {
	for _, ev := range s.Events {
		if ev.Name == "exception" {
			if t := ev.Attributes["exception.type"]; t != "" {
				return t
			}
		}
	}
	return ""
}

// Identifiers are fixed-size byte arrays in Go, which encoding/json would
// otherwise render as arrays of numbers. Every consumer — the API, the UI, and
// anyone correlating with another tracing tool — expects the lowercase hex form
// the OpenTelemetry ecosystem uses, so the encoding is defined explicitly.

// MarshalJSON renders a trace id as a hex string.
func (t TraceID) MarshalJSON() ([]byte, error) { return marshalHexID(t[:], false) }

// UnmarshalJSON parses a hex trace id.
func (t *TraceID) UnmarshalJSON(data []byte) error {
	return unmarshalHexID(data, t[:], "trace")
}

// MarshalJSON renders a span id as a hex string. A zero id becomes the empty
// string, so a root span's absent parent is falsy to a client rather than a
// run of sixteen zeros that reads like a real id.
func (s SpanID) MarshalJSON() ([]byte, error) { return marshalHexID(s[:], true) }

// UnmarshalJSON parses a hex span id, accepting the empty string as zero.
func (s *SpanID) UnmarshalJSON(data []byte) error {
	return unmarshalHexID(data, s[:], "span")
}

func marshalHexID(raw []byte, emptyWhenZero bool) ([]byte, error) {
	if emptyWhenZero && isZeroBytes(raw) {
		return []byte(`""`), nil
	}
	out := make([]byte, 0, len(raw)*2+2)
	out = append(out, '"')
	out = append(out, hex.EncodeToString(raw)...)
	out = append(out, '"')
	return out, nil
}

func unmarshalHexID(data []byte, dst []byte, kind string) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	if s == "" {
		clear(dst)
		return nil
	}
	decoded, err := hex.DecodeString(s)
	if err != nil || len(decoded) != len(dst) {
		return &IDError{Kind: kind, Value: s}
	}
	copy(dst, decoded)
	return nil
}

func isZeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
