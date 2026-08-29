package store

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/yigitf/apm2go/internal/config"
	"github.com/yigitf/apm2go/internal/model"
)

// entrySpan builds a server span, which is what ListServices counts.
func entrySpan(service, runtime string, at time.Time) *model.Span {
	span := &model.Span{
		Timestamp: at,
		Duration:  5 * time.Millisecond,
		Service:   service,
		Operation: "GET /",
		Kind:      model.SpanKind(2),
		Runtime:   runtime,
	}
	span.TraceID[0] = 1
	span.SpanID[0] = byte(len(service))
	return span
}

// The language a service is written in has to survive the write path, because
// the whole point of storing it rather than reading apm2go's live process
// inventory is that it stays true for a service that has since exited.
func TestListServicesReportsRuntime(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	now := time.Now().UTC()
	spans := []*model.Span{
		entrySpan("orders", "java", now.Add(-time.Minute)),
		entrySpan("checkout", "nodejs", now.Add(-time.Minute)),
		// A producer that never said: the column stays NULL, and the service
		// must still be listed rather than dropped by the aggregate.
		entrySpan("legacy", "", now.Add(-time.Minute)),
	}
	if err := s.WriteSpans(ctx, spans); err != nil {
		t.Fatalf("write spans: %v", err)
	}

	stats, err := s.ListServices(ctx, TimeRange{From: now.Add(-time.Hour), To: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("list services: %v", err)
	}

	got := make(map[string]string, len(stats))
	for _, st := range stats {
		got[st.Service] = st.Runtime
	}
	for service, want := range map[string]string{"orders": "java", "checkout": "nodejs", "legacy": ""} {
		if _, ok := got[service]; !ok {
			t.Fatalf("service %q missing from %v", service, got)
		}
		if got[service] != want {
			t.Errorf("service %q runtime = %q, want %q", service, got[service], want)
		}
	}
}

// A database written before the runtime column existed must keep working: the
// span appender is positional, so a column present in the DDL but missing from
// an existing table would misalign every row written after the upgrade.
func TestOpenMigratesSpansWithoutRuntimeColumn(t *testing.T) {
	dir := t.TempDir()
	cfg := config.StorageConfig{Path: "test.duckdb", MemoryLimit: "256MB", Threads: 1}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	// The spans table as it stood before this column was added, created
	// directly so the migration has a genuinely older database to act on:
	// CREATE TABLE IF NOT EXISTS would leave this one exactly as it is.
	previous, err := sql.Open("duckdb", filepath.Join(dir, "test.duckdb"))
	if err != nil {
		t.Fatalf("open raw database: %v", err)
	}
	if _, err := previous.ExecContext(ctx, `CREATE TABLE spans (
		ts TIMESTAMP NOT NULL, trace_id BLOB NOT NULL, span_id BLOB NOT NULL,
		parent_span_id BLOB, service VARCHAR NOT NULL, operation VARCHAR NOT NULL,
		kind TINYINT NOT NULL, duration_ns BIGINT NOT NULL, dur_bucket SMALLINT NOT NULL,
		status TINYINT NOT NULL, status_message VARCHAR, is_error BOOLEAN NOT NULL,
		is_root BOOLEAN NOT NULL, http_method VARCHAR, http_route VARCHAR,
		http_status INTEGER, db_system VARCHAR, db_statement VARCHAR, db_name VARCHAR,
		peer_service VARCHAR, host_name VARCHAR, pid INTEGER, exception_type VARCHAR,
		attributes VARCHAR, events VARCHAR
	)`); err != nil {
		t.Fatalf("create previous schema: %v", err)
	}
	if err := previous.Close(); err != nil {
		t.Fatalf("close raw database: %v", err)
	}

	upgraded, err := Open(cfg, dir, log)
	if err != nil {
		t.Fatalf("open store on previous schema: %v", err)
	}
	t.Cleanup(func() { _ = upgraded.Close() })

	now := time.Now().UTC()
	if err := upgraded.WriteSpans(ctx, []*model.Span{entrySpan("orders", "java", now.Add(-time.Minute))}); err != nil {
		t.Fatalf("write span after migration: %v", err)
	}

	stats, err := upgraded.ListServices(ctx, TimeRange{From: now.Add(-time.Hour), To: now.Add(time.Minute)})
	if err != nil {
		t.Fatalf("list services: %v", err)
	}
	if len(stats) != 1 || stats[0].Service != "orders" || stats[0].Runtime != "java" {
		t.Fatalf("after migration got %+v, want one orders/java row", stats)
	}
}
