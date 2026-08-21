package jvmdiag

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ClassHistogram is a parsed GC.class_histogram result: how many live instances
// of each class there are and what they weigh.
type ClassHistogram struct {
	Classes []ClassCount `json:"classes"`
	// TotalInstances and TotalBytes are the JVM's own totals, which cover every
	// class including the ones trimmed from Classes.
	TotalInstances int64 `json:"total_instances"`
	TotalBytes     int64 `json:"total_bytes"`
	// ClassCount is how many distinct classes the JVM reported, so a trimmed
	// list still says how much was left out.
	ClassCount int `json:"class_count"`
}

// ClassCount is one row of the histogram.
type ClassCount struct {
	Rank      int    `json:"rank"`
	Name      string `json:"name"`
	Instances int64  `json:"instances"`
	Bytes     int64  `json:"bytes"`
}

// histogramRowRe matches "   1:        123456       12345678  [B (java.base@21)".
// The module suffix is dropped: it is noise in a memory table, and keeping it
// would make the same class from two JVM versions look like two classes when
// dumps are compared.
var histogramRowRe = regexp.MustCompile(`^\s*(\d+):\s+(\d+)\s+(\d+)\s+(\S+)`)

// histogramTotalRe matches the trailing "Total    999999   99999999" line.
var histogramTotalRe = regexp.MustCompile(`^Total\s+(\d+)\s+(\d+)`)

// ParseClassHistogram turns GC.class_histogram output into structure. Like the
// thread dump parser it does not fail: an unparsed row is skipped, not fatal.
func ParseClassHistogram(raw string) *ClassHistogram {
	h := &ClassHistogram{}

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")

		if m := histogramTotalRe.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			h.TotalInstances, _ = strconv.ParseInt(m[1], 10, 64)
			h.TotalBytes, _ = strconv.ParseInt(m[2], 10, 64)
			continue
		}

		m := histogramRowRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		rank, _ := strconv.Atoi(m[1])
		instances, _ := strconv.ParseInt(m[2], 10, 64)
		bytes, _ := strconv.ParseInt(m[3], 10, 64)
		h.Classes = append(h.Classes, ClassCount{
			Rank:      rank,
			Name:      m[4],
			Instances: instances,
			Bytes:     bytes,
		})
	}

	h.ClassCount = len(h.Classes)
	// A JVM that reported no total still deserves one; summing the rows is
	// exact when every row parsed and close enough when one did not.
	if h.TotalBytes == 0 {
		for _, c := range h.Classes {
			h.TotalInstances += c.Instances
			h.TotalBytes += c.Bytes
		}
	}
	return h
}

// Top returns a copy holding only the n heaviest classes. The totals and the
// class count are preserved, so the trimmed histogram still reports the whole
// heap even though it lists part of it.
//
// This is what gets stored: the tail of a histogram is thousands of classes
// holding a few hundred bytes each, and keeping it would grow the database
// faster than the telemetry it sits next to.
func (h *ClassHistogram) Top(n int) *ClassHistogram {
	if h == nil {
		return nil
	}
	out := *h
	if n > 0 && len(h.Classes) > n {
		out.Classes = append([]ClassCount(nil), h.Classes[:n]...)
	} else {
		out.Classes = append([]ClassCount(nil), h.Classes...)
	}
	return &out
}

// ClassDelta is one class's change between two histograms.
type ClassDelta struct {
	Name string `json:"name"`
	// Instances and Bytes are the later dump's values; the Delta fields are the
	// change from the earlier one. A class absent from the earlier dump has a
	// delta equal to its whole size.
	Instances      int64 `json:"instances"`
	Bytes          int64 `json:"bytes"`
	InstancesDelta int64 `json:"instances_delta"`
	BytesDelta     int64 `json:"bytes_delta"`
}

// HistogramDiff is the change between two histograms of the same JVM.
type HistogramDiff struct {
	// Growth lists classes that gained bytes, heaviest gain first — the view
	// that answers "what is leaking".
	Growth []ClassDelta `json:"growth"`
	// Shrink lists classes that lost bytes, largest loss first. A leak hunt
	// needs both: memory that moved is not memory that leaked.
	Shrink          []ClassDelta `json:"shrink"`
	TotalBytesDelta int64        `json:"total_bytes_delta"`
}

// Diff compares two histograms taken from the same JVM at different times.
//
// Comparing a histogram against one from a *different* process is meaningless,
// so callers are responsible for pairing dumps from one pid; nothing here can
// detect the mistake.
func Diff(earlier, later *ClassHistogram, limit int) *HistogramDiff {
	if earlier == nil || later == nil {
		return nil
	}

	before := make(map[string]ClassCount, len(earlier.Classes))
	for _, c := range earlier.Classes {
		before[c.Name] = c
	}

	deltas := make([]ClassDelta, 0, len(later.Classes))
	for _, c := range later.Classes {
		prev := before[c.Name]
		deltas = append(deltas, ClassDelta{
			Name:           c.Name,
			Instances:      c.Instances,
			Bytes:          c.Bytes,
			InstancesDelta: c.Instances - prev.Instances,
			BytesDelta:     c.Bytes - prev.Bytes,
		})
		delete(before, c.Name)
	}
	// Whatever is left was in the earlier dump and is gone from the later one.
	for _, c := range before {
		deltas = append(deltas, ClassDelta{
			Name:           c.Name,
			InstancesDelta: -c.Instances,
			BytesDelta:     -c.Bytes,
		})
	}

	sort.Slice(deltas, func(i, j int) bool {
		if deltas[i].BytesDelta != deltas[j].BytesDelta {
			return deltas[i].BytesDelta > deltas[j].BytesDelta
		}
		return deltas[i].Name < deltas[j].Name
	})

	diff := &HistogramDiff{TotalBytesDelta: later.TotalBytes - earlier.TotalBytes}
	for _, d := range deltas {
		switch {
		case d.BytesDelta > 0 && (limit <= 0 || len(diff.Growth) < limit):
			diff.Growth = append(diff.Growth, d)
		case d.BytesDelta < 0:
			diff.Shrink = append(diff.Shrink, d)
		}
	}
	// Shrink was built from a descending sort, so its largest losses are last.
	sort.Slice(diff.Shrink, func(i, j int) bool {
		if diff.Shrink[i].BytesDelta != diff.Shrink[j].BytesDelta {
			return diff.Shrink[i].BytesDelta < diff.Shrink[j].BytesDelta
		}
		return diff.Shrink[i].Name < diff.Shrink[j].Name
	})
	if limit > 0 && len(diff.Shrink) > limit {
		diff.Shrink = diff.Shrink[:limit]
	}
	return diff
}
