//go:build linux

package ebpf

import (
	_ "embed"
)

// The OpenTelemetry eBPF Instrumentation binary, downloaded by the Makefile and
// compiled into apm2go the same way the Java agent jars are.
//
// The embed lives in a Linux-only file for two reasons. It is a Linux binary,
// so carrying it in a developer's macOS build would add ~113MB that could never
// run; and it is architecture-specific, so each packaging build downloads the
// one matching the container it builds in.
//
//go:embed files/obi
var obiBinary []byte

// Available reports whether this build carries OBI.
func Available() bool { return len(obiBinary) > 0 }

// binary returns the embedded OBI executable.
func binary() []byte { return obiBinary }
