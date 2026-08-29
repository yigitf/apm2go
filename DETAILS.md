# apm2go

A standalone APM for Linux hosts. Install it, and it finds the Java processes
already running on the machine, instruments them **without restarting them**,
stores their traces in an embedded database, and serves a web interface over the
result. One binary, no JDK, no database, no separate collector.

```bash
sudo rpm -i apm2go-0.1.0-1.x86_64.rpm
sudo systemctl enable --now apm2go
# open http://<host>:8080
```

Or as a container, which finds the host's processes and every other
container's alongside them:

```bash
docker run -d --name apm2go \
  --pid=host --network=host --privileged \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v apm2go-data:/var/lib/apm2go \
  ghcr.io/yigitf/apm2go:latest
```

Both deployments are interchangeable and share a data directory: pointing the
container at `/var/lib/apm2go` from an existing package install carries its
ingest credentials across, so applications already instrumented by the service
keep reporting without being restarted.

## Why this exists

Every open-source Java APM asks for something the situation rarely allows.
Glowroot is standalone and embedded — but its agent has to be on the command
line before the JVM starts. SkyWalking and Pinpoint want Elasticsearch or HBase.
SigNoz and Uptrace want ClickHouse and a compose file. The commercial agents
attach to a live process, which is the part that actually matters when a service
is misbehaving right now and cannot be restarted — but they are closed and tied
to a vendor's backend.

apm2go is the intersection: dynamic attach, zero dependencies, one file.

## How it works

```
discovery ──→ inventory ──→ attach (HotSpot protocol, pure Go)
/proc scan     state machine    │
                                ↓
                    ┌───────────────────────────┐
                    │ Target JVM — keeps running│
                    │  apm2go-bootstrap.jar     │  applies configuration
                    │  opentelemetry-javaagent  │  does the instrumenting
                    └──────────┬────────────────┘
                               │ OTLP → 127.0.0.1:4317
   receiver ──→ pipeline ──→ store (DuckDB) ──→ API + web UI :8080
```

**Discovery** reads `/proc`, confirming a JVM by its mapped `libjvm.so` rather
than its process name, and parses HotSpot's `hsperfdata` file for the exact Java
version and VM arguments. Service names come from `otel.service.name`, then
`spring.application.name`, then the jar name, then the systemd unit.

**Attach** speaks HotSpot's dynamic attach protocol directly — no JDK, no
`jattach` binary. Because the JVM checks the peer credentials of its attach
socket and accepts only its own user, apm2go re-executes itself as that user for
the handshake rather than holding elevated credentials in the daemon.

Two agents are loaded in sequence: a ~3KB bootstrap jar that writes apm2go's
configuration into the target's system properties, then the OpenTelemetry agent,
which reads them during its own initialisation. Keeping the steps separate means
apm2go depends on no OpenTelemetry internals and survives agent upgrades.

**Storage** is DuckDB. Latency is stored as a logarithmic histogram bucket per
span, so percentiles over any time range are computed by summing bucket counts —
percentiles cannot be averaged, and storing a precomputed p95 per minute would
make every multi-minute chart wrong.

## Containers

Attaching across a container boundary was never the hard part: `/proc/<pid>/root`
gives the target's own filesystem view, so the agent jars stage correctly and the
handshake works unchanged. The hard part is the return path. A containerized
application told to export to `127.0.0.1` is exporting into its own loopback,
where nothing is listening.

apm2go resolves the address each process can actually reach it at, by reading
that process's routing table from `/proc/<pid>/net/route` — a file that is
namespace-aware, so opening it from the host shows the container's routes, not
the reader's. The default route's gateway is an address the host owns, and
apm2go binds ingest there as containers are discovered, extending it to exactly
the container networks in use rather than to every interface.

Listening on a bridge means every container on it can reach ingest, so each
instrumented process is issued a token, delivered in the configuration the
injector already writes, and exports without one are refused. An APM that can be
fed fabricated spans is worse than one missing data: the fabrication is
indistinguishable from a measurement.

Container ids are turned into names, images and orchestration labels through the
Docker socket, mounted read-only and used for nothing else. Where no socket is
readable, the cgroup path still yields the container id and, under Kubernetes,
the pod UID.

**Where apm2go runs matters for reachability.** Binding a container gateway
requires being in the host's network namespace: as a systemd service that is
automatic, and as a container it means `--network=host`. Without it, apm2go
still discovers and instruments everything, but only processes sharing its own
network can export to it — which the UI states per process rather than leaving
you to infer from missing data.

## The one honest limitation

Not everything survives attaching to a process that is already running. The
distinction that decides it is **where the instrumentation hooks**. Most of
it advises methods called on *every request* — `Servlet.service()`,
`FilterChain.doFilter()`, JDBC `Statement.execute()`, HTTP client `send()` — and
retransforming those already-loaded classes takes effect on the very next
request. That covers real Java services: Spring Boot, Tomcat, Jetty, Undertow,
JAX-RS and everything built on them.

What does not work is instrumentation that hooks a call an application makes
once, at start-up. The clearest case is the JDK's own
`com.sun.net.httpserver.HttpServer`: OpenTelemetry instruments it by advising
`createContext`, which installs a tracing filter as the server is being wired
up. A server that was already listening before apm2go attached will never call
`createContext` again, so it is never instrumented — and without a server span
there is nothing to continue an incoming trace into, so cross-service traces
break at that hop. The same shape applies to any client object built once and
held for the process's lifetime.

apm2go says so in the UI rather than hiding it. The gap closes itself the next
time the process restarts for any reason — a deploy, a crash, an orchestrator
cycling the pod — because apm2go rediscovers and re-attaches automatically.
Nothing has to be configured by hand for that to happen, on this restart or the
next one.

## Beyond Java

Java is attached to. Everything else is watched from the kernel, through
OpenTelemetry's eBPF instrumentation, which apm2go ships and supervises as a
child process. Discovery is the part apm2go does itself: it scans `/proc` for
interpreters (Node.js, Python, PHP-FPM), for Go binaries — recognised by their
build metadata, since every compiled binary has a different name — and for the
web servers in front of them, nginx and Apache httpd. Each one is named from
what it is running, not from the interpreter every service of that language
shares, and handed over with the ports that identify it.

A web server needs two things the others do not. It is a master with a pool of
workers that all hold the same listening socket, so it is reported once, under
the master — the only pid that survives a reload. And it binds more than one
port as a matter of course, so all of them are instrumented; a port that two
servers both listen on identifies neither and is dropped from both, because the
kernel-level selector cannot see that they are in different containers.

What this yields is not identical everywhere, and apm2go's tests assert the
differences rather than averaging over them. A request entering a Node.js,
Python or Go service comes out as one trace across everything it touched. A
request entering a web server does not, dependably: nginx never carries the
trace context to the backend, and Apache httpd carries it for some requests and
then stops, so the backend starts a trace of its own. Both are still traced for
their own inbound request and their outbound call, so a proxy's latency and the
fact that it called the backend are visible either way. PHP has the same gap.
All three are limits of the eBPF layer's context propagation rather than of
discovery, and they are said here rather than left to be found in a chart that
looks complete.

None of this needs the process restarted, and none of it needs anything
installed beside it.

## Metrics

Instrumented JVMs report their runtime state — heap by memory pool, CPU, class
loading — alongside traces, on the same OTLP connection. apm2go measures the
host itself too: CPU, memory, filesystems, network and load average. The two
share a time axis with the traces, which is the point: a service that got slower
and a machine that ran out of memory look identical in a latency chart, and are
told apart by putting them side by side.

Counters are stored as the cumulative totals the source reports and differenced
at query time, so a chart shows a rate rather than a line that only ever climbs,
and a process restarting shows as a gap rather than a cliff.

One agent setting is deliberately left alone:
`runtime-telemetry-java17.enable-all` switches the agent to a JFR-based
implementation that does not initialise under a runtime attach. Measured on
Java 21, turning it on replaced every JVM instrument with nothing but the
agent's own internal counters, so apm2go stays on the JMX-based default, which
reports memory by pool, GC duration, threads, CPU and class loading.

## Commands

```bash
apm2go run                  # the service; what systemd starts
apm2go list                 # what JVMs are on this host, and can they be attached
apm2go attach <pid>         # instrument one process now
apm2go version
```

`apm2go list` is the first thing to run when something is missing — it reports
each JVM's version, owner and whether apm2go considers it attachable, without
touching anything.

## Configuration

`/etc/apm2go/config.yaml`, fully commented, every value at its default. apm2go
runs correctly with that file empty or absent. The settings reached for most
often:

| Setting | Default | What it is for |
|---|---|---|
| `attach.auto_attach` | `true` | Set false to make attaching a deliberate action |
| `attach.sample_ratio` | `1.0` | Lower this first if apm2go costs more than you want |
| `discovery.include` / `exclude` | `[]` | Substring filters over service name, command line and unit |
| `storage.span_retention` | `72h` | Raw spans — what a waterfall is drawn from |
| `storage.rollup_retention` | `720h` | Aggregates — what long-range charts read |
| `api.addr` | `0.0.0.0:8080` | The web interface |

The Settings page shows what apm2go is doing with these: spans accepted, spans
dropped, queue depth, and how long the last write took. If traces stop arriving,
that page distinguishes "nothing is being sent" from "apm2go is shedding load".

## Installing

Container image or RPM/DEB, both covered in **[INSTALL.md](INSTALL.md)**:
what each `docker run` flag is for, what the package puts where, how to narrow
the container's privileges, and what to check when something is missing.

## Requirements

- Linux, glibc 2.25 or newer (RHEL/Rocky/Alma 8+, Ubuntu 18.04+, Debian 10+)
- Root, which the attach protocol requires in order to act as each JVM's own user
- Java 8 or newer in the processes being monitored

## Building

Docker is the only requirement: the web UI, both agent jars, the OBI binary and
the attach helper are all built or fetched inside `build/Dockerfile.build`, so
no Go toolchain, Node.js or JDK is needed on the build host.

```bash
make deb           # the .deb, and only the .deb
make rpm           # the .rpm, and only the .rpm
make image         # the container image
make test          # gofmt, go vet and the unit tests, in a container
```

The packages are built for x86-64 and that is not a knob: apm2go is installed on
servers, and an arm64 RPM would be a second artefact to build, publish and answer
questions about for no one who exists. The container image is the exception,
since trying apm2go out often happens on an arm64 laptop — `make image` follows
the host and `ARCH=amd64` or `ARCH=arm64` overrides it.

Each deliverable is made of one Linux binary, `dist/apm2go_$ARCH`, produced by
whichever target needs it.
