package jvmdiag

import (
	"regexp"
	"strconv"
	"strings"
)

// HeapInfo is a parsed GC.heap_info result.
//
// It is a snapshot taken on request, and complements rather than replaces the
// heap metrics streaming in from the agent: the metrics say how the heap moved
// over time, this says exactly how it is laid out right now, including the
// metaspace figures the runtime instrumentation does not break out.
type HeapInfo struct {
	// Collector is the heap's own name in the JVM's words, such as
	// "garbage-first heap" or "PSYoungGen".
	Collector  string `json:"collector,omitempty"`
	TotalBytes int64  `json:"total_bytes,omitempty"`
	UsedBytes  int64  `json:"used_bytes,omitempty"`

	// Metaspace and ClassSpace are reported separately by every collector, and
	// are where a class-loader leak shows up while the heap looks healthy.
	Metaspace  *Pool `json:"metaspace,omitempty"`
	ClassSpace *Pool `json:"class_space,omitempty"`

	// Regions are the generation-level spaces the collector printed: eden,
	// survivor and old for the generational collectors, and the young/survivor
	// region counts for G1.
	Regions []Region `json:"regions,omitempty"`
}

// Pool is a used/committed/reserved triple.
type Pool struct {
	UsedBytes      int64 `json:"used_bytes"`
	CommittedBytes int64 `json:"committed_bytes,omitempty"`
	ReservedBytes  int64 `json:"reserved_bytes,omitempty"`
}

// Region is one named space within the heap.
type Region struct {
	Name        string  `json:"name"`
	TotalBytes  int64   `json:"total_bytes,omitempty"`
	UsedPercent float64 `json:"used_percent,omitempty"`
}

var (
	// heapLineRe matches "garbage-first heap   total 262144K, used 76800K [...".
	heapLineRe = regexp.MustCompile(`^(\S.*?)\s+total\s+(\d+)([KMG]?),\s+used\s+(\d+)([KMG]?)`)
	// poolLineRe matches "Metaspace  used 45678K, committed 46208K, reserved 1114112K".
	poolLineRe = regexp.MustCompile(`^(Metaspace|class space)\s+used\s+(\d+)([KMG]?),\s+committed\s+(\d+)([KMG]?),\s+reserved\s+(\d+)([KMG]?)`)
	// regionLineRe matches "eden space 65536K, 15% used [0x...".
	regionLineRe = regexp.MustCompile(`^(\w[\w ]*?)\s+space\s+(\d+)([KMG]?),\s+(\d+)%\s+used`)
)

// ParseHeapInfo turns GC.heap_info output into structure.
//
// Every collector prints a different shape and new ones keep appearing, so an
// unrecognised line is skipped rather than treated as an error; the raw text
// travels alongside for exactly this reason.
func ParseHeapInfo(raw string) *HeapInfo {
	info := &HeapInfo{}

	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if trimmed == "" {
			continue
		}

		if m := poolLineRe.FindStringSubmatch(trimmed); m != nil {
			pool := &Pool{
				UsedBytes:      scaleSize(m[2], m[3]),
				CommittedBytes: scaleSize(m[4], m[5]),
				ReservedBytes:  scaleSize(m[6], m[7]),
			}
			if m[1] == "Metaspace" {
				info.Metaspace = pool
			} else {
				info.ClassSpace = pool
			}
			continue
		}

		if m := regionLineRe.FindStringSubmatch(trimmed); m != nil {
			percent, _ := strconv.ParseFloat(m[4], 64)
			info.Regions = append(info.Regions, Region{
				Name:        strings.TrimSpace(m[1]),
				TotalBytes:  scaleSize(m[2], m[3]),
				UsedPercent: percent,
			})
			continue
		}

		// The heap's own line comes first; later "total ... used ..." lines
		// belong to individual generations, which Regions already covers.
		if info.TotalBytes == 0 {
			if m := heapLineRe.FindStringSubmatch(trimmed); m != nil {
				info.Collector = strings.TrimSpace(m[1])
				info.TotalBytes = scaleSize(m[2], m[3])
				info.UsedBytes = scaleSize(m[4], m[5])
			}
		}
	}
	return info
}

// scaleSize converts the JVM's "262144K" style figures to bytes.
func scaleSize(digits, suffix string) int64 {
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0
	}
	switch suffix {
	case "K":
		return n * 1024
	case "M":
		return n * 1024 * 1024
	case "G":
		return n * 1024 * 1024 * 1024
	default:
		return n
	}
}
