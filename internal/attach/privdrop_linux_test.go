//go:build linux

package attach

import (
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

// The property this whole function exists for: the process's real, effective
// and saved uid and gid all become the target's -- exactly what HotSpot's own
// peer-credential check reads -- while CAP_SYS_PTRACE stays effective, which
// is what lets it resolve a containerized target's filesystem afterward.
//
// This test genuinely drops root and cannot get it back, so it runs in its
// own subprocess (see TestDropPrivilegesRetainingPtraceSubprocess) rather than
// in the test binary directly, which Go's test runner reuses across cases.
//
// It is also only meaningful compiled and run on its own, the way
// `go test ./internal/attach/` does it: AllThreadsSyscall, which this depends
// on, refuses to run at all the moment cgo is linked into the binary, and the
// full apm2go binary always is. This package itself has no cgo dependency, so
// its own test binary does not either -- which is exactly the property that
// makes a standalone apm2go-attach-helper binary work in production too.
func TestDropPrivilegesRetainingPtrace(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("dropping to an arbitrary uid requires root")
	}
	if os.Getenv("APM2GO_PRIVDROP_SUBPROCESS") != "1" {
		t.Skip("run via TestDropPrivilegesRetainingPtraceSubprocess, which re-execs this test in its own process")
	}

	const targetUID, targetGID = 65534, 65534 // "nobody" on virtually every Linux system

	if err := DropPrivilegesRetainingPtrace(targetUID, targetGID); err != nil {
		t.Fatalf("DropPrivilegesRetainingPtrace: %v", err)
	}

	if uid := os.Getuid(); uid != targetUID {
		t.Errorf("real uid = %d, want %d", uid, targetUID)
	}
	if uid := os.Geteuid(); uid != targetUID {
		t.Errorf("effective uid = %d, want %d", uid, targetUID)
	}
	if gid := os.Getgid(); gid != targetGID {
		t.Errorf("real gid = %d, want %d", gid, targetGID)
	}
	if gid := os.Getegid(); gid != targetGID {
		t.Errorf("effective gid = %d, want %d", gid, targetGID)
	}

	// The actual point of this function: CAP_SYS_PTRACE must still be
	// effective, on the SAME thread this goroutine happens to run on right
	// now -- proving the raise was not merely applied to whichever thread
	// called capset and forgotten on every other one.
	var hdr unix.CapUserHeader
	hdr.Version = unix.LINUX_CAPABILITY_VERSION_3
	var data [2]unix.CapUserData
	if err := unix.Capget(&hdr, &data[0]); err != nil {
		t.Fatalf("capget: %v", err)
	}
	bit := uint32(1) << (capSysPtrace % 32)
	if data[0].Effective&bit == 0 {
		t.Error("CAP_SYS_PTRACE is not in the effective set after dropping privileges")
	}

	// Root cannot be regained: a permitted setuid(0) here would mean the drop
	// was not real, which is the one failure mode that must never pass silently.
	if err := syscall.Setuid(0); err == nil {
		t.Fatal("regained root after DropPrivilegesRetainingPtrace; the drop is not real")
	}
}

// TestDropPrivilegesRetainingPtraceSubprocess re-executes the test above in a
// fresh process, since it permanently drops root and Go's test binary runs
// every test in the same process.
func TestDropPrivilegesRetainingPtraceSubprocess(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("dropping to an arbitrary uid requires root")
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(self, "-test.run", "^TestDropPrivilegesRetainingPtrace$", "-test.v")
	cmd.Env = append(os.Environ(), "APM2GO_PRIVDROP_SUBPROCESS=1")
	out, err := cmd.CombinedOutput()
	t.Logf("subprocess output:\n%s", out)
	if err != nil {
		t.Fatalf("subprocess failed: %v", err)
	}
}
