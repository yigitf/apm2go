//go:build !linux

package attach

import (
	"fmt"
	"runtime"
)

// DropPrivilegesRetainingPtrace is unavailable off Linux for the same reason
// as the rest of this package: dynamic attach itself is Linux-only.
func DropPrivilegesRetainingPtrace(int, int) error {
	return fmt.Errorf("dynamic attach requires Linux (running on %s)", runtime.GOOS)
}
