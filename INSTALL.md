# Installing apm2go

Two supported ways: the **container image**, or an **RPM / DEB package** with a
systemd unit. Both ship the same single binary — the Java agent jars, the eBPF
instrumentation and the web UI are inside it. There is nothing else to install:
no JDK, no database, no collector.

Pick the package if apm2go should watch a whole host. Pick the container if you
are trying it out, or if the host is managed by something that would rather not
have a package installed on it.

## Getting the artefacts

Everything is prebuilt. The packages are release assets, and the image is on the
GitHub container registry:

- **Packages:** the [releases page](https://github.com/yigitf/apm2go/releases)
  carries one `.rpm` and one `.deb` per version, x86-64.
- **Image:** `ghcr.io/yigitf/apm2go:latest`, one tag serving both x86-64 and
  arm64. Version tags such as `ghcr.io/yigitf/apm2go:v0.1.0` pin a release.

To build them yourself instead, see
[CONTRIBUTING.md](CONTRIBUTING.md#before-you-build); it needs nothing but Docker.

## Before you start

- **Linux**, glibc 2.25 or newer (RHEL/Rocky/Alma 8+, Ubuntu 18.04+, Debian 10+).
- **Root.** Attaching to a JVM means connecting to its attach socket as that
  JVM's own user, which means being able to become any user on the host. The
  attach itself is done by a short-lived child process with the target's
  credentials, never by the long-running one.
- **Kernel 5.8+ with BTF**, only for watching non-Java processes. Java works on
  anything. Kernel 5.17+ additionally lets a non-Java service's trace join the
  services it calls; below that they are measured but their traces stand alone.
  apm2go works this out at start-up and says so in its log, rather than leaving
  you to infer it from an empty chart:

  ```bash
  journalctl -u apm2go | grep -i ebpf     # or: docker logs apm2go | grep -i ebpf
  ```

apm2go does not need the applications restarted, reconfigured, or rebuilt. It
also does not need to be installed before them.

---

## Container

```bash
docker run -d --name apm2go \
  --pid=host --network=host --privileged \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v apm2go-data:/var/lib/apm2go \
  ghcr.io/yigitf/apm2go:latest
```

Then open **http://\<host\>:8080**.

Every flag is load-bearing:

| Flag | Why |
| --- | --- |
| `--pid=host` | Discovery reads `/proc`. Without it apm2go sees only its own container and finds nothing. |
| `--network=host` | Applications in other containers export telemetry to apm2go across their bridge gateway. Without this apm2go is reachable only from inside its own network, and their telemetry goes nowhere. |
| `--privileged` | Loading eBPF programs, and attaching as another user. Can be narrowed — see below. |
| `-v /var/run/docker.sock:ro` | Turns container ids into names. Optional; without it containers show as ids. |
| `-v apm2go-data` | Keeps traces, and the ingest tokens, across restarts. Optional but wanted: see the note on tokens. |

### Narrowing `--privileged`

`--privileged` is the easy setting, not the required one. This set is measured
to work:

```bash
--cap-drop=ALL \
--cap-add=SETUID --cap-add=SETGID --cap-add=KILL \
--cap-add=DAC_READ_SEARCH --cap-add=SYS_PTRACE \
--cap-add=BPF --cap-add=PERFMON --cap-add=NET_ADMIN --cap-add=NET_RAW \
--cap-add=SYS_RESOURCE --cap-add=CHECKPOINT_RESTORE
```

The first three lines are the Java side. The last two are eBPF, and dropping
them **fails silently**: apm2go starts, the instrumentation subprocess starts,
and not one span is ever produced for a Node.js, Python, Go or web-server
process, with no error anywhere. If non-Java services are missing from a
working install, check this first.

### Java only

If nothing on the host is worth watching except JVMs, drop the eBPF
capabilities and turn the feature off, so the log says it is disabled rather
than leaving you to wonder why nothing appears:

```bash
-e APM2GO_EBPF_ENABLED=false
```

---

## RPM and DEB

```bash
# Red Hat, Rocky, Alma, Fedora
sudo dnf install ./apm2go-<version>.<arch>.rpm

# Debian, Ubuntu
sudo apt install ./apm2go_<version>_<arch>.deb
```

Installing does **not** start the service. That is deliberate: apm2go begins
instrumenting live processes as soon as it runs, and you should look at the
configuration before it does.

```bash
sudo apm2go list                        # what it would find, without touching anything
sudoedit /etc/apm2go/config.yaml        # optional
sudo systemctl enable --now apm2go      # start it, and on boot
```

Then open **http://\<host\>:8080**.

### What the package puts where

| Path | What |
| --- | --- |
| `/usr/bin/apm2go` | The binary. Everything is inside it. |
| `/etc/apm2go/config.yaml` | Configuration. Marked `noreplace`: an upgrade never overwrites your edits. |
| `/var/lib/apm2go/` | The database, the staged agent jars, and the ingest tokens. |
| `/usr/lib/systemd/system/apm2go.service` | The unit. |
| `journalctl -u apm2go` | Logs. |

### What the unit already does

It runs as root, because it has to, and then gives most of it back: a capability
bounding set instead of full root, a read-only filesystem apart from its own
state directory, and ceilings of 1 GB of memory and 50% of one CPU so apm2go can
never be the reason an application it is watching gets starved.

`/proc` is deliberately left fully visible. A `hidepid` or `ProcSubset=pid`
setting would make discovery blind to exactly the processes it exists to find.

### Upgrading

```bash
sudo dnf upgrade ./apm2go-<newer>.<arch>.rpm
```

A running service is restarted; a stopped one is left stopped. Your config
survives. **JVMs already instrumented stay instrumented** and keep reporting
across the restart — apm2go persists their ingest tokens beside the database
precisely so that upgrading itself is not the disruption it exists to avoid.
This is also why the container form wants a volume on `/var/lib/apm2go`: without
one, a restarted container has forgotten the tokens, and every JVM instrumented
by the previous run has its telemetry refused until that JVM itself restarts.

### Removing

```bash
sudo dnf remove apm2go       # or: sudo apt remove apm2go
sudo rm -rf /var/lib/apm2go  # only if you want the traces gone too
```

Removing apm2go does not un-instrument anything. A JVM it attached to keeps its
agent until that process restarts, and simply has nowhere to send telemetry.
Nothing crashes; the spans are dropped at the socket.

---

## Running on a different port

The web UI and API listen on `api.addr`, `0.0.0.0:8080` by default. There are
two ways to change it, and which one applies depends on how apm2go is running.

**Package (systemd).** Edit `/etc/apm2go/config.yaml`:

```yaml
api:
  addr: 0.0.0.0:9090
```

```bash
sudo systemctl restart apm2go
```

**Container.** Set the `APM2GO_API_ADDR` environment variable instead of
editing the config file — every setting in `config.yaml` has an environment
variable equivalent, and env vars are simpler to pass through `docker run`:

```bash
docker run -d --name apm2go \
  --pid=host --network=host --privileged \
  -e APM2GO_API_ADDR=0.0.0.0:9090 \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v apm2go-data:/var/lib/apm2go \
  ghcr.io/yigitf/apm2go:latest
```

Then open **http://\<host\>:9090**. There is no need to publish the port with
`-p`: the container already runs with `--network=host`, so whatever port
apm2go binds is reachable directly on the host.

If you are running the container *without* `--network=host` (only Java on
the same host is being watched, for example), `-p` works normally instead:

```bash
docker run -d --name apm2go \
  -p 9090:9090 \
  -e APM2GO_API_ADDR=0.0.0.0:9090 \
  ...
```

`receiver.grpc_addr` and `receiver.http_addr` (where instrumented
applications send telemetry, `127.0.0.1:4317`/`:4318` by default) are
separate settings and are not usually worth changing — they only need to be
reachable from processes on the same host, since containerized applications
already reach apm2go through `receiver.container_bind` rather than through
this address.

## Setting a retention (time) limit

apm2go keeps three kinds of data on separate clocks, each with its own
default, under `storage:` in `config.yaml`:

| Setting | Default | What it bounds |
| --- | --- | --- |
| `span_retention` | `72h` | Raw spans — what a trace waterfall is drawn from. The largest of the three, deliberately kept short. |
| `metric_retention` | `336h` (14 days) | Raw JVM and host metric points. |
| `rollup_retention` | `720h` (30 days) | Aggregated metrics — what long-range charts read after the raw points age out. |

A background job (`storage.maintenance_interval`, every 5 minutes by default)
deletes anything older than these on a rolling basis — there is no separate
"cleanup" command to run. To keep a week of traces instead of three days:

```yaml
storage:
  span_retention: 168h   # 7d also works; apm2go accepts Go duration syntax
```

```bash
sudo systemctl restart apm2go
```

Or as environment variables, which cover `span_retention` and
`rollup_retention` (`metric_retention` is config-file only):

```bash
-e APM2GO_SPAN_RETENTION=168h
-e APM2GO_ROLLUP_RETENTION=1440h
```

Retention only bounds how long data is *kept* — it has no effect on what is
*collected*. To reduce the volume being written in the first place (and so
make a given retention window cheaper), lower `attach.sample_ratio` instead;
see the Settings page in the UI for what apm2go is doing with its current
limits before changing either.

---

## Checking it worked

```bash
sudo apm2go list                                  # JVMs found on this host
curl -s localhost:8080/api/v1/health              # the service is up
curl -s localhost:8080/api/v1/jvms | head         # what it did about each one
```

In the UI, the **JVMs** page is the one to open when a service is missing from
the trace views: every process apm2go found is listed there with what it decided
and why, including the ones it skipped.

## When something is missing

**No JVMs found.** apm2go ignores processes younger than `min_uptime`
(10 seconds by default) so it does not attach to something still starting.
Check `discovery.exclude` too.

**A JVM is listed but not attached.** The JVMs page gives the reason per
process. The most common one is a JVM already carrying another agent, which
apm2go refuses to instrument twice.

**Java works, nothing else does.** Almost always the eBPF capabilities — see
the container section above. `journalctl -u apm2go | grep -i ebpf` says what
this host supports and, where something is unavailable, why.

Read that output carefully: a missing *capability* is not one of the reasons it
can give. apm2go checks that it is running as root, which under systemd it is;
the bounding set only bites later, inside the instrumentation subprocess, which
does not complain. So the signature of this particular fault is an eBPF log line
that looks perfectly healthy next to services that never appear.

**A containerized JVM is attached but reports nothing.** It is exporting to its
own loopback rather than to apm2go. Run apm2go with `--network=host` and leave
`receiver.container_bind: auto` on, which is what binds the container gateway
addresses those processes can actually reach.

**Everything stopped reporting after an apm2go restart.** The ingest tokens were
lost — a container without a persistent `/var/lib/apm2go` volume. The JVMs page
says so per process.
