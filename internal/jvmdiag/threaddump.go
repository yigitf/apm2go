package jvmdiag

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ThreadDump is a parsed Thread.print result.
type ThreadDump struct {
	// Header is the JVM's own banner, e.g. "Full thread dump OpenJDK 64-Bit
	// Server VM (21.0.12+7 mixed mode, sharing)".
	Header string `json:"header,omitempty"`

	Threads []Thread `json:"threads"`
	// StateCounts is how many threads sit in each Thread.State, which is the
	// first thing worth knowing about a dump.
	StateCounts map[string]int `json:"state_counts"`
	// Deadlocks are cycles of threads each waiting for a lock another holds.
	Deadlocks []Deadlock `json:"deadlocks,omitempty"`
	// Pileups are frames many threads are sitting on at once — the shape a
	// saturated pool or a contended lock makes.
	Pileups []Pileup `json:"pileups,omitempty"`
}

// Thread is one thread's entry in a dump.
type Thread struct {
	Name     string `json:"name"`
	Number   int    `json:"number,omitempty"`
	Daemon   bool   `json:"daemon,omitempty"`
	Priority int    `json:"priority,omitempty"`
	// TID and NID are the JVM's and the operating system's identifiers. NID
	// matches the thread id in top and perf, which is how a dump is lined up
	// against a CPU profile.
	TID string `json:"tid,omitempty"`
	NID string `json:"nid,omitempty"`
	// State is the java.lang.Thread.State value; Detail is the parenthesised
	// qualifier, such as "on object monitor".
	State  string `json:"state"`
	Detail string `json:"detail,omitempty"`
	// VMInternal marks a thread belonging to the JVM itself — garbage
	// collection, JIT compilation, the VM thread. They are not Java threads and
	// have no Thread.State, so counting them as "unknown" would put the JVM's
	// own housekeeping at the top of a list meant to show what the application
	// is doing.
	VMInternal bool `json:"vm_internal,omitempty"`
	// Status is the JVM's own words from the header line, e.g. "waiting for
	// monitor entry". It says why a RUNNABLE-looking thread is not running.
	Status string `json:"status,omitempty"`
	// CPUms and ElapsedSec come from the header when the JVM reports them.
	CPUms      float64 `json:"cpu_ms,omitempty"`
	ElapsedSec float64 `json:"elapsed_sec,omitempty"`

	Frames []string `json:"frames,omitempty"`

	// WaitingOn is the lock this thread is trying to acquire, and Blocking is
	// what the JVM said about it. Only a genuine acquisition attempt is
	// recorded here: a thread inside Object.wait() has released its monitor and
	// is not blocked on anything.
	WaitingOn      string `json:"waiting_on,omitempty"`
	WaitingOnClass string `json:"waiting_on_class,omitempty"`
	// Holds lists the lock identities this thread owns, both object monitors
	// and ownable synchronizers such as ReentrantLock.
	Holds []string `json:"holds,omitempty"`
}

// Deadlock is a set of threads that can never proceed, because each waits for a
// lock the next one holds.
type Deadlock struct {
	// Threads names the participants, in cycle order.
	Threads []string `json:"threads"`
	// Source is "jvm" when HotSpot reported the deadlock itself and "inferred"
	// when apm2go found the cycle in the dump. The JVM's own detector does not
	// see every kind of cycle, so both are kept.
	Source string `json:"source"`
	// Detail is the JVM's description, when it gave one.
	Detail string `json:"detail,omitempty"`
}

// Pileup is a frame that many threads are sitting on simultaneously.
type Pileup struct {
	Frame  string   `json:"frame"`
	Count  int      `json:"count"`
	States []string `json:"states,omitempty"`
}

// pileupThreshold is how many threads must share a frame before it is worth
// reporting. Two threads in the same method is ordinary; a dozen is a queue.
const pileupThreshold = 3

// maxPileups bounds how many pile-ups are reported, so a dump of a thousand
// idle threads does not produce a wall of them.
const maxPileups = 10

var (
	// threadHeaderRe matches a thread's header line. The name is greedy so that
	// a thread whose own name contains a quote still ends at the real closing
	// one, which is always followed by whitespace.
	threadHeaderRe = regexp.MustCompile(`^"(.+)"\s+(.*)$`)
	threadNumberRe = regexp.MustCompile(`#(\d+)`)
	priorityRe     = regexp.MustCompile(`\bprio=(\d+)`)
	tidRe          = regexp.MustCompile(`\btid=(0x[0-9a-fA-F]+)`)
	nidRe          = regexp.MustCompile(`\bnid=(0x[0-9a-fA-F]+)`)
	cpuRe          = regexp.MustCompile(`\bcpu=([0-9.]+)ms`)
	elapsedRe      = regexp.MustCompile(`\belapsed=([0-9.]+)s`)
	// lockRe matches the lock annotations under a frame:
	//   - waiting to lock <0x...> (a java.lang.Object)
	//   - locked <0x...> (a java.lang.Object)
	//   - parking to wait for  <0x...> (a ...ReentrantLock$NonfairSync)
	lockRe = regexp.MustCompile(`^-\s+(.*?)\s*<(0x[0-9a-fA-F]+)>\s*(?:\(a\s+([^)]+)\))?`)
	// ownableRe matches an entry in the "Locked ownable synchronizers" list,
	// which has no verb: "- <0x...> (a ...ReentrantLock$NonfairSync)".
	ownableRe = regexp.MustCompile(`^-\s+<(0x[0-9a-fA-F]+)>`)
	// deadlockHeaderRe matches HotSpot's own finding, singular or plural.
	deadlockHeaderRe = regexp.MustCompile(`Found (?:one|\d+) Java-level deadlocks?:`)
	heldByRe         = regexp.MustCompile(`which is held by "(.*)"`)
	participantRe    = regexp.MustCompile(`^"(.+)":\s*$`)
)

// ParseThreadDump turns Thread.print output into structure.
//
// It never fails: a dump from a JVM whose format has drifted still yields its
// raw text, and a partially understood dump is more useful than none. Callers
// that need certainty check whether Threads came back empty.
func ParseThreadDump(raw string) *ThreadDump {
	body, deadlockSection := splitDeadlockSection(raw)

	dump := &ThreadDump{StateCounts: map[string]int{}}

	var current *Thread
	// inOwnable tracks the "Locked ownable synchronizers:" list, whose entries
	// look like lock lines but name locks the thread holds, not ones it wants.
	inOwnable := false

	flush := func() {
		if current != nil {
			classifyVMInternal(current)
			dump.Threads = append(dump.Threads, *current)
		}
		current = nil
	}

	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)

		if dump.Header == "" && strings.HasPrefix(trimmed, "Full thread dump") {
			dump.Header = trimmed
			continue
		}

		if m := threadHeaderRe.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, " ") {
			flush()
			inOwnable = false
			current = parseThreadHeader(m[1], m[2])
			continue
		}
		if current == nil {
			continue
		}

		switch {
		case strings.HasPrefix(trimmed, "java.lang.Thread.State:"):
			current.State, current.Detail = parseStateLine(trimmed)
			inOwnable = false
		case strings.HasPrefix(trimmed, "Locked ownable synchronizers:"):
			inOwnable = true
		case strings.HasPrefix(trimmed, "at "):
			current.Frames = append(current.Frames, strings.TrimSpace(trimmed[3:]))
			inOwnable = false
		case strings.HasPrefix(trimmed, "- "):
			applyLockLine(current, trimmed, inOwnable)
		case trimmed == "":
			// A blank line ends a thread's block, but the next header is what
			// actually starts the next one, so nothing to do.
		}
	}
	flush()

	for _, t := range dump.Threads {
		state := t.State
		if state == "" {
			state = "UNKNOWN"
		}
		dump.StateCounts[state]++
	}

	dump.Deadlocks = mergeDeadlocks(parseReportedDeadlocks(deadlockSection), inferDeadlocks(dump.Threads))
	dump.Pileups = findPileups(dump.Threads)
	return dump
}

// StateVMInternal is the state reported for the JVM's own threads, which have
// no java.lang.Thread.State because they are not Java threads.
const StateVMInternal = "VM_INTERNAL"

// classifyVMInternal labels a thread that belongs to the JVM rather than the
// application.
//
// The signal is that HotSpot printed neither a Thread.State line nor a thread
// number: both exist for every Java thread, including ones with no stack such
// as the Attach Listener, and neither exists for the garbage collector, the
// JIT compiler threads or the VM thread. A thread with frames but no state line
// is something else — a dump format we do not understand — and is left as
// unknown rather than quietly filed away as the JVM's own.
func classifyVMInternal(t *Thread) {
	if t.State != "" || t.Number != 0 || len(t.Frames) > 0 {
		return
	}
	t.VMInternal = true
	t.State = StateVMInternal
}

// splitDeadlockSection separates the thread listing from HotSpot's deadlock
// report. They must be parsed apart: the report names threads with lines that
// look enough like thread headers to be mistaken for a second copy of them.
func splitDeadlockSection(raw string) (body, deadlocks string) {
	loc := deadlockHeaderRe.FindStringIndex(raw)
	if loc == nil {
		return raw, ""
	}
	return raw[:loc[0]], raw[loc[0]:]
}

// parseThreadHeader reads the name and the attribute soup that follows it.
func parseThreadHeader(name, rest string) *Thread {
	t := &Thread{Name: name, Daemon: strings.Contains(rest, " daemon ") || strings.HasPrefix(rest, "daemon ")}

	if m := threadNumberRe.FindStringSubmatch(rest); m != nil {
		t.Number, _ = strconv.Atoi(m[1])
	}
	if m := priorityRe.FindStringSubmatch(rest); m != nil {
		t.Priority, _ = strconv.Atoi(m[1])
	}
	if m := tidRe.FindStringSubmatch(rest); m != nil {
		t.TID = m[1]
	}
	if m := nidRe.FindStringSubmatch(rest); m != nil {
		t.NID = m[1]
	}
	if m := cpuRe.FindStringSubmatch(rest); m != nil {
		t.CPUms, _ = strconv.ParseFloat(m[1], 64)
	}
	if m := elapsedRe.FindStringSubmatch(rest); m != nil {
		t.ElapsedSec, _ = strconv.ParseFloat(m[1], 64)
	}
	t.Status = parseStatus(rest)
	return t
}

// parseStatus recovers the JVM's plain-language status, which trails the
// key=value attributes and precedes the stack pointer in brackets.
func parseStatus(rest string) string {
	// Drop the trailing "[0x00007f...]" stack base, then take what follows the
	// last key=value pair.
	if i := strings.LastIndex(rest, "["); i >= 0 {
		rest = rest[:i]
	}
	fields := strings.Fields(rest)
	var words []string
	for _, f := range fields {
		if strings.Contains(f, "=") || strings.HasPrefix(f, "#") || f == "daemon" {
			words = words[:0]
			continue
		}
		words = append(words, f)
	}
	return strings.Join(words, " ")
}

// parseStateLine splits "java.lang.Thread.State: BLOCKED (on object monitor)".
func parseStateLine(line string) (state, detail string) {
	value := strings.TrimSpace(strings.TrimPrefix(line, "java.lang.Thread.State:"))
	state, rest, found := strings.Cut(value, "(")
	state = strings.TrimSpace(state)
	if !found {
		return state, ""
	}
	return state, strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), ")"))
}

// applyLockLine records what a "- ..." annotation says about this thread's
// relationship to a lock.
//
// Only "waiting to lock" and "parking to wait for" become a wait edge. A thread
// inside Object.wait() prints "- waiting on <addr>" but has *released* that
// monitor, so treating it as blocked would invent cycles that cannot happen.
func applyLockLine(t *Thread, line string, inOwnable bool) {
	if inOwnable {
		if m := ownableRe.FindStringSubmatch(line); m != nil {
			t.Holds = append(t.Holds, m[1])
		}
		return
	}

	m := lockRe.FindStringSubmatch(line)
	if m == nil {
		return
	}
	verb, addr, class := strings.TrimSpace(m[1]), m[2], strings.TrimSpace(m[3])

	switch {
	case strings.HasPrefix(verb, "locked"):
		t.Holds = append(t.Holds, addr)
	case strings.HasPrefix(verb, "waiting to lock"), strings.HasPrefix(verb, "parking to wait for"):
		if t.WaitingOn == "" {
			t.WaitingOn, t.WaitingOnClass = addr, class
		}
	}
}

// inferDeadlocks finds cycles in the wait-for graph the dump describes.
//
// This runs regardless of what the JVM reported: HotSpot's own detector covers
// object monitors and ownable synchronizers, but a dump collected from a JVM
// that did not run its detector, or a cycle it declined to report, still shows
// up here as long as the lock annotations are present.
func inferDeadlocks(threads []Thread) []Deadlock {
	ownerOf := map[string]int{}
	for i, t := range threads {
		for _, addr := range t.Holds {
			ownerOf[addr] = i
		}
	}

	// waitsFor is the edge set: thread i is blocked until thread waitsFor[i]
	// releases what it holds.
	waitsFor := make([]int, len(threads))
	for i := range threads {
		waitsFor[i] = -1
		if threads[i].WaitingOn == "" {
			continue
		}
		if owner, ok := ownerOf[threads[i].WaitingOn]; ok && owner != i {
			waitsFor[i] = owner
		}
	}

	const (
		unvisited = 0
		onStack   = 1
		done      = 2
	)
	mark := make([]int, len(threads))
	var out []Deadlock

	for start := range threads {
		if mark[start] != unvisited {
			continue
		}
		// Walk the chain from start; each node has at most one outgoing edge,
		// so a cycle is found by walking until a node repeats.
		var path []int
		node := start
		for node != -1 && mark[node] == unvisited {
			mark[node] = onStack
			path = append(path, node)
			node = waitsFor[node]
		}
		if node != -1 && mark[node] == onStack {
			// Found a cycle: it starts where the walk re-entered the path.
			at := 0
			for ; at < len(path) && path[at] != node; at++ {
			}
			names := make([]string, 0, len(path)-at)
			for _, idx := range path[at:] {
				names = append(names, threads[idx].Name)
			}
			out = append(out, Deadlock{Threads: names, Source: "inferred"})
		}
		for _, idx := range path {
			mark[idx] = done
		}
	}
	return out
}

// parseReportedDeadlocks reads HotSpot's own deadlock report.
func parseReportedDeadlocks(section string) []Deadlock {
	if strings.TrimSpace(section) == "" {
		return nil
	}

	var out []Deadlock
	var current *Deadlock
	var participant string

	commit := func() {
		if current != nil && len(current.Threads) > 0 {
			out = append(out, *current)
		}
		current = nil
	}

	for _, line := range strings.Split(section, "\n") {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r"))

		switch {
		case deadlockHeaderRe.MatchString(trimmed):
			// A dump can report several independent deadlocks in a row.
			commit()
			current = &Deadlock{Source: "jvm"}
		case strings.HasPrefix(trimmed, "Java stack information"):
			// The stacks that follow repeat the same participants.
			commit()
			return out
		case current == nil:
			// Text between reports; nothing to attribute it to.
		default:
			if m := participantRe.FindStringSubmatch(trimmed); m != nil {
				participant = m[1]
				current.Threads = append(current.Threads, participant)
				continue
			}
			if m := heldByRe.FindStringSubmatch(trimmed); m != nil && participant != "" && current.Detail == "" {
				current.Detail = participant + " is waiting for a lock held by " + m[1]
			}
		}
	}
	commit()
	return out
}

// mergeDeadlocks combines the JVM's findings with ours, preferring the JVM's
// wording for a cycle both found. Two reports describe the same deadlock when
// they name the same set of threads.
func mergeDeadlocks(reported, inferred []Deadlock) []Deadlock {
	seen := map[string]bool{}
	out := make([]Deadlock, 0, len(reported)+len(inferred))

	add := func(d Deadlock) {
		key := participantKey(d.Threads)
		if key == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, d)
	}
	for _, d := range reported {
		add(d)
	}
	for _, d := range inferred {
		add(d)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// participantKey identifies a cycle by its members, independent of where the
// walk happened to start.
func participantKey(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	return strings.Join(sorted, "\x00")
}

// blockingFrames are the primitives threads park in. They are skipped when
// choosing the frame that identifies a pile-up, because every blocked thread in
// the process shares them and grouping on them says nothing about where the
// threads actually are.
var blockingFrames = []string{
	"jdk.internal.misc.Unsafe.park",
	"sun.misc.Unsafe.park",
	"java.lang.Object.wait",
	"java.lang.Thread.sleep",
	"java.util.concurrent.locks.LockSupport.park",
	"java.util.concurrent.locks.AbstractQueuedSynchronizer",
	"sun.nio.ch.",
}

// findPileups groups threads by the frame that best identifies where they are.
func findPileups(threads []Thread) []Pileup {
	type group struct {
		count  int
		states map[string]bool
	}
	groups := map[string]*group{}

	for _, t := range threads {
		frame := signatureFrame(t.Frames)
		if frame == "" {
			continue
		}
		g, ok := groups[frame]
		if !ok {
			g = &group{states: map[string]bool{}}
			groups[frame] = g
		}
		g.count++
		if t.State != "" {
			g.states[t.State] = true
		}
	}

	out := make([]Pileup, 0, len(groups))
	for frame, g := range groups {
		if g.count < pileupThreshold {
			continue
		}
		states := make([]string, 0, len(g.states))
		for s := range g.states {
			states = append(states, s)
		}
		sort.Strings(states)
		out = append(out, Pileup{Frame: frame, Count: g.count, States: states})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Frame < out[j].Frame
	})
	if len(out) > maxPileups {
		out = out[:maxPileups]
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// signatureFrame picks the topmost frame that is not a blocking primitive, so
// threads queued on the same lock group by the code that queued them rather
// than by the park call they all share.
func signatureFrame(frames []string) string {
	for _, f := range frames {
		if !isBlockingFrame(f) {
			return f
		}
	}
	if len(frames) > 0 {
		return frames[0]
	}
	return ""
}

func isBlockingFrame(frame string) bool {
	for _, p := range blockingFrames {
		if strings.HasPrefix(frame, p) {
			return true
		}
	}
	return false
}
