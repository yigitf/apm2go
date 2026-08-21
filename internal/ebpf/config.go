package ebpf

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// obiConfig mirrors the slice of OBI's own YAML schema that apm2go drives.
// Field placement was verified empirically against v0.11.0, not assumed from
// its documentation: context propagation lives under a nested `ebpf:` key —
// a top-level `context_propagation:` is accepted by the parser and silently
// does nothing, which cost real time to track down. Get this struct wrong in
// the same way and every trace this package produces stops joining the ones
// apm2go's own agents produce, with no error to say why.
type obiConfig struct {
	EBPF      obiEBPF         `yaml:"ebpf"`
	Discovery obiDiscovery    `yaml:"discovery"`
	Traces    obiOTLPTraces   `yaml:"otel_traces_export"`
	Metrics   *obiOTLPMetrics `yaml:"otel_metrics_export,omitempty"`
	LogLevel  string          `yaml:"log_level"`
}

type obiEBPF struct {
	// "all" enables both the HTTP-header and TCP-option propagation OBI knows;
	// this is the setting a Java agent's traceparent header depends on.
	ContextPropagation string `yaml:"context_propagation"`
}

type obiDiscovery struct {
	Instrument []obiInstrumentRule `yaml:"instrument"`
}

// obiInstrumentRule names one target. OpenPorts is the selector: discovery
// has no pid field, and a glob on the executable path would merge every
// process of one runtime under a single name.
//
// It is a string because OBI's selector accepts a comma-separated list, and a
// web server binding both 80 and 443 needs one. A single port renders as a
// quoted scalar, which OBI's own parser accepts as readily as a bare number —
// verified against the running binary, not inferred from its documentation,
// for the same reason the comment on obiConfig gives.
type obiInstrumentRule struct {
	Name      string `yaml:"name"`
	OpenPorts string `yaml:"open_ports"`
}

// portSelector renders a target's ports the way OBI's open_ports reads them.
func portSelector(ports []int) string {
	parts := make([]string, 0, len(ports))
	for _, port := range ports {
		parts = append(parts, strconv.Itoa(port))
	}
	return strings.Join(parts, ",")
}

type obiOTLPTraces struct {
	Endpoint string `yaml:"endpoint"`
	Protocol string `yaml:"protocol"`
}

type obiOTLPMetrics struct {
	Endpoint string   `yaml:"endpoint"`
	Protocol string   `yaml:"protocol"`
	Interval string   `yaml:"interval"`
	Features []string `yaml:"features"`
}

// metricFeatures are the OBI metric groups apm2go asks for: request-level RED
// metrics and the runtime counters (event loop, GC, memory) that match what
// the JVM side already reports per process.
var metricFeatures = []string{"application", "application_host", "application_runtime"}

// renderConfig builds the YAML OBI reads at start-up for one set of targets.
//
// OBI has no live-reload; the only way to change what it watches is to
// rewrite this file and restart the process, which is why the caller of this
// function is a supervisor that restarts on every target-set change rather
// than something that expects to edit this in place.
func renderConfig(targets []Target, otlpEndpoint string, withMetrics bool) ([]byte, error) {
	cfg := obiConfig{
		EBPF: obiEBPF{ContextPropagation: "all"},
		Traces: obiOTLPTraces{
			Endpoint: otlpEndpoint,
			Protocol: "grpc",
		},
		LogLevel: "info",
	}

	// Sorted so that a target set that has not actually changed produces byte-
	// identical YAML — the supervisor diffs on that to decide whether a
	// disruptive restart is warranted.
	sorted := append([]Target(nil), targets...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	for _, t := range sorted {
		cfg.Discovery.Instrument = append(cfg.Discovery.Instrument, obiInstrumentRule{
			Name:      t.Name,
			OpenPorts: portSelector(t.Ports),
		})
	}

	if withMetrics {
		cfg.Metrics = &obiOTLPMetrics{
			Endpoint: otlpEndpoint,
			Protocol: "grpc",
			Interval: "15s",
			Features: metricFeatures,
		}
	}

	out, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("render OBI config: %w", err)
	}
	return out, nil
}

// writeConfig writes the rendered YAML to dir, replacing any previous config.
func writeConfig(dir string, targets []Target, otlpEndpoint string, withMetrics bool) (string, error) {
	data, err := renderConfig(targets, otlpEndpoint, withMetrics)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, "obi.yaml")
	// OBI reads this once at start-up, so there is no concurrent reader to
	// race against; a plain write is enough.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}
