// Package attach injects a Java agent into an already-running JVM using
// HotSpot's dynamic attach mechanism, so instrumented applications never need
// a restart.
//
// The mechanism is a unix socket handshake that the JVM's AttachListener thread
// serves. That thread is not started by default: the first attacher creates a
// well-known trigger file next to the process and sends it SIGQUIT, which the
// JVM interprets as "start listening" rather than "dump threads".
//
// This package reimplements the client side of that handshake in Go, so apm2go
// needs neither a JDK on the host nor a helper process per attach.
package attach

import (
	"bytes"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// protocolVersion is the only version HotSpot has ever spoken.
const protocolVersion = "1"

// Command names understood by the AttachListener.
const (
	// cmdLoad loads a JVMTI agent library. Loading a Java agent jar means
	// loading the built-in "instrument" library with the jar as its option.
	cmdLoad = "load"
	// cmdProperties dumps the target's system properties, which is how we
	// confirm an agent took effect.
	cmdProperties = "properties"
	// cmdJCmd runs a diagnostic command; used for liveness checks.
	cmdJCmd = "jcmd"
)

// instrumentLibrary is the JVM's built-in JPLIS agent, responsible for loading
// java.lang.instrument agents from a jar.
const instrumentLibrary = "instrument"

// maxResponseBytes bounds how much of a JVM's reply we buffer. Replies are
// normally a few hundred bytes; the properties dump can reach tens of kilobytes.
//
// Diagnostic commands are the exception: a class histogram of a large heap runs
// to several megabytes, so those callers raise the bound through
// Options.MaxResponseBytes rather than silently reading a truncated dump.
const maxResponseBytes = 1 << 20

// encodeRequest builds an attach request frame.
//
// The frame is the protocol version followed by the command and exactly three
// arguments, each terminated by a NUL byte. Unused arguments are sent as empty
// strings — the listener reads a fixed count and would otherwise block.
func encodeRequest(command string, args ...string) ([]byte, error) {
	if len(args) > 3 {
		return nil, fmt.Errorf("attach protocol accepts at most 3 arguments, got %d", len(args))
	}
	// An embedded NUL would desynchronise the framing.
	for _, a := range args {
		if strings.ContainsRune(a, 0) {
			return nil, fmt.Errorf("attach argument contains NUL byte: %q", a)
		}
	}

	var buf bytes.Buffer
	buf.WriteString(protocolVersion)
	buf.WriteByte(0)
	buf.WriteString(command)
	buf.WriteByte(0)
	for i := 0; i < 3; i++ {
		if i < len(args) {
			buf.WriteString(args[i])
		}
		buf.WriteByte(0)
	}
	return buf.Bytes(), nil
}

// jcmdRequest builds the frame that runs a diagnostic command.
//
// The whole command line goes in a single argument, exactly as jcmd sends it:
// "Thread.print" or "GC.class_histogram -all".
func jcmdRequest(command string) ([]byte, error) {
	if strings.TrimSpace(command) == "" {
		return nil, fmt.Errorf("diagnostic command is required")
	}
	return encodeRequest(cmdJCmd, command)
}

// loadAgentRequest builds the frame that loads a Java agent jar.
//
// The three arguments are the library name, whether the library path is
// absolute, and the option string. For the instrument library the option string
// is "<jar path>=<agent options>", which is exactly the -javaagent syntax.
func loadAgentRequest(jarPath, agentOptions string) ([]byte, error) {
	options := jarPath
	if agentOptions != "" {
		options = jarPath + "=" + agentOptions
	}
	return encodeRequest(cmdLoad, instrumentLibrary, "false", options)
}

// Response is a decoded reply from the AttachListener.
type Response struct {
	// Code is the listener's result: 0 means the command was accepted.
	Code int
	// Body is everything after the first line.
	Body string
	// AgentCode is the inner result of an agent load. The listener reports
	// success as soon as it dispatches the command, so a failing agent shows up
	// only as a "return code: N" line in the body.
	AgentCode int
	// HasAgentCode distinguishes "agent reported 0" from "no agent code seen".
	HasAgentCode bool
	// Truncated reports that the reply hit the read bound and the body is only
	// its beginning. A partial thread dump parses without complaint and would
	// otherwise be read as a complete one.
	Truncated bool
}

// OK reports whether both the listener and the agent, if it reported at all,
// succeeded.
func (r *Response) OK() bool {
	if r.Code != 0 {
		return false
	}
	return !r.HasAgentCode || r.AgentCode == 0
}

// Err renders a diagnosable error for a failed response, or nil when it
// succeeded. JVM-side messages are passed through verbatim: they name the real
// cause, such as dynamic agent loading being disabled.
func (r *Response) Err() error {
	if r.OK() {
		return nil
	}
	msg := strings.TrimSpace(r.Body)
	switch {
	case r.Code != 0 && msg != "":
		return fmt.Errorf("JVM rejected the request (code %d): %s", r.Code, msg)
	case r.Code != 0:
		return fmt.Errorf("JVM rejected the request (code %d)", r.Code)
	default:
		return fmt.Errorf("agent failed to load (return code %d): %s", r.AgentCode, explainAgentCode(r.AgentCode, msg))
	}
}

// agentReturnCodePrefix is how the instrument library reports its own outcome.
const agentReturnCodePrefix = "return code: "

// decodeResponse parses a reply frame: a decimal result code on the first line,
// then a free-form message. limit bounds the body; zero means the default.
func decodeResponse(r io.Reader, limit int) (*Response, error) {
	if limit <= 0 {
		limit = maxResponseBytes
	}
	// Read one byte past the bound so hitting it is distinguishable from a
	// reply that happens to end exactly there.
	data, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("read attach response: %w", err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("empty attach response (the JVM closed the connection)")
	}

	truncated := false
	if len(data) > limit {
		data, truncated = data[:limit], true
	}

	text := string(data)
	firstLine, rest, _ := strings.Cut(text, "\n")

	code, err := strconv.Atoi(strings.TrimSpace(firstLine))
	if err != nil {
		return nil, fmt.Errorf("malformed attach response %q", truncate(text, 200))
	}

	resp := &Response{Code: code, Body: rest, Truncated: truncated}

	// The instrument library appends its own status; find it anywhere in the
	// body, since JVM builds differ in what they print around it.
	for _, line := range strings.Split(rest, "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, agentReturnCodePrefix); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(after)); err == nil {
				resp.AgentCode, resp.HasAgentCode = n, true
			}
		}
	}
	return resp, nil
}

// explainAgentCode turns the JPLIS agent's numeric status into something an
// operator can act on. The values come from the JVM's AgentInitializationError.
func explainAgentCode(code int, msg string) string {
	var reason string
	switch code {
	case 1:
		reason = "the agent jar could not be opened or is unreadable by the target process"
	case 2:
		reason = "the agent jar has no Agent-Class or Premain-Class manifest attribute"
	case 3:
		reason = "the agent class could not be loaded"
	case 4:
		reason = "the agent's agentmain method threw an exception"
	default:
		// Codes 0-4 are java.instrument's own. Anything else came from deeper
		// in the JVM — JVMTI, or a JDK that refuses dynamic agents — and this
		// package cannot name it without inventing a meaning. What it can do is
		// say where the answer is: a JVM that rejects an agent almost always
		// writes the reason to its own stdout or stderr, which for a container
		// is `docker logs`, and which nothing in apm2go can see.
		reason = "the JVM rejected the agent with a code java.instrument does not define; " +
			"the reason is in the target process's own stdout or stderr"
	}
	if msg = strings.TrimSpace(msg); msg != "" {
		return reason + "; JVM said: " + truncate(msg, 500)
	}
	return reason
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
