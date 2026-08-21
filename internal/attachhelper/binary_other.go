//go:build !linux

package attachhelper

// Available reports whether this build carries the helper. Always false off
// Linux: dynamic attach is Linux-only, and so is everything this package
// stages for it.
func Available() bool { return false }

func binary() []byte { return nil }
