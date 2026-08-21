<p align="center">
  <img src="logo_md.png" alt="apm2go" width="360">
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

There's no prebuilt package or image distributed yet — you first clone the
repo and build your own package/image. The build happens inside Docker
containers (cross-building for Linux amd64/arm64), so it doesn't matter
whether your build machine is macOS or Linux; the only requirement is having
Docker installed.

You can install it one of three ways below, all using the same binary.

### Docker

```bash
git clone https://github.com/yigitf/apm2go.git
cd apm2go
make image                          # builds the apm2go:latest image
```

Then run it — `--pid=host --network=host` is needed so it can see both the
host's processes and the ones in other containers:

```bash
docker run -d --name apm2go \
  --pid=host --network=host --privileged \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v apm2go-data:/var/lib/apm2go \
  apm2go:latest
```

Once it's up, open `http://<host>:8080`.

### Debian / Ubuntu

```bash
git clone https://github.com/yigitf/apm2go.git
cd apm2go
make package                        # builds the binary + .deb + .rpm into dist/
sudo apt install ./dist/apm2go_*.deb
```

### RHEL / Rocky / Alma / Fedora

```bash
git clone https://github.com/yigitf/apm2go.git
cd apm2go
make package                        # builds the binary + .deb + .rpm into dist/
sudo dnf install ./dist/apm2go-*.rpm
```

`make package` builds packages for both the Debian/Ubuntu and RHEL families
in one go, into `dist/`; just install whichever one matches your
distribution.

Installing the package doesn't start the service automatically — you can
first run `sudo apm2go list` to see what apm2go finds on the host, then
start it with `sudo systemctl enable --now apm2go`.

For the details of the package and container installs, why each permission
is needed, and troubleshooting steps, see [INSTALL.md](INSTALL.md).

## Requirements

- Linux, glibc 2.25 or newer (RHEL/Rocky/Alma 8+, Ubuntu 18.04+, Debian 10+)
- Root (attaching to a JVM requires becoming that JVM's own user)
- Java 8 or newer in the processes being monitored

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

## Building

```bash
make build         # binary for the host
make test          # unit tests
make package       # linux amd64 + arm64 binaries, RPM and DEB packages
```

For more development commands and architecture notes, see
[CLAUDE.md](CLAUDE.md).

## More detail

For a more technical writeup of the architecture, container network
resolution, eBPF limits and metric storage, see [DETAILS.md](DETAILS.md).
