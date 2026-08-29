package receiver

import (
	"math"
	"testing"
	"time"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricpb "go.opentelemetry.io/proto/otlp/metrics/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"

	"github.com/yigitf/apm2go/internal/model"
)

// resourceMetrics wraps one instrument in the envelope OTLP delivers.
func resourceMetrics(service string, m *metricpb.Metric) []*metricpb.ResourceMetrics {
	return []*metricpb.ResourceMetrics{{
		Resource: &resourcepb.Resource{
			Attributes: []*commonpb.KeyValue{{
				Key:   attrServiceName,
				Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: service}},
			}},
		},
		ScopeMetrics: []*metricpb.ScopeMetrics{{Metrics: []*metricpb.Metric{m}}},
	}}
}

func nowNanos() uint64 { return uint64(time.Now().UnixNano()) }

func TestConvertMetricsReadsGauges(t *testing.T) {
	m := &metricpb.Metric{
		Name: "jvm.memory.used",
		Unit: "By",
		Data: &metricpb.Metric_Gauge{Gauge: &metricpb.Gauge{
			DataPoints: []*metricpb.NumberDataPoint{{
				TimeUnixNano: nowNanos(),
				Value:        &metricpb.NumberDataPoint_AsInt{AsInt: 1 << 20},
			}},
		}},
	}

	metrics, skipped := ConvertMetrics(resourceMetrics("orders", m))
	if skipped != 0 || len(metrics) != 1 {
		t.Fatalf("got %d metrics, %d skipped; want 1 and 0", len(metrics), skipped)
	}
	got := metrics[0]
	if got.Kind != model.KindGauge {
		t.Errorf("Kind = %v, want gauge", got.Kind)
	}
	if got.Value != float64(1<<20) {
		t.Errorf("Value = %v, want %v", got.Value, float64(1<<20))
	}
	if got.Service != "orders" || got.Unit != "By" {
		t.Errorf("Service/Unit = %q/%q", got.Service, got.Unit)
	}
}

func TestNonMonotonicSumIsTreatedAsAGauge(t *testing.T) {
	// A sum that goes down as well as up — threads alive, connections open —
	// is a level, not a counter. Differencing it would report negative rates.
	m := &metricpb.Metric{
		Name: "jvm.thread.count",
		Data: &metricpb.Metric_Sum{Sum: &metricpb.Sum{
			IsMonotonic: false,
			DataPoints: []*metricpb.NumberDataPoint{{
				TimeUnixNano: nowNanos(),
				Value:        &metricpb.NumberDataPoint_AsInt{AsInt: 42},
			}},
		}},
	}

	metrics, _ := ConvertMetrics(resourceMetrics("orders", m))
	if len(metrics) != 1 {
		t.Fatalf("got %d metrics, want 1", len(metrics))
	}
	if metrics[0].Kind != model.KindGauge {
		t.Errorf("Kind = %v, want gauge for a non-monotonic sum", metrics[0].Kind)
	}
}

func TestMonotonicSumStaysASum(t *testing.T) {
	m := &metricpb.Metric{
		Name: "jvm.gc.duration.count",
		Data: &metricpb.Metric_Sum{Sum: &metricpb.Sum{
			IsMonotonic: true,
			DataPoints: []*metricpb.NumberDataPoint{{
				TimeUnixNano: nowNanos(),
				Value:        &metricpb.NumberDataPoint_AsDouble{AsDouble: 17},
			}},
		}},
	}

	metrics, _ := ConvertMetrics(resourceMetrics("orders", m))
	if len(metrics) != 1 || metrics[0].Kind != model.KindSum {
		t.Fatalf("a monotonic sum must keep its kind, got %v", metrics)
	}
}

func TestHistogramKeepsItsBuckets(t *testing.T) {
	// OTLP sends N bounds and N+1 counts; the extra count is the overflow above
	// the last bound, which must survive as an infinite bucket or the
	// distribution's tail is silently lost.
	sum := 12.5
	m := &metricpb.Metric{
		Name: "jvm.gc.duration",
		Unit: "s",
		Data: &metricpb.Metric_Histogram{Histogram: &metricpb.Histogram{
			DataPoints: []*metricpb.HistogramDataPoint{{
				TimeUnixNano:   nowNanos(),
				Count:          10,
				Sum:            &sum,
				ExplicitBounds: []float64{0.01, 0.1},
				BucketCounts:   []uint64{6, 3, 1},
			}},
		}},
	}

	metrics, skipped := ConvertMetrics(resourceMetrics("orders", m))
	if skipped != 0 || len(metrics) != 1 {
		t.Fatalf("got %d metrics, %d skipped", len(metrics), skipped)
	}
	got := metrics[0]
	if got.Kind != model.KindHistogram || got.Count != 10 || got.Sum != sum {
		t.Errorf("histogram summary wrong: kind=%v count=%d sum=%v", got.Kind, got.Count, got.Sum)
	}
	if len(got.Buckets) != 3 {
		t.Fatalf("got %d buckets, want 3 (two bounds plus the overflow)", len(got.Buckets))
	}
	if !math.IsInf(got.Buckets[2].UpperBound, 1) {
		t.Errorf("last bucket bound = %v, want +Inf", got.Buckets[2].UpperBound)
	}
	if got.Buckets[2].Count != 1 {
		t.Errorf("overflow bucket count = %d, want 1", got.Buckets[2].Count)
	}
}

func TestUnusableDataPointsAreSkippedNotStored(t *testing.T) {
	tests := []struct {
		name   string
		metric *metricpb.Metric
	}{
		{
			name: "no name",
			metric: &metricpb.Metric{Data: &metricpb.Metric_Gauge{Gauge: &metricpb.Gauge{
				DataPoints: []*metricpb.NumberDataPoint{{TimeUnixNano: nowNanos()}},
			}}},
		},
		{
			// A point with no timestamp cannot be placed on a chart.
			name: "no timestamp",
			metric: &metricpb.Metric{Name: "x", Data: &metricpb.Metric_Gauge{Gauge: &metricpb.Gauge{
				DataPoints: []*metricpb.NumberDataPoint{{
					Value: &metricpb.NumberDataPoint_AsInt{AsInt: 1},
				}},
			}}},
		},
		{
			// NaN would poison every aggregate that touched it.
			name: "NaN value",
			metric: &metricpb.Metric{Name: "x", Data: &metricpb.Metric_Gauge{Gauge: &metricpb.Gauge{
				DataPoints: []*metricpb.NumberDataPoint{{
					TimeUnixNano: nowNanos(),
					Value:        &metricpb.NumberDataPoint_AsDouble{AsDouble: math.NaN()},
				}},
			}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metrics, skipped := ConvertMetrics(resourceMetrics("orders", tt.metric))
			if len(metrics) != 0 {
				t.Errorf("stored %d metrics, want none", len(metrics))
			}
			if skipped == 0 {
				t.Error("the point was dropped without being counted as skipped")
			}
		})
	}
}
