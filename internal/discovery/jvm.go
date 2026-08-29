// Package discovery finds JVM processes on the host by reading /proc, and
// derives enough metadata about each one to name it as a service and decide
// whether it can be instrumented.
package discovery

import (
	"fmt"
	"strings"
	"time"

	"github.com/yigitf/apm2go/internal/container"
)

// JVM describes one discovered Java process.
type JVM struct {
	// PID is the process id in our namespace; NSPid is its id inside its own
	// pid namespace. They differ for containerized JVMs, and the attach
	// protocol needs the latter.
	PID   int `json:"pid"`
	NSPid int `json:"ns_pid"`

	// UID and GID own the process. Attaching must be done as this user even
	// when apm2go runs as root.
	UID  int    `json:"uid"`
	GID  int    `json:"gid"`
	User string `json:"user"`

	// ServiceName is the display name derived by deriveServiceName.
	ServiceName string `json:"service_name"`
	// ServiceNameSource records which rule produced ServiceName, so the UI can
	// explain a surprising name and offer to override it.
	ServiceNameSource string `json:"service_name_source"`

	// Cmdline is the full argv of the process.
	Cmdline []string `json:"cmdline"`
	// ExePath is the resolved /proc/<pid>/exe target, e.g. /usr/lib/jvm/.../bin/java.
	ExePath string `json:"exe_path"`
	// JavaHome is derived from ExePath by stripping /bin/java.
	JavaHome string `json:"java_home"`

	// MainClass is the entry point class, empty when running from a jar.
	MainClass string `json:"main_class,omitempty"`
	// JarPath is the -jar argument, empty when running a main class.
	JarPath string `json:"jar_path,omitempty"`

	// JavaVersion is the full version string; JavaMajor its feature number.
	JavaVersion string `json:"java_version"`
	JavaMajor   int    `json:"java_major"`
	VMName      string `json:"vm_name,omitempty"`
	// VMArgs holds the JVM flags the process was started with.
	VMArgs []string `json:"vm_args,omitempty"`
	// SystemProps holds -D properties parsed out of the command line.
	SystemProps map[string]string `json:"system_props,omitempty"`

	// StartTime is when the process started; used to skip JVMs that are still
	// booting and to detect pid reuse.
	StartTime time.Time `json:"start_time"`

	// InContainer is true when the process lives in a different mount or pid
	// namespace than the host's, which means its agent jars must be staged
	// through /proc/<pid>/root rather than written straight to /tmp.
	InContainer bool `json:"in_container"`
	// SharesOurNetwork is true when the process is in apm2go's own network
	// namespace and therefore reaches it on loopback. When false, the OTLP
	// endpoint it is given has to be routable from its own namespace — see
	// internal/netns.
	SharesOurNetwork bool `json:"shares_our_network"`
	// Gateway is the address that reaches apm2go from inside this process's
	// network namespace. Only resolved when SharesOurNetwork is false.
	Gateway string `json:"gateway,omitempty"`
	// ContainerID and SystemdUnit are best-effort labels pulled from
	// /proc/<pid>/cgroup.
	ContainerID string `json:"container_id,omitempty"`
	SystemdUnit string `json:"systemd_unit,omitempty"`
	// PodUID is the Kubernetes pod this process belongs to, read from the
	// cgroup path. It needs no runtime API and so is available wherever the
	// process is running under a kubelet.
	PodUID string `json:"pod_uid,omitempty"`
	// Container carries the runtime's own view — name, image, orchestration
	// labels — when a metadata source could supply it.
	Container *container.Info `json:"container,omitempty"`

	// AlreadyInstrumented is true when the command line already carries a
	// -javaagent flag, which is worth surfacing before injecting another one.
	AlreadyInstrumented bool `json:"already_instrumented"`
	// InstrumentedByUs is true when that -javaagent is our own permanent flag.
	InstrumentedByUs bool `json:"instrumented_by_us"`
}

// Key identifies a process across scans. A bare pid is not enough because pids
// are reused, so the start time is folded in.
func (j *JVM) Key() string {
	return fmt.Sprintf("%d-%d", j.PID, j.StartTime.UnixNano())
}

// Attachable reports whether this JVM can be instrumented, and why not when it
// cannot. Java 8 is the floor because that is where the OpenTelemetry agent's
// support begins.
func (j *JVM) Attachable() (bool, string) {
	if j.JavaMajor > 0 && j.JavaMajor < 8 {
		return false, fmt.Sprintf("Java %d is not supported (minimum is Java 8)", j.JavaMajor)
	}
	if j.InstrumentedByUs {
		return false, "already instrumented by apm2go"
	}
	return true, ""
}

// CommandLine renders the argv for display.
func (j *JVM) CommandLine() string { return strings.Join(j.Cmdline, " ") }
