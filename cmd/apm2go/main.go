// Command apm2go is a standalone APM for Linux hosts: it discovers running JVMs,
// injects tracing into them without a restart, stores the resulting traces in an
// embedded database and serves a web UI over them — all from a single binary.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "apm2go:", err)
		os.Exit(1)
	}
}
