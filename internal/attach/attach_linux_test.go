//go:build linux

package attach

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// When neither trigger location can be written, both failures have to reach the
// operator. Reporting only the last one hides the more informative half: the
// working directory and /tmp fail for different reasons, and which one failed
// where is the whole diagnosis.
func TestCreateTriggerFilesReportsEveryPathThatFailed(t *testing.T) {
	root := t.TempDir()
	const pid, nspid = 4242, 7

	// A /proc-shaped tree whose cwd and root/tmp both exist but are not
	// writable, which is what a read-only container looks like from outside.
	base := filepath.Join(root, strconv.Itoa(pid))
	cwd := filepath.Join(base, "cwd")
	tmp := filepath.Join(base, "root", "tmp")
	for _, dir := range []string{cwd, tmp} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o555); err != nil {
			t.Fatal(err)
		}
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, which writes into an unwritable directory anyway")
	}

	_, err := createTriggerFiles(Options{ProcRoot: root, PID: pid, NSPid: nspid, UID: 1100})
	if err == nil {
		t.Fatal("expected an error when neither trigger file could be created")
	}

	message := err.Error()
	for _, want := range []string{
		filepath.Join(cwd, ".attach_pid7"),
		filepath.Join(tmp, ".attach_pid7"),
	} {
		if !strings.Contains(message, want) {
			t.Errorf("error does not mention %s:\n%s", want, message)
		}
	}
	// The uid matters: the helper deliberately runs as the target's user, and
	// an operator reading "permission denied" needs to know which user was
	// denied before they can check anything.
	if !strings.Contains(message, "1100") {
		t.Errorf("error does not name the uid it tried as:\n%s", message)
	}
}
