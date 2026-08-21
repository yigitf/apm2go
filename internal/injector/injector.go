// Package injector instruments a running JVM: it stages the agent jars where
// the target can read them, applies apm2go's configuration, and loads the
// OpenTelemetry agent — all without restarting the application.
//
// The work is split into two attaches on purpose. The first loads a tiny
// bootstrap agent that writes apm2go's settings into the target's system
// properties; the second loads the OpenTelemetry agent, which reads them during
// its own initialisation. Because each step uses the JVM's ordinary agent
// loading path, apm2go depends on no OpenTelemetry internals.
package injector

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apm2go/apm2go/internal/assets"
	"github.com/apm2go/apm2go/internal/attach"
	"github.com/apm2go/apm2go/internal/config"
	"github.com/apm2go/apm2go/internal/discovery"
	"github.com/apm2go/apm2go/internal/ingesttoken"
)

// stagingDirName is the directory, inside the target's own /tmp, where agent
// jars and per-process configuration are placed.
const stagingDirName = "apm2go"

// markerProperty mirrors BootstrapAgent.MARKER_PROPERTY and is how apm2go
// recognises a JVM it has already instrumented.
const markerProperty = "apm2go.bootstrap.applied"

// Injector instruments JVMs according to the attach configuration.
type Injector struct {
	cfg      config.AttachConfig
	procRoot string
	hostName string
	store    *assets.Store
	runner   *attach.Runner
	tokens   *ingesttoken.Registry
	log      *slog.Logger
}

// New returns an Injector. The asset store is verified up front so a corrupted
// build fails at startup rather than at the first attach.
//
// helperPath is apm2go-attach-helper, already staged to disk — see
// internal/attachhelper. Attaching to a JVM whose container runs it as
// anything but root needs that binary specifically, not apm2go re-executing
// itself; see attach.Runner's own comment for why.
func New(cfg config.AttachConfig, procRoot, helperPath string, store *assets.Store, tokens *ingesttoken.Registry, log *slog.Logger) (*Injector, error) {
	if err := store.Verify(); err != nil {
		return nil, err
	}
	host, err := os.Hostname()
	if err != nil {
		host = "unknown"
	}
	return &Injector{
		cfg:      cfg,
		procRoot: procRoot,
		hostName: host,
		store:    store,
		runner:   attach.NewRunner(helperPath, log),
		tokens:   tokens,
		log:      log,
	}, nil
}

// Result describes the outcome of a successful injection.
type Result struct {
	// AlreadyInstrumented is true when the JVM carried apm2go's marker before
	// this call, in which case nothing was injected.
	AlreadyInstrumented bool
	// BootstrapJar and OtelJar are the paths as the target sees them.
	BootstrapJar string
	OtelJar      string
	// ConfigFile describes how the bootstrap agent received its configuration:
	// either "(inline, N bytes)" when it travelled in the attach option string
	// itself, or the file path as the target sees it, for the rarer case where
	// the configuration did not fit in one attach argument.
	ConfigFile string
	// Endpoint is the OTLP address the target was told to export to.
	Endpoint string
	// Warnings are non-fatal problems worth showing in the UI, such as a
	// containerized JVM that probably cannot reach our receiver.
	Warnings []string
	// Duration is how long the whole injection took.
	Duration time.Duration
}

// Inject instruments a JVM. It is idempotent: a JVM that already carries
// apm2go's marker is left untouched and reported as such.
func (i *Injector) Inject(ctx context.Context, jvm *discovery.JVM) (*Result, error) {
	start := time.Now()

	if ok, reason := jvm.Attachable(); !ok {
		return nil, fmt.Errorf("pid %d is not attachable: %s", jvm.PID, reason)
	}

	base := i.attachOptions(jvm)
	result := &Result{Endpoint: i.endpointFor(jvm)}

	if warning := i.reachabilityWarning(jvm); warning != "" {
		result.Warnings = append(result.Warnings, warning)
	}

	// A JVM instrumented by an earlier apm2go run still carries the marker, and
	// loading the OpenTelemetry agent twice would be at best wasteful.
	if applied, err := i.alreadyInstrumented(ctx, base); err != nil {
		i.log.Debug("could not read target properties before attaching",
			"pid", jvm.PID, "error", err)
	} else if applied {
		result.AlreadyInstrumented = true
		// An agent loaded by a previous apm2go carries that instance's ingest
		// token. If this one has no record of a token for the service, every
		// export it makes is being refused — and since a running JVM's exporter
		// cannot be reconfigured, only restarting it will help.
		if i.cfg.RequireToken && i.tokens != nil && !i.tokens.HasTokenFor(jvm.ServiceName) {
			result.Warnings = append(result.Warnings,
				"this JVM was instrumented by an earlier apm2go whose ingest credentials this one "+
					"does not have, so its telemetry is being refused. Restart the service to "+
					"re-instrument it; nothing about a running JVM's exporter can be changed in place.")
		}
		result.Duration = time.Since(start)
		return result, nil
	}

	stagingHost, stagingTarget := i.stagingPaths(jvm)

	bootstrapHost, otelHost, err := i.store.Materialize(stagingHost)
	if err != nil {
		return nil, fmt.Errorf("stage agent jars for pid %d: %w", jvm.PID, err)
	}
	result.BootstrapJar = filepath.Join(stagingTarget, filepath.Base(bootstrapHost))
	result.OtelJar = filepath.Join(stagingTarget, filepath.Base(otelHost))

	configText, err := i.renderProperties(jvm)
	if err != nil {
		return nil, err
	}

	// Step one: apply configuration. A failure here means the target never
	// received its settings, so there is no point loading the agent.
	bootstrapOpts := base
	bootstrapOpts.JarPath = result.BootstrapJar

	// Inline whenever the configuration fits in a single attach argument, so
	// BootstrapAgent never has to open a file to read it. That is what lets
	// apm2go attach to a JVM running under a SecurityManager that grants no
	// FilePermission to a dynamically loaded agent: Elasticsearch is exactly
	// this case, measured directly — the bootstrap agent loaded and ran, then
	// failed with an AccessControlException the instant it opened the
	// properties file, a failure invisible until the target's own log was
	// read. Inline sidesteps the check by never making the call it applies to.
	if fitsInline(result.BootstrapJar, configText) {
		bootstrapOpts.AgentOptions = configText
		result.ConfigFile = fmt.Sprintf("(inline, %d bytes)", len(configText))
	} else {
		// Too large for one attach argument — a long resource attribute list,
		// a deep staging path, or an operator's own extra_properties. Falls
		// back to a file, which is what apm2go has always used, and which
		// needs the target's filesystem, and under a SecurityManager its
		// permission, to be readable by the JVM's own user.
		configHost, err := i.writeConfigFile(stagingHost, configText, jvm)
		if err != nil {
			return nil, err
		}
		result.ConfigFile = filepath.Join(stagingTarget, filepath.Base(configHost))
		bootstrapOpts.AgentOptions = result.ConfigFile
	}

	if err := i.runner.LoadAgent(ctx, bootstrapOpts); err != nil {
		return nil, fmt.Errorf("apply apm2go configuration to pid %d: %w", jvm.PID, err)
	}
	i.log.Debug("configuration applied", "pid", jvm.PID, "config", result.ConfigFile)

	// Step two: load the OpenTelemetry agent, which reads what we just set.
	otelOpts := base
	otelOpts.JarPath = result.OtelJar
	if err := i.runner.LoadAgent(ctx, otelOpts); err != nil {
		return nil, fmt.Errorf("load OpenTelemetry agent into pid %d: %w", jvm.PID, err)
	}

	result.Duration = time.Since(start)

	i.log.Info("JVM instrumented",
		"pid", jvm.PID,
		"service", jvm.ServiceName,
		"java", jvm.JavaVersion,
		"endpoint", result.Endpoint,
		"took", result.Duration.Round(time.Millisecond))
	return result, nil
}

// attachOptions builds the attach parameters shared by both steps.
func (i *Injector) attachOptions(jvm *discovery.JVM) attach.Options {
	return attach.Options{
		ProcRoot: i.procRoot,
		PID:      jvm.PID,
		NSPid:    jvm.NSPid,
		UID:      jvm.UID,
		GID:      jvm.GID,
		Timeout:  i.cfg.Timeout,
	}
}

// alreadyInstrumented asks the target for its system properties and looks for
// the marker the bootstrap agent sets.
func (i *Injector) alreadyInstrumented(ctx context.Context, opts attach.Options) (bool, error) {
	props, err := attach.Properties(ctx, opts)
	if err != nil {
		return false, err
	}
	_, ok := props[markerProperty]
	return ok, nil
}

// stagingPaths returns the directory to write into, and the same directory as
// the target sees it. For a JVM sharing our mount namespace the two describe
// the same place; for a containerized one they differ, which is exactly why the
// agent options must use the target's view.
func (i *Injector) stagingPaths(jvm *discovery.JVM) (host, target string) {
	host = filepath.Join(i.procRoot, strconv.Itoa(jvm.PID), "root", "tmp", stagingDirName)
	target = filepath.Join("/tmp", stagingDirName)
	return host, target
}

// endpointFor picks the OTLP address to hand to a target.
//
// Reachability is what decides this, not containerization: a JVM that shares
// apm2go's network namespace reaches it over loopback however isolated its
// filesystem is, and a JVM outside that namespace does not, even when apm2go is
// the containerized one.
//
// For an unreachable target the address is the gateway of its own network,
// which on a bridge is an address this host owns. An explicit configuration
// setting overrides the discovered value, for topologies where the gateway is
// not us — a container behind a NAT to another host, say.
func (i *Injector) endpointFor(jvm *discovery.JVM) string {
	if jvm.SharesOurNetwork {
		return i.cfg.OTLPEndpoint
	}
	if i.cfg.ContainerOTLPEndpoint != "" {
		return i.cfg.ContainerOTLPEndpoint
	}
	if jvm.Gateway != "" {
		return rewriteHost(i.cfg.OTLPEndpoint, jvm.Gateway)
	}
	// Nothing better is known. The loopback address will not work, but
	// attaching anyway means the JVM is instrumented the moment an operator
	// fixes the configuration, and the warning says exactly what to fix.
	return i.cfg.OTLPEndpoint
}

// reachabilityWarning explains, in terms an operator can act on, why a target's
// traces may not arrive.
func (i *Injector) reachabilityWarning(jvm *discovery.JVM) string {
	if jvm.SharesOurNetwork {
		return ""
	}
	if i.cfg.ContainerOTLPEndpoint != "" {
		return ""
	}
	if jvm.Gateway != "" {
		// The endpoint was resolved, but only works if apm2go is listening on
		// that address — which is what receiver.container_bind controls.
		if i.cfg.ContainerBind == config.ContainerBindOff {
			return "this JVM is on its own network and reaches this host at " + jvm.Gateway +
				", but apm2go is listening on loopback only. Set receiver.container_bind to \"auto\" " +
				"so it also listens on container gateways, or its traces will never arrive."
		}
		return ""
	}
	return "this JVM is on its own network and no route from it to this host could be determined, " +
		"so its traces will not arrive. Set attach.container_otlp_endpoint to an address reachable " +
		"from inside it."
}

// rewriteHost replaces the host part of an endpoint, keeping its scheme and
// port. The port matters more than the address here: apm2go listens on the same
// port on every address it binds.
func rewriteHost(endpoint, host string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" {
		return endpoint
	}
	if port := parsed.Port(); port != "" {
		parsed.Host = net.JoinHostPort(host, port)
	} else {
		parsed.Host = host
	}
	return parsed.String()
}

// inlineOptionBudget bounds the attach option string apm2go will try to embed
// configuration in. It sits well under HotSpot's own long-stable 1024-byte cap
// per attach argument (AttachOperation::arg_length_max in attachListener.hpp,
// unchanged since JDK 7), deliberately not pushed to the edge: an argument that
// exceeds the real limit does not fail loudly, and a silently truncated
// properties blob — the target starting with half its configuration — is a
// worse failure than the file this falls back to.
const inlineOptionBudget = 900

// fitsInline reports whether a configuration can travel in the same attach
// argument as the bootstrap jar path, "<jarPath>=<configText>" — the shape
// loadAgentRequest sends over the wire.
func fitsInline(jarPath, configText string) bool {
	return len(jarPath)+1+len(configText) <= inlineOptionBudget
}

// renderProperties renders a JVM's configuration as the properties-file text
// BootstrapAgent parses, whether it travels inline in the attach option string
// or, when it does not fit, from a file.
func (i *Injector) renderProperties(jvm *discovery.JVM) (string, error) {
	props, err := i.properties(jvm)
	if err != nil {
		return "", err
	}

	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("# Generated by apm2go for pid ")
	b.WriteString(strconv.Itoa(jvm.PID))
	b.WriteString(" at ")
	b.WriteString(time.Now().Format(time.RFC3339))
	b.WriteString("\n")
	for _, k := range keys {
		b.WriteString(escapeProperty(k))
		b.WriteByte('=')
		b.WriteString(escapeProperty(props[k]))
		b.WriteByte('\n')
	}
	return b.String(), nil
}

// writeConfigFile writes rendered properties text to dir, for the fallback
// path where the configuration does not fit in a single attach argument. It is
// world readable because the target JVM usually runs as a different user than
// apm2go.
func (i *Injector) writeConfigFile(dir, text string, jvm *discovery.JVM) (string, error) {
	path := filepath.Join(dir, fmt.Sprintf("agent-%d.properties", jvm.NSPid))
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("write agent configuration for pid %d: %w", jvm.PID, err)
	}
	// WriteFile's mode is masked by the umask, which commonly strips the read
	// bit the target user needs.
	if err := os.Chmod(path, 0o644); err != nil {
		return "", fmt.Errorf("set permissions on %s: %w", path, err)
	}
	return path, nil
}

// properties builds the OpenTelemetry configuration for one JVM.
func (i *Injector) properties(jvm *discovery.JVM) (map[string]string, error) {
	endpoint := i.endpointFor(jvm)

	props := map[string]string{
		"otel.service.name": jvm.ServiceName,

		// gRPC is the agent's native OTLP transport and the one our receiver
		// listens on by default.
		"otel.exporter.otlp.protocol": "grpc",
		"otel.exporter.otlp.endpoint": endpoint,

		"otel.traces.exporter": "otlp",
		// The agent already knows how to report heap, GC, threads and class
		// loading; turning the exporter on is the whole cost of collecting them.
		"otel.metrics.exporter": i.metricsExporter(),
		// Singular "metric" is what the OpenTelemetry specification defines,
		// and the agent silently ignores anything else — so the plural spelling
		// left every JVM on the 60 second default while appearing to be
		// configured. Measured: with the plural name, the first metrics arrived
		// a minute after attach rather than at the interval asked for.
		"otel.metric.export.interval": strconv.FormatInt(
			i.cfg.MetricsInterval.Milliseconds(), 10),
		"otel.instrumentation.runtime-telemetry.enabled": strconv.FormatBool(i.cfg.MetricsEnabled),
		// Deliberately not setting runtime-telemetry-java17.enable-all. It
		// switches the agent to a JFR-based implementation that does not work
		// under a runtime attach: measured on Java 21, turning it on replaced
		// every JVM instrument with nothing but the agent's own internal
		// counters. The JMX-based default reports memory, CPU and class
		// loading, which is what actually arrives.
		// Logs remain off: apm2go has nowhere to put them yet, and exporting
		// them would cost the target overhead for data nothing consumes.
		"otel.logs.exporter": "none",

		"otel.traces.sampler":     "parentbased_traceidratio",
		"otel.traces.sampler.arg": strconv.FormatFloat(i.cfg.SampleRatio, 'f', -1, 64),

		// Keep the agent quiet inside the customer's application log unless an
		// operator turns it up to diagnose an attach.
		"otel.javaagent.logging": i.agentLogging(),

		"otel.resource.attributes": i.resourceAttributes(jvm),
	}

	// The token is what lets the receiver tell this JVM's telemetry apart from
	// anything else that can reach it once it listens on a container network.
	if i.tokens != nil {
		token, err := i.tokens.Issue(jvm.ServiceName)
		if err != nil {
			return nil, err
		}
		props["otel.exporter.otlp.headers"] = ingesttoken.Header + "=" + token
	}

	// Operator overrides win over every default above.
	for k, v := range i.cfg.ExtraProperties {
		props[k] = v
	}
	return props, nil
}

// metricsExporter turns JVM runtime metrics on or off at the source, so a
// target that is not being charted is not paying to produce the numbers.
func (i *Injector) metricsExporter() string {
	if i.cfg.MetricsEnabled {
		return "otlp"
	}
	return "none"
}

func (i *Injector) agentLogging() string {
	if i.cfg.AgentLogging == "" {
		return "none"
	}
	return i.cfg.AgentLogging
}

// resourceAttributes describes where a trace came from, which is what lets the
// UI group spans by host and process.
func (i *Injector) resourceAttributes(jvm *discovery.JVM) string {
	attrs := []string{
		"host.name=" + i.hostName,
		"process.pid=" + strconv.Itoa(jvm.PID),
		"process.runtime.version=" + jvm.JavaVersion,
		// Marks traces as coming from a runtime attach rather than a permanent
		// -javaagent, which explains any gaps in coverage.
		"apm2go.injected=true",
	}
	if jvm.ContainerID != "" {
		attrs = append(attrs, "container.id="+jvm.ContainerID)
	}
	if jvm.SystemdUnit != "" {
		attrs = append(attrs, "apm2go.systemd.unit="+jvm.SystemdUnit)
	}
	return strings.Join(attrs, ",")
}

// escapeProperty escapes the characters java.util.Properties treats specially,
// so a value containing '=' or ':' survives the round trip.
func escapeProperty(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch r {
		case '\\', '=', ':', '#', '!':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
