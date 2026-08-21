package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/marcboeker/go-duckdb/v2"

	"github.com/apm2go/apm2go/internal/model"
)

// WriteMetrics persists a batch of metrics.
//
// It shares the writer lock with WriteSpans rather than taking its own: DuckDB
// admits one writer at a time, so two locks would only hide the contention
// behind a second queue.
func (s *Store) WriteMetrics(ctx context.Context, metrics []*model.Metric) error {
	if len(metrics) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	conn, err := s.connector.Connect(ctx)
	if err != nil {
		return fmt.Errorf("open writer connection: %w", err)
	}
	defer conn.Close()

	appender, err := duckdb.NewAppenderFromConn(conn, "", "metrics")
	if err != nil {
		return fmt.Errorf("open metric appender: %w", err)
	}

	if err := appendMetrics(appender, metrics, s.log); err != nil {
		_ = appender.Close()
		return err
	}
	if err := appender.Close(); err != nil {
		return fmt.Errorf("flush metric batch: %w", err)
	}
	return nil
}

// appendMetrics writes each metric as a row. Column order must match the DDL in
// schema.go exactly, because the appender is positional.
func appendMetrics(appender *duckdb.Appender, metrics []*model.Metric, log *slog.Logger) error {
	for _, metric := range metrics {
		// One unencodable metric must not cost the batch. Batches mix every
		// instrument from every service, so aborting on the first bad row threw
		// away hundreds of perfectly good measurements — which is how a single
		// histogram silently emptied whole services from the metrics view.
		attributes, err := encodeJSON(metric.Attributes)
		if err != nil {
			log.Warn("skipping metric with unencodable attributes",
				"metric", metric.Name, "service", metric.Service, "error", err)
			continue
		}

		// Buckets are stored as JSON rather than a DuckDB list: they are read
		// back whole, never filtered on, and JSON keeps the bound-count pairing
		// explicit instead of relying on two parallel arrays staying aligned.
		var buckets any
		if len(metric.Buckets) > 0 {
			encoded, err := json.Marshal(metric.Buckets)
			if err != nil {
				log.Warn("skipping metric with unencodable buckets",
					"metric", metric.Name, "service", metric.Service, "error", err)
				continue
			}
			buckets = string(encoded)
		}

		err = appender.AppendRow(
			metric.Timestamp.UTC(),
			metric.Name,
			int8(metric.Kind),
			metric.Service,
			nullableString(metric.HostName),
			nullableInt32(metric.PID),
			metric.Value,
			nullableUint64(metric.Count),
			metric.Sum,
			buckets,
			nullableString(metric.Unit),
			attributes,
		)
		if err != nil {
			return fmt.Errorf("append metric %s: %w", metric.Name, err)
		}
	}
	return nil
}

// nullableUint64 keeps a zero count out of the column, so "no observations" and
// "not a histogram" are both NULL rather than a misleading zero.
func nullableUint64(n uint64) any {
	if n == 0 {
		return nil
	}
	return int64(n)
}
