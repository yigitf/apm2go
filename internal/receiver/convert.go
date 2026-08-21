package receiver

import (
	"strconv"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/apm2go/apm2go/internal/model"
)

// unknownService is used when a resource carries no service.name. The OTLP
// spec says such data is still valid, and dropping it would hide a
// misconfigured agent rather than surface it.
const unknownService = "unknown_service"

// Convert flattens OTLP resource spans into apm2go's internal representation.
//
// Malformed spans are skipped rather than failing the whole batch: one bad span
// from one library should not cost an operator every other span in the request.
// The number skipped is returned so the caller can report it.
func Convert(resourceSpans []*tracepb.ResourceSpans) (spans []*model.Span, skipped int) {
	for _, rs := range resourceSpans {
		resource := attributesToMap(rs.GetResource().GetAttributes())

		service := firstNonEmpty(resource[attrServiceName], unknownService)
		host := resource[attrHostName]
		pid, _ := strconv.Atoi(resource[attrProcessPID])

		for _, ss := range rs.GetScopeSpans() {
			for _, s := range ss.GetSpans() {
				span, ok := convertSpan(s, service, host, pid, resource)
				if !ok {
					skipped++
					continue
				}
				spans = append(spans, span)
			}
		}
	}
	return spans, skipped
}

// convertSpan flattens a single OTLP span. It reports false for spans that
// cannot be stored or queried meaningfully.
func convertSpan(s *tracepb.Span, service, host string, pid int, resource map[string]string) (*model.Span, bool) {
	traceID, ok := toTraceID(s.GetTraceId())
	if !ok {
		return nil, false
	}
	spanID, ok := toSpanID(s.GetSpanId())
	if !ok {
		return nil, false
	}

	start := s.GetStartTimeUnixNano()
	end := s.GetEndTimeUnixNano()
	if start == 0 {
		return nil, false
	}
	// A missing or rewound end time yields a nonsensical duration; clamp to zero
	// rather than storing a negative one that would poison every percentile.
	var duration time.Duration
	if end > start {
		duration = time.Duration(end - start)
	}

	attrs := attributesToMap(s.GetAttributes())

	span := &model.Span{
		Timestamp:     time.Unix(0, int64(start)).UTC(),
		Duration:      duration,
		TraceID:       traceID,
		SpanID:        spanID,
		Service:       service,
		Operation:     s.GetName(),
		Kind:          model.SpanKind(s.GetKind()),
		Status:        model.StatusCode(s.GetStatus().GetCode()),
		StatusMessage: s.GetStatus().GetMessage(),
		HostName:      host,
		PID:           pid,
		Runtime:       detectRuntime(resource),
		Events:        convertEvents(s.GetEvents()),
	}
	if parent, ok := toSpanID(s.GetParentSpanId()); ok {
		span.ParentSpanID = parent
	}

	applyHTTP(span, attrs)
	applyDatabase(span, attrs)
	applyPeer(span, attrs)

	// Whatever was not promoted to a field stays queryable as an attribute.
	span.Attributes = attrs
	if resource[attrContainerID] != "" {
		span.Attributes[attrContainerID] = resource[attrContainerID]
	}
	if resource[attrApm2goInjected] != "" {
		span.Attributes[attrApm2goInjected] = resource[attrApm2goInjected]
	}
	if resource[attrServiceVersion] != "" {
		span.Attributes[attrServiceVersion] = resource[attrServiceVersion]
	}

	return span, true
}

// applyHTTP promotes HTTP attributes into columns.
func applyHTTP(span *model.Span, attrs map[string]string) {
	span.HTTPMethod = firstNonEmpty(attrs[attrHTTPRequestMethod], attrs[attrHTTPMethod])
	if span.HTTPMethod == "" {
		return
	}

	status, _ := strconv.Atoi(attrs[attrHTTPResponseStatusCode])
	legacyStatus, _ := strconv.Atoi(attrs[attrHTTPStatusCode])
	span.HTTPStatus = firstNonZero(status, legacyStatus)

	// A route is what makes an operation groupable. Frameworks supply one;
	// bare HTTP clients do not, so the raw path is normalized instead.
	span.HTTPRoute = attrs[attrHTTPRoute]
	if span.HTTPRoute == "" {
		raw := firstNonEmpty(attrs[attrURLPath], attrs[attrHTTPTarget], attrs[attrURLFull], attrs[attrHTTPURL])
		span.HTTPRoute = NormalizeRoute(raw)
	}
}

// applyDatabase promotes database attributes into columns.
func applyDatabase(span *model.Span, attrs map[string]string) {
	span.DBSystem = firstNonEmpty(attrs[attrDBSystemName], attrs[attrDBSystem])
	if span.DBSystem == "" {
		return
	}
	span.DBStatement = firstNonEmpty(attrs[attrDBQueryText], attrs[attrDBStatement])
	span.DBName = firstNonEmpty(attrs[attrDBNamespace], attrs[attrDBName])
}

// applyPeer identifies the remote side of an outbound span, which is what the
// service map uses to draw an edge.
func applyPeer(span *model.Span, attrs map[string]string) {
	switch span.Kind {
	case model.KindClient, model.KindProducer:
	default:
		return
	}
	span.PeerService = firstNonEmpty(
		attrs[attrPeerService],
		attrs[attrServerAddress],
		attrs[attrNetPeerName],
		attrs[attrMessagingDestination],
	)
}

// convertEvents flattens span events, which carry recorded exceptions.
func convertEvents(events []*tracepb.Span_Event) []model.Event {
	if len(events) == 0 {
		return nil
	}
	out := make([]model.Event, 0, len(events))
	for _, e := range events {
		out = append(out, model.Event{
			Timestamp:  time.Unix(0, int64(e.GetTimeUnixNano())).UTC(),
			Name:       e.GetName(),
			Attributes: attributesToMap(e.GetAttributes()),
		})
	}
	return out
}

func toTraceID(b []byte) (model.TraceID, bool) {
	var id model.TraceID
	if len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, !id.IsZero()
}

// toSpanID reports false for an absent id, which is valid for a parent id and
// invalid for a span's own id; the caller decides which case it is in.
func toSpanID(b []byte) (model.SpanID, bool) {
	var id model.SpanID
	if len(b) != len(id) {
		return id, false
	}
	copy(id[:], b)
	return id, !id.IsZero()
}

// attributesToMap renders OTLP key/value pairs as strings.
//
// Storing every attribute as a string trades a little type fidelity for a
// uniform, indexable representation; the fields where the type matters are
// promoted to real columns anyway.
func attributesToMap(kvs []*commonpb.KeyValue) map[string]string {
	out := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		if kv.GetKey() == "" {
			continue
		}
		if v := anyValueToString(kv.GetValue()); v != "" {
			out[kv.GetKey()] = v
		}
	}
	return out
}

// anyValueToString renders an OTLP value. Nested arrays and maps are rendered
// compactly rather than recursed into, since nothing queries their interior.
func anyValueToString(v *commonpb.AnyValue) string {
	if v == nil {
		return ""
	}
	switch val := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		return val.StringValue
	case *commonpb.AnyValue_BoolValue:
		return strconv.FormatBool(val.BoolValue)
	case *commonpb.AnyValue_IntValue:
		return strconv.FormatInt(val.IntValue, 10)
	case *commonpb.AnyValue_DoubleValue:
		return strconv.FormatFloat(val.DoubleValue, 'f', -1, 64)
	case *commonpb.AnyValue_BytesValue:
		return strconv.Itoa(len(val.BytesValue)) + " bytes"
	case *commonpb.AnyValue_ArrayValue:
		parts := make([]string, 0, len(val.ArrayValue.GetValues()))
		for _, item := range val.ArrayValue.GetValues() {
			parts = append(parts, anyValueToString(item))
		}
		return "[" + joinComma(parts) + "]"
	case *commonpb.AnyValue_KvlistValue:
		parts := make([]string, 0, len(val.KvlistValue.GetValues()))
		for _, kv := range val.KvlistValue.GetValues() {
			parts = append(parts, kv.GetKey()+"="+anyValueToString(kv.GetValue()))
		}
		return "{" + joinComma(parts) + "}"
	default:
		return ""
	}
}

func joinComma(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += "," + p
	}
	return out
}
