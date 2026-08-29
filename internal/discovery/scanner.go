package discovery

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/yigitf/apm2go/internal/container"
	"github.com/yigitf/apm2go/internal/netns"
)

// clockTicks is the kernel's USER_HZ, used to convert /proc/<pid>/stat's
// starttime field into wall clock time. It is 100 on every mainstream Linux
// build; reading the real value would require cgo, and a wrong value here only
// skews the "still booting" grace period.
const clockTicks = 100

// Scanner walks a /proc tree and reports the JVM processes it finds.
//
// It is safe for concurrent use and holds no state between scans, so callers
// can decide their own polling cadence.
type Scanner struct {
	procRoot string
	// selfPID is skipped so apm2go never tries to instrument itself.
	selfPID int
	// bootTime anchors process start times; resolved lazily on first use.
	bootTime time.Time
	// userCache avoids a passwd lookup per process per scan.
	userCache map[int]string
	// containers resolves container ids to names and orchestration labels. It
	// may be nil, in which case a containerized process keeps its raw id.
	containers *container.Resolver
}

// NewScanner returns a Scanner rooted at procRoot (normally "/proc"; tests
// point it at a fixture tree).
func NewScanner(procRoot string) *Scanner {
	return &Scanner{
		procRoot:  procRoot,
		selfPID:   os.Getpid(),
		userCache: make(map[int]string),
	}
}

// WithContainers attaches a metadata resolver, so containerized processes are
// named after their workload rather than their container id.
func (s *Scanner) WithContainers(resolver *container.Resolver) *Scanner {
	s.containers = resolver
	return s
}

// ContainerSources reports which container metadata sources are in use.
func (s *Scanner) ContainerSources() []string { return s.containers.Sources() }

// Scan returns every JVM process currently visible under procRoot. Processes
// that vanish mid-scan are skipped silently; that is the normal case on a busy
// host, not an error.
func (s *Scanner) Scan() ([]*JVM, error) {
	entries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.procRoot, err)
	}

	var out []*JVM
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == s.selfPID {
			continue
		}
		jvm, err := s.Inspect(pid)
		if err != nil || jvm == nil {
			continue
		}
		out = append(out, jvm)
	}
	return out, nil
}

// Inspect examines a single pid and returns its JVM description, or nil when
// the process is not a JVM.
func (s *Scanner) Inspect(pid int) (*JVM, error) {
	dir := filepath.Join(s.procRoot, strconv.Itoa(pid))
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}

	cmdline := s.readCmdline(pid)
	exe, _ := os.Readlink(filepath.Join(dir, "exe"))

	if !s.isJVM(pid, exe, cmdline) {
		return nil, nil
	}

	jvm := &JVM{
		PID:         pid,
		NSPid:       pid,
		Cmdline:     cmdline,
		ExePath:     exe,
		SystemProps: map[string]string{},
	}

	s.readStatus(pid, jvm)
	jvm.User = s.lookupUser(jvm.UID)
	jvm.StartTime = s.startTime(pid)
	jvm.JavaHome = deriveJavaHome(exe, cmdline)
	s.readCgroup(pid, jvm)
	s.detectNamespace(pid, jvm)
	s.resolveGateway(pid, jvm)
	s.resolveContainer(jvm)

	parseSystemProps(cmdline, jvm)
	parseAgentFlags(cmdline, jvm)
	parseEntryPoint(cmdline, jvm)

	// Perf data supersedes the command line where they overlap: it carries the
	// exact java version and the untruncated VM arguments.
	if pd, err := readPerfData(s.procRoot, pid, jvm.NSPid, jvm.User); err == nil {
		applyPerfData(pd, jvm)
	}

	deriveServiceName(jvm)
	return jvm, nil
}

// isJVM decides whether a process runs a Java virtual machine. The executable
// name is the cheap check; a mapped libjvm is the authoritative one and catches
// launchers, wrappers and embedded JVMs whose argv[0] is not "java".
func (s *Scanner) isJVM(pid int, exe string, cmdline []string) bool {
	if len(cmdline) == 0 {
		// Kernel threads have an empty cmdline.
		return false
	}
	if isJavaBinary(exe) || isJavaBinary(cmdline[0]) {
		return true
	}
	return s.hasLibJVM(pid)
}

func isJavaBinary(p string) bool {
	if p == "" {
		return false
	}
	base := filepath.Base(p)
	return base == "java" || base == "javaw"
}

// hasLibJVM scans /proc/<pid>/maps for a mapped libjvm shared object. The file
// can be large, so it is read line by line and abandoned as soon as a match
// appears.
func (s *Scanner) hasLibJVM(pid int) bool {
	f, err := os.Open(filepath.Join(s.procRoot, strconv.Itoa(pid), "maps"))
	if err != nil {
		return false
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "libjvm.so") {
			return true
		}
	}
	return false
}

// readCmdline splits the NUL-separated argv of a process.
func (s *Scanner) readCmdline(pid int) []string {
	data, err := os.ReadFile(filepath.Join(s.procRoot, strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return nil
	}
	parts := bytes.Split(bytes.TrimRight(data, "\x00"), []byte{0})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if len(p) > 0 {
			out = append(out, string(p))
		}
	}
	return out
}

// readStatus pulls the real uid/gid and the namespace-local pid out of
// /proc/<pid>/status. NSpid's last field is the pid as the process itself sees
// it, which is what the attach protocol must use.
func (s *Scanner) readStatus(pid int, jvm *JVM) {
	f, err := os.Open(filepath.Join(s.procRoot, strconv.Itoa(pid), "status"))
	if err != nil {
		return
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Uid:"):
			if fields := strings.Fields(line); len(fields) > 1 {
				jvm.UID, _ = strconv.Atoi(fields[1])
			}
		case strings.HasPrefix(line, "Gid:"):
			if fields := strings.Fields(line); len(fields) > 1 {
				jvm.GID, _ = strconv.Atoi(fields[1])
			}
		case strings.HasPrefix(line, "NSpid:"):
			if fields := strings.Fields(line); len(fields) > 1 {
				// The innermost namespace is last.
				if n, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
					jvm.NSPid = n
				}
			}
		}
	}
}

// startTime converts field 22 of /proc/<pid>/stat into wall clock time. The
// modification time of the /proc/<pid> directory is used as a fallback, since
// Linux sets it when the process is created.
func (s *Scanner) startTime(pid int) time.Time {
	dir := filepath.Join(s.procRoot, strconv.Itoa(pid))

	if s.bootTime.IsZero() {
		s.bootTime = s.readBootTime()
	}
	if !s.bootTime.IsZero() {
		if data, err := os.ReadFile(filepath.Join(dir, "stat")); err == nil {
			// The second field is the comm, which may itself contain spaces and
			// parentheses, so split after the final ')'.
			if idx := bytes.LastIndexByte(data, ')'); idx > 0 && idx+2 < len(data) {
				fields := strings.Fields(string(data[idx+2:]))
				// starttime is field 22 overall, i.e. index 19 after comm+state.
				if len(fields) > 19 {
					if ticks, err := strconv.ParseInt(fields[19], 10, 64); err == nil {
						return s.bootTime.Add(time.Duration(ticks) * time.Second / clockTicks)
					}
				}
			}
		}
	}

	if fi, err := os.Stat(dir); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// readBootTime reads the btime field of /proc/stat.
func (s *Scanner) readBootTime() time.Time {
	data, err := os.ReadFile(filepath.Join(s.procRoot, "stat"))
	if err != nil {
		return time.Time{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		if after, ok := strings.CutPrefix(line, "btime "); ok {
			if secs, err := strconv.ParseInt(strings.TrimSpace(after), 10, 64); err == nil {
				return time.Unix(secs, 0)
			}
		}
	}
	return time.Time{}
}

var (
	// Docker, containerd and CRI-O all embed a 64 hex character id in the
	// cgroup path, sometimes with a "docker-" prefix and a ".scope" suffix.
	containerIDRe = regexp.MustCompile(`[0-9a-f]{64}`)
	systemdUnitRe = regexp.MustCompile(`/([\w\-.@\\]+\.service)`)
)

// readCgroup labels the process with its systemd unit or container id, both of
// which make far better service names than a main class.
func (s *Scanner) readCgroup(pid int, jvm *JVM) {
	data, err := os.ReadFile(filepath.Join(s.procRoot, strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return
	}
	text := string(data)
	if m := containerIDRe.FindString(text); m != "" {
		jvm.ContainerID = m
	}
	if m := systemdUnitRe.FindStringSubmatch(text); len(m) > 1 {
		jvm.SystemdUnit = strings.TrimSuffix(m[1], ".service")
	}
	// The pod UID comes from the path itself, so it is known even on hosts
	// where no runtime socket is readable.
	jvm.PodUID = container.PodUIDFromCgroup(text)
}

// resolveContainer asks the metadata sources what this container actually is.
// A failure leaves the raw id in place: a process is still worth instrumenting
// when apm2go cannot look up its pretty name.
func (s *Scanner) resolveContainer(jvm *JVM) {
	if s.containers == nil || jvm.ContainerID == "" {
		return
	}
	info := s.containers.Resolve(jvm.ContainerID)
	if info == nil {
		return
	}
	// The cgroup path is authoritative for the pod UID and is available even
	// when the runtime is not, so it is not overwritten by an empty answer.
	if info.PodUID == "" {
		info.PodUID = jvm.PodUID
	}
	jvm.Container = info
}

// detectNamespace answers two separate questions about a process, each of which
// needs its own point of reference. Getting them confused produces warnings
// about problems that do not exist, or silence about ones that do.
//
//	Is it containerized?      compare its mount namespace against pid 1's
//	Can it reach us?          compare its network namespace against our own
//
// The first is about the filesystem: a process seeing a different mount
// namespace than the host's init needs its agent jars staged through
// /proc/<pid>/root. Pid 1 is the reference because apm2go's own systemd unit
// sets ProtectSystem and ProtectHome, which put apm2go in a private mount
// namespace — comparing against ourselves would label every ordinary process on
// the host as containerized.
//
// The second is about reachability, and there the reference has to be apm2go
// itself, because apm2go is what is listening. A process sharing our network
// namespace reaches us on loopback no matter how containerized it otherwise is;
// a process outside it does not, even when apm2go is the containerized one.
// Measured directly: with apm2go running in a container alongside four JVMs,
// those four exported over loopback while a JVM in a *different* container
// could not — yet comparing against pid 1 called all five unreachable.
func (s *Scanner) detectNamespace(pid int, jvm *JVM) {
	// A pid that differs inside the target's own namespace is conclusive on its
	// own, and a container id from the cgroup path is direct evidence.
	if jvm.NSPid != pid || jvm.ContainerID != "" {
		jvm.InContainer = true
	}
	if s.namespaceDiffers(pid, "mnt", s.hostNamespace("mnt")) {
		jvm.InContainer = true
	}

	jvm.SharesOurNetwork = !s.namespaceDiffers(pid, "net", s.ourNamespace("net"))
}

// hostNamespace returns pid 1's named namespace identifier.
func (s *Scanner) hostNamespace(namespace string) string {
	link, _ := os.Readlink(filepath.Join(s.procRoot, "1", "ns", namespace))
	return link
}

// ourNamespace returns apm2go's own named namespace identifier.
func (s *Scanner) ourNamespace(namespace string) string {
	link, _ := os.Readlink(filepath.Join(s.procRoot, "self", "ns", namespace))
	return link
}

// namespaceDiffers reports whether a process is in a different namespace than
// the given reference.
//
// Missing evidence yields false, which for both callers is the benign answer:
// an unreadable mount namespace means we do not claim the process is
// containerized, and an unreadable network namespace means we assume it can
// reach us and let a failed export speak for itself, rather than refusing to
// instrument it on a guess.
func (s *Scanner) namespaceDiffers(pid int, namespace, reference string) bool {
	if reference == "" {
		return false
	}
	target, err := os.Readlink(filepath.Join(s.procRoot, strconv.Itoa(pid), "ns", namespace))
	if err != nil {
		return false
	}
	return target != reference
}

// resolveGateway finds the address that reaches apm2go from inside a process
// that does not share its network namespace. A failure is recorded as an empty
// gateway rather than an error: the process is still worth instrumenting, and
// the injector reports the missing route where an operator will see it.
func (s *Scanner) resolveGateway(pid int, jvm *JVM) {
	if jvm.SharesOurNetwork {
		return
	}
	gateway, err := netns.Gateway(s.procRoot, pid)
	if err != nil {
		return
	}
	jvm.Gateway = gateway.String()
}

// lookupUser resolves a uid to a name, caching both hits and misses.
func (s *Scanner) lookupUser(uid int) string {
	if name, ok := s.userCache[uid]; ok {
		return name
	}
	name := ""
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		name = u.Username
	}
	s.userCache[uid] = name
	return name
}

// deriveJavaHome strips the trailing /bin/java from the executable path, and
// falls back to a -Djava.home property when /proc/<pid>/exe is unreadable.
func deriveJavaHome(exe string, cmdline []string) string {
	if exe != "" {
		if dir := filepath.Dir(exe); filepath.Base(dir) == "bin" {
			return filepath.Dir(dir)
		}
	}
	for _, arg := range cmdline {
		if v, ok := strings.CutPrefix(arg, "-Djava.home="); ok {
			return v
		}
	}
	return ""
}

// parseSystemProps records every -D flag on the command line.
func parseSystemProps(cmdline []string, jvm *JVM) {
	for _, arg := range cmdline {
		rest, ok := strings.CutPrefix(arg, "-D")
		if !ok {
			continue
		}
		key, value, found := strings.Cut(rest, "=")
		if !found {
			// A bare -Dflag means the empty string, matching JVM behaviour.
			value = ""
		}
		if key != "" {
			jvm.SystemProps[key] = value
		}
	}
}

// parseAgentFlags notes any pre-existing -javaagent, so the UI can warn about
// stacking agents and recognise our own permanent installation.
func parseAgentFlags(cmdline []string, jvm *JVM) {
	for _, arg := range cmdline {
		if !strings.HasPrefix(arg, "-javaagent:") {
			continue
		}
		jvm.AlreadyInstrumented = true
		if strings.Contains(arg, "apm2go") {
			jvm.InstrumentedByUs = true
		}
	}
}

// parseEntryPoint finds the main class or the -jar argument. Everything before
// it is a JVM flag; the first non-flag token is the entry point unless -jar
// appeared first.
func parseEntryPoint(cmdline []string, jvm *JVM) {
	if len(cmdline) < 2 {
		return
	}
	for i := 1; i < len(cmdline); i++ {
		arg := cmdline[i]
		if arg == "-jar" {
			if i+1 < len(cmdline) {
				jvm.JarPath = cmdline[i+1]
			}
			return
		}
		if arg == "-cp" || arg == "-classpath" || arg == "--module-path" || arg == "-p" {
			// Skip the flag and its value.
			i++
			continue
		}
		if strings.HasPrefix(arg, "-") {
			jvm.VMArgs = append(jvm.VMArgs, arg)
			continue
		}
		jvm.MainClass = arg
		return
	}
}

// applyPerfData overlays hsperfdata values, which are more reliable than the
// command line for the version and the VM arguments.
func applyPerfData(pd *perfData, jvm *JVM) {
	if pd.JavaVersion != "" {
		jvm.JavaVersion = pd.JavaVersion
		jvm.JavaMajor = majorJavaVersion(pd.JavaVersion)
	}
	if pd.VMName != "" {
		jvm.VMName = pd.VMName
	}
	if pd.VMArgs != "" {
		jvm.VMArgs = strings.Fields(pd.VMArgs)
		// The command line can be truncated by the kernel, so re-parse the
		// authoritative flags for -D properties and agents.
		parseSystemProps(jvm.VMArgs, jvm)
		parseAgentFlags(jvm.VMArgs, jvm)
	}
	// javaCommand is "<main class or jar> <args...>"; only the head matters.
	if pd.JavaCommand != "" && jvm.MainClass == "" && jvm.JarPath == "" {
		head, _, _ := strings.Cut(pd.JavaCommand, " ")
		if strings.HasSuffix(head, ".jar") {
			jvm.JarPath = head
		} else {
			jvm.MainClass = head
		}
	}
}
