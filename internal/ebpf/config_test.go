package ebpf

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// context_propagation belongs under a nested ebpf: key. A top-level key of the
// same name is accepted by OBI's parser without error and silently does
// nothing — every trace this package produces would stop joining the ones
// apm2go's own agents produce, with nothing in any log to explain why. This
// test exists because getting that placement wrong once already cost real
// measurement time to diagnose.
func TestRenderConfigNestsContextPropagation(t *testing.T) {
	data, err := renderConfig(nil, "http://127.0.0.1:4317", false)
	if err != nil {
		t.Fatalf("renderConfig: %v", err)
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal rendered config: %v", err)
	}

	if _, ok := doc["context_propagation"]; ok {
		t.Fatal("context_propagation must not appear at the top level; OBI ignores it there")
	}
	ebpfSection, ok := doc["ebpf"].(map[string]any)
	if !ok {
		t.Fatal("no ebpf: section in rendered config")
	}
	if got := ebpfSection["context_propagation"]; got != "all" {
		t.Errorf("ebpf.context_propagation = %v, want %q", got, "all")
	}
}

func TestRenderConfigInstrumentRules(t *testing.T) {
	targets := []Target{
		{Name: "svc-b", Ports: []int{8092}, Runtime: RuntimeNode},
		{Name: "svc-a", Ports: []int{8091}, Runtime: RuntimePython},
		// A web server binding plain HTTP and TLS at once. Both have to reach
		// the selector: instrumenting one of the two would trace a fraction of
		// the traffic and give no sign that the rest was missing.
		{Name: "edge", Ports: []int{80, 443}, Runtime: RuntimeNginx},
	}
	data, err := renderConfig(targets, "http://127.0.0.1:4317", false)
	if err != nil {
		t.Fatalf("renderConfig: %v", err)
	}

	var doc struct {
		Discovery struct {
			Instrument []struct {
				Name      string `yaml:"name"`
				OpenPorts string `yaml:"open_ports"`
			} `yaml:"instrument"`
		} `yaml:"discovery"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	rules := doc.Discovery.Instrument
	if len(rules) != 3 {
		t.Fatalf("got %d rules, want 3", len(rules))
	}
	// Sorted by name, so a config that did not actually change renders
	// byte-identical regardless of the order targets were discovered in.
	want := []struct {
		name  string
		ports string
	}{
		{"edge", "80,443"},
		{"svc-a", "8091"},
		{"svc-b", "8092"},
	}
	for i, w := range want {
		if rules[i].Name != w.name || rules[i].OpenPorts != w.ports {
			t.Errorf("rule %d = %+v, want %s/%s", i, rules[i], w.name, w.ports)
		}
	}
}

func TestRenderConfigDeterministic(t *testing.T) {
	targets := []Target{
		{Name: "b", Ports: []int{2}, Runtime: RuntimeNode},
		{Name: "a", Ports: []int{1}, Runtime: RuntimePython},
	}
	reversed := []Target{targets[1], targets[0]}

	first, err := renderConfig(targets, "http://x:4317", true)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderConfig(reversed, "http://x:4317", true)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("renderConfig is order-sensitive; the same target set in a different discovery order produced different YAML, which would trigger a needless restart")
	}
}

func TestRenderConfigOmitsMetricsWhenDisabled(t *testing.T) {
	data, err := renderConfig(nil, "http://x:4317", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "otel_metrics_export") {
		t.Error("metrics section present despite withMetrics=false")
	}
}

func TestWriteConfigProducesReadableFile(t *testing.T) {
	dir := t.TempDir()
	path, err := writeConfig(dir, []Target{{Name: "svc", Ports: []int{9090}}}, "http://127.0.0.1:4317", false)
	if err != nil {
		t.Fatalf("writeConfig: %v", err)
	}
	if !strings.HasSuffix(path, "obi.yaml") {
		t.Errorf("path = %q, want it to end in obi.yaml", path)
	}
}
