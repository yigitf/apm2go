//go:build linux

package attach

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// capSysPtrace is CAP_SYS_PTRACE's bit number. It has been 19 since Linux
// first defined the capability set and is part of the stable kernel ABI.
const capSysPtrace = 19

// DropPrivilegesRetainingPtrace changes the calling process's real, effective
// and saved uid and gid to the target's, and keeps CAP_SYS_PTRACE effective
// afterward. It must be called before this process does anything else, in a
// process that starts out root and has nothing else running yet.
//
// The capability is not a nicety; without it, attaching a real,
// security-conscious container fails outright. Resolving a path through
// /proc/<pid>/root — which is how every step of the attach protocol reaches a
// containerized target's filesystem, trigger file included — is governed by
// the same access check as ptrace(2) itself (PTRACE_MODE_READ_FSCREDS), and
// that check does not treat "my uid matches yours" as sufficient on its own:
// it is satisfied by CAP_SYS_PTRACE, or by conditions that do not reliably
// hold for an unrelated process reaching in from outside the target's
// container. Measured directly, this is not a rare edge case: it is the
// difference between attaching to an Elasticsearch container, whose official
// image drops root to its final user with the ordinary gosu-style pattern,
// and a Graylog one, whose image starts life already at its final uid — two
// entirely standard, widely deployed images, one of which this kernel check
// blocked outright with a bare "permission denied" that named no cause. A
// plain credential-dropped process has no way to pass that check no matter
// which of the target's uid or gid it matches.
//
// The peer-credential check HotSpot itself performs is not weakened by this:
// SO_PEERCRED reads this process's actual uid and gid, which the setresuid
// and setresgid calls below still set to the target's, exactly as a plain
// drop would. CAP_SYS_PTRACE only lets the filesystem calls that precede that
// check succeed; it grants nothing over the socket itself.
//
// Every step runs across all of this process's OS threads via
// AllThreadsSyscall, which is what makes it safe to call from Go rather than
// from a single-threaded C program. Linux capabilities and credentials are a
// per-thread kernel property, and the Go runtime schedules one goroutine's
// work across many OS threads; a change applied to only the calling thread
// would leave the process's other threads still root, and since the Go
// scheduler — not this code — decides which thread eventually makes the
// connection, that would be a real privilege leak, not a narrow one.
//
// AllThreadsSyscall itself refuses to run — returning ENOTSUP — the instant
// cgo is linked into the binary, because it cannot make the same guarantee
// about threads a C runtime created outside Go's own bookkeeping. That is why
// this can only be called from a process that does not link cgo. apm2go's own
// binary always does, for the DuckDB driver, which is the entire reason this
// function lives in a separate, dedicated helper binary
// (cmd/apm2go-attach-helper) rather than running inline in the daemon that
// re-executes it.
func DropPrivilegesRetainingPtrace(uid, gid int) error {
	if uid < 0 || gid < 0 {
		return fmt.Errorf("invalid target credentials uid=%d gid=%d", uid, gid)
	}

	// KEEPCAPS has to be set before the uid transition below: without it, the
	// kernel unconditionally clears the permitted capability set the instant
	// the effective uid leaves 0, and there is nothing left to raise back into
	// the effective set afterward.
	if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_PRCTL, unix.PR_SET_KEEPCAPS, 1, 0); errno != 0 {
		return fmt.Errorf("prctl(PR_SET_KEEPCAPS): %w", errno)
	}

	// Group before user: dropping the uid first can leave this process unable
	// to change its own gid at all.
	if err := unix.Setresgid(gid, gid, gid); err != nil {
		return fmt.Errorf("setresgid(%d): %w", gid, err)
	}
	if err := unix.Setresuid(uid, uid, uid); err != nil {
		return fmt.Errorf("setresuid(%d): %w", uid, err)
	}

	// KEEPCAPS preserved the permitted set across the transition above, but
	// the effective set is cleared regardless — capset raises CAP_SYS_PTRACE
	// back into it. Capget/Capset go through AllThreadsSyscall directly rather
	// than the package-level unix.Capget/unix.Capset, which are plain
	// single-thread syscalls; using those here would raise the capability on
	// the one thread that happened to call this function and leave every
	// other thread in the process without it.
	hdr := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_CAPGET,
		uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return fmt.Errorf("capget: %w", errno)
	}
	bit := uint32(1) << (capSysPtrace % 32)
	data[0].Effective |= bit
	data[0].Permitted |= bit
	if _, _, errno := syscall.AllThreadsSyscall(unix.SYS_CAPSET,
		uintptr(unsafe.Pointer(&hdr)), uintptr(unsafe.Pointer(&data[0])), 0); errno != 0 {
		return fmt.Errorf("capset: %w", errno)
	}
	return nil
}
