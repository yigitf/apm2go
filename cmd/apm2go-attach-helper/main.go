// Command apm2go-attach-helper performs one attach handshake as the target
// JVM's own user, then exits.
//
// It exists as a separate binary, built without cgo, for one reason: HotSpot's
// attach protocol reaches a containerized target's filesystem through
// /proc/<pid>/root at every step — the trigger file, the socket, all of it —
// and that path is gated by the same access check ptrace(2) itself uses.
// Matching the target's uid is not sufficient on its own to pass that check;
// it wants CAP_SYS_PTRACE, or conditions a plain credential drop cannot
// guarantee. Keeping that capability while still becoming the target's uid
// needs Go's AllThreadsSyscall, applied across every OS thread in the
// process — and AllThreadsSyscall refuses to run at all the instant cgo is
// linked in, because it cannot make the same guarantee about threads a C
// runtime created outside Go's own bookkeeping. apm2go's own daemon binary is
// always cgo-linked, for the DuckDB driver, so the privilege drop has to
// happen somewhere else: this binary, embedded in the daemon and staged to
// disk the same way the Java agent jars are, built with CGO_ENABLED=0
// specifically so the mechanism it exists for actually works.
//
// This program is not meant to be run by hand. internal/attach.Runner
// launches it, still root, and it is the very first thing this program does
// to stop being root — see attach.DropPrivilegesRetainingPtrace.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/apm2go/apm2go/internal/attach"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	var (
		procRoot    = flag.String("proc-root", "/proc", "proc filesystem root")
		pid         = flag.Int("pid", 0, "target pid in this namespace")
		nspid       = flag.Int("nspid", 0, "target pid inside its own namespace")
		uid         = flag.Int("uid", -1, "target user id")
		gid         = flag.Int("gid", -1, "target group id")
		jar         = flag.String("jar", "", "agent jar path as the target sees it")
		options     = flag.String("options", "", "agent options")
		jcmd        = flag.String("jcmd", "", "run this diagnostic command instead of loading an agent")
		maxResponse = flag.Int("max-response", 0, "maximum reply size in bytes (0 for the default)")
		timeout     = flag.Duration("timeout", 30*time.Second, "handshake timeout")
	)
	flag.Parse()

	// The very first thing this process does, before touching anything else.
	// It is launched still holding the daemon's own root, specifically so it
	// can perform this drop itself — retaining a capability across a uid
	// transition is something only the process making that transition can do;
	// a parent cannot hand a child a capability the child's own exec already
	// stripped. Any failure here must abort immediately: the alternative is
	// silently continuing as root, which is the one outcome this whole
	// separate-binary design exists to prevent.
	if err := attach.DropPrivilegesRetainingPtrace(*uid, *gid); err != nil {
		return fmt.Errorf("drop privileges to uid %d gid %d: %w", *uid, *gid, err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	opts := attach.Options{
		ProcRoot:         *procRoot,
		PID:              *pid,
		NSPid:            *nspid,
		UID:              *uid,
		GID:              *gid,
		JarPath:          *jar,
		AgentOptions:     *options,
		MaxResponseBytes: *maxResponse,
		Timeout:          *timeout,
	}

	// A diagnostic command produces output the parent needs, so it goes to
	// stdout unadorned; errors go to stderr, which the parent already
	// surfaces verbatim.
	if *jcmd != "" {
		out, err := attach.JCmd(ctx, opts, *jcmd)
		if err != nil {
			return err
		}
		_, err = io.WriteString(os.Stdout, out)
		return err
	}

	_, err := attach.LoadAgent(ctx, opts)
	return err
}
