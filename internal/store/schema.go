package store

import "strconv"

// schemaVersion is bumped when the DDL below changes incompatibly. The store
// refuses to open a database written by a newer apm2go rather than corrupting
// it.
const schemaVersion = 4

// The schema is deliberately narrow. Raw spans keep a short retention and carry
// the columns needed to render a trace; the rollup tables keep a long retention
// and carry only what the charts read. Everything a query filters or groups on
// is a real column, so no query has to reach into the JSON attribute bag.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS meta (
		key    VARCHAR PRIMARY KEY,
		value  VARCHAR NOT NULL
	)`,

	// Raw spans. trace_id and span_id are stored as bytes rather than hex,
	// which halves their size and makes the joins that assemble a trace cheaper.
	`CREATE TABLE IF NOT EXISTS spans (
		ts             TIMESTAMP   NOT NULL,
		trace_id       BLOB        NOT NULL,
		span_id        BLOB        NOT NULL,
		parent_span_id BLOB,
		service        VARCHAR     NOT NULL,
		operation      VARCHAR     NOT NULL,
		kind           TINYINT     NOT NULL,
		duration_ns    BIGINT      NOT NULL,
		-- Precomputed at write time so latency rollups are a plain GROUP BY.
		dur_bucket     SMALLINT    NOT NULL,
		status         TINYINT     NOT NULL,
		status_message VARCHAR,
		is_error       BOOLEAN     NOT NULL,
		is_root        BOOLEAN     NOT NULL,
		http_method    VARCHAR,
		http_route     VARCHAR,
		http_status    INTEGER,
		db_system      VARCHAR,
		db_statement   VARCHAR,
		db_name        VARCHAR,
		peer_service   VARCHAR,
		host_name      VARCHAR,
		pid            INTEGER,
		exception_type VARCHAR,
		attributes     VARCHAR,
		events         VARCHAR,
		-- The language the emitting process runs, so a chart can say what a
		-- service is written in without depending on that process still being
		-- alive. It is last, and must stay last: a database created before this
		-- column existed gets it appended by the migration below, and the span
		-- appender is positional, so a fresh database has to lay its columns out
		-- in the same order a migrated one ends up with.
		runtime        VARCHAR
	)`,

	// Trace lookup is the single hottest query in the UI: open a trace, fetch
	// all of its spans.
	`CREATE INDEX IF NOT EXISTS idx_spans_trace ON spans (trace_id)`,
	// The trace list filters by time and service together.
	`CREATE INDEX IF NOT EXISTS idx_spans_ts ON spans (ts)`,
	`CREATE INDEX IF NOT EXISTS idx_spans_service_ts ON spans (service, ts)`,

	// Per-minute latency histogram, one row per occupied bucket. Sparse, so a
	// service with tight latency costs a handful of rows per minute.
	`CREATE TABLE IF NOT EXISTS rollup_1m (
		bucket_ts   TIMESTAMP NOT NULL,
		service     VARCHAR   NOT NULL,
		operation   VARCHAR   NOT NULL,
		kind        TINYINT   NOT NULL,
		dur_bucket  SMALLINT  NOT NULL,
		span_count  BIGINT    NOT NULL,
		error_count BIGINT    NOT NULL,
		sum_ns      BIGINT    NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_rollup_ts ON rollup_1m (bucket_ts)`,
	`CREATE INDEX IF NOT EXISTS idx_rollup_service ON rollup_1m (service, bucket_ts)`,

	// Service dependency edges, which draw the service map.
	`CREATE TABLE IF NOT EXISTS deps_1m (
		bucket_ts   TIMESTAMP NOT NULL,
		caller      VARCHAR   NOT NULL,
		callee      VARCHAR   NOT NULL,
		span_count  BIGINT    NOT NULL,
		error_count BIGINT    NOT NULL,
		sum_ns      BIGINT    NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_deps_ts ON deps_1m (bucket_ts)`,

	// Metrics. The shape mirrors spans: what is filtered or grouped on is a
	// column, and the rest stays in an attribute bag. service, host_name and
	// pid are the columns that let a metric be lined up against the traces
	// from the same process.
	`CREATE TABLE IF NOT EXISTS metrics (
		ts          TIMESTAMP NOT NULL,
		name        VARCHAR   NOT NULL,
		kind        TINYINT   NOT NULL,
		service     VARCHAR   NOT NULL,
		host_name   VARCHAR,
		pid         INTEGER,
		-- Gauges and sums carry a value; a sum reports its cumulative total,
		-- and is differenced at query time where the neighbouring point is known.
		value       DOUBLE,
		-- Histograms carry a summary and their buckets, so percentiles stay
		-- computable over any range rather than being fixed at write time.
		count       BIGINT,
		sum         DOUBLE,
		buckets     VARCHAR,
		unit        VARCHAR,
		attributes  VARCHAR
	)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_ts ON metrics (ts)`,
	// Charts always ask for one instrument on one service over a time range.
	`CREATE INDEX IF NOT EXISTS idx_metrics_lookup ON metrics (service, name, ts)`,

	// Per-minute metric rollup, so a long range costs a row per minute rather
	// than a row per collection interval.
	`CREATE TABLE IF NOT EXISTS metrics_1m (
		bucket_ts   TIMESTAMP NOT NULL,
		name        VARCHAR   NOT NULL,
		kind        TINYINT   NOT NULL,
		service     VARCHAR   NOT NULL,
		host_name   VARCHAR,
		point_count BIGINT    NOT NULL,
		value_avg   DOUBLE,
		value_min   DOUBLE,
		value_max   DOUBLE,
		-- The last value in the bucket, which is what a cumulative sum needs:
		-- differencing consecutive buckets gives the rate.
		value_last  DOUBLE
	)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_1m_ts ON metrics_1m (bucket_ts)`,
	`CREATE INDEX IF NOT EXISTS idx_metrics_1m_lookup ON metrics_1m (service, name, bucket_ts)`,

	// On-demand JVM diagnostics: thread dumps, class histograms, heap and flag
	// snapshots. Unlike everything above these do not stream in — each row is a
	// command an operator ran against one process at one moment.
	//
	// Both the parsed summary and the JVM's verbatim output are kept. The
	// summary is what the UI reads; the raw text is what survives a parser that
	// does not understand a future JVM's format, and is the only copy of
	// evidence that cannot be collected again once the process is restarted.
	`CREATE TABLE IF NOT EXISTS diagnostics (
		id          VARCHAR   NOT NULL,
		ts          TIMESTAMP NOT NULL,
		kind        VARCHAR   NOT NULL,
		pid         INTEGER   NOT NULL,
		-- start_time distinguishes two processes that shared a pid, which is
		-- the same reason the inventory keys entries on it.
		start_time  TIMESTAMP,
		service     VARCHAR,
		host_name   VARCHAR,
		duration_ms BIGINT,
		size_bytes  BIGINT,
		-- headline is a handful of counts computed at write time. The history
		-- list renders from it alone, so choosing which of two dumps to open
		-- never costs reading either of them.
		headline    VARCHAR,
		summary     VARCHAR,
		raw         VARCHAR
	)`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_diagnostics_id ON diagnostics (id)`,
	// The history list asks for one process's dumps of one kind, newest first.
	`CREATE INDEX IF NOT EXISTS idx_diagnostics_lookup ON diagnostics (pid, kind, ts)`,
}

// pragmaStatements tune the embedded engine for a monitoring workload sharing a
// host with the applications it watches.
func pragmaStatements(memoryLimit string, threads int) []string {
	var out []string
	if memoryLimit != "" {
		out = append(out, "SET memory_limit='"+memoryLimit+"'")
	}
	if threads > 0 {
		// apm2go must not consume the box it is monitoring, so the default is a
		// small fixed thread count rather than every core.
		out = append(out, "SET threads="+strconv.Itoa(threads))
	}
	return out
}

// Migrations bring an existing database up to the current schema.
//
// The statements above are all CREATE ... IF NOT EXISTS, which creates a
// missing table but never alters one that already exists — so a column added to
// a table's DDL reaches new databases only. These statements are what reach the
// rest, and they run on every open: each is written to be a no-op once applied.
var migrationStatements = []string{
	`ALTER TABLE spans ADD COLUMN IF NOT EXISTS runtime VARCHAR`,
}
