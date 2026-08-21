package receiver

import (
	"math"
	"strconv"
	"time"

	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"

	"github.com/apm2go/apm2go/internal/model"
)

// OTLP describes a metric as a name plus a list of data points, each carrying
// its own attributes and timestamp. Flattening that into one row per point
// mirrors what Convert does for spans, and for the same reason: queries want a
// flat row, and doing the work once at ingest keeps it off the query path.

// ConvertMetrics flattens OTLP resource metrics into apm2go's representation.
//
// Points that cannot be stored meaningfully are skipped rather than failing the
// batch, and counted, so one misbehaving instrument does not cost an operator
// every other metric in the request.
func ConvertMetrics(resourceMetrics []*metricpb.ResourceMetrics) (metrics []*model.Metric, skipped int) {
	for _, rm := range resourceMetrics {
		resource := attributesToMap(rm.GetResource().GetAttributes())

		service := firstNonEmpty(resource[attrServiceName], unknownService)
		host := resource[attrHostName]
		pid, _ := strconv.Atoi(resource[attrProcessPID])

		for _, sm := range rm.GetScopeMetrics() {
			for _, m := range sm.GetMetrics() {
				converted, bad := convertMetric(m, service, host, pid)
				metrics = append(metrics, converted...)
				skipped += bad
			}
		}
	}
	return metrics, skipped
}

// convertMetric turns one instrument into a row per data point.
func convertMetric(m *metricpb.Metric, service, host string, pid int) (out []*model.Metric, skipped int) {
	name := m.GetName()
	if name == "" {
		return nil, 1
	}
	unit := m.GetUnit()

	switch data := m.GetData().(type) {
	case *metricpb.Metric_Gauge:
		for _, point := range data.Gauge.GetDataPoints() {
			metric, ok := numberPoint(point, name, unit, model.KindGauge, service, host, pid)
			if !ok {
				skipped++
				continue
			}
			out = append(out, metric)
		}

	case *metricpb.Metric_Sum:
		// A non-monotonic sum counts something that goes up and down — threads
		// alive, connections open — which behaves like a gauge, not a counter,
		// and must not be differenced.
		kind := model.KindSum
		if !data.Sum.GetIsMonotonic() {
			kind = model.KindGauge
		}
		for _, point := range data.Sum.GetDataPoints() {
			metric, ok := numberPoint(point, name, unit, kind, service, host, pid)
			if !ok {
				skipped++
				continue
			}
			out = append(out, metric)
		}

	case *metricpb.Metric_Histogram:
		for _, point := range data.Histogram.GetDataPoints() {
			metric, ok := histogramPoint(point, name, unit, service, host, pid)
			if !ok {
				skipped++
				continue
			}
			out = append(out, metric)
		}

	default:
		// Exponential histograms and summaries are not stored yet. They are
		// counted as skipped rather than dropped silently, so the self page
		// shows that something arrived which apm2go did not keep.
		skipped++
	}
	return out, skipped
}

// numberPoint converts a gauge or sum data point.
func numberPoint(
	point *metricpb.NumberDataPoint,
	name, unit string,
	kind model.MetricKind,
	service, host string,
	pid int,
) (*model.Metric, bool) {
	timestamp := pointTime(point.GetTimeUnixNano())
	if timestamp.IsZero() {
		return nil, false
	}

	var value float64
	switch v := point.GetValue().(type) {
	case *metricpb.NumberDataPoint_AsDouble:
		value = v.AsDouble
	case *metricpb.NumberDataPoint_AsInt:
		value = float64(v.AsInt)
	default:
		return nil, false
	}
	// NaN and infinity would poison every aggregate they touch, and there is no
	// sensible way to store them.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, false
	}

	return &model.Metric{
		Timestamp:  timestamp,
		Name:       name,
		Kind:       kind,
		Service:    service,
		HostName:   host,
		PID:        pid,
		Value:      value,
		Unit:       unit,
		Attributes: attributesToMap(point.GetAttributes()),
	}, true
}

// histogramPoint converts a histogram data point, keeping its buckets so
// percentiles stay computable over any time range.
func histogramPoint(
	point *metricpb.HistogramDataPoint,
	name, unit string,
	service, host string,
	pid int,
) (*model.Metric, bool) {
	timestamp := pointTime(point.GetTimeUnixNano())
	if timestamp.IsZero() {
		return nil, false
	}

	sum := point.GetSum()
	if math.IsNaN(sum) || math.IsInf(sum, 0) {
		sum = 0
	}

	metric := &model.Metric{
		Timestamp:  timestamp,
		Name:       name,
		Kind:       model.KindHistogram,
		Service:    service,
		HostName:   host,
		PID:        pid,
		Count:      point.GetCount(),
		Sum:        sum,
		Unit:       unit,
		Attributes: attributesToMap(point.GetAttributes()),
	}

	// OTLP gives N explicit bounds and N+1 counts: the last count is everything
	// above the final bound, which is represented here as an infinite bound.
	bounds := point.GetExplicitBounds()
	counts := point.GetBucketCounts()
	for i, count := range counts {
		upper := math.Inf(1)
		if i < len(bounds) {
			upper = bounds[i]
		}
		metric.Buckets = append(metric.Buckets, model.Bucket{UpperBound: upper, Count: count})
	}
	return metric, true
}

// pointTime decodes a data point's timestamp, returning the zero time when it
// is absent — a point with no time cannot be placed on a chart.
func pointTime(unixNano uint64) time.Time {
	if unixNano == 0 {
		return time.Time{}
	}
	return time.Unix(0, int64(unixNano)).UTC()
}
