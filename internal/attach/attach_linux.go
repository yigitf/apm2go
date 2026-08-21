//go:build linux

package attach

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// pollInterval is how often we look for the attach socket while waiting for the
// JVM's AttachListener thread to come up.
const pollInterval = 100 * time.Millisecond

// execute runs one request/response exchange against the target JVM.
func execute(ctx context.Context, opts Options, request []byte) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	conn, err := connect(ctx, opts)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if _, err := conn.Write(request); err != nil {
		return nil, fmt.Errorf("send attach request to pid %d: %w", opts.PID, err)
	}
	// HotSpot reads a fixed number of NUL-terminated fields and only then
	// replies, so half-closing here is safe and signals end of request.
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}

	resp, err := decodeResponse(conn, opts.MaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("pid %d: %w", opts.PID, err)
	}
	return resp, nil
}

// connect returns a connection to the target's attach socket, starting its
// AttachListener first if it is not already running.
func connect(ctx context.Context, opts Options) (net.Conn, error) {
	socketPath := attachSocketPath(opts)

	if conn, err := dialUnix(socketPath); err == nil {
		return conn, nil
	}

	// No listener yet: place the trigger file the JVM looks for, then nudge it
	// with SIGQUIT. The JVM treats SIGQUIT as "start the attach listener"
	// rather than "dump threads" precisely when that file exists.
	cleanup, err := createTriggerFiles(opts)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	if err := syscall.Kill(opts.PID, syscall.SIGQUIT); err != nil {
		return nil, fmt.Errorf("signal pid %d to start its attach listener: %w", opts.PID, err)
	}

	conn, err := waitForSocket(ctx, socketPath)
	if err != nil {
		return nil, fmt.Errorf("pid %d never opened its attach socket: %w", opts.PID, err)
	}
	return conn, nil
}

// attachSocketPath is where HotSpot creates its listening socket, resolved
// through /proc/<pid>/root so that a JVM inside a container is reached in its
// own filesystem view.
func attachSocketPath(opts Options) string {
	return filepath.Join(opts.ProcRoot, strconv.Itoa(opts.PID), "root", "tmp",
		".java_pid"+strconv.Itoa(opts.NSPid))
}

// triggerFilePaths are the locations HotSpot checks for the init trigger, in
// the order it checks them: first the process's working directory, then its
// /tmp. Both are resolved through /proc so namespaces are handled.
func triggerFilePaths(opts Options) []string {
	base := filepath.Join(opts.ProcRoot, strconv.Itoa(opts.PID))
	name := ".attach_pid" + strconv.Itoa(opts.NSPid)
	return []string{
		filepath.Join(base, "cwd", name),
		filepath.Join(base, "root", "tmp", name),
	}
}

// createTriggerFiles creates the init trigger in every location HotSpot
// inspects, as this process's own credentials, and returns a function that
// removes the ones it created.
//
// HotSpot requires the trigger file to be owned by its own uid, which is why
// this only works when the calling process already IS that uid: either
// apm2go's own euid already equals the target's (attaching to a JVM running as
// the same user apm2go does, with no privilege drop at all), or this is
// apm2go-attach-helper, which has already become the target's uid — and,
// critically, retained CAP_SYS_PTRACE while doing so. That capability is what
// the plain uid match alone does not supply: resolving a path through
// /proc/<pid>/root, which every one of these paths does, is gated by the same
// access check ptrace(2) itself uses, and matching the target's uid satisfies
// only half of it. See attach.DropPrivilegesRetainingPtrace for the rest.
func createTriggerFiles(opts Options) (func(), error) {
	var created []string
	var failures []string

	for _, path := range triggerFilePaths(opts) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				// A previous attach left it behind, or another attacher is
				// mid-flight. Either way the JVM will see it, so treat it as
				// usable but do not delete a file we did not create.
				created = append(created, "")
				continue
			}
			// Every path's own failure is kept. Reporting only the last one
			// hides the more informative half as often as not: the two
			// locations fail for different reasons — a working directory that
			// is not writable is ordinary, a /tmp that is not is a read-only
			// container or a mount the target's own user cannot write — and an
			// operator needs to see which of the two happened where.
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		if err := f.Close(); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			_ = os.Remove(path)
			continue
		}
		created = append(created, path)
	}

	cleanup := func() {
		for _, p := range created {
			if p != "" {
				_ = os.Remove(p)
			}
		}
	}

	if len(created) == 0 {
		cleanup()
		return func() {}, fmt.Errorf(
			"create attach trigger file for pid %d as uid %d: %s. "+
				"HotSpot only starts its attach listener when it finds this file, "+
				"and requires it to be owned by the JVM's own user. Check that the "+
				"target's working directory or /tmp is writable and that its "+
				"filesystem is not read-only",
			opts.PID, opts.UID, strings.Join(failures, "; "))
	}
	return cleanup, nil
}

// waitForSocket polls until the attach socket appears and accepts a connection.
func waitForSocket(ctx context.Context, socketPath string) (net.Conn, error) {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, fmt.Errorf("%w (last error: %v)", ctx.Err(), lastErr)
			}
			return nil, ctx.Err()
		case <-ticker.C:
			conn, err := dialUnix(socketPath)
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
	}
}

// dialUnix connects to a unix stream socket, reporting a missing socket as a
// distinct, quiet error since that is the expected state before the listener
// starts.
func dialUnix(path string) (net.Conn, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	return net.Dial("unix", path)
}
