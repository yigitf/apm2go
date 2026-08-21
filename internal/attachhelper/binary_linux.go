//go:build linux

package attachhelper

import (
	_ "embed"
)

// The cgo-free apm2go-attach-helper binary, built by the Makefile or
// build/package.sh immediately before the main apm2go binary that embeds it,
// the same way the OBI eBPF binary is.
//
// Linux-only and architecture-specific for the same two reasons OBI's own
// embed is: it is a Linux binary, so carrying it in a developer's macOS build
// would add dead weight that could never run; and each packaging build
// compiles it fresh for the architecture it is building, immediately
// beforehand, overwriting whatever a previous architecture's pass left here.
//
//go:embed files/apm2go-attach-helper
var helperBinary []byte

// Available reports whether this build carries the helper.
func Available() bool { return len(helperBinary) > 0 }

// binary returns the embedded helper executable.
func binary() []byte { return helperBinary }
