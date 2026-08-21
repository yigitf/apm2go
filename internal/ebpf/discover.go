package ebpf

import (
	"debug/buildinfo"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Runtime names the language a Target runs. It travels with the target rather
// than being inferred again downstream, because the detection that produced it
// — reading /proc, matching an executable name, sniffing an ELF header — is not
// something worth repeating.
type Runtime string

const (
	// The spellings here are the ones stored on a span and matched by the UI's
	// badges, not the ones a binary happens to be called. "nodejs" rather than
	// "node" because that is what the OpenTelemetry attribute uses and what the
	// receiver normalises telemetry to — and a service whose runtime is filled
	// in from discovery must not end up spelled differently from the identical
	// service whose runtime came from its own telemetry.
	RuntimeNode   Runtime = "nodejs"
	RuntimePython Runtime = "python"
	RuntimePHP    Runtime = "php"
	RuntimeGo     Runtime = "go"
	// A web server is named by the software rather than by the language it
	// happens to be written in. "C" is true of nginx and httpd and tells an
	// operator nothing; which server is in front of their application is the
	// fact they are actually looking for, and it is the one apm2go can
	// establish from the executable alone.
	RuntimeNginx Runtime = "nginx"
	RuntimeHTTPD Runtime = "httpd"
)

// Target is one process apm2go has decided to hand to OBI.
type Target struct {
	PID     int
	Runtime Runtime
	// Name is the service name apm2go derived. OBI receives it verbatim
	// (OTEL_EBPF_SERVICE_NAME equivalent, per target) rather than deriving its
	// own: two naming authorities for the same process would show it twice.
	Name string
	// ContainerID is the id of the container the process runs in, or empty for
	// a host process.
	ContainerID string
	// Ports are the TCP ports the process listens on, ascending.
	// discovery.instrument has no pid selector, only glob-based ones, so this
	// is what pins OBI's rule to this exact process rather than every process
	// of the same runtime.
	//
	// All of them, not the first one found: a web server binds 80 and 443 as a
	// matter of course, and instrumenting whichever of the two /proc/net/tcp
	// happened to list first would trace half its traffic. Sorting also makes
	// the choice stable — the table's order is not — and an unstable Port was
	// a target-set change, which restarts OBI and drops telemetry every scan.
	Ports []int
}

// ContainerID is the container this process runs in, when it runs in one. It
// is read straight out of /proc rather than looked up, because the lookup needs
// a container runtime that may not answer, and every scan would pay for it.
// Turning it into a name, an image and a Compose project happens once, where
// somebody actually asks.
func (t Target) Container() string { return t.ContainerID }

// Port is the port a target is identified by when only one will do, such as in
// a name that has to be made unique.
func (t Target) Port() int {
	if len(t.Ports) == 0 {
		return 0
	}
	return t.Ports[0]
}

// interpreterPrefix matches the basename of an interpreter binary against the
// runtime it identifies, by prefix rather than exact string: distributions
// disagree on whether /proc/<pid>/exe resolves to "python3" or the fully
// versioned "python3.9" it is really symlinked from, and PHP-FPM binaries
// carry their own version the same way ("php-fpm8.1"). A prefix survives both.
//
// This is an allow-list, not a Java exclusion list, on purpose: a JVM's
// executable is named "java", which shares no prefix with any entry here, so
// there is no code path — and no future drift — through which a Java process
// could be picked up by this scanner.
var interpreterPrefix = []struct {
	prefix  string
	runtime Runtime
}{
	{"node", RuntimeNode}, // node, nodejs
	{"python", RuntimePython},
	{"php-fpm", RuntimePHP},
}

// nativeServers maps a web server's executable basename to the runtime it
// identifies. Matched exactly rather than by prefix, unlike the interpreters
// above: "nginx-prometheus-exporter" and "apache2ctl" both start with a name
// here and are neither a web server nor, in the exporter's case, something
// whose own scrape traffic anybody wants in their trace list.
//
// Debian calls Apache's binary apache2 and Red Hat calls it httpd. Both fold
// to the same runtime, so a service does not change identity by moving between
// distributions.
var nativeServers = map[string]Runtime{
	"nginx":   RuntimeNginx,
	"httpd":   RuntimeHTTPD,
	"apache2": RuntimeHTTPD,
}

// classifyExe returns the runtime a process's executable basename identifies,
// if any.
func classifyExe(base string) (Runtime, bool) {
	for _, p := range interpreterPrefix {
		if strings.HasPrefix(base, p.prefix) {
			return p.runtime, true
		}
	}
	return "", false
}

// goDaemonDenylist excludes Go binaries that are almost certainly
// infrastructure rather than an application to trace: instrumenting a
// container runtime or apm2go's own OBI child would add noise no operator
// asked for and, for apm2go itself, would be instrumenting the instrumenter.
var goDaemonDenylist = map[string]bool{
	"apm2go":     true,
	"obi":        true,
	"dockerd":    true,
	"containerd": true,
	"kubelet":    true,
	"etcd":       true,
	"runc":       true,
	// docker-proxy is the worst of them, and not merely noise. It holds every
	// published port on the host while the container behind it holds the same
	// number in its own namespace — and OBI's selector is a port number with no
	// notion of namespaces, so a rule named after the proxy captures the real
	// service instead. Measured on a live host: a rule named docker-proxy-9000
	// collected Graylog's Elasticsearch, MongoDB and Redis calls and reported
	// them, under that name, as a Java service. Not a mislabelled service: the
	// wrong service entirely, filed under Docker's plumbing.
	"docker-proxy": true,
}

// Scan walks /proc once and returns every process worth handing to OBI.
//
// It is deliberately cheap for the common case and expensive only where it has
// to be: interpreter processes (node, python, php-fpm) are recognised by exact
// executable name, which is a map lookup per process. Go is the exception —
// every compiled binary has a different name, so the only way to tell a Go
// service from anything else is to read its build metadata — and that read is
// skipped for every process that is not already listening on a port, since a
// Go binary with no open port is not a service this exists to trace.
func Scan(procRoot string) ([]Target, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}

	var targets []Target
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}

		exe, err := os.Readlink(filepath.Join(procRoot, e.Name(), "exe"))
		if err != nil {
			// Exited between the readdir and here, or a kernel thread with no
			// exe link; either way there is nothing to classify.
			continue
		}
		base := filepath.Base(exe)

		if runtime, ok := classifyExe(base); ok {
			if t, ok := classifyInterpreter(procRoot, pid, runtime, base); ok {
				targets = append(targets, t)
			}
			continue
		}

		if runtime, ok := nativeServers[base]; ok {
			if t, ok := classifyNativeServer(procRoot, pid, runtime); ok {
				targets = append(targets, t)
			}
			continue
		}

		if goDaemonDenylist[base] {
			continue
		}
		if t, ok := classifyGoBinary(procRoot, pid, base); ok {
			targets = append(targets, t)
		}
	}
	return targets, nil
}

// classifyInterpreter builds a Target for a recognised interpreter process,
// deriving its name from the script it is running.
func classifyInterpreter(procRoot string, pid int, runtime Runtime, exeBase string) (Target, bool) {
	cmdline := readCmdline(procRoot, pid)

	var name string
	if runtime == RuntimePHP {
		name = phpFPMPoolName(cmdline)
		// The master process fronts every pool but handles no requests itself;
		// instrumenting it would attach to a process that never serves an HTTP
		// request, and its workers are what the plan explicitly targets.
		if name == "" {
			return Target{}, false
		}
	} else {
		name = scriptName(cmdline, exeBase)
	}

	ports, err := listenPorts(procRoot, pid)
	if err != nil || len(ports) == 0 {
		// Not yet listening, or not a network service at all (a build step, a
		// worker pool member that only ever receives work in-process). Neither
		// is something OBI's port-based selector can target.
		return Target{}, false
	}
	return Target{PID: pid, Runtime: runtime, Name: name, Ports: ports,
		ContainerID: containerID(procRoot, pid)}, true
}

// classifyNativeServer builds a Target for a web server process.
//
// Only the master is returned, and the workers it forked are skipped. Both
// hold the listening socket — the master opens it and the workers inherit it —
// so without this every worker would be its own identically named, identically
// ported target, and OBI would be handed the same rule as many times as the
// server has workers.
//
// The master is also the only pid that is stable. nginx replaces its entire
// worker pool on a reload and httpd recycles children on MaxRequestsPerChild;
// a target set keyed on worker pids would therefore change on its own every
// few minutes, and a changed target set restarts OBI, which drops telemetry
// for every other service it is watching at the same time.
func classifyNativeServer(procRoot string, pid int, runtime Runtime) (Target, bool) {
	if !isProcessMaster(procRoot, pid) {
		return Target{}, false
	}

	ports, err := listenPorts(procRoot, pid)
	if err != nil || len(ports) == 0 {
		// A master that is configured but has not bound yet, or one serving
		// only Unix sockets, which OBI's port selector cannot name.
		return Target{}, false
	}
	// Named for the software rather than for the binary: on Debian the
	// executable is apache2 and on Red Hat it is httpd, and a service should
	// not be renamed by the distribution it happens to run on.
	return Target{PID: pid, Runtime: runtime, Name: string(runtime), Ports: ports,
		ContainerID: containerID(procRoot, pid)}, true
}

// isProcessMaster reports whether pid is the top process of its own program,
// rather than a worker its master forked.
//
// The test is that the parent is running a different executable. Nothing in
// the argv distinguishes them reliably: nginx does label its processes
// ("nginx: master process", "nginx: worker process") but Apache's children
// carry a command line byte-identical to their parent's, so a rule written
// against nginx's labels would silently classify every httpd worker as a
// master. The parent's identity is true of both.
//
// A process whose parent cannot be read is treated as a master. That is the
// safe direction: it means a target that might be a duplicate, rather than a
// web server that is silently never instrumented.
func isProcessMaster(procRoot string, pid int) bool {
	parent, ok := parentPID(procRoot, pid)
	if !ok || parent <= 0 {
		return true
	}
	self, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(pid), "exe"))
	if err != nil {
		return true
	}
	parentExe, err := os.Readlink(filepath.Join(procRoot, strconv.Itoa(parent), "exe"))
	if err != nil {
		// The parent exited, or is outside what apm2go may read — an
		// orphaned worker reparented to init reads this way too. Either way
		// there is no evidence this process is a worker.
		return true
	}
	return filepath.Base(self) != filepath.Base(parentExe)
}

// parentPID reads PPid out of /proc/<pid>/status.
//
// status rather than stat: stat's second field is the executable name in
// parentheses, and a program free to contain spaces or a ')' in its own name
// makes every field after it unsafe to reach by counting.
func parentPID(procRoot string, pid int) (int, bool) {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		value, found := strings.CutPrefix(line, "PPid:")
		if !found {
			continue
		}
		parent, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return 0, false
		}
		return parent, true
	}
	return 0, false
}

// classifyGoBinary reports whether pid is a Go service worth tracing: compiled
// with the Go toolchain, and listening on a port. The build-info read is what
// makes this expensive, which is why it only runs for processes that already
// clear the port check.
func classifyGoBinary(procRoot string, pid int, exeBase string) (Target, bool) {
	ports, err := listenPorts(procRoot, pid)
	if err != nil || len(ports) == 0 {
		return Target{}, false
	}

	// Opened as /proc/<pid>/exe rather than as the path that symlink points at.
	// The two look interchangeable and are not: readlink returns the path as it
	// reads in the *target's* mount namespace, so opening it resolves against
	// apm2go's own filesystem, where a containerized binary's path either does
	// not exist or — worse — names a different file entirely. The magic symlink
	// resolves to the running binary's inode whatever namespace it lives in,
	// and keeps working when the original file has been replaced or deleted.
	if _, err := buildinfo.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "exe")); err != nil {
		return Target{}, false
	}
	return Target{PID: pid, Runtime: RuntimeGo, Name: exeBase, Ports: ports,
		ContainerID: containerID(procRoot, pid)}, true
}

// containerIDRe matches the 64-hex container id that appears in a
// containerized process's cgroup path. It is the same shape the JVM scanner
// looks for, and deliberately so: a process is in the same container whichever
// half of apm2go noticed it.
var containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)

// containerID reads the container a process belongs to out of its cgroup, or
// returns empty for a host process.
func containerID(procRoot string, pid int) string {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return ""
	}
	return containerIDRe.FindString(string(data))
}

// readCmdline returns a process's argv, split on the NUL apm2go's /proc
// exposes them with.
func readCmdline(procRoot string, pid int) []string {
	data, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// scriptName derives a service name from an interpreter's argv: the first
// argument that looks like a script path, stripped of its directory and
// extension. "node /opt/app/server.js" and "python3 app.py --port=8080" both
// read as the script that will actually run, which is a better name than the
// interpreter every Node or Python service on the host shares.
func scriptName(cmdline []string, fallback string) string {
	for _, arg := range cmdline[minInt(1, len(cmdline)):] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		base := filepath.Base(arg)
		if ext := filepath.Ext(base); ext == ".js" || ext == ".mjs" || ext == ".cjs" || ext == ".py" {
			return strings.TrimSuffix(base, ext)
		}
	}
	return fallback
}

// phpFPMPoolName reads the pool name FPM reports in its own argv, e.g.
// "php-fpm: pool www". It returns "" for the master process, which reports
// itself as "php-fpm: master process (...)" and fronts every pool without
// handling a request itself.
func phpFPMPoolName(cmdline []string) string {
	joined := strings.Join(cmdline, " ")
	const marker = "pool "
	i := strings.Index(joined, marker)
	if i < 0 {
		return ""
	}
	name := joined[i+len(marker):]
	if sp := strings.IndexByte(name, ' '); sp >= 0 {
		name = name[:sp]
	}
	return strings.TrimSpace(name)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
