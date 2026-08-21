// Package jvmdiag runs HotSpot diagnostic commands against an already-running
// JVM and turns their output into something a UI can render.
//
// The transport is the attach channel apm2go already opens to inject agents:
// HotSpot's AttachListener serves a "jcmd" command that runs any diagnostic the
// jcmd tool can, so a thread dump costs no new mechanism, no JDK on the host and
// no restart of the target.
//
// These commands are not free to the target. Thread.print and
// GC.class_histogram both run at a safepoint, which stops the application for
// as long as the walk takes — milliseconds for a small heap, longer for a large
// one. That is why nothing here runs on a timer: every command is the result of
// an operator asking for it.
package jvmdiag

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/apm2go/apm2go/internal/attach"
	"github.com/apm2go/apm2go/internal/discovery"
)

// The diagnostic commands apm2go issues. These strings are HotSpot's own
// command names, passed through verbatim.
const (
	// CommandThreadDump lists every thread with its stack, and the JVM's own
	// deadlock findings when it has any.
	CommandThreadDump = "Thread.print"
	// CommandClassHistogram counts live instances and bytes per class.
	CommandClassHistogram = "GC.class_histogram"
	// CommandHeapInfo summarises the heap and metaspace.
	CommandHeapInfo = "GC.heap_info"
	// CommandVMFlags lists the flags the JVM is running with.
	CommandVMFlags = "VM.flags -all"
)

// Kind names a stored diagnostic, and is what the API and the UI address them
// by. It is deliberately separate from the command string so that changing how
// a dump is collected does not invalidate the ones already stored.
type Kind string

const (
	KindThreadDump     Kind = "thread_dump"
	KindClassHistogram Kind = "class_histogram"
	KindHeapInfo       Kind = "heap_info"
	KindVMFlags        Kind = "vm_flags"
)

// commandFor maps a Kind to the HotSpot command that produces it.
var commandFor = map[Kind]string{
	KindThreadDump:     CommandThreadDump,
	KindClassHistogram: CommandClassHistogram,
	KindHeapInfo:       CommandHeapInfo,
	KindVMFlags:        CommandVMFlags,
}

// Valid reports whether k is a diagnostic apm2go knows how to collect.
func (k Kind) Valid() bool {
	_, ok := commandFor[k]
	return ok
}

// Kinds lists every collectable diagnostic, in the order the UI offers them.
func Kinds() []Kind {
	return []Kind{KindThreadDump, KindClassHistogram, KindHeapInfo, KindVMFlags}
}

// maxResponseBytes bounds a diagnostic reply. A class histogram carries a row
// per loaded class, so an application with a large dependency tree runs to
// megabytes; the default attach bound would cut it off mid-table.
const maxResponseBytes = 32 << 20

// defaultTimeout bounds a single command. It is generous because the target may
// be at a safepoint doing the work we asked for, and cutting the connection
// then leaves us without the answer while the target paid the cost anyway.
const defaultTimeout = 60 * time.Second

// HeapDumpCommand is the one diagnostic apm2go will not run.
//
// GC.heap_dump writes the entire heap to disk: gigabytes of file, and a
// safepoint held for the duration. On a live system that is an outage, not an
// observation. The UI shows this command so an operator can run it deliberately
// during a maintenance window, and apm2go never issues it.
const HeapDumpCommand = "GC.heap_dump -all=true /path/to/dump.hprof"

// Client runs diagnostic commands against targets on this host.
type Client struct {
	runner   *attach.Runner
	procRoot string
	timeout  time.Duration
	log      *slog.Logger
}

// New returns a Client that reaches targets through procRoot.
//
// helperPath is apm2go-attach-helper, already staged to disk — see
// internal/attachhelper and attach.Runner's own comment for why a diagnostic
// command needs it just as much as injection does: it rides the same attach
// channel, with the same peer-credential and /proc/<pid>/root requirements.
func New(procRoot, helperPath string, log *slog.Logger) *Client {
	if procRoot == "" {
		procRoot = "/proc"
	}
	return &Client{
		runner:   attach.NewRunner(helperPath, log),
		procRoot: procRoot,
		timeout:  defaultTimeout,
		log:      log,
	}
}

// Dump is one collected diagnostic, raw output plus whatever structure the
// parser could recover from it.
type Dump struct {
	Kind       Kind      `json:"kind"`
	PID        int       `json:"pid"`
	Service    string    `json:"service,omitempty"`
	CapturedAt time.Time `json:"captured_at"`
	// DurationMS is how long the command took, which is roughly how long the
	// target was paused. The UI shows it so the cost is visible after the fact.
	DurationMS int64 `json:"duration_ms"`
	// Raw is the command's verbatim output, kept because a parser that misreads
	// a future JVM's format must not lose the evidence.
	Raw string `json:"raw,omitempty"`

	// Exactly one of these is set, according to Kind. Threads and Histogram are
	// what the UI renders; the other kinds are read as text.
	Threads   *ThreadDump     `json:"threads,omitempty"`
	Histogram *ClassHistogram `json:"histogram,omitempty"`
	Heap      *HeapInfo       `json:"heap,omitempty"`
}

// Headline reduces a dump to the few counts a history list shows.
//
// It exists so that choosing between two stored dumps never costs reading
// either of them: a thread dump of a busy application is megabytes, and the
// question the list answers is only "which one do I want".
func (d *Dump) Headline() map[string]any {
	out := map[string]any{}
	switch {
	case d.Threads != nil:
		out["threads"] = len(d.Threads.Threads)
		out["deadlocks"] = len(d.Threads.Deadlocks)
		out["pileups"] = len(d.Threads.Pileups)
		out["states"] = d.Threads.StateCounts
	case d.Histogram != nil:
		out["classes"] = d.Histogram.ClassCount
		out["total_bytes"] = d.Histogram.TotalBytes
		out["total_instances"] = d.Histogram.TotalInstances
	case d.Heap != nil:
		out["used_bytes"] = d.Heap.UsedBytes
		out["total_bytes"] = d.Heap.TotalBytes
		if d.Heap.Metaspace != nil {
			out["metaspace_used_bytes"] = d.Heap.Metaspace.UsedBytes
		}
	default:
		// VM.flags and anything else without a parser: the size is all the
		// list can honestly say about it.
		out["bytes"] = len(d.Raw)
	}
	return out
}

// Collect runs one diagnostic against a JVM and parses the result.
//
// Unlike injection this does not check jvm.Attachable(): that check exists to
// stop apm2go loading a second agent into a JVM it has already instrumented,
// which has nothing to do with jcmd. A permanently-instrumented JVM — the
// steady state a production install is meant to reach — is exactly as live and
// exactly as diagnosable as any other; refusing it here would make the most
// common case the one apm2go cannot look inside.
func (c *Client) Collect(ctx context.Context, jvm *discovery.JVM, kind Kind) (*Dump, error) {
	command, ok := commandFor[kind]
	if !ok {
		return nil, fmt.Errorf("unknown diagnostic %q", kind)
	}

	start := time.Now()
	raw, err := c.runner.JCmd(ctx, c.optionsFor(jvm), command)
	if err != nil {
		return nil, fmt.Errorf("run %s on pid %d: %w", command, jvm.PID, err)
	}
	elapsed := time.Since(start)

	if c.log != nil {
		c.log.Info("collected JVM diagnostic",
			"pid", jvm.PID, "service", jvm.ServiceName,
			"kind", kind, "took", elapsed.Round(time.Millisecond), "bytes", len(raw))
	}

	dump := &Dump{
		Kind:       kind,
		PID:        jvm.PID,
		Service:    jvm.ServiceName,
		CapturedAt: start,
		DurationMS: elapsed.Milliseconds(),
		Raw:        raw,
	}
	switch kind {
	case KindThreadDump:
		dump.Threads = ParseThreadDump(raw)
	case KindClassHistogram:
		dump.Histogram = ParseClassHistogram(raw)
	case KindHeapInfo:
		dump.Heap = ParseHeapInfo(raw)
	}
	return dump, nil
}

// optionsFor mirrors the injector's attach options: same target identity, same
// privilege drop, with a bound sized for diagnostic output.
func (c *Client) optionsFor(jvm *discovery.JVM) attach.Options {
	return attach.Options{
		ProcRoot:         c.procRoot,
		PID:              jvm.PID,
		NSPid:            jvm.NSPid,
		UID:              jvm.UID,
		GID:              jvm.GID,
		Timeout:          c.timeout,
		MaxResponseBytes: maxResponseBytes,
	}
}
