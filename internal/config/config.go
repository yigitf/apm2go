// Package config defines the apm2go configuration schema and its defaults.
//
// Configuration resolves in three layers, last one wins:
//  1. built-in defaults (Default)
//  2. YAML file (/etc/apm2go/config.yaml by default)
//  3. APM2GO_* environment variables
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Mode selects which subsystems run in this process. Phase 3 splits agent and
// server across hosts; today "all" is the only mode a normal install uses.
type Mode string

const (
	// ModeAll runs discovery, attach, ingest, storage and UI in one process.
	ModeAll Mode = "all"
	// ModeAgent runs only discovery/attach and forwards OTLP to a remote server.
	ModeAgent Mode = "agent"
	// ModeServer runs only ingest, storage and UI.
	ModeServer Mode = "server"
)

// Config is the root configuration document.
type Config struct {
	Mode        Mode              `yaml:"mode"`
	DataDir     string            `yaml:"data_dir"`
	Log         LogConfig         `yaml:"log"`
	Discovery   DiscoveryConfig   `yaml:"discovery"`
	Attach      AttachConfig      `yaml:"attach"`
	Receiver    ReceiverConfig    `yaml:"receiver"`
	Pipeline    PipelineConfig    `yaml:"pipeline"`
	Storage     StorageConfig     `yaml:"storage"`
	HostMetrics HostMetricsConfig `yaml:"host_metrics"`
	EBPF        EBPFConfig        `yaml:"ebpf"`
	API         APIConfig         `yaml:"api"`
	// Forward is only consulted in ModeAgent.
	Forward ForwardConfig `yaml:"forward"`
}

// LogConfig controls the structured logger.
type LogConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // text, json
}

// DiscoveryConfig controls how JVM processes are found on the host.
type DiscoveryConfig struct {
	Enabled  bool          `yaml:"enabled"`
	Interval time.Duration `yaml:"interval"`
	// ProcRoot is overridable so tests can point at a fixture tree.
	ProcRoot string `yaml:"proc_root"`
	// Include, when non-empty, keeps only processes whose command line or
	// derived service name matches one of these substrings.
	Include []string `yaml:"include"`
	// Exclude drops matching processes and is applied after Include.
	Exclude []string `yaml:"exclude"`
	// MinUptime avoids attaching to a JVM that is still booting.
	MinUptime time.Duration `yaml:"min_uptime"`
	// DockerSocket is read, never written, to turn container ids into names,
	// images and orchestration labels. Left empty the default path is tried;
	// if it is absent apm2go falls back to what the cgroup path alone reveals.
	DockerSocket string `yaml:"docker_socket"`
}

// AttachConfig controls dynamic agent injection into discovered JVMs.
type AttachConfig struct {
	// AutoAttach injects the agent as soon as a JVM is discovered. When false
	// attaching is a manual action from the UI or CLI.
	AutoAttach bool `yaml:"auto_attach"`
	// Timeout bounds a single attach handshake.
	Timeout time.Duration `yaml:"timeout"`
	// RetryBackoff is the wait before retrying a failed attach.
	RetryBackoff time.Duration `yaml:"retry_backoff"`
	// MaxRetries caps attach attempts per process before giving up.
	MaxRetries int `yaml:"max_retries"`
	// SampleRatio is the head sampling probability handed to the OTel agent.
	SampleRatio float64 `yaml:"sample_ratio"`
	// OTLPEndpoint is the collector address as seen *from the target JVM*.
	// It normally points at our own receiver over loopback.
	OTLPEndpoint string `yaml:"otlp_endpoint"`
	// ContainerOTLPEndpoint overrides OTLPEndpoint for JVMs in their own
	// network namespace, where loopback does not reach us. Left empty, such
	// JVMs are attached anyway and flagged as unable to export. JVMs that
	// merely have their own mount namespace are unaffected: they still reach
	// apm2go over loopback.
	ContainerOTLPEndpoint string `yaml:"container_otlp_endpoint"`
	// AgentLogging maps to otel.javaagent.logging: "none" keeps the target's
	// own logs clean, "simple" is useful while diagnosing an attach.
	AgentLogging string `yaml:"agent_logging"`
	// MetricsEnabled asks instrumented JVMs to report their runtime metrics —
	// heap, GC, threads, class loading — alongside traces.
	MetricsEnabled bool `yaml:"metrics_enabled"`
	// MetricsInterval is how often they report. Shorter intervals sharpen a
	// GC chart and cost proportionally more storage.
	MetricsInterval time.Duration `yaml:"metrics_interval"`
	// ContainerBind mirrors ReceiverConfig.ContainerBind. The injector reads it
	// to explain why an otherwise reachable container's traces will not arrive;
	// it is filled in by Load rather than configured here.
	ContainerBind ContainerBind `yaml:"-"`
	// RequireToken mirrors ReceiverConfig.RequireToken, so the injector can tell
	// whether a missing credential actually costs anything. Filled in by Load.
	RequireToken bool `yaml:"-"`
	// ExtraProperties are passed through to the injected agent as OTel system
	// properties, e.g. {"otel.instrumentation.jdbc.enabled": "true"}.
	ExtraProperties map[string]string `yaml:"extra_properties"`
}

// ContainerBind decides which addresses the OTLP receiver listens on, and so
// whether containerized applications can reach it at all.
type ContainerBind string

const (
	// ContainerBindOff listens on the configured addresses only. Applications
	// outside apm2go's network namespace cannot export to it.
	ContainerBindOff ContainerBind = "off"
	// ContainerBindAuto additionally listens on the gateway addresses of the
	// networks discovered containers are attached to. Those are addresses this
	// host already owns, so this opens ingest to those container networks and
	// nothing else.
	ContainerBindAuto ContainerBind = "auto"
	// ContainerBindAll listens on every interface.
	ContainerBindAll ContainerBind = "all"
)

// ReceiverConfig controls the OTLP ingest endpoints.
type ReceiverConfig struct {
	GRPCAddr string `yaml:"grpc_addr"`
	HTTPAddr string `yaml:"http_addr"`
	// MaxRecvMsgBytes bounds a single OTLP payload.
	MaxRecvMsgBytes int `yaml:"max_recv_msg_bytes"`
	// ContainerBind controls whether containers can reach the receiver.
	ContainerBind ContainerBind `yaml:"container_bind"`
	// RequireToken rejects spans that do not carry the per-process token
	// apm2go injected. It matters once the receiver listens on a container
	// network, where any container on that bridge could otherwise write spans.
	RequireToken bool `yaml:"require_token"`
}

// PipelineConfig controls normalization and backpressure between the receiver
// and the store.
type PipelineConfig struct {
	// QueueSize is the number of spans buffered before the receiver sheds load.
	QueueSize int `yaml:"queue_size"`
	// MaxSpansPerSecond is a global ingest ceiling; 0 disables the limit.
	MaxSpansPerSecond int `yaml:"max_spans_per_second"`
	// BatchSize and BatchTimeout decide when a batch is flushed to the store.
	BatchSize    int           `yaml:"batch_size"`
	BatchTimeout time.Duration `yaml:"batch_timeout"`
	// MaxAttrBytes truncates oversized attribute values such as SQL statements.
	MaxAttrBytes int `yaml:"max_attr_bytes"`
	// MaxServices and MaxOperations cap cardinality; overflow lands in "__other__".
	MaxServices   int `yaml:"max_services"`
	MaxOperations int `yaml:"max_operations"`
}

// StorageConfig controls the embedded DuckDB store.
type StorageConfig struct {
	// Path is relative to DataDir unless absolute.
	Path string `yaml:"path"`
	// SpanRetention bounds how long raw spans are kept.
	SpanRetention time.Duration `yaml:"span_retention"`
	// RollupRetention bounds how long aggregated metrics are kept.
	RollupRetention time.Duration `yaml:"rollup_retention"`
	// MaintenanceInterval is how often retention and rollups run.
	MaintenanceInterval time.Duration `yaml:"maintenance_interval"`
	// MetricRetention bounds how long raw metric points are kept. They are far
	// smaller than spans and worth keeping longer.
	MetricRetention time.Duration `yaml:"metric_retention"`
	// DiagnosticRetention bounds how long thread dumps and heap histograms are
	// kept. They are collected only when an operator asks, so there are few of
	// them, but each is large; keeping them well past the spans they explain is
	// what lets two dumps from different days be compared.
	DiagnosticRetention time.Duration `yaml:"diagnostic_retention"`
	// MaxDiagnosticsPerJVM caps how many dumps of one kind are kept per
	// process, oldest dropped first. A retention window alone is not enough:
	// an operator debugging a leak can take dozens of histograms in an hour.
	MaxDiagnosticsPerJVM int `yaml:"max_diagnostics_per_jvm"`
	// MemoryLimit is handed to DuckDB's memory_limit pragma.
	MemoryLimit string `yaml:"memory_limit"`
	// Threads is handed to DuckDB's threads pragma; 0 lets DuckDB decide.
	Threads int `yaml:"threads"`
}

// HostMetricsConfig controls measurement of the machine itself.
type HostMetricsConfig struct {
	// Enabled turns host measurement on. It costs one sample per interval and
	// is what distinguishes "this service got slower" from "this box is full".
	Enabled bool `yaml:"enabled"`
	// Interval is how often the host is sampled.
	Interval time.Duration `yaml:"interval"`
}

// EBPFConfig controls instrumenting non-Java processes via the embedded OBI
// binary.
//
// This is a separate off switch from HostMetrics or Discovery for a reason
// that matters beyond convenience: loading eBPF programs needs CAP_BPF and
// CAP_PERFMON held continuously, which is a wider privilege than anything else
// apm2go holds — attaching to a JVM re-executes as that JVM's own user instead
// of using elevated credentials at all. An operator who has decided that
// trade-off is not worth it for their host needs one flag that reliably turns
// it off, not a side effect of disabling something else.
type EBPFConfig struct {
	// Enabled turns on discovery and instrumentation of non-Java processes.
	// Java instrumentation does not depend on this in either direction: it is
	// unaffected whether this is on, off, or unsupported by the running kernel.
	Enabled bool `yaml:"enabled"`
	// Metrics additionally asks OBI for RED and runtime metrics per process,
	// alongside the traces Enabled already produces on its own.
	Metrics bool `yaml:"metrics"`
	// Interval is how often apm2go rescans for new non-Java processes.
	Interval time.Duration `yaml:"interval"`
}

// APIConfig controls the HTTP API and the embedded web UI.
type APIConfig struct {
	Addr string `yaml:"addr"`
	// BasePath allows serving behind a reverse proxy subpath.
	BasePath string `yaml:"base_path"`
	// ReadOnly hides mutating actions such as attach from the UI.
	ReadOnly bool `yaml:"read_only"`
}

// ForwardConfig points an agent-mode process at a remote apm2go server.
type ForwardConfig struct {
	Endpoint string            `yaml:"endpoint"`
	Insecure bool              `yaml:"insecure"`
	Headers  map[string]string `yaml:"headers"`
}

// Default returns a configuration suitable for a single Linux host.
func Default() Config {
	return Config{
		Mode:    ModeAll,
		DataDir: "/var/lib/apm2go",
		Log: LogConfig{
			Level:  "info",
			Format: "text",
		},
		Discovery: DiscoveryConfig{
			Enabled:   true,
			Interval:  3 * time.Second,
			ProcRoot:  "/proc",
			MinUptime: 10 * time.Second,
		},
		Attach: AttachConfig{
			AutoAttach:      true,
			Timeout:         30 * time.Second,
			RetryBackoff:    2 * time.Minute,
			MaxRetries:      3,
			SampleRatio:     1.0,
			OTLPEndpoint:    "http://127.0.0.1:4317",
			AgentLogging:    "none",
			MetricsEnabled:  true,
			MetricsInterval: 15 * time.Second,
			ExtraProperties: map[string]string{},
		},
		Receiver: ReceiverConfig{
			GRPCAddr:        "127.0.0.1:4317",
			HTTPAddr:        "127.0.0.1:4318",
			MaxRecvMsgBytes: 16 << 20,
			// Containers are common enough that discovering them and then
			// refusing their traces would be the surprising default.
			ContainerBind: ContainerBindAuto,
			RequireToken:  true,
		},
		Pipeline: PipelineConfig{
			QueueSize:         50000,
			MaxSpansPerSecond: 20000,
			BatchSize:         5000,
			BatchTimeout:      2 * time.Second,
			MaxAttrBytes:      4096,
			MaxServices:       500,
			MaxOperations:     5000,
		},
		Storage: StorageConfig{
			Path:                "apm2go.duckdb",
			SpanRetention:       72 * time.Hour,
			MetricRetention:     14 * 24 * time.Hour,
			RollupRetention:     30 * 24 * time.Hour,
			MaintenanceInterval: 5 * time.Minute,
			MemoryLimit:         "1GB",
			Threads:             2,

			DiagnosticRetention:  7 * 24 * time.Hour,
			MaxDiagnosticsPerJVM: 20,
		},
		HostMetrics: HostMetricsConfig{
			Enabled:  true,
			Interval: 15 * time.Second,
		},
		EBPF: EBPFConfig{
			// On by default, the same way HostMetrics is: a host that cannot
			// support it degrades to "not available", logged once, not an
			// error. An operator who has decided the CAP_BPF trade-off is not
			// worth it turns this off explicitly rather than apm2go guessing
			// for them.
			Enabled:  true,
			Metrics:  true,
			Interval: 15 * time.Second,
		},
		API: APIConfig{
			Addr:     "0.0.0.0:8080",
			BasePath: "/",
		},
	}
}

// Load reads configuration from path (optional) and then applies APM2GO_*
// environment overrides. A missing file is not an error, so the binary runs
// with no configuration at all.
func Load(path string) (Config, error) {
	cfg := Default()

	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return cfg, fmt.Errorf("parse %s: %w", path, err)
			}
		case os.IsNotExist(err):
			// Defaults are a complete configuration; nothing to do.
		default:
			return cfg, fmt.Errorf("read %s: %w", path, err)
		}
	}

	applyEnv(&cfg)

	// The injector explains reachability failures and needs to know whether the
	// receiver is even listening where a container could reach it.
	cfg.Attach.ContainerBind = cfg.Receiver.ContainerBind
	cfg.Attach.RequireToken = cfg.Receiver.RequireToken

	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// applyEnv maps a small set of high-traffic settings to environment variables,
// which is how the systemd unit and container images are usually tuned.
func applyEnv(cfg *Config) {
	str := func(key string, dst *string) {
		if v, ok := os.LookupEnv(key); ok {
			*dst = v
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := os.LookupEnv(key); ok {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}
	dur := func(key string, dst *time.Duration) {
		if v, ok := os.LookupEnv(key); ok {
			if d, err := time.ParseDuration(v); err == nil {
				*dst = d
			}
		}
	}
	ratio := func(key string, dst *float64) {
		if v, ok := os.LookupEnv(key); ok {
			if f, err := strconv.ParseFloat(v, 64); err == nil {
				*dst = f
			}
		}
	}

	if v, ok := os.LookupEnv("APM2GO_MODE"); ok {
		cfg.Mode = Mode(v)
	}
	str("APM2GO_DATA_DIR", &cfg.DataDir)
	str("APM2GO_LOG_LEVEL", &cfg.Log.Level)
	str("APM2GO_LOG_FORMAT", &cfg.Log.Format)
	boolean("APM2GO_DISCOVERY_ENABLED", &cfg.Discovery.Enabled)
	dur("APM2GO_DISCOVERY_INTERVAL", &cfg.Discovery.Interval)
	str("APM2GO_PROC_ROOT", &cfg.Discovery.ProcRoot)
	boolean("APM2GO_AUTO_ATTACH", &cfg.Attach.AutoAttach)
	boolean("APM2GO_EBPF_ENABLED", &cfg.EBPF.Enabled)
	boolean("APM2GO_EBPF_METRICS", &cfg.EBPF.Metrics)
	dur("APM2GO_EBPF_INTERVAL", &cfg.EBPF.Interval)
	ratio("APM2GO_SAMPLE_RATIO", &cfg.Attach.SampleRatio)
	str("APM2GO_OTLP_GRPC_ADDR", &cfg.Receiver.GRPCAddr)
	str("APM2GO_OTLP_HTTP_ADDR", &cfg.Receiver.HTTPAddr)
	str("APM2GO_API_ADDR", &cfg.API.Addr)
	if v, ok := os.LookupEnv("APM2GO_CONTAINER_BIND"); ok {
		cfg.Receiver.ContainerBind = ContainerBind(v)
	}
	boolean("APM2GO_REQUIRE_TOKEN", &cfg.Receiver.RequireToken)
	dur("APM2GO_SPAN_RETENTION", &cfg.Storage.SpanRetention)
	dur("APM2GO_ROLLUP_RETENTION", &cfg.Storage.RollupRetention)
	str("APM2GO_FORWARD_ENDPOINT", &cfg.Forward.Endpoint)
}

// Validate rejects configurations that would fail later in confusing ways.
func (c *Config) Validate() error {
	switch c.Mode {
	case ModeAll, ModeAgent, ModeServer:
	default:
		return fmt.Errorf("invalid mode %q (want all, agent or server)", c.Mode)
	}
	if c.Mode == ModeAgent && c.Forward.Endpoint == "" {
		return fmt.Errorf("mode=agent requires forward.endpoint")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data_dir must not be empty")
	}
	if c.Attach.SampleRatio < 0 || c.Attach.SampleRatio > 1 {
		return fmt.Errorf("attach.sample_ratio %v out of range [0,1]", c.Attach.SampleRatio)
	}
	if c.Discovery.Interval <= 0 {
		return fmt.Errorf("discovery.interval must be positive")
	}
	if c.Pipeline.BatchSize <= 0 {
		return fmt.Errorf("pipeline.batch_size must be positive")
	}
	if c.Pipeline.QueueSize < c.Pipeline.BatchSize {
		return fmt.Errorf("pipeline.queue_size must be >= batch_size")
	}
	switch c.Receiver.ContainerBind {
	case ContainerBindOff, ContainerBindAuto, ContainerBindAll:
	default:
		return fmt.Errorf("invalid receiver.container_bind %q (want off, auto or all)", c.Receiver.ContainerBind)
	}
	switch strings.ToLower(c.Log.Format) {
	case "text", "json":
	default:
		return fmt.Errorf("invalid log.format %q (want text or json)", c.Log.Format)
	}
	return nil
}

// RunsDiscovery reports whether this mode owns the host-side subsystems.
func (c *Config) RunsDiscovery() bool { return c.Mode == ModeAll || c.Mode == ModeAgent }

// RunsStorage reports whether this mode owns storage and the UI.
func (c *Config) RunsStorage() bool { return c.Mode == ModeAll || c.Mode == ModeServer }
