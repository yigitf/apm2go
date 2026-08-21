package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// metaRollupWatermark records the last minute that has been rolled up, so a
// restart resumes rather than recomputing or double counting.
const metaRollupWatermark = "rollup_watermark"

// rollupCatchUpLimit bounds how much history one maintenance pass will process.
// After a long outage the backlog is worked through over several passes instead
// of one query that would monopolise the database.
const rollupCatchUpLimit = 6 * time.Hour

// Maintainer keeps the database in shape: it aggregates raw spans into the
// rollup tables and enforces retention.
//
// Both jobs exist because raw spans and long-range charts have opposite needs.
// Raw spans are large and are kept for hours so a trace can be opened in full;
// the rollups are small and are kept for weeks so a month-long latency chart
// costs a few thousand rows rather than a few hundred million.
type Maintainer struct {
	store *Store
	log   *slog.Logger
}

// NewMaintainer returns a maintenance component for a store.
func NewMaintainer(store *Store, log *slog.Logger) *Maintainer {
	return &Maintainer{store: store, log: log}
}

// Name identifies this component in logs.
func (m *Maintainer) Name() string { return "store-maintenance" }

// Run performs maintenance on the configured interval until ctx is cancelled.
func (m *Maintainer) Run(ctx context.Context) error {
	interval := m.store.cfg.MaintenanceInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			m.runOnce(ctx)
		}
	}
}

// runOnce performs one full maintenance pass, logging rather than returning
// errors: a failed pass should not take the process down, since the next one is
// minutes away and ingest is unaffected.
func (m *Maintainer) runOnce(ctx context.Context) {
	start := time.Now()

	rolled, err := m.Rollup(ctx)
	if err != nil {
		m.log.Error("rollup failed", "error", err)
	}

	deleted, err := m.Retention(ctx)
	if err != nil {
		m.log.Error("retention failed", "error", err)
	}

	if rolled > 0 || deleted > 0 {
		m.log.Info("maintenance completed",
			"rolled_up_minutes", rolled,
			"deleted_rows", deleted,
			"took", time.Since(start).Round(time.Millisecond))
	}
}

// Rollup aggregates complete minutes of raw spans into the rollup tables and
// returns how many minutes were processed.
func (m *Maintainer) Rollup(ctx context.Context) (int, error) {
	from, err := m.watermark(ctx)
	if err != nil {
		return 0, err
	}

	// Only minutes that have certainly finished are rolled up: aggregating the
	// current minute would produce a row that later arrivals would contradict.
	to := time.Now().UTC().Truncate(time.Minute)
	if !from.Before(to) {
		return 0, nil
	}
	if to.Sub(from) > rollupCatchUpLimit {
		to = from.Add(rollupCatchUpLimit)
	}

	m.store.writeMu.Lock()
	defer m.store.writeMu.Unlock()

	tx, err := m.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin rollup transaction: %w", err)
	}
	defer tx.Rollback()

	// Re-running a window must not double count, so the target range is cleared
	// first. Together with the transaction this makes the whole pass idempotent.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM rollup_1m WHERE bucket_ts >= ? AND bucket_ts < ?`, from, to); err != nil {
		return 0, fmt.Errorf("clear rollup window: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM deps_1m WHERE bucket_ts >= ? AND bucket_ts < ?`, from, to); err != nil {
		return 0, fmt.Errorf("clear dependency window: %w", err)
	}

	if _, err := tx.ExecContext(ctx, rollupInsert, from, to); err != nil {
		return 0, fmt.Errorf("build latency rollups: %w", err)
	}
	if _, err := tx.ExecContext(ctx, depsInsert, from, to, from, to); err != nil {
		return 0, fmt.Errorf("build dependency rollups: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?, ?)
		 ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		metaRollupWatermark, to.Format(time.RFC3339Nano)); err != nil {
		return 0, fmt.Errorf("record rollup watermark: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit rollup: %w", err)
	}
	return int(to.Sub(from) / time.Minute), nil
}

// rollupInsert aggregates spans into per-minute, per-bucket latency histograms.
// Grouping by dur_bucket is what makes percentiles mergeable across any range.
const rollupInsert = `
	INSERT INTO rollup_1m
		(bucket_ts, service, operation, kind, dur_bucket, span_count, error_count, sum_ns)
	SELECT
		time_bucket(INTERVAL '1 minute', ts),
		service, operation, kind, dur_bucket,
		count(*),
		count(*) FILTER (WHERE is_error),
		sum(duration_ns)
	FROM spans
	WHERE ts >= ? AND ts < ? AND ` + entrySpanPredicate + `
	GROUP BY 1, 2, 3, 4, 5`

// depsInsert aggregates cross-service parent/child pairs into map edges.
const depsInsert = `
	INSERT INTO deps_1m
		(bucket_ts, caller, callee, span_count, error_count, sum_ns)
	SELECT
		time_bucket(INTERVAL '1 minute', child.ts),
		parent.service,
		child.service,
		count(*),
		count(*) FILTER (WHERE child.is_error),
		sum(child.duration_ns)
	FROM spans AS child
	JOIN spans AS parent
	  ON child.parent_span_id = parent.span_id
	 AND child.trace_id = parent.trace_id
	WHERE child.ts >= ? AND child.ts < ?
	  AND parent.ts >= ? AND parent.ts < ?
	  AND child.service <> parent.service
	GROUP BY 1, 2, 3`

// watermark returns the timestamp rollups should resume from.
func (m *Maintainer) watermark(ctx context.Context) (time.Time, error) {
	var raw string
	err := m.store.db.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = ?`, metaRollupWatermark).Scan(&raw)

	switch {
	case err == sql.ErrNoRows:
		// First run: start at the oldest span rather than the beginning of
		// time, so an empty database does not scan an empty range forever.
		var oldest sql.NullTime
		if err := m.store.db.QueryRowContext(ctx, `SELECT min(ts) FROM spans`).Scan(&oldest); err != nil {
			return time.Time{}, fmt.Errorf("find oldest span: %w", err)
		}
		if !oldest.Valid {
			return time.Now().UTC().Truncate(time.Minute), nil
		}
		return oldest.Time.UTC().Truncate(time.Minute), nil

	case err != nil:
		return time.Time{}, fmt.Errorf("read rollup watermark: %w", err)
	}

	parsed, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("rollup watermark %q is unreadable: %w", raw, err)
	}
	return parsed.UTC(), nil
}

// Retention deletes data past its configured lifetime and returns how many rows
// were removed.
func (m *Maintainer) Retention(ctx context.Context) (int64, error) {
	now := time.Now().UTC()

	m.store.writeMu.Lock()
	defer m.store.writeMu.Unlock()

	var total int64

	if d := m.store.cfg.SpanRetention; d > 0 {
		cutoff := now.Add(-d)
		// Spans are only dropped once the minutes covering them have been
		// rolled up, otherwise a maintenance hiccup would silently lose the
		// history those minutes represent.
		watermark, err := m.watermark(ctx)
		if err != nil {
			return total, err
		}
		if watermark.Before(cutoff) {
			cutoff = watermark
		}

		n, err := m.deleteRows(ctx, `DELETE FROM spans WHERE ts < ?`, cutoff)
		if err != nil {
			return total, fmt.Errorf("delete expired spans: %w", err)
		}
		total += n
	}

	if d := m.store.cfg.MetricRetention; d > 0 {
		// Metrics have no rollup dependency the way spans do, so they expire on
		// their own schedule.
		n, err := m.deleteRows(ctx, `DELETE FROM metrics WHERE ts < ?`, now.Add(-d))
		if err != nil {
			return total, fmt.Errorf("delete expired metrics: %w", err)
		}
		total += n
	}

	if d := m.store.cfg.RollupRetention; d > 0 {
		cutoff := now.Add(-d)
		for _, stmt := range []string{
			`DELETE FROM rollup_1m WHERE bucket_ts < ?`,
			`DELETE FROM deps_1m WHERE bucket_ts < ?`,
		} {
			n, err := m.deleteRows(ctx, stmt, cutoff)
			if err != nil {
				return total, fmt.Errorf("delete expired rollups: %w", err)
			}
			total += n
		}
	}

	// Diagnostics are pruned by age and by a per-process cap, which is why they
	// do not go through deleteRows like the streaming tables.
	n, err := m.store.pruneDiagnosticsLocked(ctx,
		m.store.cfg.DiagnosticRetention, m.store.cfg.MaxDiagnosticsPerJVM)
	if err != nil {
		return total, err
	}
	total += n

	if total > 0 {
		// DuckDB only returns space to the file on checkpoint, so a database
		// that never checkpoints grows even as rows are deleted.
		if _, err := m.store.db.ExecContext(ctx, `CHECKPOINT`); err != nil {
			m.log.Warn("checkpoint after retention failed", "error", err)
		}
	}
	return total, nil
}

func (m *Maintainer) deleteRows(ctx context.Context, stmt string, cutoff time.Time) (int64, error) {
	res, err := m.store.db.ExecContext(ctx, stmt, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		// Not every driver path reports this; the deletion still happened.
		return 0, nil
	}
	return n, nil
}
