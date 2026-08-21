// Package attachhelper carries the cgo-free apm2go-attach-helper binary that
// performs the actual attach handshake, and stages it to disk where the
// daemon can run it.
//
// The daemon itself cannot do this work in-process: it links cgo, for the
// DuckDB driver, and the mechanism that lets an attach reach a
// security-conscious container — see internal/attach.DropPrivilegesRetainingPtrace —
// refuses to run at all once cgo is linked in. Splitting it into its own
// binary, embedded and staged exactly the way the Java agent jars are, is
// what lets that mechanism actually run.
package attachhelper

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// stagedName is content-addressed, like the Java agent jars, so an upgraded
// apm2go never runs a stale helper left behind by a previous version.
const stagedNamePrefix = "apm2go-attach-helper-"

// Store provides the embedded helper binary and writes it where the daemon
// can execute it.
type Store struct {
	// Digest is the full hex SHA-256 of the embedded binary.
	Digest string
	// Size is the byte length of the embedded binary.
	Size int64

	data []byte

	mu sync.Mutex
}

// New loads the embedded binary and computes its digest. On a build that does
// not carry one (any non-Linux build), Store's other methods report that
// plainly rather than the caller crashing on a nil slice.
func New() (*Store, error) {
	data := binary()
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return &Store{Digest: digest, Size: int64(len(data)), data: data}, nil
}

// Verify checks that the embedded binary is intact and looks like an ELF
// executable. It runs at startup so a corrupted or missing build fails
// immediately and plainly, rather than at the first attach that needs to drop
// privileges into a restrictive container.
func (s *Store) Verify() error {
	if !Available() {
		return fmt.Errorf("this build does not carry the attach helper (it is built for Linux only)")
	}
	if s.Size == 0 {
		return fmt.Errorf("embedded attach helper is empty")
	}
	// \x7fELF is the ELF magic every Linux executable starts with.
	if len(s.data) < 4 || string(s.data[:4]) != "\x7fELF" {
		return fmt.Errorf("embedded attach helper is not an ELF executable")
	}
	return nil
}

// Materialize writes the helper binary into dir, creating it if needed, and
// returns its path. An existing file with the right content-addressed name and
// size is left alone.
//
// The file is staged 0700, owned by whoever apm2go itself runs as (normally
// root): unlike the Java agent jars, nothing but apm2go ever needs to read or
// execute this binary, and it starts every run still holding apm2go's own
// privileges, so it must not be writable — or even readable — by anyone this
// process would not also trust with root.
func (s *Store) Materialize(dir string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !Available() {
		return "", fmt.Errorf("this build does not carry the attach helper (it is built for Linux only)")
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create attach helper directory %s: %w", dir, err)
	}

	// Eight hex characters is ample to separate releases while keeping the
	// path short enough to read in a log line, matching the agent jars.
	name := stagedNamePrefix + s.Digest[:8]
	path := filepath.Join(dir, name)

	if fi, err := os.Stat(path); err == nil && fi.Size() == s.Size {
		if err := os.Chmod(path, 0o700); err != nil {
			return "", fmt.Errorf("set permissions on %s: %w", path, err)
		}
		return path, nil
	}

	// Written to a temporary file first so a concurrent attach never execs a
	// half-written binary.
	tmp, err := os.CreateTemp(dir, name+".tmp*")
	if err != nil {
		return "", fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(s.data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o700); err != nil {
		return "", fmt.Errorf("set permissions on %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("install %s: %w", path, err)
	}
	return path, nil
}
