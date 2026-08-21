// Package ingesttoken issues and checks the credential an instrumented process
// presents when it exports telemetry.
//
// It exists because of one consequence of supporting containers: to be
// reachable from a container network, the OTLP receiver has to listen on that
// network's gateway — an address every container on that bridge can also reach.
// Without a credential, any of them could write spans attributed to any
// service, and an APM that can be lied to is worse than one that is missing
// data, because the lie is indistinguishable from a measurement.
//
// The token is minted per process and handed over through the same properties
// file the injector already writes, so no new channel is involved.
package ingesttoken

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// Header is the OTLP metadata key the token travels in. gRPC lowercases
// metadata keys, so this is written lowercase everywhere to avoid a mismatch
// that would only show up at runtime.
const Header = "x-apm2go-token"

// tokenBytes is the entropy per token. 24 bytes is well beyond guessing and
// still short enough to sit comfortably in a properties file.
const tokenBytes = 24

// Registry mints tokens and recognises them again.
//
// Tokens must survive apm2go's own restart. A JVM is instrumented once and its
// OpenTelemetry exporter is configured with its token at that moment — nothing
// about a running JVM can be changed afterward without another attach. An
// already-instrumented JVM is deliberately never re-attached on discovery (see
// Injector.Inject), so if apm2go forgot the token on restart, that JVM's
// telemetry would be rejected permanently, until the JVM itself happened to
// restart. That would make apm2go's own restart a regression against the one
// thing this tool exists to guarantee, so tokens are persisted to a file
// alongside the database and reloaded at start-up.
type Registry struct {
	mu sync.RWMutex
	// valid maps token to the service it was issued for, which lets a rejected
	// export name the service that should have presented it.
	valid map[string]string
	// path is where the registry is persisted; empty means memory-only, which
	// is what the one-shot `attach` CLI command uses.
	path string
}

// NewRegistry returns an in-memory registry, used where there is no restart to
// survive — the one-shot `attach` command re-attaches idempotently every time
// it runs anyway.
func NewRegistry() *Registry {
	return &Registry{valid: make(map[string]string)}
}

// NewPersistentRegistry returns a registry backed by a file at path, loading
// any tokens already there. A missing file is not an error — the first run has
// none — but a corrupt one is, since silently discarding valid tokens would
// itself cause the regression this exists to prevent.
func NewPersistentRegistry(path string) (*Registry, error) {
	r := &Registry{valid: make(map[string]string), path: path}

	data, err := os.ReadFile(path)
	switch {
	case os.IsNotExist(err):
		return r, nil
	case err != nil:
		return nil, fmt.Errorf("read ingest token store %s: %w", path, err)
	}

	if len(data) > 0 {
		if err := json.Unmarshal(data, &r.valid); err != nil {
			return nil, fmt.Errorf("parse ingest token store %s: %w", path, err)
		}
	}
	return r, nil
}

// Issue mints a token for a service and records it as valid.
func (r *Registry) Issue(service string) (string, error) {
	raw := make([]byte, tokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate ingest token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)

	r.mu.Lock()
	r.valid[token] = service
	saveErr := r.saveLocked()
	r.mu.Unlock()

	if saveErr != nil {
		return "", saveErr
	}
	return token, nil
}

// Accepts reports whether a token was issued by this registry.
//
// The comparison is constant time. The set is small and the tokens are long, so
// a timing attack is far-fetched — but the cost of doing it properly is one
// function call, and the cost of being wrong is silent.
func (r *Registry) Accepts(token string) bool {
	if token == "" {
		return false
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	for candidate := range r.valid {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			return true
		}
	}
	return false
}

// Revoke forgets a token, so a process that has exited stops being able to
// export under its name. A persistence failure is logged by the caller's
// choice, not here — losing the revocation is far less harmful than losing an
// active token, since the exited process is not exporting anything anyway.
func (r *Registry) Revoke(token string) error {
	if token == "" {
		return nil
	}
	r.mu.Lock()
	delete(r.valid, token)
	err := r.saveLocked()
	r.mu.Unlock()
	return err
}

// saveLocked writes the current token set to disk. The caller must hold mu.
// Memory-only registries have no path and this is a no-op for them.
//
// The write is atomic — a temporary file renamed into place — so a crash
// mid-write cannot leave a half-written file that the next start-up refuses to
// parse, which would be a self-inflicted repeat of the exact failure this
// registry exists to prevent.
func (r *Registry) saveLocked() error {
	if r.path == "" {
		return nil
	}

	data, err := json.Marshal(r.valid)
	if err != nil {
		return fmt.Errorf("encode ingest token store: %w", err)
	}

	dir := filepath.Dir(r.path)
	tmp, err := os.CreateTemp(dir, filepath.Base(r.path)+".tmp*")
	if err != nil {
		return fmt.Errorf("create ingest token store: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write ingest token store: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close ingest token store: %w", err)
	}
	// Tokens are bearer credentials: readable only by the owner, matching the
	// database they sit beside.
	if err := os.Chmod(tmpPath, 0o600); err != nil {
		return fmt.Errorf("set permissions on ingest token store: %w", err)
	}
	if err := os.Rename(tmpPath, r.path); err != nil {
		return fmt.Errorf("install ingest token store: %w", err)
	}
	return nil
}

// HasTokenFor reports whether any valid token was issued for a service.
//
// It answers a question that would otherwise be invisible: a JVM instrumented
// by an earlier apm2go, whose token this instance never knew, is attached and
// healthy in every way except that everything it sends is refused. Nothing
// about a running JVM's exporter can be reconfigured after the fact, so the
// only remedy is restarting that JVM — and an operator can only know to do
// that if apm2go says so.
func (r *Registry) HasTokenFor(service string) bool {
	if r == nil {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, issued := range r.valid {
		if issued == service {
			return true
		}
	}
	return false
}

// Count reports how many tokens are currently valid, for self-monitoring.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.valid)
}
