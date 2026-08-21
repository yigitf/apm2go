package ingesttoken

import (
	"os"
	"testing"
)

func TestIssuedTokensAreAcceptedAndUnique(t *testing.T) {
	registry := NewRegistry()

	first, err := registry.Issue("orders")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	second, err := registry.Issue("payments")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if first == second {
		t.Error("two issued tokens are identical; they must not be guessable from each other")
	}
	for _, token := range []string{first, second} {
		if !registry.Accepts(token) {
			t.Errorf("an issued token was rejected")
		}
	}
}

func TestUnknownTokensAreRejected(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Issue("orders"); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// The empty case matters most: it is what an unauthenticated exporter on a
	// shared bridge network sends.
	for _, token := range []string{"", "not-a-real-token", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"} {
		if registry.Accepts(token) {
			t.Errorf("token %q was accepted but was never issued", token)
		}
	}
}

func TestRevokedTokensStopWorking(t *testing.T) {
	registry := NewRegistry()
	token, err := registry.Issue("orders")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	registry.Revoke(token)
	if registry.Accepts(token) {
		t.Error("a revoked token was still accepted")
	}
	if registry.Count() != 0 {
		t.Errorf("Count = %d after revoking the only token, want 0", registry.Count())
	}
}

func TestPersistentRegistrySurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tokens.json"

	first, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("NewPersistentRegistry: %v", err)
	}
	token, err := first.Issue("gateway-service")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// A fresh registry stands in for apm2go restarting: the process that
	// issued the token is gone, but the JVM holding it never stopped running,
	// and its exporter is still configured with exactly this token.
	second, err := NewPersistentRegistry(path)
	if err != nil {
		t.Fatalf("NewPersistentRegistry after restart: %v", err)
	}
	if !second.Accepts(token) {
		t.Error("a token issued before restart was rejected after restart; " +
			"the already-running JVM that holds it would be permanently cut off")
	}
}

func TestPersistentRegistryStartsEmptyOnFirstRun(t *testing.T) {
	dir := t.TempDir()
	registry, err := NewPersistentRegistry(dir + "/does-not-exist-yet.json")
	if err != nil {
		t.Fatalf("NewPersistentRegistry: %v", err)
	}
	if registry.Count() != 0 {
		t.Errorf("Count = %d for a registry with no prior file, want 0", registry.Count())
	}
}

func TestPersistentRegistryRejectsCorruptState(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/tokens.json"
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Silently discarding a corrupt file would mean silently discarding every
	// currently-valid token — the exact failure this registry exists to avoid.
	// It must fail loudly instead.
	if _, err := NewPersistentRegistry(path); err == nil {
		t.Error("expected an error loading a corrupt token store, got nil")
	}
}

func TestHasTokenForDistinguishesKnownFromLegacyServices(t *testing.T) {
	registry := NewRegistry()
	if _, err := registry.Issue("gateway-service"); err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if !registry.HasTokenFor("gateway-service") {
		t.Error("a service this registry issued a token for was reported as unknown")
	}
	// A JVM instrumented by an earlier apm2go: attached and healthy, but every
	// export refused, and nothing but a restart of that JVM can fix it.
	if registry.HasTokenFor("orders-service") {
		t.Error("a service with no issued token was reported as known; " +
			"its rejected telemetry would go unexplained")
	}
}
