package ebpf

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSocketLink(t *testing.T) {
	tests := []struct {
		target string
		inode  uint64
		ok     bool
	}{
		{"socket:[123456]", 123456, true},
		{"socket:[1]", 1, true},
		{"/dev/pts/3", 0, false},
		{"pipe:[789]", 0, false},
		{"anon_inode:[eventpoll]", 0, false},
		{"", 0, false},
	}
	for _, tt := range tests {
		inode, ok := parseSocketLink(tt.target)
		if ok != tt.ok || inode != tt.inode {
			t.Errorf("parseSocketLink(%q) = %d, %v; want %d, %v", tt.target, inode, ok, tt.inode, tt.ok)
		}
	}
}

func TestParseLocalPort(t *testing.T) {
	tests := []struct {
		addr string
		port int
		ok   bool
	}{
		{"0100007F:1F90", 8080, true}, // 127.0.0.1:8080
		{"00000000:0050", 80, true},   // 0.0.0.0:80
		{"malformed", 0, false},
	}
	for _, tt := range tests {
		port, ok := parseLocalPort(tt.addr)
		if ok != tt.ok || port != tt.port {
			t.Errorf("parseLocalPort(%q) = %d, %v; want %d, %v", tt.addr, port, ok, tt.port, tt.ok)
		}
	}
}

// A fixture shaped like /proc/net/tcp: header, one listening socket, one
// established connection, and one listening socket that belongs to a
// different process. Only the first should be reported.
const procNetTCPFixture = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 555555 1 0000000000000000 100 0 0 10 0
   1: 0100007F:1F90 0100007F:E8AC 01 00000000:00000000 00:00000000 00000000     0        0 666666 1 0000000000000000 100 0 0 10 0
   2: 00000000:0050 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 777777 1 0000000000000000 100 0 0 10 0
`

func TestListeningPortsInTable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	if err := os.WriteFile(path, []byte(procNetTCPFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// Only the inode for the listening socket at row 0 (port 8080) is owned by
	// our fictitious process; row 2 listens too, but its inode belongs to
	// something else and must not leak into the result.
	ports, err := listeningPortsInTable(path, map[uint64]bool{555555: true})
	if err != nil {
		t.Fatalf("listeningPortsInTable: %v", err)
	}
	if len(ports) != 1 || ports[0] != 8080 {
		t.Errorf("ports = %v, want [8080]", ports)
	}
}

func TestListeningPortsInTableIgnoresNonListeningState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tcp")
	if err := os.WriteFile(path, []byte(procNetTCPFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// Inode 666666 is the established connection on row 1 (state 01, not 0A).
	// Owning that inode must not produce a port: an outbound connection is not
	// a service to instrument.
	ports, err := listeningPortsInTable(path, map[uint64]bool{666666: true})
	if err != nil {
		t.Fatalf("listeningPortsInTable: %v", err)
	}
	if len(ports) != 0 {
		t.Errorf("ports = %v, want none", ports)
	}
}

// listenPorts must work end to end against the real /proc of the process
// running this test, which always has at least one open file descriptor —
// this is what proves socketInodes and the table scan compose correctly,
// without needing a real listening socket to assert on.
func TestListenPortsAgainstRealProc(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc on this platform")
	}
	if _, err := listenPorts("/proc", os.Getpid()); err != nil {
		t.Errorf("listenPorts against the real process: %v", err)
	}
}

// The socket table is per network namespace, so the one that answers for a
// process is the one at /proc/<pid>/net — not apm2go's own /proc/net. Reading
// the wrong one fails silently and in exactly one situation: a target in a
// different network namespace, which is every containerized service once
// apm2go runs outside that container. This builds both tables with different
// contents and asserts which one is believed.
func TestListenPortsReadsTheTargetsNetworkNamespace(t *testing.T) {
	root := t.TempDir()
	const pid = 4242

	fdDir := filepath.Join(root, "4242", "fd")
	if err := os.MkdirAll(fdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A dangling symlink is exactly what /proc/<pid>/fd holds for a socket.
	if err := os.Symlink("socket:[555555]", filepath.Join(fdDir, "3")); err != nil {
		t.Fatal(err)
	}

	// apm2go's own namespace: the same inode, listening on a different port.
	// If this table is the one read, the test sees 8080 instead of 9090.
	ownNet := filepath.Join(root, "net")
	if err := os.MkdirAll(ownNet, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ownNet, "tcp"), []byte(procNetTCPFixture), 0o644); err != nil {
		t.Fatal(err)
	}

	// The target's own namespace: inode 555555 listens on 9090 (hex 2382).
	targetNet := filepath.Join(root, "4242", "net")
	if err := os.MkdirAll(targetNet, 0o755); err != nil {
		t.Fatal(err)
	}
	targetTable := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 00000000:2382 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 555555 1 0000000000000000 100 0 0 10 0\n"
	if err := os.WriteFile(filepath.Join(targetNet, "tcp"), []byte(targetTable), 0o644); err != nil {
		t.Fatal(err)
	}

	ports, err := listenPorts(root, pid)
	if err != nil {
		t.Fatalf("listenPorts: %v", err)
	}
	if len(ports) != 1 || ports[0] != 9090 {
		t.Fatalf("ports = %v, want [9090] from the target's own namespace", ports)
	}
}
