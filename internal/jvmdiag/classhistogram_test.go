package jvmdiag

import "testing"

const classHistogram = ` num     #instances         #bytes  class name (module)
-------------------------------------------------------
   1:        120000       12800000  [B (java.base@21.0.12)
   2:         90000        2160000  java.lang.String (java.base@21.0.12)
   3:          5000        1200000  com.example.OrderCache$Entry
   4:           100           4800  java.util.HashMap (java.base@21.0.12)
Total       215100       16164800
`

func TestParseClassHistogram(t *testing.T) {
	h := ParseClassHistogram(classHistogram)

	if len(h.Classes) != 4 {
		t.Fatalf("parsed %d classes, want 4: %+v", len(h.Classes), h.Classes)
	}
	if h.ClassCount != 4 {
		t.Errorf("ClassCount = %d, want 4", h.ClassCount)
	}

	first := h.Classes[0]
	if first.Rank != 1 || first.Name != "[B" || first.Instances != 120000 || first.Bytes != 12800000 {
		t.Errorf("first row = %+v", first)
	}
	// The module suffix must not become part of the name, or the same class
	// from two JVM versions compares as two different classes.
	if h.Classes[1].Name != "java.lang.String" {
		t.Errorf("second row name = %q, want the class without its module", h.Classes[1].Name)
	}
	if h.TotalInstances != 215100 || h.TotalBytes != 16164800 {
		t.Errorf("totals = %d instances / %d bytes", h.TotalInstances, h.TotalBytes)
	}
}

// A JVM that printed no Total line still has one; it is the sum of its rows.
func TestParseClassHistogramWithoutTotalLine(t *testing.T) {
	const raw = `   1:        10        100  java.lang.String
   2:         5         50  [B
`
	h := ParseClassHistogram(raw)
	if h.TotalInstances != 15 || h.TotalBytes != 150 {
		t.Errorf("derived totals = %d / %d, want 15 / 150", h.TotalInstances, h.TotalBytes)
	}
}

// Trimming the tail must not change what the histogram says about the heap.
func TestClassHistogramTopKeepsTotals(t *testing.T) {
	full := ParseClassHistogram(classHistogram)
	top := full.Top(2)

	if len(top.Classes) != 2 {
		t.Fatalf("kept %d classes, want 2", len(top.Classes))
	}
	if top.TotalBytes != full.TotalBytes || top.TotalInstances != full.TotalInstances {
		t.Errorf("totals changed: %d/%d, want %d/%d",
			top.TotalInstances, top.TotalBytes, full.TotalInstances, full.TotalBytes)
	}
	if top.ClassCount != 4 {
		t.Errorf("ClassCount = %d, want 4 so the trimming is visible", top.ClassCount)
	}
	// Trimming must copy, not alias: the caller still holds the full histogram.
	if len(full.Classes) != 4 {
		t.Errorf("Top mutated the source histogram: %d classes left", len(full.Classes))
	}
}

func TestDiffFindsGrowthAndShrink(t *testing.T) {
	earlier := ParseClassHistogram(classHistogram)
	const later = ` num     #instances         #bytes  class name (module)
-------------------------------------------------------
   1:        120000       12800000  [B (java.base@21.0.12)
   2:         50000        1200000  java.lang.String (java.base@21.0.12)
   3:        900000       90000000  com.example.OrderCache$Entry
   4:           200           9600  java.util.concurrent.ConcurrentHashMap$Node (java.base@21.0.12)
Total      1070200      104009600
`

	diff := Diff(earlier, ParseClassHistogram(later), 10)
	if diff == nil {
		t.Fatal("Diff returned nil for two valid histograms")
	}

	if len(diff.Growth) == 0 {
		t.Fatal("no growth reported")
	}
	top := diff.Growth[0]
	if top.Name != "com.example.OrderCache$Entry" {
		t.Errorf("largest gain = %q, want the cache entry", top.Name)
	}
	if top.BytesDelta != 90000000-1200000 {
		t.Errorf("BytesDelta = %d, want %d", top.BytesDelta, 90000000-1200000)
	}

	// A class that only appears in the later dump counts as all growth.
	if !containsClass(diff.Growth, "java.util.concurrent.ConcurrentHashMap$Node") {
		t.Error("a class new in the later dump was not reported as growth")
	}
	// A class that vanished counts as all shrink, and one that merely got
	// smaller belongs there too.
	if !containsClass(diff.Shrink, "java.lang.String") {
		t.Error("a class that lost bytes was not reported as shrink")
	}
	if !containsClass(diff.Shrink, "java.util.HashMap") {
		t.Error("a class absent from the later dump was not reported as shrink")
	}
	if diff.TotalBytesDelta != 104009600-16164800 {
		t.Errorf("TotalBytesDelta = %d", diff.TotalBytesDelta)
	}
}

func TestDiffNilOperand(t *testing.T) {
	h := ParseClassHistogram(classHistogram)
	if Diff(nil, h, 10) != nil || Diff(h, nil, 10) != nil {
		t.Error("Diff with a missing histogram should return nil, not a diff against zero")
	}
}

func containsClass(deltas []ClassDelta, name string) bool {
	for _, d := range deltas {
		if d.Name == name {
			return true
		}
	}
	return false
}
