package ebpf

import "testing"

func TestParseKernelVersion(t *testing.T) {
	tests := []struct {
		release string
		major   int
		minor   int
		ok      bool
	}{
		{"6.12.54-linuxkit", 6, 12, true},
		{"4.18.0-513.el8.x86_64", 4, 18, true},
		{"5.17.0", 5, 17, true},
		{"5.14.0-362.el9.aarch64", 5, 14, true},
		{"6.1.0+deb12", 6, 1, true},
		{"", 0, 0, false},
		{"not-a-version", 0, 0, false},
		{"6", 0, 0, false},
	}

	for _, tt := range tests {
		major, minor, ok := parseKernelVersion(tt.release)
		if ok != tt.ok || (ok && (major != tt.major || minor != tt.minor)) {
			t.Errorf("parseKernelVersion(%q) = %d, %d, %v; want %d, %d, %v",
				tt.release, major, minor, ok, tt.major, tt.minor, tt.ok)
		}
	}
}

// The two thresholds are separate capabilities. A kernel between them measures
// services but cannot join their traces to the ones they call — the case that
// looks like a bug unless it is reported distinctly.
func TestKernelThresholds(t *testing.T) {
	tests := []struct {
		release     string
		spans       bool
		propagation bool
	}{
		{"4.18.0-513.el8.x86_64", false, false}, // RHEL/Rocky 8
		{"5.7.0", false, false},
		{"5.8.0", true, false},
		{"5.14.0-362.el9.aarch64", true, false}, // RHEL/Rocky 9 baseline
		{"5.17.0", true, true},
		{"6.12.54-linuxkit", true, true},
	}

	for _, tt := range tests {
		major, minor, ok := parseKernelVersion(tt.release)
		if !ok {
			t.Fatalf("fixture %q did not parse", tt.release)
		}
		spans := atLeast(major, minor, minKernelMajor, minKernelMinor)
		propagation := atLeast(major, minor, propagationKernelMajor, propagationKernelMinor)
		if spans != tt.spans || propagation != tt.propagation {
			t.Errorf("kernel %s: spans=%v propagation=%v; want %v and %v",
				tt.release, spans, propagation, tt.spans, tt.propagation)
		}
	}
}

// Detect must answer on every platform, including one where eBPF cannot exist.
func TestDetectAlwaysAnswers(t *testing.T) {
	c := Detect()
	if !c.Embedded && c.Reason == "" {
		t.Error("a build without eBPF gave no reason for it")
	}
	if !c.Spans && c.Reason == "" {
		t.Error("instrumentation is unavailable but nothing explains why")
	}
	if c.Spans && c.Reason != "" {
		t.Errorf("instrumentation is available but a reason was given: %q", c.Reason)
	}
}
