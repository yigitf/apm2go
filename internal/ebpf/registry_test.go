package ebpf

import "testing"

func TestRegistryRemembersRuntimesAcrossScans(t *testing.T) {
	r := NewRegistry()
	r.SetTargets([]Target{
		{Name: "nginx", Runtime: RuntimeNginx, Ports: []int{80}},
		{Name: "api", Runtime: RuntimePython, Ports: []int{8000}},
	})

	if got := r.RuntimeFor("nginx"); got != "nginx" {
		t.Errorf("RuntimeFor(nginx) = %q", got)
	}

	// The process exited, so the next scan does not mention it. Its spans are
	// still in flight, and they still have to be labelled.
	r.SetTargets([]Target{{Name: "api", Runtime: RuntimePython, Ports: []int{8000}}})
	if got := r.RuntimeFor("nginx"); got != "nginx" {
		t.Errorf("after the process exited, RuntimeFor(nginx) = %q, want it remembered", got)
	}
}

func TestRegistryReportsNothingForUnknownService(t *testing.T) {
	r := NewRegistry()
	if got := r.RuntimeFor("never-seen"); got != "" {
		t.Errorf("RuntimeFor(never-seen) = %q, want empty", got)
	}
}

// A target with no runtime must not overwrite one already established: a
// half-classified scan should leave what is known alone.
func TestRegistryIgnoresTargetsWithNoRuntime(t *testing.T) {
	r := NewRegistry()
	r.SetTargets([]Target{{Name: "api", Runtime: RuntimeGo, Ports: []int{9000}}})
	r.SetTargets([]Target{{Name: "api", Ports: []int{9000}}})
	if got := r.RuntimeFor("api"); got != "go" {
		t.Errorf("RuntimeFor(api) = %q, want go", got)
	}
}

// The runtime a service is labelled with can arrive two ways: from its own
// telemetry, normalised by internal/receiver, or from this package when the
// telemetry says nothing. The two must agree, or one service is filed under two
// spellings depending on which path happened to answer first — and the UI,
// which matches these strings exactly to pick a badge, shows a question mark
// for one of them.
func TestRuntimeNamesMatchTheStoredSpellings(t *testing.T) {
	// Keep in step with runtimeAliases in internal/receiver/semconv.go and
	// BADGES in web/src/components/RuntimeBadge.tsx.
	want := map[Runtime]string{
		RuntimeNode:   "nodejs",
		RuntimePython: "python",
		RuntimeGo:     "go",
		RuntimePHP:    "php",
		RuntimeNginx:  "nginx",
		RuntimeHTTPD:  "httpd",
	}
	for got, expected := range want {
		if string(got) != expected {
			t.Errorf("runtime is spelled %q, but telemetry and the UI use %q", got, expected)
		}
	}
}
