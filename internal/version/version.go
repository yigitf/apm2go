// Package version carries build-time identity for the apm2go binary.
package version

import (
	"fmt"
	"runtime"
)

// Populated via -ldflags at build time.
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// String renders a single-line version banner.
func String() string {
	return fmt.Sprintf("apm2go %s (commit %s, built %s, %s %s/%s)",
		Version, Commit, BuildDate, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
