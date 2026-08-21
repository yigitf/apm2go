package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Queries read from the raw span table rather than the rollups whenever the
// requested range still has raw spans, because raw spans give exact
// percentiles and support every filter. The rollups exist for ranges older
// than the span retention, where raw data no longer exists.

// entrySpanPredicate restricts aggregate queries to the spans that represent
// work a service was asked to do — inbound requests and consumed messages.
// Counting internal and outbound spans as well would inflate throughput by
// however deeply a service happens to be instrumented.
const entrySpanPredicate = `(kind = 2 OR kind = 5 OR is_root)`

// ListServices returns RED metrics per service over a time range.
func (s *Store) ListServices(ctx context.Context, r TimeRange) ([]ServiceStats, error) {
	const query = `
		SELECT
			service,
			count(*)                            AS span_count,
			count(*) FILTER (WHERE is_error)    AS error_count,
			avg(duration_ns)::BIGINT            AS avg_ns,
			max(duration_ns)                    AS max_ns,
			quantile_cont(duration_ns, 0.50)::BIGINT AS p50_ns,
			quantile_cont(duration_ns, 0.95)::BIGINT AS p95_ns,
			quantile_cont(duration_ns, 0.99)::BIGINT AS p99_ns,
			max(ts)                             AS last_seen,
			-- One value per service, not per span: every span from a process
			-- carries the same runtime, and max() ignores the NULLs left by
			-- spans stored before the column existed.
			max(runtime)                        AS runtime
		FROM spans
		WHERE ts >= ? AND ts < ? AND ` + entrySpanPredicate + `
		GROUP BY service
		ORDER BY span_count DESC`

	rows, err := s.db.QueryContext(ctx, query, r.From, r.To)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	defer rows.Close()

	seconds := rangeSeconds(r)
	var out []ServiceStats
	for rows.Next() {
		var (
			st      ServiceStats
			runtime sql.NullString
		)
		if err := rows.Scan(
			&st.Service, &st.SpanCount, &st.ErrorCount,
			&st.AvgLatencyNanos, &st.MaxLatencyNanos,
			&st.P50LatencyNanos, &st.P95LatencyNanos, &st.P99LatencyNanos,
			&st.LastSeen, &runtime,
		); err != nil {
			return nil, fmt.Errorf("scan service row: %w", err)
		}
		st.Runtime = runtime.String
		st.finalize(seconds)
		out = append(out, st)
	}
	return out, rows.Err()
}

// ListOperations returns RED metrics per operation within one service.
func (s *Store) ListOperations(ctx context.Context, service string, r TimeRange) ([]OperationStats, error) {
	const query = `
		SELECT
			operation,
			kind,
			count(*)                            AS span_count,
			count(*) FILTER (WHERE is_error)    AS error_count,
			avg(duration_ns)::BIGINT            AS avg_ns,
			max(duration_ns)                    AS max_ns,
			quantile_cont(duration_ns, 0.50)::BIGINT AS p50_ns,
			quantile_cont(duration_ns, 0.95)::BIGINT AS p95_ns,
			quantile_cont(duration_ns, 0.99)::BIGINT AS p99_ns,
			max(ts)                             AS last_seen
		FROM spans
		WHERE ts >= ? AND ts < ? AND service = ? AND ` + entrySpanPredicate + `
		GROUP BY operation, kind
		ORDER BY span_count DESC
		LIMIT 500`

	rows, err := s.db.QueryContext(ctx, query, r.From, r.To, service)
	if err != nil {
		return nil, fmt.Errorf("list operations for %s: %w", service, err)
	}
	defer rows.Close()

	seconds := rangeSeconds(r)
	var out []OperationStats
	for rows.Next() {
		var (
			op   OperationStats
			kind int8
		)
		if err := rows.Scan(
			&op.Operation, &kind, &op.SpanCount, &op.ErrorCount,
			&op.AvgLatencyNanos, &op.MaxLatencyNanos,
			&op.P50LatencyNanos, &op.P95LatencyNanos, &op.P99LatencyNanos,
			&op.LastSeen,
		); err != nil {
			return nil, fmt.Errorf("scan operation row: %w", err)
		}
		op.Service = service
		op.Kind = spanKindName(kind)
		op.finalize(seconds)
		out = append(out, op)
	}
	return out, rows.Err()
}

// TimeSeries returns per-bucket throughput, error rate and latency for a
// service, or for all services when service is empty.
func (s *Store) TimeSeries(ctx context.Context, service string, r TimeRange, step time.Duration) ([]TimeSeriesPoint, error) {
	if step <= 0 {
		step = defaultStep(r)
	}

	// time_bucket aligns rows onto fixed boundaries so that consecutive
	// requests for the same range return the same buckets, which keeps charts
	// from shifting as time passes.
	query := `
		SELECT
			time_bucket(INTERVAL ` + intervalLiteral(step) + `, ts) AS bucket,
			count(*)                                 AS span_count,
			count(*) FILTER (WHERE is_error)         AS error_count,
			avg(duration_ns)::BIGINT                 AS avg_ns,
			quantile_cont(duration_ns, 0.95)::BIGINT AS p95_ns
		FROM spans
		WHERE ts >= ? AND ts < ? AND ` + entrySpanPredicate

	args := []any{r.From, r.To}
	if service != "" {
		query += ` AND service = ?`
		args = append(args, service)
	}
	query += ` GROUP BY bucket ORDER BY bucket`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("time series: %w", err)
	}
	defer rows.Close()

	stepSeconds := step.Seconds()
	var out []TimeSeriesPoint
	for rows.Next() {
		var p TimeSeriesPoint
		if err := rows.Scan(&p.Timestamp, &p.Count, &p.ErrorCount, &p.AvgLatencyNanos, &p.P95LatencyNanos); err != nil {
			return nil, fmt.Errorf("scan time series row: %w", err)
		}
		p.Rate = float64(p.Count) / stepSeconds
		if p.Count > 0 {
			p.ErrorRate = float64(p.ErrorCount) / float64(p.Count)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// Dependencies returns the service map edges for a time range.
//
// An edge exists when a client span in one service is the parent of an entry
// span in another. Joining parent to child is what makes the edge real rather
// than inferred from a peer.service attribute, which is often absent or wrong.
func (s *Store) Dependencies(ctx context.Context, r TimeRange) ([]Dependency, error) {
	const query = `
		SELECT
			parent.service                        AS caller,
			child.service                         AS callee,
			count(*)                              AS call_count,
			count(*) FILTER (WHERE child.is_error) AS error_count,
			avg(child.duration_ns)::BIGINT        AS avg_ns
		FROM spans AS child
		JOIN spans AS parent
		  ON child.parent_span_id = parent.span_id
		 AND child.trace_id = parent.trace_id
		WHERE child.ts >= ? AND child.ts < ?
		  AND parent.ts >= ? AND parent.ts < ?
		  AND child.service <> parent.service
		GROUP BY caller, callee
		ORDER BY call_count DESC
		LIMIT 500`

	rows, err := s.db.QueryContext(ctx, query, r.From, r.To, r.From, r.To)
	if err != nil {
		return nil, fmt.Errorf("service dependencies: %w", err)
	}
	defer rows.Close()

	var out []Dependency
	for rows.Next() {
		var d Dependency
		if err := rows.Scan(&d.Caller, &d.Callee, &d.CallCount, &d.ErrorCount, &d.AvgLatency); err != nil {
			return nil, fmt.Errorf("scan dependency row: %w", err)
		}
		if d.CallCount > 0 {
			d.ErrorRate = float64(d.ErrorCount) / float64(d.CallCount)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Stats describes the database contents, for the settings page.
func (s *Store) Stats(ctx context.Context) (StorageStats, error) {
	var st StorageStats

	err := s.db.QueryRowContext(ctx, `
		SELECT
			count(*),
			coalesce(min(ts), NULL),
			coalesce(max(ts), NULL),
			count(DISTINCT service)
		FROM spans`).Scan(&st.SpanCount, &nullTime{&st.OldestSpan}, &nullTime{&st.NewestSpan}, &st.Services)
	if err != nil {
		return st, fmt.Errorf("storage stats: %w", err)
	}

	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM rollup_1m`).Scan(&st.RollupCount); err != nil {
		return st, fmt.Errorf("rollup count: %w", err)
	}

	// Read from disk rather than a DuckDB pragma: the size an operator cares
	// about, for capacity planning or for judging storage.max_size_bytes, is
	// what the filesystem actually holds, WAL included.
	if size, err := s.FileSizeBytes(); err == nil {
		st.SizeBytes = size
	}
	return st, nil
}

// finalize derives the ratios that every consumer would otherwise recompute.
func (st *ServiceStats) finalize(rangeSeconds float64) {
	if st.SpanCount > 0 {
		st.ErrorRate = float64(st.ErrorCount) / float64(st.SpanCount)
	}
	if rangeSeconds > 0 {
		st.RequestsPerSecond = float64(st.SpanCount) / rangeSeconds
	}
}

func rangeSeconds(r TimeRange) float64 {
	if d := r.Duration().Seconds(); d > 0 {
		return d
	}
	return 1
}

// defaultStep picks a bucket width giving roughly 120 points, which is about
// as many as a chart can show without aliasing.
func defaultStep(r TimeRange) time.Duration {
	const targetPoints = 120

	step := r.Duration() / targetPoints
	for _, candidate := range []time.Duration{
		time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second,
		time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute,
		time.Hour, 3 * time.Hour, 6 * time.Hour, 12 * time.Hour, 24 * time.Hour,
	} {
		if step <= candidate {
			return candidate
		}
	}
	return 24 * time.Hour
}

// intervalLiteral renders a duration as a DuckDB interval. It is inlined into
// the SQL rather than bound as a parameter because DuckDB does not accept a
// placeholder inside an INTERVAL literal; the value is derived from
// defaultStep's fixed list, never from user input.
func intervalLiteral(step time.Duration) string {
	return "'" + fmt.Sprintf("%d", int64(step.Seconds())) + " seconds'"
}

func spanKindName(kind int8) string {
	switch kind {
	case 1:
		return "internal"
	case 2:
		return "server"
	case 3:
		return "client"
	case 4:
		return "producer"
	case 5:
		return "consumer"
	default:
		return "unspecified"
	}
}

// nullTime scans a nullable timestamp into a time.Time, leaving it zero when
// the column is NULL.
type nullTime struct{ dst *time.Time }

func (n *nullTime) Scan(value any) error {
	var t sql.NullTime
	if err := t.Scan(value); err != nil {
		return err
	}
	if t.Valid {
		*n.dst = t.Time
	}
	return nil
}
