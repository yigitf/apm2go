// Package assets carries the agent jars that apm2go injects into target JVMs.
//
// Both jars are compiled into the binary, so a apm2go install has no runtime
// dependency on a JDK, a package manager or network access. They are written to
// disk only when an attach needs them, and are named after their content hash
// so an upgraded apm2go never reuses a stale jar.
package assets

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

//go:embed files/apm2go-bootstrap.jar files/opentelemetry-javaagent.jar
var embedded embed.FS

// Asset names as embedded.
const (
	bootstrapJarName = "files/apm2go-bootstrap.jar"
	otelJarName      = "files/opentelemetry-javaagent.jar"
)

// OtelAgentVersion is the bundled OpenTelemetry Java agent release. It is
// reported in the UI so an operator can match it against upstream changelogs.
const OtelAgentVersion = "2.30.0"

// Jar is one embedded agent jar.
type Jar struct {
	// FileName is the name to write on disk, including a content hash so that
	// two apm2go versions never collide on the same path.
	FileName string
	// Digest is the full hex SHA-256 of the contents.
	Digest string
	// Size is the byte length of the contents.
	Size int64

	data []byte
}

// Store provides the embedded jars and writes them where a JVM can read them.
type Store struct {
	// Bootstrap applies apm2go's configuration as system properties.
	Bootstrap Jar
	// Otel is the OpenTelemetry Java agent that does the actual instrumenting.
	Otel Jar

	// mu guards concurrent Materialize calls racing on the same directory.
	mu sync.Mutex
}

// New loads the embedded jars and computes their digests.
func New() (*Store, error) {
	bootstrap, err := loadJar(bootstrapJarName, "apm2go-bootstrap")
	if err != nil {
		return nil, err
	}
	otel, err := loadJar(otelJarName, "opentelemetry-javaagent")
	if err != nil {
		return nil, err
	}
	return &Store{Bootstrap: bootstrap, Otel: otel}, nil
}

func loadJar(embedPath, baseName string) (Jar, error) {
	data, err := embedded.ReadFile(embedPath)
	if err != nil {
		return Jar{}, fmt.Errorf("read embedded %s: %w", embedPath, err)
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	return Jar{
		// Eight hex characters is ample to separate releases while keeping the
		// path short enough to read in a log line.
		FileName: fmt.Sprintf("%s-%s.jar", baseName, digest[:8]),
		Digest:   digest,
		Size:     int64(len(data)),
		data:     data,
	}, nil
}

// Bytes returns the jar contents.
func (j *Jar) Bytes() []byte { return j.data }

// Materialize writes both jars into dir, creating it if needed, and returns
// their paths. Existing files with the right size are left alone: the content
// hash in the name already guarantees they match.
//
// The files are world readable because the target JVM usually runs as a
// different user than apm2go, and it must be able to open the jar itself.
func (s *Store) Materialize(dir string) (bootstrapPath, otelPath string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("create agent directory %s: %w", dir, err)
	}
	// MkdirAll honours the umask, which on many hosts would strip the bits the
	// target user needs to traverse the directory.
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", "", fmt.Errorf("set permissions on %s: %w", dir, err)
	}

	bootstrapPath, err = writeJar(dir, &s.Bootstrap)
	if err != nil {
		return "", "", err
	}
	otelPath, err = writeJar(dir, &s.Otel)
	if err != nil {
		return "", "", err
	}
	return bootstrapPath, otelPath, nil
}

// writeJar writes one jar atomically, skipping the work when an identical file
// is already in place.
func writeJar(dir string, jar *Jar) (string, error) {
	path := filepath.Join(dir, jar.FileName)

	if fi, err := os.Stat(path); err == nil && fi.Size() == jar.Size {
		// Content-addressed name plus matching size: nothing to do.
		if err := os.Chmod(path, 0o644); err != nil {
			return "", fmt.Errorf("set permissions on %s: %w", path, err)
		}
		return path, nil
	}

	// Write to a temporary file first so a concurrent attach never sees a
	// half-written jar, which the JVM would reject in a confusing way.
	tmp, err := os.CreateTemp(dir, jar.FileName+".tmp*")
	if err != nil {
		return "", fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(jar.data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write %s: %w", tmpPath, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", tmpPath, err)
	}
	if err := os.Chmod(tmpPath, 0o644); err != nil {
		return "", fmt.Errorf("set permissions on %s: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return "", fmt.Errorf("install %s: %w", path, err)
	}
	return path, nil
}

// Verify checks that the embedded jars are intact and look like jars. It runs
// at startup so a corrupted build fails immediately rather than at the first
// attach.
func (s *Store) Verify() error {
	for _, jar := range []*Jar{&s.Bootstrap, &s.Otel} {
		if jar.Size == 0 {
			return fmt.Errorf("embedded jar %s is empty", jar.FileName)
		}
		// Every jar is a zip archive and starts with the local file header magic.
		if len(jar.data) < 4 || jar.data[0] != 'P' || jar.data[1] != 'K' {
			return fmt.Errorf("embedded jar %s is not a zip archive", jar.FileName)
		}
	}
	return nil
}

// List reports the embedded files, for diagnostics.
func List() ([]string, error) {
	var out []string
	err := fs.WalkDir(embedded, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	return out, err
}
