<p align="center">
  <img src="logo_md.png" alt="apm2go" width="360">
</p>

<p align="center">
  <a href="https://apm2go.fatihyigit.com"><strong>apm2go.fatihyigit.com</strong></a>
</p>

<p align="center">
  <a href="LICENSE"><img alt="License: Apache 2.0" src="https://img.shields.io/badge/license-Apache--2.0-blue.svg"></a>
  <img alt="Platform: Linux" src="https://img.shields.io/badge/platform-linux%20x86--64-lightgrey.svg">
  <img alt="Language: Go" src="https://img.shields.io/badge/go-1.26-00ADD8.svg">
</p>

# apm2go

apm2go is a single-file APM (application performance monitoring) tool that
starts watching applications that are **already running on Linux, without
restarting them**. Install it, and it finds the Java services on the machine
by itself, attaches to them while they're live, stores traces and metrics in
its own embedded database, and shows the results through a web interface.
No separate database, no separate collector, no JDK to install — everything
is inside one binary.

Beyond Java, it also watches Node.js, Python, Go, and the web servers in
front of them (nginx, Apache) at the kernel level (eBPF), with no code
changes required.

## Why this exists

Most open-source Java APM tools ask for something you rarely have in real
life: either the agent has to be on the command line before the JVM starts,
or they want a heavy backend like Elasticsearch/ClickHouse, or they're
commercial and closed-source. apm2go targets the intersection of these:
it can attach to an already-running process, has zero external dependencies,
and is one file.

## Installing

Three ways, all the same binary. The packages are x86-64, since the hosts apm2go
gets installed on are servers; the container image also comes in arm64, since
trying apm2go out often happens on an arm64 laptop.

Replace `v0.1.0` below with the newest tag on the
[releases page](https://github.com/yigitf/apm2go/releases).

### 1. RHEL / Rocky / Alma / Fedora

```bash
sudo dnf install https://github.com/yigitf/apm2go/releases/download/v0.1.0/apm2go-0.1.0-1.x86_64.rpm
```

### 2. Debian / Ubuntu

```bash
curl -LO https://github.com/yigitf/apm2go/releases/download/v0.1.0/apm2go_0.1.0_amd64.deb
sudo apt install ./apm2go_0.1.0_amd64.deb
```

### After either package

Installing the package doesn't start the service automatically — you can first
run `sudo apm2go list` to see what apm2go finds on the host, then start it with:

```bash
sudo systemctl enable --now apm2go
```

The web interface is on `http://<host>:8080`.

### 3. Docker

```bash
docker pull ghcr.io/yigitf/apm2go:latest
```

One tag serves both x86-64 and arm64. `--pid=host --network=host` are needed so
it can see both the host's processes and the ones in other containers:

```bash
docker run -d --name apm2go \
  --pid=host --network=host --privileged \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v apm2go-data:/var/lib/apm2go \
  ghcr.io/yigitf/apm2go:latest
```

Then open `http://<host>:8080`.

For the details of the package and container installs, why each permission is
needed, and troubleshooting steps, see [INSTALL.md](INSTALL.md). To build any of
the three yourself instead, see [CONTRIBUTING.md](CONTRIBUTING.md#before-you-build)
— it needs nothing but Docker.

## Requirements

On the machine being monitored:

- Linux, glibc 2.25 or newer (RHEL/Rocky/Alma 8+, Ubuntu 18.04+, Debian 10+)
- x86-64 for the packages; the container image is built for x86-64 or arm64
- Root (attaching to a JVM requires becoming that JVM's own user)
- Java 8 or newer in the processes being monitored
- Kernel 5.8+ with BTF, only for watching non-Java processes; Java works on
  anything

Nothing is needed on the machine you install *from* — the packages and the image
are prebuilt. Building them yourself needs Docker and nothing else.

## How it works

```
discovery ──→ inventory ──→ attach (HotSpot protocol)
/proc scan     state machine    │
                                ↓
                    target JVM — keeps running
                                │ OTLP
receiver ──→ pipeline ──→ store (DuckDB) ──→ API + web UI :8080
```

apm2go scans `/proc` to find the JVMs already running, and by speaking
HotSpot's attach protocol directly (no JDK or `jattach` needed) loads two
small agents into that process: one writes the configuration, the other
(the OpenTelemetry agent) does the actual instrumenting. The process never
stops, never restarts.

Services outside Java, and web servers, are watched by a kernel-level, eBPF-
based subprocess — again with no code changes or restarts.

## Commands

```bash
apm2go run                  # runs the service; what systemd starts
apm2go list                 # lists the JVMs on this host and whether they can be attached to
apm2go attach <pid>         # instruments one process right now
apm2go version
```

## Building it yourself

Docker is the only requirement: the web UI, both agent jars, the eBPF binary and
the attach helper are all built or fetched inside the build container, so there
is no Go toolchain, Node.js or JDK to install.

```bash
git clone https://github.com/yigitf/apm2go.git
cd apm2go

make            # lists every target
make rpm        # the .rpm, and only the .rpm
make deb        # the .deb, and only the .deb
make image      # the container image; ARCH=amd64 or ARCH=arm64
make test       # gofmt, go vet and the unit tests, in a container
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for how to report a bug usefully and what
a good pull request looks like.

## More detail

For a more technical writeup of the architecture, container network
resolution, eBPF limits and metric storage, see [DETAILS.md](DETAILS.md).
The project site is at [apm2go.fatihyigit.com](https://apm2go.fatihyigit.com).

## Security

apm2go runs as root and attaches to processes it did not start. To report a
vulnerability, and for what is in and out of scope, see
[SECURITY.md](SECURITY.md).

## Licence

Apache License 2.0 — see [LICENSE](LICENSE). The bundled third-party
components and their licences are listed in [NOTICE](NOTICE).
