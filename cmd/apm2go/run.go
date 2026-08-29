package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/yigitf/apm2go/internal/api"
	"github.com/yigitf/apm2go/internal/app"
	"github.com/yigitf/apm2go/internal/assets"
	"github.com/yigitf/apm2go/internal/attachhelper"
	"github.com/yigitf/apm2go/internal/container"
	"github.com/yigitf/apm2go/internal/ebpf"
	"github.com/yigitf/apm2go/internal/hostmetrics"
	"github.com/yigitf/apm2go/internal/ingesttoken"
	"github.com/yigitf/apm2go/internal/injector"
	"github.com/yigitf/apm2go/internal/inventory"
	"github.com/yigitf/apm2go/internal/jvmdiag"
	"github.com/yigitf/apm2go/internal/pipeline"
	"github.com/yigitf/apm2go/internal/procmetrics"
	"github.com/yigitf/apm2go/internal/receiver"
	"github.com/yigitf/apm2go/internal/store"
	"github.com/yigitf/apm2go/internal/version"
)

func newRunCmd(gf *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run the apm2go service",
		Long: "Run the apm2go service: discover JVMs on this host, instrument them without " +
			"a restart, ingest their traces and serve the web UI.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, log, err := gf.load()
			if err != nil {
				return err
			}

			log.Info("starting apm2go", "version", version.Version, "mode", cfg.Mode)

			application, err := app.New(cfg, log)
			if err != nil {
				return err
			}

			// Handlers are built first and populated as each subsystem comes
			// up, so the API can expose whichever of them this mode runs.
			handlers := api.NewHandlers(cfg, log)

			// One registry is shared: the injector issues a token per process,
			// the receiver recognises it. Split instances would reject
			// everything apm2go itself instrumented.
			//
			// It is persisted, not just shared: an already-instrumented JVM is
			// never re-attached on discovery, so if apm2go forgot its token on
			// restart, that JVM's telemetry would be rejected until the JVM
			// itself happened to restart — turning apm2go's own restart into
			// exactly the kind of disruption this tool exists to avoid.
			tokens, err := ingesttoken.NewPersistentRegistry(application.DataPath("ingest-tokens.json"))
			if err != nil {
				return err
			}

			// Storage and ingest come up first: a JVM instrumented before the
			// receiver is listening would spend its first seconds failing to
			// export.
			var gatewayListener inventory.GatewayListener
			if cfg.RunsStorage() {
				db, err := store.Open(cfg.Storage, cfg.DataDir, log)
				if err != nil {
					return err
				}
				defer db.Close()

				ingest := pipeline.New(cfg.Pipeline, db, log)
				otlp := receiver.New(cfg.Receiver, ingest, tokens, log)

				application.Add(ingest)
				application.Add(otlp)
				application.Add(store.NewMaintainer(db, log))

				// The host is measured wherever telemetry is stored, since that
				// is where the traces it explains already live.
				if cfg.HostMetrics.Enabled {
					application.Add(hostmetrics.New(cfg.HostMetrics.Interval, ingest, log))
				}

				handlers.Store = db
				handlers.Pipeline = ingest
				handlers.Receiver = otlp
				gatewayListener = otlp
			}

			if cfg.RunsDiscovery() {
				store, err := assets.New()
				if err != nil {
					return err
				}

				// apm2go-attach-helper does the actual attach handshake; see
				// attach.Runner's own comment for why apm2go cannot do this
				// in-process. Staged once, here, into apm2go's own data
				// directory — not the target's, unlike the jars above, since
				// nothing but apm2go itself ever runs this binary.
				helperStore, err := attachhelper.New()
				if err != nil {
					return err
				}
				if err := helperStore.Verify(); err != nil {
					return fmt.Errorf("attach helper: %w", err)
				}
				helperPath, err := helperStore.Materialize(filepath.Join(cfg.DataDir, "attach-helper"))
				if err != nil {
					return fmt.Errorf("stage attach helper: %w", err)
				}

				inj, err := injector.New(cfg.Attach, cfg.Discovery.ProcRoot, helperPath, store, tokens, log)
				if err != nil {
					return err
				}
				// The receiver is the gateway listener, so discovering a
				// container extends ingest to its network. In agent mode there
				// is no receiver here and the manager gets nil.
				manager := inventory.NewManager(cfg, inj, gatewayListener, log)
				application.Add(manager)
				handlers.Inventory = manager

				// Diagnostics ride the same attach channel as injection, so
				// they are available wherever discovery runs — including a
				// server-less agent, where the dump is returned rather than
				// stored.
				handlers.Diagnostics = jvmdiag.New(cfg.Discovery.ProcRoot, helperPath, log)

				log.Info("JVM discovery enabled",
					"interval", cfg.Discovery.Interval,
					"auto_attach", cfg.Attach.AutoAttach,
					"otel_agent", assets.OtelAgentVersion)

				// eBPF instrumentation needs an OTLP endpoint to send to, which
				// only exists where this process also stores telemetry. The
				// agent/server split this would otherwise also apply to is
				// deliberately out of scope for now (see the development plan);
				// an agent-only host simply does not get non-Java coverage yet.
				if cfg.EBPF.Enabled && cfg.RunsStorage() {
					endpoint, err := otlpEndpoint(cfg.Receiver.GRPCAddr)
					if err != nil {
						return fmt.Errorf("determine OTLP endpoint for eBPF instrumentation: %w", err)
					}
					// One shared credential covers every process OBI ever
					// instruments: the receiver validates a token against what
					// it issued, not against which service claims to send it,
					// so a per-target token would buy nothing an operator could
					// observe.
					token, err := tokens.Issue("ebpf")
					if err != nil {
						return err
					}

					supervisor := ebpf.NewSupervisor(cfg.DataDir, endpoint, token, cfg.EBPF.Metrics, log)
					application.Add(supervisor)

					// CPU, memory and disk I/O for these same processes come
					// from apm2go reading /proc directly, not from OBI: measured
					// directly, none of OBI's metric features produced runtime
					// figures for a non-Java process, only HTTP counters. The
					// discoverer feeds both consumers from one scan.
					procStats := procmetrics.NewCollector(cfg.EBPF.Interval, handlers.Pipeline, log)
					application.Add(procStats)

					// The third consumer of the same scan: what discovery
					// worked out about a process is the only place the runtime
					// of a native web server is ever known. OBI reports a
					// language for the runtimes it can read out of a process's
					// own symbols and nothing usable for nginx or httpd, so
					// without this they arrive with no language at all.
					runtimes := ebpf.NewRegistry()
					handlers.Pipeline.SetRuntimeResolver(runtimes)
					// The same registry answers "what is being watched" for the
					// API. Without it a watched-but-idle service is invisible
					// everywhere, because every other view is built from spans.
					handlers.Processes = runtimes
					handlers.Containers = container.NewResolver(cfg.Discovery.DockerSocket, log)

					// apm2go watches itself the same way it watches any other Go
					// service: its resource use always, and its own HTTP
					// handling too, wherever its API port could be determined.
					//
					// The API is traced but the OTLP receiver deliberately is
					// not — self's Ports below name only the API, never the
					// receiver's. OBI selects purely by port number with no
					// other notion of identity, so a self target that included
					// the receiver's port would trace its own arrival: every
					// span OBI exports about apm2go would itself be a fresh
					// request into a now-traced receiver, generating a span
					// that itself needs exporting, forever. Scoping the port
					// list to the API alone breaks that cycle before it can
					// start — the API is watched, and the export that reports
					// on it is invisible to the watcher, the same way OBI's own
					// process is (see goDaemonDenylist).
					self := ebpf.Target{PID: os.Getpid(), Name: "apm2go", Runtime: ebpf.RuntimeGo}
					apiSink := ebpf.TargetSink(supervisor)
					if port, err := apiPort(cfg.API.Addr); err != nil {
						log.Warn("could not determine apm2go's own API port; its HTTP traffic will not be traced (resource metrics still will be)",
							"addr", cfg.API.Addr, "error", err)
					} else {
						traced := self
						traced.Ports = []int{port}
						apiSink = ebpf.WithSelf(supervisor, traced)
					}

					discoverer := ebpf.NewDiscoverer(cfg.Discovery.ProcRoot, cfg.EBPF.Interval, log,
						apiSink,
						ebpf.WithSelf(procStats, self),
						ebpf.WithSelf(runtimes, self),
					)
					application.Add(discoverer)

					log.Info("eBPF instrumentation enabled",
						"interval", cfg.EBPF.Interval, "metrics", cfg.EBPF.Metrics)
				}
			}

			// The web interface is served wherever the data lives.
			if cfg.RunsStorage() {
				application.Add(api.NewServer(cfg.API, handlers, log))
			}

			// SIGINT and SIGTERM both mean "shut down"; systemd sends the latter.
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer stop()

			if err := application.Run(ctx); err != nil && ctx.Err() == nil {
				return err
			}
			log.Info("apm2go stopped")
			return nil
		},
	}
}

// otlpEndpoint turns the receiver's own listen address into a URL OBI can be
// pointed at.
//
// OBI always runs as apm2go's own child process on this same host, unlike a
// target JVM which may sit across a container boundary — so unlike the
// injector's reachability logic, this never has to reason about namespaces or
// gateways. Loopback always reaches a process's own parent.
func otlpEndpoint(grpcAddr string) (string, error) {
	_, port, err := net.SplitHostPort(grpcAddr)
	if err != nil {
		return "", fmt.Errorf("parse receiver address %q: %w", grpcAddr, err)
	}
	return "http://127.0.0.1:" + port, nil
}

// apiPort extracts the port apm2go's own REST API listens on, as a bare
// number for ebpf.Target.Ports.
func apiPort(addr string) (int, error) {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, fmt.Errorf("parse API address %q: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse API port %q: %w", portStr, err)
	}
	return port, nil
}

// runContext is a small helper so subcommands share signal handling.
func runContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
}
