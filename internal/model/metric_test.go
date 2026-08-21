package model

import (
	"encoding/json"
	"math"
	"testing"
)

func TestBucketRoundTripsAnUnboundedBound(t *testing.T) {
	// The last bucket of an OTLP histogram is unbounded. JSON cannot spell
	// infinity, and encoding it fails outright — which took down every metric
	// batched alongside the histogram until buckets were encoded this way.
	original := []Bucket{
		{UpperBound: 0.01, Count: 6},
		{UpperBound: 0.1, Count: 3},
		{UpperBound: math.Inf(1), Count: 1},
	}

	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("encoding a histogram with an unbounded bucket failed: %v", err)
	}

	var decoded []Bucket
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded) != len(original) {
		t.Fatalf("got %d buckets, want %d", len(decoded), len(original))
	}
	for i := range original {
		if decoded[i].Count != original[i].Count {
			t.Errorf("bucket %d count = %d, want %d", i, decoded[i].Count, original[i].Count)
		}
	}
	if !decoded[2].IsUnbounded() {
		t.Error("the overflow bucket lost its unbounded bound in the round trip")
	}
	if decoded[0].UpperBound != 0.01 {
		t.Errorf("finite bound = %v, want 0.01", decoded[0].UpperBound)
	}
}

func TestEveryMetricWithAHistogramIsEncodable(t *testing.T) {
	// A whole metric, as the writer serialises it: if this fails, the batch it
	// belongs to fails with it.
	metric := &Metric{
		Name: "jvm.gc.duration",
		Kind: KindHistogram,
		Buckets: []Bucket{
			{UpperBound: 0.001, Count: 4},
			{UpperBound: math.Inf(1), Count: 2},
		},
	}
	if _, err := json.Marshal(metric.Buckets); err != nil {
		t.Fatalf("a realistic GC histogram could not be encoded: %v", err)
	}
}
