package ebpf

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Kernel versions eBPF instrumentation needs.
//
// The two thresholds are different capabilities, not one: spans can be
// collected on an older kernel than the one required to rewrite a request's
// headers in flight. Reporting them separately is what lets apm2go say "these
// services are measured but their traces will not join up" instead of failing
// wholesale or, worse, appearing to work while every trace silently breaks at
// the first hop.
const (
	minKernelMajor, minKernelMinor                 = 5, 8
	propagationKernelMajor, propagationKernelMinor = 5, 17
)

// btfPath is the kernel's own type information, which OBI needs to attach its
// programs to a kernel it was not compiled against. Distributions built without
// CONFIG_DEBUG_INFO_BTF do not have it.
const btfPath = "/sys/kernel/btf/vmlinux"

// Capability is what this host and this build can actually do.
type Capability struct {
	// Embedded reports whether this apm2go build carries the OBI binary at all.
	// A macOS developer build does not.
	Embedded bool
	// KernelVersion is the running kernel, as the kernel reports it.
	KernelVersion string
	// HasBTF reports whether the kernel exposes its own type information.
	HasBTF bool
	// Privileged reports whether apm2go can load eBPF programs. Loading them
	// needs CAP_BPF and CAP_PERFMON, which in practice means running as root.
	Privileged bool

	// Spans reports whether processes can be instrumented at all.
	Spans bool
	// ContextPropagation reports whether OBI can write a traceparent into an
	// outgoing request, which is what joins a non-Java service to a Java one.
	ContextPropagation bool

	// Reason explains, in an operator's terms, why Spans is false. It is empty
	// when instrumentation is available.
	Reason string
	// PropagationReason explains why ContextPropagation is false while Spans is
	// true — the case that is easy to misread as a bug.
	PropagationReason string
}

// Detect measures what this host supports. It never fails: an unsupported host
// is an ordinary answer, and the Java side of apm2go is unaffected by it.
func Detect() Capability {
	c := Capability{Embedded: Available()}

	if !c.Embedded {
		c.Reason = "this apm2go build does not include eBPF instrumentation " +
			"(it is built for Linux only)"
		return c
	}

	c.KernelVersion = readKernelVersion()
	major, minor, ok := parseKernelVersion(c.KernelVersion)
	if !ok {
		c.Reason = fmt.Sprintf("could not read the kernel version (%q)", c.KernelVersion)
		return c
	}

	if _, err := os.Stat(btfPath); err == nil {
		c.HasBTF = true
	}
	// Loading eBPF programs needs privileges no unprivileged process has. This
	// is deliberately a simple check: apm2go either runs as root or it does not.
	c.Privileged = os.Geteuid() == 0

	switch {
	case !atLeast(major, minor, minKernelMajor, minKernelMinor):
		c.Reason = fmt.Sprintf(
			"kernel %s is too old for eBPF instrumentation (%d.%d or newer is required); "+
				"Java instrumentation is unaffected",
			c.KernelVersion, minKernelMajor, minKernelMinor)
		return c
	case !c.HasBTF:
		c.Reason = fmt.Sprintf(
			"this kernel was built without BTF type information (%s is missing), "+
				"which eBPF instrumentation needs; Java instrumentation is unaffected", btfPath)
		return c
	case !c.Privileged:
		c.Reason = "loading eBPF programs requires root (CAP_BPF and CAP_PERFMON); " +
			"Java instrumentation is unaffected"
		return c
	}

	c.Spans = true

	if atLeast(major, minor, propagationKernelMajor, propagationKernelMinor) {
		c.ContextPropagation = true
	} else {
		c.PropagationReason = fmt.Sprintf(
			"kernel %s can measure these services but cannot write trace context into their "+
				"outgoing requests (%d.%d or newer is required), so their traces will not join "+
				"up with the services they call",
			c.KernelVersion, propagationKernelMajor, propagationKernelMinor)
	}
	return c
}

// readKernelVersion reads the running kernel's release string.
func readKernelVersion() string {
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// parseKernelVersion pulls the major and minor numbers out of a release string
// such as "6.12.54-linuxkit" or "4.18.0-513.el8.x86_64".
func parseKernelVersion(release string) (major, minor int, ok bool) {
	if release == "" {
		return 0, 0, false
	}
	// Everything after the first '-' is distribution packaging, not version.
	if i := strings.IndexAny(release, "-+"); i >= 0 {
		release = release[:i]
	}
	parts := strings.Split(release, ".")
	if len(parts) < 2 {
		return 0, 0, false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	minor, err = strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0, false
	}
	return major, minor, true
}

// atLeast compares a kernel version against a minimum.
func atLeast(major, minor, wantMajor, wantMinor int) bool {
	if major != wantMajor {
		return major > wantMajor
	}
	return minor >= wantMinor
}
