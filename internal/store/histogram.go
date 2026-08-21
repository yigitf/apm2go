package store

import "math"

// Latency percentiles have to be computed over arbitrary time ranges, which
// rules out storing p95 per minute: percentiles cannot be averaged. apm2go
// instead stores a logarithmic histogram, which merges by simple addition and
// yields a correct percentile over any range.
//
// Each span's duration is mapped to a bucket at write time, so the rollup is a
// plain GROUP BY and the percentile query is a window function over bucket
// counts. Bucket boundaries grow geometrically, giving constant relative error
// across six orders of magnitude rather than constant absolute error, which is
// what latency data needs: 8% of 2ms and 8% of 2s are both useful.
//
// A percentile is reported as its bucket's upper bound, so the error is
// one-sided and never understates latency. That makes the bucket width the
// error bound directly: 256 buckets over nine orders of magnitude gives a
// growth ratio of about 1.084, i.e. at most 8.4% over the true value. Halving
// the count would double that error, and doubling it buys precision nobody
// reads off a latency chart.
const (
	// histogramBuckets covers roughly 1µs to 1000s.
	histogramBuckets = 256
	// minDurationNanos is the low edge of bucket 1; anything faster lands in
	// bucket 0.
	minDurationNanos = 1000
	// maxDurationNanos is the high edge; anything slower saturates the last
	// bucket.
	maxDurationNanos = 1e12
)

// histogramGrowth is the ratio between consecutive bucket boundaries, solving
// growth^buckets = max/min.
var histogramGrowth = math.Pow(maxDurationNanos/minDurationNanos, 1.0/float64(histogramBuckets))

// logGrowth is precomputed because BucketIndex runs once per ingested span.
var logGrowth = math.Log(histogramGrowth)

// BucketIndex maps a duration in nanoseconds to its histogram bucket.
//
// Bucket 0 holds everything below one microsecond, which in practice means
// instrumentation overhead rather than real work.
func BucketIndex(durationNanos int64) int16 {
	if durationNanos <= minDurationNanos {
		return 0
	}
	if durationNanos >= maxDurationNanos {
		return histogramBuckets - 1
	}
	idx := int(math.Log(float64(durationNanos)/minDurationNanos) / logGrowth)
	if idx < 0 {
		return 0
	}
	if idx >= histogramBuckets {
		return histogramBuckets - 1
	}
	return int16(idx)
}

// BucketUpperBound returns the highest duration a bucket can hold, in
// nanoseconds. A percentile is reported as the upper bound of the bucket it
// falls in, which never understates latency.
func BucketUpperBound(index int16) int64 {
	if index < 0 {
		return minDurationNanos
	}
	if index >= histogramBuckets-1 {
		return maxDurationNanos
	}
	return int64(minDurationNanos * math.Pow(histogramGrowth, float64(index+1)))
}

// BucketMidpoint returns the geometric centre of a bucket, which is the better
// estimate when a single representative value is needed.
func BucketMidpoint(index int16) int64 {
	lower := float64(minDurationNanos) * math.Pow(histogramGrowth, float64(index))
	upper := float64(minDurationNanos) * math.Pow(histogramGrowth, float64(index+1))
	return int64(math.Sqrt(lower * upper))
}

// Histogram is a bucket-count pair list, as read back from the rollup table.
type Histogram struct {
	// Counts is indexed by bucket.
	Counts [histogramBuckets]int64
	// Total is the sum of Counts, cached because every quantile call needs it.
	Total int64
}

// Add records count observations in a bucket.
func (h *Histogram) Add(bucket int16, count int64) {
	if bucket < 0 || int(bucket) >= histogramBuckets {
		return
	}
	h.Counts[bucket] += count
	h.Total += count
}

// Merge folds another histogram into this one. Merging by addition is the whole
// reason for this representation.
func (h *Histogram) Merge(other *Histogram) {
	for i := range h.Counts {
		h.Counts[i] += other.Counts[i]
	}
	h.Total += other.Total
}

// Quantile returns the requested quantile in nanoseconds, or zero when the
// histogram is empty.
//
// The result is the upper bound of the bucket containing the quantile, so it
// is accurate to the bucket width and never reports a latency lower than the
// true value.
func (h *Histogram) Quantile(q float64) int64 {
	if h.Total == 0 {
		return 0
	}
	if q <= 0 {
		return BucketUpperBound(h.firstNonEmpty())
	}
	if q >= 1 {
		return BucketUpperBound(h.lastNonEmpty())
	}

	// Rank is 1-based: the q-th value in ascending order.
	target := int64(math.Ceil(q * float64(h.Total)))
	var cumulative int64
	for i := range h.Counts {
		cumulative += h.Counts[i]
		if cumulative >= target {
			return BucketUpperBound(int16(i))
		}
	}
	return BucketUpperBound(h.lastNonEmpty())
}

func (h *Histogram) firstNonEmpty() int16 {
	for i := range h.Counts {
		if h.Counts[i] > 0 {
			return int16(i)
		}
	}
	return 0
}

func (h *Histogram) lastNonEmpty() int16 {
	for i := len(h.Counts) - 1; i >= 0; i-- {
		if h.Counts[i] > 0 {
			return int16(i)
		}
	}
	return 0
}
