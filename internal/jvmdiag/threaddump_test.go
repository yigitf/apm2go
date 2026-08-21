package jvmdiag

import (
	"sort"
	"strings"
	"testing"
)

// deadlockDump is a Thread.print reply from a JVM holding a two-thread
// deadlock, with the shapes that matter alongside it: a parked main thread that
// must not be read as blocked, and an idle Jetty pool that should group.
const deadlockDump = `2026-08-19 12:00:00
Full thread dump OpenJDK 64-Bit Server VM (21.0.12+7-LTS mixed mode, sharing):

Threads class SMR info:
_java_thread_list=0x00007f8c1c0b2800, length=6, elements={
0x00007f8c1c0b2810, 0x00007f8c1c0b2820
}

"main" #1 [3654] prio=5 os_prio=0 cpu=120.50ms elapsed=95.20s tid=0x00007f8c1c0b2800 nid=0xe46 waiting on condition  [0x00007f8c0a1fe000]
   java.lang.Thread.State: WAITING (parking)
    at jdk.internal.misc.Unsafe.park(java.base@21.0.12/Native Method)
    - parking to wait for  <0x00000000e0d3a000> (a java.util.concurrent.CountDownLatch$Sync)
    at java.util.concurrent.locks.LockSupport.park(java.base@21.0.12/LockSupport.java:221)
    at ChainNode.main(ChainNode.java:30)

   Locked ownable synchronizers:
    - None

"deadlock-a" #20 daemon prio=5 os_prio=0 cpu=1.20ms elapsed=90.00s tid=0x00007f8c1c0b3000 nid=0xe50 waiting for monitor entry  [0x00007f8c0a2fe000]
   java.lang.Thread.State: BLOCKED (on object monitor)
    at ChainNode$Deadlock.second(ChainNode.java:120)
    - waiting to lock <0x00000000e0d3b000> (a java.lang.Object)
    - locked <0x00000000e0d3a800> (a java.lang.Object)
    at ChainNode$Deadlock.lambda$start$0(ChainNode.java:110)

   Locked ownable synchronizers:
    - None

"deadlock-b" #21 daemon prio=5 os_prio=0 cpu=1.10ms elapsed=90.00s tid=0x00007f8c1c0b3100 nid=0xe51 waiting for monitor entry  [0x00007f8c0a3fe000]
   java.lang.Thread.State: BLOCKED (on object monitor)
    at ChainNode$Deadlock.first(ChainNode.java:130)
    - waiting to lock <0x00000000e0d3a800> (a java.lang.Object)
    - locked <0x00000000e0d3b000> (a java.lang.Object)
    at ChainNode$Deadlock.lambda$start$1(ChainNode.java:112)

   Locked ownable synchronizers:
    - None

"qtp-1" #30 daemon prio=5 os_prio=0 tid=0x00007f8c1c0b4000 nid=0xe60 waiting on condition  [0x00007f8c0a4fe000]
   java.lang.Thread.State: TIMED_WAITING (parking)
    at jdk.internal.misc.Unsafe.park(java.base@21.0.12/Native Method)
    at org.eclipse.jetty.util.thread.QueuedThreadPool.idleJobPoll(QueuedThreadPool.java:974)
    at org.eclipse.jetty.util.thread.QueuedThreadPool$Runner.run(QueuedThreadPool.java:1018)

"qtp-2" #31 daemon prio=5 os_prio=0 tid=0x00007f8c1c0b4100 nid=0xe61 waiting on condition  [0x00007f8c0a5fe000]
   java.lang.Thread.State: TIMED_WAITING (parking)
    at jdk.internal.misc.Unsafe.park(java.base@21.0.12/Native Method)
    at org.eclipse.jetty.util.thread.QueuedThreadPool.idleJobPoll(QueuedThreadPool.java:974)
    at org.eclipse.jetty.util.thread.QueuedThreadPool$Runner.run(QueuedThreadPool.java:1018)

"qtp-3" #32 daemon prio=5 os_prio=0 tid=0x00007f8c1c0b4200 nid=0xe62 waiting on condition  [0x00007f8c0a6fe000]
   java.lang.Thread.State: TIMED_WAITING (parking)
    at jdk.internal.misc.Unsafe.park(java.base@21.0.12/Native Method)
    at org.eclipse.jetty.util.thread.QueuedThreadPool.idleJobPoll(QueuedThreadPool.java:974)
    at org.eclipse.jetty.util.thread.QueuedThreadPool$Runner.run(QueuedThreadPool.java:1018)

Found one Java-level deadlock:
=============================
"deadlock-a":
  waiting to lock monitor 0x00007f8c1c0c0000 (object 0x00000000e0d3b000, a java.lang.Object),
  which is held by "deadlock-b"
"deadlock-b":
  waiting to lock monitor 0x00007f8c1c0c0100 (object 0x00000000e0d3a800, a java.lang.Object),
  which is held by "deadlock-a"

Java stack information for the threads listed above:
===================================================
"deadlock-a":
    at ChainNode$Deadlock.second(ChainNode.java:120)
    - waiting to lock <0x00000000e0d3b000> (a java.lang.Object)

Found 1 deadlock.
`

func TestParseThreadDumpThreads(t *testing.T) {
	dump := ParseThreadDump(deadlockDump)

	if want := "Full thread dump OpenJDK 64-Bit Server VM (21.0.12+7-LTS mixed mode, sharing):"; dump.Header != want {
		t.Errorf("Header = %q, want %q", dump.Header, want)
	}
	// The deadlock report repeats two of the threads; parsing must not count
	// them a second time.
	if len(dump.Threads) != 6 {
		names := make([]string, len(dump.Threads))
		for i, th := range dump.Threads {
			names[i] = th.Name
		}
		t.Fatalf("parsed %d threads (%v), want 6", len(dump.Threads), names)
	}

	want := map[string]int{"WAITING": 1, "BLOCKED": 2, "TIMED_WAITING": 3}
	for state, count := range want {
		if dump.StateCounts[state] != count {
			t.Errorf("StateCounts[%s] = %d, want %d", state, dump.StateCounts[state], count)
		}
	}

	main := findThread(t, dump, "main")
	if main.Daemon {
		t.Error("main reported as a daemon thread")
	}
	if main.Number != 1 || main.Priority != 5 || main.NID != "0xe46" {
		t.Errorf("main header parsed as number=%d prio=%d nid=%s", main.Number, main.Priority, main.NID)
	}
	if main.CPUms != 120.50 || main.ElapsedSec != 95.20 {
		t.Errorf("main cpu=%v elapsed=%v, want 120.5 and 95.2", main.CPUms, main.ElapsedSec)
	}
	if main.Status != "waiting on condition" {
		t.Errorf("main Status = %q, want %q", main.Status, "waiting on condition")
	}
	if len(main.Frames) != 3 || main.Frames[2] != "ChainNode.main(ChainNode.java:30)" {
		t.Errorf("main frames = %v", main.Frames)
	}

	a := findThread(t, dump, "deadlock-a")
	if !a.Daemon {
		t.Error("deadlock-a not reported as a daemon thread")
	}
	if a.State != "BLOCKED" || a.Detail != "on object monitor" {
		t.Errorf("deadlock-a state = %q/%q", a.State, a.Detail)
	}
	if a.WaitingOn != "0x00000000e0d3b000" {
		t.Errorf("deadlock-a WaitingOn = %q", a.WaitingOn)
	}
	if a.WaitingOnClass != "java.lang.Object" {
		t.Errorf("deadlock-a WaitingOnClass = %q", a.WaitingOnClass)
	}
	if len(a.Holds) != 1 || a.Holds[0] != "0x00000000e0d3a800" {
		t.Errorf("deadlock-a Holds = %v", a.Holds)
	}
}

// A parked thread is waiting for something no one holds. Recording that as a
// wait edge is how a healthy pool turns into an imaginary deadlock.
func TestParseThreadDumpParkedThreadIsNotBlocked(t *testing.T) {
	dump := ParseThreadDump(deadlockDump)

	main := findThread(t, dump, "main")
	if main.WaitingOn != "0x00000000e0d3a000" {
		t.Errorf("main WaitingOn = %q, want the latch it parked on", main.WaitingOn)
	}
	for _, d := range dump.Deadlocks {
		for _, name := range d.Threads {
			if name == "main" {
				t.Fatalf("main reported as part of a deadlock: %+v", d)
			}
		}
	}
}

func TestParseThreadDumpDeadlockReportedByJVM(t *testing.T) {
	dump := ParseThreadDump(deadlockDump)

	if len(dump.Deadlocks) != 1 {
		t.Fatalf("found %d deadlocks, want 1: %+v", len(dump.Deadlocks), dump.Deadlocks)
	}
	d := dump.Deadlocks[0]
	if d.Source != "jvm" {
		t.Errorf("Source = %q, want jvm (the JVM reported this one itself)", d.Source)
	}
	assertParticipants(t, d.Threads, "deadlock-a", "deadlock-b")
	if !strings.Contains(d.Detail, "deadlock-b") {
		t.Errorf("Detail = %q, want it to name the holder", d.Detail)
	}
}

// A dump collected from a JVM that did not print its own deadlock report still
// describes the cycle in its lock annotations.
func TestParseThreadDumpDeadlockInferredWithoutJVMReport(t *testing.T) {
	raw, _, found := strings.Cut(deadlockDump, "Found one Java-level deadlock:")
	if !found {
		t.Fatal("fixture no longer contains a JVM deadlock report")
	}

	dump := ParseThreadDump(raw)
	if len(dump.Deadlocks) != 1 {
		t.Fatalf("found %d deadlocks, want 1: %+v", len(dump.Deadlocks), dump.Deadlocks)
	}
	if dump.Deadlocks[0].Source != "inferred" {
		t.Errorf("Source = %q, want inferred", dump.Deadlocks[0].Source)
	}
	assertParticipants(t, dump.Deadlocks[0].Threads, "deadlock-a", "deadlock-b")
}

// ReentrantLock ownership is reported in the "Locked ownable synchronizers"
// list rather than as a "locked" annotation, so a cycle through one is only
// visible if that list is read.
func TestParseThreadDumpInfersReentrantLockCycle(t *testing.T) {
	const raw = `Full thread dump OpenJDK 64-Bit Server VM (21.0.12 mixed mode):

"lock-a" #10 prio=5 tid=0x00007f00 nid=0x1 waiting on condition  [0x00007f10]
   java.lang.Thread.State: WAITING (parking)
    at jdk.internal.misc.Unsafe.park(java.base@21.0.12/Native Method)
    - parking to wait for  <0x00000000e0000001> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)
    at Service.transfer(Service.java:40)

   Locked ownable synchronizers:
    - <0x00000000e0000002> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)

"lock-b" #11 prio=5 tid=0x00007f01 nid=0x2 waiting on condition  [0x00007f11]
   java.lang.Thread.State: WAITING (parking)
    at jdk.internal.misc.Unsafe.park(java.base@21.0.12/Native Method)
    - parking to wait for  <0x00000000e0000002> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)
    at Service.transfer(Service.java:40)

   Locked ownable synchronizers:
    - <0x00000000e0000001> (a java.util.concurrent.locks.ReentrantLock$NonfairSync)
`

	dump := ParseThreadDump(raw)
	if len(dump.Deadlocks) != 1 {
		t.Fatalf("found %d deadlocks, want 1: %+v", len(dump.Deadlocks), dump.Deadlocks)
	}
	assertParticipants(t, dump.Deadlocks[0].Threads, "lock-a", "lock-b")
}

// The JVM's own threads carry no Thread.State because they are not Java
// threads. Counting them as "unknown" puts garbage collection and JIT
// compilation at the top of a list meant to show what the application is doing.
func TestParseThreadDumpLabelsVMInternalThreads(t *testing.T) {
	const raw = `Full thread dump OpenJDK 64-Bit Server VM (21.0.12 mixed mode):

"Attach Listener" #40 [197] daemon prio=9 os_prio=0 cpu=2493.86ms elapsed=68.23s tid=0x0000ffff6c000f60 nid=197 waiting on condition  [0x0000000000000000]
   java.lang.Thread.State: RUNNABLE

"VM Thread" os_prio=0 cpu=97.39ms elapsed=77.33s tid=0x0000ffffb4138410 nid=29 runnable

"GC Thread#0" os_prio=0 cpu=22.35ms elapsed=77.35s tid=0x0000ffffb40812e0 nid=17 runnable

JNI global refs: 17, weak refs: 0
`

	dump := ParseThreadDump(raw)

	if n := dump.StateCounts["UNKNOWN"]; n != 0 {
		t.Errorf("StateCounts[UNKNOWN] = %d, want 0", n)
	}
	if n := dump.StateCounts[StateVMInternal]; n != 2 {
		t.Errorf("StateCounts[%s] = %d, want 2", StateVMInternal, n)
	}

	// A Java thread with a state but no stack is still a Java thread.
	listener := findThread(t, dump, "Attach Listener")
	if listener.VMInternal {
		t.Error("the Attach Listener was filed as a JVM-internal thread")
	}
	if listener.State != "RUNNABLE" {
		t.Errorf("Attach Listener state = %q", listener.State)
	}

	for _, name := range []string{"VM Thread", "GC Thread#0"} {
		th := findThread(t, dump, name)
		if !th.VMInternal {
			t.Errorf("%s was not recognised as a JVM-internal thread", name)
		}
	}
}

func TestParseThreadDumpPileups(t *testing.T) {
	dump := ParseThreadDump(deadlockDump)

	const want = "org.eclipse.jetty.util.thread.QueuedThreadPool.idleJobPoll(QueuedThreadPool.java:974)"
	for _, p := range dump.Pileups {
		if p.Frame == want {
			if p.Count != 3 {
				t.Errorf("pileup count = %d, want 3", p.Count)
			}
			// Grouping on the park primitive every blocked thread shares would
			// say nothing about where the threads actually are.
			if strings.Contains(p.Frame, "Unsafe.park") {
				t.Errorf("pileup grouped on the blocking primitive: %q", p.Frame)
			}
			return
		}
	}
	t.Errorf("no pileup on the idle pool frame; got %+v", dump.Pileups)
}

// Nothing here may panic on input it does not recognise: the alternative to a
// partial parse is losing the dump entirely.
func TestParseThreadDumpTolerantOfJunk(t *testing.T) {
	for _, raw := range []string{"", "\n\n", "not a thread dump at all", `"unterminated`} {
		dump := ParseThreadDump(raw)
		if dump == nil {
			t.Fatalf("ParseThreadDump(%q) = nil", raw)
		}
	}
}

func findThread(t *testing.T, dump *ThreadDump, name string) Thread {
	t.Helper()
	for _, th := range dump.Threads {
		if th.Name == name {
			return th
		}
	}
	t.Fatalf("thread %q not found in dump", name)
	return Thread{}
}

func assertParticipants(t *testing.T, got []string, want ...string) {
	t.Helper()
	sorted := append([]string(nil), got...)
	sort.Strings(sorted)
	sort.Strings(want)
	if strings.Join(sorted, ",") != strings.Join(want, ",") {
		t.Errorf("participants = %v, want %v", got, want)
	}
}
