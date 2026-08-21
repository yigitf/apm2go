package attach

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Options describes a single attach attempt.
type Options struct {
	// ProcRoot is the /proc mount to resolve the target through.
	ProcRoot string
	// PID is the target's pid in our namespace; NSPid is its pid inside its own
	// namespace. HotSpot names its socket and trigger file after NSPid.
	PID   int
	NSPid int
	// UID and GID own the target. HotSpot compares the peer credentials of the
	// attach socket against these and refuses anything else, root included, so
	// the handshake must run as this user.
	UID int
	GID int

	// JarPath is the agent jar as the *target* sees it. For a containerized JVM
	// that is a path inside the container, not on the host.
	JarPath string
	// AgentOptions is passed to the agent verbatim, matching -javaagent:jar=opts.
	AgentOptions string

	// Timeout bounds the whole handshake, including waiting for the JVM to
	// start its AttachListener thread.
	Timeout time.Duration

	// MaxResponseBytes bounds the reply body. Zero uses the default, which is
	// ample for an agent load or a properties dump; diagnostic commands raise
	// it because a class histogram scales with the number of loaded classes.
	MaxResponseBytes int
}

// defaultTimeout is used when Options.Timeout is zero.
const defaultTimeout = 30 * time.Second

func (o *Options) applyDefaults() {
	if o.ProcRoot == "" {
		o.ProcRoot = "/proc"
	}
	if o.NSPid == 0 {
		o.NSPid = o.PID
	}
	if o.Timeout <= 0 {
		o.Timeout = defaultTimeout
	}
}

func (o *Options) validate() error {
	if o.PID <= 0 {
		return fmt.Errorf("invalid pid %d", o.PID)
	}
	if o.JarPath == "" {
		return fmt.Errorf("jar path is required")
	}
	return nil
}

// LoadAgent injects a Java agent jar into the target JVM.
//
// The caller must already be running as the target's user; use the privilege
// dropping helper in the CLI when apm2go runs as root. A successful return
// means the JVM loaded the agent and its agentmain returned without throwing.
func LoadAgent(ctx context.Context, opts Options) (*Response, error) {
	opts.applyDefaults()
	if err := opts.validate(); err != nil {
		return nil, err
	}

	req, err := loadAgentRequest(opts.JarPath, opts.AgentOptions)
	if err != nil {
		return nil, err
	}

	resp, err := execute(ctx, opts, req)
	if err != nil {
		return nil, err
	}
	return resp, resp.Err()
}

// Ping checks that a JVM is reachable over the attach channel without changing
// anything about it. It is used to verify a target before and after injection.
func Ping(ctx context.Context, opts Options) error {
	opts.applyDefaults()
	if opts.PID <= 0 {
		return fmt.Errorf("invalid pid %d", opts.PID)
	}

	req, err := encodeRequest(cmdProperties)
	if err != nil {
		return err
	}
	resp, err := execute(ctx, opts, req)
	if err != nil {
		return err
	}
	return resp.Err()
}

// JCmd runs a diagnostic command in the target and returns its output.
//
// This is the same channel `jcmd` itself uses, so anything that tool can ask
// for — a thread dump, a class histogram, the JVM's flags — is available
// without a restart and without a JDK on the host. The command runs inside the
// target: the safepoint it may need is the target's cost, not ours.
func JCmd(ctx context.Context, opts Options, command string) (string, error) {
	opts.applyDefaults()
	if opts.PID <= 0 {
		return "", fmt.Errorf("invalid pid %d", opts.PID)
	}

	req, err := jcmdRequest(command)
	if err != nil {
		return "", err
	}
	resp, err := execute(ctx, opts, req)
	if err != nil {
		return "", err
	}

	// Deliberately not resp.Err(): that scans the body for the instrument
	// library's "return code:" line, and a thread dump is arbitrary application
	// text that could contain anything.
	if resp.Code != 0 {
		msg := strings.TrimSpace(resp.Body)
		if msg == "" {
			return "", fmt.Errorf("JVM rejected %q (code %d)", command, resp.Code)
		}
		return "", fmt.Errorf("JVM rejected %q (code %d): %s", command, resp.Code, truncate(msg, 500))
	}
	if err := jcmdBodyError(command, resp.Body); err != nil {
		return "", err
	}
	if resp.Truncated {
		return resp.Body, fmt.Errorf("output of %q exceeded %d bytes and was truncated", command, opts.MaxResponseBytes)
	}
	return resp.Body, nil
}

// jcmdBodyError catches the failures some JVM builds report as a successful
// reply whose body is an exception. Left undetected, an unknown command reads
// as a dump with no threads in it.
func jcmdBodyError(command, body string) error {
	trimmed := strings.TrimSpace(body)
	for _, marker := range []string{
		"Unknown diagnostic command",
		"java.lang.IllegalArgumentException",
		"Invalid argument",
	} {
		if strings.HasPrefix(trimmed, marker) {
			return fmt.Errorf("JVM refused %q: %s", command, truncate(trimmed, 300))
		}
	}
	return nil
}

// Properties reads the target's system properties. apm2go uses it to confirm
// that an injected agent actually applied its configuration.
func Properties(ctx context.Context, opts Options) (map[string]string, error) {
	opts.applyDefaults()
	if opts.PID <= 0 {
		return nil, fmt.Errorf("invalid pid %d", opts.PID)
	}

	req, err := encodeRequest(cmdProperties)
	if err != nil {
		return nil, err
	}
	resp, err := execute(ctx, opts, req)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}
	return parseProperties(resp.Body), nil
}
