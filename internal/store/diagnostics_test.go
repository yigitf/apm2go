package store

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/apm2go/apm2go/internal/config"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()

	s, err := Open(
		config.StorageConfig{Path: "test.duckdb", MemoryLimit: "256MB", Threads: 1},
		t.TempDir(),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// processStart stands in for a target's start time. It is fixed per pid on
// purpose: a real process has one start time for its whole life, and the
// per-process cap partitions on it to tell a reused pid from the original.
func processStart(pid int) time.Time {
	return time.Date(2026, 8, 18, 9, 0, pid, 0, time.UTC)
}

// writeDump stores a thread dump for pid, aged minutesAgo minutes.
func writeDump(t *testing.T, s *Store, id string, pid int, kind string, minutesAgo int) *Diagnostic {
	t.Helper()

	d := &Diagnostic{
		ID:         id,
		TS:         time.Now().UTC().Add(-time.Duration(minutesAgo) * time.Minute),
		Kind:       kind,
		PID:        pid,
		StartTime:  processStart(pid),
		Service:    "orders-service",
		HostName:   "test-host",
		DurationMS: 42,
		SizeBytes:  1234,
		Headline:   json.RawMessage(`{"threads":12,"deadlocks":1}`),
		Summary:    json.RawMessage(`{"threads":[{"name":"main"}]}`),
		Raw:        "Full thread dump ...",
	}
	if err := s.WriteDiagnostic(context.Background(), d); err != nil {
		t.Fatalf("write diagnostic %s: %v", id, err)
	}
	return d
}

func TestDiagnosticRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	want := writeDump(t, s, "dump-1", 100, "thread_dump", 0)

	got, err := s.GetDiagnostic(ctx, "dump-1")
	if err != nil {
		t.Fatalf("GetDiagnostic: %v", err)
	}
	if got.PID != want.PID || got.Kind != want.Kind || got.Service != want.Service {
		t.Errorf("identity fields = %d/%s/%s", got.PID, got.Kind, got.Service)
	}
	if got.DurationMS != 42 || got.SizeBytes != 1234 {
		t.Errorf("duration/size = %d/%d", got.DurationMS, got.SizeBytes)
	}
	// The verbatim output is the point of storing these: a parser that fails to
	// understand a future JVM must not cost the evidence.
	if got.Raw != want.Raw {
		t.Errorf("Raw = %q, want %q", got.Raw, want.Raw)
	}
	if string(got.Summary) != string(want.Summary) {
		t.Errorf("Summary = %s", got.Summary)
	}
	if got.StartTime.IsZero() {
		t.Error("StartTime was not stored; two dumps from a reused pid become indistinguishable")
	}
}

func TestGetDiagnosticMissing(t *testing.T) {
	s := newTestStore(t)

	_, err := s.GetDiagnostic(context.Background(), "no-such-dump")
	if !errors.Is(err, ErrDiagnosticNotFound) {
		t.Errorf("err = %v, want ErrDiagnosticNotFound so callers can answer 404", err)
	}
}

// The list is for choosing between dumps, so it must not drag their bodies
// along: a thread dump of a busy application is megabytes.
func TestListDiagnosticsOmitsBodies(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	writeDump(t, s, "dump-1", 100, "thread_dump", 10)
	writeDump(t, s, "dump-2", 100, "thread_dump", 5)
	writeDump(t, s, "dump-3", 200, "thread_dump", 1)

	list, err := s.ListDiagnostics(ctx, DiagnosticFilter{PID: 100})
	if err != nil {
		t.Fatalf("ListDiagnostics: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d dumps for pid 100, want 2", len(list))
	}
	// Newest first, so the history reads top-down.
	if list[0].ID != "dump-2" || list[1].ID != "dump-1" {
		t.Errorf("order = %s, %s; want dump-2 then dump-1", list[0].ID, list[1].ID)
	}
	for _, d := range list {
		if d.Raw != "" || len(d.Summary) != 0 {
			t.Errorf("%s carried its body into the list", d.ID)
		}
		if len(d.Headline) == 0 {
			t.Errorf("%s has no headline, so the list has nothing to show", d.ID)
		}
	}
}

func TestListDiagnosticsFiltersByKind(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	writeDump(t, s, "threads", 100, "thread_dump", 2)
	writeDump(t, s, "classes", 100, "class_histogram", 1)

	list, err := s.ListDiagnostics(ctx, DiagnosticFilter{PID: 100, Kind: "class_histogram"})
	if err != nil {
		t.Fatalf("ListDiagnostics: %v", err)
	}
	if len(list) != 1 || list[0].ID != "classes" {
		t.Fatalf("kind filter returned %+v", list)
	}
}

// An afternoon of leak hunting must not outgrow the telemetry it sits beside.
func TestPruneDiagnosticsKeepsNewestPerProcess(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		writeDump(t, s, "old-"+strconv.Itoa(i), 100, "thread_dump", 10-i)
	}
	writeDump(t, s, "other-pid", 200, "thread_dump", 1)
	// A different kind is capped separately, not against the same budget.
	writeDump(t, s, "histogram", 100, "class_histogram", 1)

	if _, err := s.PruneDiagnostics(ctx, 0, 2); err != nil {
		t.Fatalf("PruneDiagnostics: %v", err)
	}

	kept, err := s.ListDiagnostics(ctx, DiagnosticFilter{PID: 100, Kind: "thread_dump"})
	if err != nil {
		t.Fatalf("ListDiagnostics: %v", err)
	}
	if len(kept) != 2 {
		t.Fatalf("kept %d thread dumps for pid 100, want 2", len(kept))
	}
	// The two newest survive: old-4 was taken 6 minutes ago, old-3 seven.
	if kept[0].ID != "old-4" || kept[1].ID != "old-3" {
		t.Errorf("kept %s and %s, want the two newest", kept[0].ID, kept[1].ID)
	}

	for _, f := range []DiagnosticFilter{{PID: 200}, {PID: 100, Kind: "class_histogram"}} {
		others, err := s.ListDiagnostics(ctx, f)
		if err != nil {
			t.Fatalf("ListDiagnostics: %v", err)
		}
		if len(others) != 1 {
			t.Errorf("filter %+v has %d dumps, want 1: one process or kind must not consume another's budget", f, len(others))
		}
	}
}

func TestPruneDiagnosticsByAge(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	writeDump(t, s, "fresh", 100, "thread_dump", 1)
	writeDump(t, s, "stale", 100, "thread_dump", 120)

	if _, err := s.PruneDiagnostics(ctx, time.Hour, 0); err != nil {
		t.Fatalf("PruneDiagnostics: %v", err)
	}

	list, err := s.ListDiagnostics(ctx, DiagnosticFilter{PID: 100})
	if err != nil {
		t.Fatalf("ListDiagnostics: %v", err)
	}
	if len(list) != 1 || list[0].ID != "fresh" {
		t.Errorf("after pruning past an hour, kept %+v", list)
	}
}
