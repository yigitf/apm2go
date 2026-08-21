package discovery

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// procFixture builds a miniature /proc tree so namespace detection can be
// tested without containers. Namespace links are plain symlinks whose target
// string stands in for the kernel's "mnt:[4026531840]" identifier — only
// equality between them matters.
type procFixture struct {
	root string
	t    *testing.T
}

func newProcFixture(t *testing.T) *procFixture {
	t.Helper()
	return &procFixture{root: t.TempDir(), t: t}
}

// addProcess creates /proc/<pid> with the given namespace identifiers. An empty
// identifier leaves that link absent, standing in for a namespace apm2go is not
// permitted to read.
func (f *procFixture) addProcess(pid int, mntNS, netNS string) {
	f.t.Helper()

	nsDir := filepath.Join(f.root, strconv.Itoa(pid), "ns")
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		f.t.Fatalf("create %s: %v", nsDir, err)
	}
	for name, target := range map[string]string{"mnt": mntNS, "net": netNS} {
		if target == "" {
			continue
		}
		if err := os.Symlink(target, filepath.Join(nsDir, name)); err != nil {
			f.t.Fatalf("link %s namespace for pid %d: %v", name, pid, err)
		}
	}
}

// addSelf models /proc/self — apm2go's own namespaces, which are the reference
// for reachability and are deliberately not pid 1's.
func (f *procFixture) addSelf(mntNS, netNS string) {
	f.t.Helper()

	nsDir := filepath.Join(f.root, "self", "ns")
	if err := os.MkdirAll(nsDir, 0o755); err != nil {
		f.t.Fatalf("create %s: %v", nsDir, err)
	}
	for name, target := range map[string]string{"mnt": mntNS, "net": netNS} {
		if target == "" {
			continue
		}
		if err := os.Symlink(target, filepath.Join(nsDir, name)); err != nil {
			f.t.Fatalf("link %s namespace for self: %v", name, err)
		}
	}
}

func TestDetectNamespaceComparesAgainstPidOneNotOurselves(t *testing.T) {
	// apm2go's own systemd unit sets ProtectSystem and ProtectHome, which put
	// it in a private mount namespace. If detection compared the target against
	// apm2go rather than against pid 1, every ordinary process on the host
	// would be reported as containerized — and warned about.
	f := newProcFixture(t)
	f.addProcess(1, "mnt:[host]", "net:[host]")
	f.addProcess(500, "mnt:[host]", "net:[host]") // an ordinary service
	f.addSelf("mnt:[apm2go-private]", "net:[host]")

	scanner := NewScanner(f.root)
	jvm := &JVM{PID: 500, NSPid: 500}
	scanner.detectNamespace(500, jvm)

	if jvm.InContainer {
		t.Error("a process sharing pid 1's mount namespace was reported as containerized")
	}
	if !jvm.SharesOurNetwork {
		t.Error("a process sharing apm2go's network namespace was reported as unreachable")
	}
}

func TestDetectNamespaceIdentifiesRealIsolation(t *testing.T) {
	tests := []struct {
		name        string
		nsPid       int
		containerID string
		mntNS       string
		netNS       string
		wantMount   bool
		wantShares  bool
	}{
		{
			name:       "fully containerized",
			nsPid:      1,
			mntNS:      "mnt:[container]",
			netNS:      "net:[container]",
			wantMount:  true,
			wantShares: false,
		},
		{
			name: "container sharing the host network",
			// The common case for a container started with host networking:
			// its jars must be staged through /proc, but loopback still reaches
			// apm2go, so it must not be warned about exporting.
			nsPid:      500,
			mntNS:      "mnt:[container]",
			netNS:      "net:[host]",
			wantMount:  true,
			wantShares: true,
		},
		{
			name:        "cgroup names a container even where namespaces look shared",
			nsPid:       500,
			containerID: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			mntNS:       "mnt:[host]",
			netNS:       "net:[host]",
			wantMount:   true,
			wantShares:  true,
		},
		{
			name: "unreadable namespace links are not treated as isolation",
			// Without evidence the safer answer is "ordinary host process":
			// guessing the other way warns on every JVM apm2go cannot inspect.
			nsPid:      500,
			mntNS:      "",
			netNS:      "",
			wantMount:  false,
			wantShares: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newProcFixture(t)
			f.addProcess(1, "mnt:[host]", "net:[host]")
			f.addSelf("mnt:[host]", "net:[host]")
			f.addProcess(500, tt.mntNS, tt.netNS)

			scanner := NewScanner(f.root)
			jvm := &JVM{PID: 500, NSPid: tt.nsPid, ContainerID: tt.containerID}
			scanner.detectNamespace(500, jvm)

			if jvm.InContainer != tt.wantMount {
				t.Errorf("InContainer = %v, want %v", jvm.InContainer, tt.wantMount)
			}
			if jvm.SharesOurNetwork != tt.wantShares {
				t.Errorf("SharesOurNetwork = %v, want %v", jvm.SharesOurNetwork, tt.wantShares)
			}
		})
	}
}
