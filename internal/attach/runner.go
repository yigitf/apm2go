package attach

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Runner performs attaches, handling the privilege drop that HotSpot requires.
//
// The JVM checks the peer credentials of its attach socket and accepts only
// its own user — running as root is not a shortcut. A Runner launches
// apm2go-attach-helper, a separate binary, to do the actual handshake.
//
// It has to be a separate binary rather than apm2go re-executing itself, and
// the reason is specific: reaching a containerized target's filesystem, at
// every step of this protocol, goes through /proc/<pid>/root, which is gated
// by the same access check ptrace(2) itself uses. Matching the target's uid is
// not sufficient to pass that check on its own; it wants CAP_SYS_PTRACE, which
// a plain drop-at-exec throws away along with every other capability the
// instant the effective uid leaves 0. Keeping it requires the process making
// the transition to do so itself — see DropPrivilegesRetainingPtrace — using a
// mechanism (Go's AllThreadsSyscall) that refuses to run at all once cgo is
// linked into the binary. apm2go's own binary always is, for the DuckDB
// driver, so the transition cannot happen inside it. It happens in
// apm2go-attach-helper instead: built with CGO_ENABLED=0, embedded in the
// daemon and staged to disk the same way the Java agent jars are, launched
// still root and dropping itself as the very first thing it does. Measured
// against real, ordinary images: this is what let apm2go attach to
// Elasticsearch and Graylog containers that a plain credential drop could not
// reach at all, with an error that named no cause.
type Runner struct {
	// HelperPath is the apm2go-attach-helper binary to launch.
	HelperPath string
	Log        *slog.Logger
}

// NewRunner returns a Runner that launches the helper staged at helperPath.
func NewRunner(helperPath string, log *slog.Logger) *Runner {
	return &Runner{HelperPath: helperPath, Log: log}
}

// LoadAgent injects an agent jar into the target, dropping privileges first
// when apm2go is running as a different user than the JVM.
func (r *Runner) LoadAgent(ctx context.Context, opts Options) error {
	opts.applyDefaults()
	if err := opts.validate(); err != nil {
		return err
	}

	if os.Geteuid() == opts.UID {
		_, err := LoadAgent(ctx, opts)
		return err
	}
	if err := r.canDropTo(opts); err != nil {
		return err
	}

	extra := []string{"--jar", opts.JarPath}
	if opts.AgentOptions != "" {
		extra = append(extra, "--options", opts.AgentOptions)
	}
	_, err := r.runHelper(ctx, opts, extra)
	return err
}

// JCmd runs a diagnostic command in the target, dropping privileges the same
// way an attach does — the peer credential check applies to every command on
// this channel, not just agent loads.
func (r *Runner) JCmd(ctx context.Context, opts Options, command string) (string, error) {
	opts.applyDefaults()
	if opts.PID <= 0 {
		return "", fmt.Errorf("invalid pid %d", opts.PID)
	}

	if os.Geteuid() == opts.UID {
		return JCmd(ctx, opts, command)
	}
	if err := r.canDropTo(opts); err != nil {
		return "", err
	}

	extra := []string{"--jcmd", command}
	if opts.MaxResponseBytes > 0 {
		extra = append(extra, "--max-response", strconv.Itoa(opts.MaxResponseBytes))
	}
	return r.runHelper(ctx, opts, extra)
}

// canDropTo reports whether this process can launch the helper with enough
// privilege for it to drop into the target's identity itself.
func (r *Runner) canDropTo(opts Options) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf(
			"cannot attach to pid %d: it runs as uid %d and apm2go runs as uid %d; run apm2go as root or as that user",
			opts.PID, opts.UID, os.Geteuid())
	}
	if r.HelperPath == "" {
		return fmt.Errorf(
			"cannot attach to pid %d: no apm2go-attach-helper is available (this build may not carry one, "+
				"or it failed to stage — check the startup log)", opts.PID)
	}
	return nil
}

// runHelper launches apm2go-attach-helper to perform the handshake, returning
// whatever it wrote to stdout. For an agent load that is nothing; for a
// diagnostic command it is the dump itself.
//
// The helper is launched with no credential change at all — it inherits this
// process's own root privileges across the exec, and drops them itself, first
// thing, keeping CAP_SYS_PTRACE effective afterward. See the Runner and
// DropPrivilegesRetainingPtrace comments for why that has to happen inside the
// helper rather than out here.
func (r *Runner) runHelper(ctx context.Context, opts Options, extra []string) (string, error) {
	args := append([]string{
		"--proc-root", opts.ProcRoot,
		"--pid", strconv.Itoa(opts.PID),
		"--nspid", strconv.Itoa(opts.NSPid),
		"--uid", strconv.Itoa(opts.UID),
		"--gid", strconv.Itoa(opts.GID),
		"--timeout", opts.Timeout.String(),
	}, extra...)

	cmd := exec.CommandContext(ctx, r.HelperPath, args...)
	// The helper inherits nothing from our environment: it only needs to open a
	// socket, and a stray APM2GO_* variable would silently change its behaviour.
	cmd.Env = []string{"PATH=/usr/bin:/bin"}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if r.Log != nil {
		// Only the operation is logged, never the arguments: the agent options
		// carry this process's ingest token.
		r.Log.Debug("running attach helper",
			"pid", opts.PID, "uid", opts.UID, "gid", opts.GID, "operation", helperOperation(extra))
	}

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			return "", fmt.Errorf("attach helper for pid %d failed: %w", opts.PID, err)
		}
		return "", fmt.Errorf("attach helper for pid %d failed: %s", opts.PID, msg)
	}
	return stdout.String(), nil
}

// helperOperation names what a helper invocation is doing, for logs.
func helperOperation(extra []string) string {
	for i, a := range extra {
		if a == "--jcmd" && i+1 < len(extra) {
			return extra[i+1]
		}
	}
	return "load-agent"
}
