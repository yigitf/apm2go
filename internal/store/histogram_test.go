package store

import (
	"math"
	"testing"
	"time"
)

func TestBucketIndexIsMonotonic(t *testing.T) {
	previous := int16(-1)
	for _, d := range []time.Duration{
		100 * time.Nanosecond,
		time.Microsecond,
		10 * time.Microsecond,
		time.Millisecond,
		50 * time.Millisecond,
		time.Second,
		30 * time.Second,
		20 * time.Minute,
	} {
		got := BucketIndex(int64(d))
		if got < previous {
			t.Errorf("BucketIndex(%v) = %d, which is below the previous bucket %d", d, got, previous)
		}
		if got < 0 || int(got) >= histogramBuckets {
			t.Errorf("BucketIndex(%v) = %d, outside [0,%d)", d, got, histogramBuckets)
		}
		previous = got
	}
}

func TestBucketBoundsContainTheirDurations(t *testing.T) {
	// The reported percentile is a bucket's upper bound, so a duration must
	// never exceed the bound of the bucket it lands in — otherwise a p95 could
	// be reported as faster than requests that were actually served.
	for _, d := range []time.Duration{
		2 * time.Microsecond,
		750 * time.Microsecond,
		3 * time.Millisecond,
		1200 * time.Millisecond,
		45 * time.Second,
	} {
		bucket := BucketIndex(int64(d))
		if upper := BucketUpperBound(bucket); upper < int64(d) {
			t.Errorf("duration %v lands in bucket %d whose upper bound %v is below it",
				d, bucket, time.Duration(upper))
		}
	}
}

func TestHistogramQuantileAccuracy(t *testing.T) {
	// A uniform spread from 1ms to 1000ms: the histogram's relative error is
	// about 8% by construction, so a 15% tolerance is a meaningful check.
	var h Histogram
	for i := 1; i <= 1000; i++ {
		h.Add(BucketIndex(int64(time.Duration(i)*time.Millisecond)), 1)
	}

	tests := []struct {
		quantile float64
		want     time.Duration
	}{
		{0.5, 500 * time.Millisecond},
		{0.95, 950 * time.Millisecond},
		{0.99, 990 * time.Millisecond},
	}

	for _, tt := range tests {
		got := time.Duration(h.Quantile(tt.quantile))
		ratio := math.Abs(float64(got-tt.want)) / float64(tt.want)
		if ratio > 0.15 {
			t.Errorf("Quantile(%v) = %v, want within 15%% of %v (off by %.1f%%)",
				tt.quantile, got, tt.want, ratio*100)
		}
	}
}

func TestHistogramMergeIsAdditive(t *testing.T) {
	// Mergeability is the whole reason for storing a histogram rather than a
	// precomputed p95: a percentile over two minutes must be computable from
	// the two per-minute rows.
	var a, b, combined Histogram
	for i := 1; i <= 100; i++ {
		bucket := BucketIndex(int64(time.Duration(i) * time.Millisecond))
		a.Add(bucket, 1)
		combined.Add(bucket, 1)
	}
	for i := 101; i <= 200; i++ {
		bucket := BucketIndex(int64(time.Duration(i) * time.Millisecond))
		b.Add(bucket, 1)
		combined.Add(bucket, 1)
	}

	a.Merge(&b)
	if a.Total != combined.Total {
		t.Fatalf("merged total = %d, want %d", a.Total, combined.Total)
	}
	if got, want := a.Quantile(0.95), combined.Quantile(0.95); got != want {
		t.Errorf("merged p95 = %v, want %v", time.Duration(got), time.Duration(want))
	}
}

func TestHistogramQuantileOfEmptyIsZero(t *testing.T) {
	var h Histogram
	if got := h.Quantile(0.95); got != 0 {
		t.Errorf("Quantile of an empty histogram = %d, want 0", got)
	}
}
