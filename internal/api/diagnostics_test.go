package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/apm2go/apm2go/internal/config"
	"github.com/apm2go/apm2go/internal/jvmdiag"
	"github.com/apm2go/apm2go/internal/store"
)

func newTestServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(
		config.StorageConfig{Path: "test.duckdb", MemoryLimit: "256MB", Threads: 1},
		t.TempDir(), log)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	handlers := NewHandlers(config.Default(), log)
	handlers.Store = db
	// Inventory and Diagnostics stay nil on purpose: this models a server-mode
	// process, which owns no JVMs but still holds every dump ever taken of them.
	return NewServer(config.APIConfig{}, handlers, log), db
}

// storeHistogram files a class histogram for a process, so the comparison path
// has something real to read back.
func storeHistogram(t *testing.T, db *store.Store, id string, pid int, start time.Time, at time.Time, raw string) {
	t.Helper()

	parsed := jvmdiag.ParseClassHistogram(raw)
	summary, err := json.Marshal(map[string]any{"histogram": parsed})
	if err != nil {
		t.Fatalf("encode summary: %v", err)
	}

	err = db.WriteDiagnostic(context.Background(), &store.Diagnostic{
		ID:         id,
		TS:         at,
		Kind:       string(jvmdiag.KindClassHistogram),
		PID:        pid,
		StartTime:  start,
		Service:    "orders-service",
		DurationMS: 30,
		SizeBytes:  int64(len(raw)),
		Headline:   json.RawMessage(`{"classes":2}`),
		Summary:    summary,
		Raw:        raw,
	})
	if err != nil {
		t.Fatalf("write diagnostic: %v", err)
	}
}

const earlierHistogram = `   1:        1000        100000  com.example.Cache$Entry
   2:         500         20000  java.lang.String
Total        1500        120000
`

const laterHistogram = `   1:        9000        900000  com.example.Cache$Entry
   2:         500         20000  java.lang.String
Total        9500        920000
`

func do(t *testing.T, srv *Server, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response %q: %v", rec.Body.String(), err)
	}
	return out
}

// The stored history must be readable where it lives, even by a process that
// owns no JVMs and so has no inventory.
func TestListDiagnosticsWithoutInventory(t *testing.T) {
	srv, db := newTestServer(t)
	start := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	storeHistogram(t, db, "h1", 100, start, time.Now().UTC(), earlierHistogram)

	rec := do(t, srv, http.MethodGet, "/api/v1/jvms/100/diagnostics")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	body := decode(t, rec)
	list, _ := body["diagnostics"].([]any)
	if len(list) != 1 {
		t.Fatalf("listed %d diagnostics, want 1", len(list))
	}
	if _, ok := body["available"]; !ok {
		t.Error("response does not say which diagnostics can be collected")
	}

	// The one command apm2go refuses to run must still be shown, with its
	// reason, or an operator has no way to reach it.
	heapDump, ok := body["heap_dump"].(map[string]any)
	if !ok {
		t.Fatal("response does not mention heap dumps at all")
	}
	if cmd, _ := heapDump["command"].(string); !strings.Contains(cmd, "GC.heap_dump") {
		t.Errorf("heap_dump.command = %q", cmd)
	}
	if note, _ := heapDump["note"].(string); !strings.Contains(note, "does not") {
		t.Errorf("heap_dump.note does not explain the refusal: %q", note)
	}
}

// A dump body is megabytes; the JSON view must not carry it.
func TestGetDiagnosticOmitsRawBody(t *testing.T) {
	srv, db := newTestServer(t)
	start := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	storeHistogram(t, db, "h1", 100, start, time.Now().UTC(), earlierHistogram)

	rec := do(t, srv, http.MethodGet, "/api/v1/diagnostics/h1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}
	body := decode(t, rec)
	if raw, ok := body["raw"]; ok && raw != "" {
		t.Errorf("JSON view carried the raw dump: %v", raw)
	}
	if _, ok := body["summary"]; !ok {
		t.Error("JSON view has no parsed summary")
	}
}

func TestGetDiagnosticRawServesText(t *testing.T) {
	srv, db := newTestServer(t)
	start := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	storeHistogram(t, db, "h1", 100, start, time.Now().UTC(), earlierHistogram)

	rec := do(t, srv, http.MethodGet, "/api/v1/diagnostics/h1/raw")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Body.String() != earlierHistogram {
		t.Error("raw endpoint did not return the verbatim output")
	}
}

// A dump that aged out of retention is an ordinary outcome, not a server fault.
func TestGetDiagnosticMissingIs404(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/api/v1/diagnostics/nope")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestCompareHistogramsFindsGrowth(t *testing.T) {
	srv, db := newTestServer(t)
	start := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	now := time.Now().UTC()

	storeHistogram(t, db, "early", 100, start, now.Add(-time.Hour), earlierHistogram)
	storeHistogram(t, db, "late", 100, start, now, laterHistogram)

	rec := do(t, srv, http.MethodGet, "/api/v1/diagnostics/compare?from=early&to=late")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body)
	}

	body := decode(t, rec)
	diff, ok := body["diff"].(map[string]any)
	if !ok {
		t.Fatalf("no diff in response: %s", rec.Body)
	}
	growth, _ := diff["growth"].([]any)
	if len(growth) == 0 {
		t.Fatal("comparison found no growth between a 100KB and a 900KB cache")
	}
	top, _ := growth[0].(map[string]any)
	if name, _ := top["name"].(string); name != "com.example.Cache$Entry" {
		t.Errorf("largest gain = %q, want the cache entry", name)
	}
	if delta, _ := top["bytes_delta"].(float64); delta != 800000 {
		t.Errorf("bytes_delta = %v, want 800000", delta)
	}
}

// A pid is not an identity. Diffing two processes that happened to share one
// invents a leak out of an ordinary restart.
func TestCompareRejectsDifferentProcesses(t *testing.T) {
	srv, db := newTestServer(t)
	now := time.Now().UTC()

	storeHistogram(t, db, "before-restart", 100,
		time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC), now.Add(-time.Hour), earlierHistogram)
	storeHistogram(t, db, "after-restart", 100,
		time.Date(2026, 8, 19, 9, 0, 0, 0, time.UTC), now, laterHistogram)

	rec := do(t, srv, http.MethodGet, "/api/v1/diagnostics/compare?from=before-restart&to=after-restart")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "different processes") {
		t.Errorf("error does not explain the refusal: %s", rec.Body)
	}
}

func TestCompareRequiresBothIDs(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodGet, "/api/v1/diagnostics/compare?from=only-one")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// Collecting pauses the target, so it must not be reachable on an instance that
// does not manage JVMs.
func TestCollectDiagnosticWithoutInventory(t *testing.T) {
	srv, _ := newTestServer(t)

	rec := do(t, srv, http.MethodPost, "/api/v1/jvms/100/diagnostics/thread_dump")
	if rec.Code != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", rec.Code)
	}
}

func TestCollectDiagnosticRejectsUnknownKind(t *testing.T) {
	srv, _ := newTestServer(t)
	// Give it a diagnostics client so the kind check is what answers, not the
	// missing-JVM-support check ahead of it.
	srv.handlers.Diagnostics = jvmdiag.New(t.TempDir(), "", slog.New(slog.NewTextHandler(io.Discard, nil)))

	rec := do(t, srv, http.MethodPost, "/api/v1/jvms/100/diagnostics/heap_dump")
	if rec.Code == http.StatusOK {
		t.Fatal("heap_dump was accepted; apm2go must never run one")
	}
}
