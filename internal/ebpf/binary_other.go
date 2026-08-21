//go:build !linux

package ebpf

// eBPF instrumentation exists only on Linux. apm2go still builds and its tests
// still run on a developer's machine; the capability simply reports itself as
// unavailable rather than the package failing to compile.

// Available reports whether this build carries OBI.
func Available() bool { return false }

// binary returns the embedded OBI executable.
func binary() []byte { return nil }
