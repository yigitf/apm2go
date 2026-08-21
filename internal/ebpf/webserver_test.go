package ebpf

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// fakeProcess writes the parts of /proc/<pid> that discovery reads: the exe
// link, the parent in status, and — when ports are given — a socket fd and the
// namespace-local table that fd resolves in.
func fakeProcess(t *testing.T, root string, pid, ppid int, exe string, ports ...int) {
	t.Helper()

	dir := filepath.Join(root, strconv.Itoa(pid))
	if err := os.MkdirAll(filepath.Join(dir, "fd"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Dangling on purpose: /proc/<pid>/exe is a magic link, and what discovery
	// reads off it is the basename, not the file.
	if err := os.Symlink(exe, filepath.Join(dir, "exe")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "status"),
		[]byte(fmt.Sprintf("Name:\t%s\nPid:\t%d\nPPid:\t%d\n", filepath.Base(exe), pid, ppid)),
		0o644); err != nil {
		t.Fatal(err)
	}

	table := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"
	for i, port := range ports {
		inode := uint64(pid)*1000 + uint64(i)
		if err := os.Symlink(fmt.Sprintf("socket:[%d]", inode),
			filepath.Join(dir, "fd", strconv.Itoa(3+i))); err != nil {
			t.Fatal(err)
		}
		table += fmt.Sprintf(
			"  %2d: 00000000:%04X 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 %d 1 0000000000000000 100 0 0 10 0\n",
			i, port, inode)
	}
	netDir := filepath.Join(dir, "net")
	if err := os.MkdirAll(netDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(netDir, "tcp"), []byte(table), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nginx and httpd both hold the listening socket in every worker, because the
// master opens it and the workers inherit it. Returning one target per process
// would hand OBI the same rule once per worker, and — worse — key the target
// set on pids that a reload replaces, so a set that nothing asked to change
// would change on its own and restart OBI along with it.
func TestScanReturnsOnlyTheWebServerMaster(t *testing.T) {
	root := t.TempDir()

	// nginx: master under init, four workers under the master. Every one of
	// them holds the inherited listening sockets on 80 and 443.
	fakeProcess(t, root, 100, 1, "/usr/sbin/nginx", 80, 443)
	for pid := 101; pid <= 104; pid++ {
		fakeProcess(t, root, pid, 100, "/usr/sbin/nginx", 80, 443)
	}

	// httpd: identical shape, and the case where argv cannot tell parent from
	// child because Apache's children carry their parent's command line.
	fakeProcess(t, root, 200, 1, "/usr/sbin/httpd", 8080)
	fakeProcess(t, root, 201, 200, "/usr/sbin/httpd", 8080)

	targets, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	byName := make(map[string]Target, len(targets))
	for _, target := range targets {
		if _, duplicate := byName[target.Name]; duplicate {
			t.Fatalf("%q reported more than once: %+v", target.Name, targets)
		}
		byName[target.Name] = target
	}
	if len(targets) != 2 {
		t.Fatalf("got %d targets, want 2 (one master each): %+v", len(targets), targets)
	}

	nginx, ok := byName["nginx"]
	if !ok {
		t.Fatalf("no nginx target in %+v", targets)
	}
	if nginx.PID != 100 {
		t.Errorf("nginx pid = %d, want the master 100", nginx.PID)
	}
	if len(nginx.Ports) != 2 || nginx.Ports[0] != 80 || nginx.Ports[1] != 443 {
		t.Errorf("nginx ports = %v, want [80 443]", nginx.Ports)
	}
	if nginx.Runtime != RuntimeNginx {
		t.Errorf("nginx runtime = %q", nginx.Runtime)
	}

	httpd, ok := byName["httpd"]
	if !ok {
		t.Fatalf("no httpd target in %+v", targets)
	}
	if httpd.PID != 200 {
		t.Errorf("httpd pid = %d, want the master 200", httpd.PID)
	}
	if httpd.Runtime != RuntimeHTTPD {
		t.Errorf("httpd runtime = %q", httpd.Runtime)
	}
}

// Debian's Apache binary is apache2 and Red Hat's is httpd. A service must not
// be renamed, and so split into two in every chart, by the distribution it
// runs on.
func TestScanNamesApache2AsHTTPD(t *testing.T) {
	root := t.TempDir()
	fakeProcess(t, root, 300, 1, "/usr/sbin/apache2", 80)

	targets, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("got %d targets, want 1: %+v", len(targets), targets)
	}
	if targets[0].Name != "httpd" || targets[0].Runtime != RuntimeHTTPD {
		t.Errorf("target = %+v, want name httpd and runtime httpd", targets[0])
	}
}

// Names that merely start with a web server's are not that web server. The
// exporter in particular listens on a port and would otherwise be traced as
// the server it scrapes.
func TestScanIgnoresLookalikeBinaries(t *testing.T) {
	root := t.TempDir()
	fakeProcess(t, root, 400, 1, "/usr/bin/nginx-prometheus-exporter", 9113)
	fakeProcess(t, root, 401, 1, "/usr/sbin/httpd-foo", 9114)

	targets, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, target := range targets {
		if target.Runtime == RuntimeNginx || target.Runtime == RuntimeHTTPD {
			t.Errorf("%+v was classified as a web server", target)
		}
	}
}

// A master that has not bound a port yet cannot be named to OBI, whose only
// selector is the port. Reporting it anyway would render a rule selecting
// nothing, or — with an empty selector — everything.
func TestScanSkipsWebServerWithNoListeningPort(t *testing.T) {
	root := t.TempDir()
	fakeProcess(t, root, 500, 1, "/usr/sbin/nginx")

	targets, err := Scan(root)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(targets) != 0 {
		t.Errorf("got %+v, want no targets", targets)
	}
}
