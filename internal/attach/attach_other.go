//go:build !linux

package attach

import (
	"context"
	"fmt"
	"runtime"
)

// execute is unavailable off Linux. apm2go targets Linux hosts, but the package
// still compiles elsewhere so the rest of the tree can be built and tested on a
// developer's machine.
func execute(context.Context, Options, []byte) (*Response, error) {
	return nil, fmt.Errorf("dynamic attach requires Linux (running on %s)", runtime.GOOS)
}

// createAndOwnTriggerFiles is unavailable off Linux for the same reason.
func createAndOwnTriggerFiles(Options) (func(), error) {
	return nil, fmt.Errorf("dynamic attach requires Linux (running on %s)", runtime.GOOS)
}
