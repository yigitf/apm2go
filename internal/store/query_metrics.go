package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// MetricSeries is one instrument's values over time, for one set of attributes.
type MetricSeries struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Unit string `json:"unit,omitempty"`
	// Labels are the attributes distinguishing this series from others sharing
	// the name — which memory pool, which disk, which direction.
	Labels map[string]string `json:"labels,omitempty"`
	Points []MetricPoint     `json:"points"`
}

// MetricPoint is one plotted value.
type MetricPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// MetricName describes an instrument that has data, for populating a picker.
type MetricName struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Unit        string `json:"unit,omitempty"`
	SeriesCount int64  `json:"series_count"`
}

// ListMetricNames returns the instruments a service reported in a time range.
func (s *Store) ListMetricNames(ctx context.Context, service string, r TimeRange) ([]MetricName, error) {
	const query = `
		SELECT name, any_value(kind), any_value(unit),
			-- A metric with no attributes still has one series, and count
			-- DISTINCT skips NULLs, which would report it as none.
			count(DISTINCT coalesce(attributes, ''))
		FROM metrics
		WHERE ts >= ? AND ts < ? AND service = ?
		GROUP BY name
		ORDER BY name`

	rows, err := s.db.QueryContext(ctx, query, r.From, r.To, service)
	if err != nil {
		return nil, fmt.Errorf("list metric names: %w", err)
	}
	defer rows.Close()

	var out []MetricName
	for rows.Next() {
		var (
			name MetricName
			kind int8
			unit sql.NullString
		)
		if err := rows.Scan(&name.Name, &kind, &unit, &name.SeriesCount); err != nil {
			return nil, fmt.Errorf("scan metric name: %w", err)
		}
		name.Kind = metricKindName(kind)
		name.Unit = unit.String
		out = append(out, name)
	}
	return out, rows.Err()
}

// QueryMetric returns one instrument's series over a time range, bucketed.
//
// A cumulative sum is differenced between consecutive buckets, because its
// stored value is a running total: charting it raw would draw a line that only
// ever climbs, whatever the system was actually doing.
func (s *Store) QueryMetric(ctx context.Context, service, name string, r TimeRange, step time.Duration) ([]MetricSeries, error) {
	if step <= 0 {
		step = defaultStep(r)
	}

	query := `
		SELECT
			attributes,
			any_value(kind) AS kind,
			any_value(unit) AS unit,
			time_bucket(INTERVAL ` + intervalLiteral(step) + `, ts) AS bucket,
			-- A gauge is averaged within the bucket; a cumulative sum needs its
			-- last value, so consecutive buckets can be differenced below.
			avg(value)  AS value_avg,
			last(value ORDER BY ts) AS value_last,
			-- A histogram has no single value. Its mean over the bucket is
			-- sum divided by count, which for a duration histogram is the
			-- average pause — the number the chart is asking about. The
			-- buckets stay in the table for percentiles later.
			sum(sum)    AS hist_sum,
			sum(count)  AS hist_count
		FROM metrics
		WHERE ts >= ? AND ts < ? AND service = ? AND name = ?
		GROUP BY attributes, bucket
		ORDER BY attributes, bucket`

	rows, err := s.db.QueryContext(ctx, query, r.From, r.To, service, name)
	if err != nil {
		return nil, fmt.Errorf("query metric %s: %w", name, err)
	}
	defer rows.Close()

	// Series are grouped by their attribute blob, which arrives sorted, so a
	// series is complete when the blob changes.
	var (
		out       []MetricSeries
		current   *MetricSeries
		lastKey   string
		lastTotal float64
		haveTotal bool
	)

	for rows.Next() {
		var (
			attributes sql.NullString
			kind       int8
			unit       sql.NullString
			bucket     time.Time
			avg        sql.NullFloat64
			last       sql.NullFloat64
			histSum    sql.NullFloat64
			histCount  sql.NullFloat64
		)
		if err := rows.Scan(&attributes, &kind, &unit, &bucket, &avg, &last, &histSum, &histCount); err != nil {
			return nil, fmt.Errorf("scan metric row: %w", err)
		}

		if current == nil || attributes.String != lastKey {
			out = append(out, MetricSeries{
				Name:   name,
				Kind:   metricKindName(kind),
				Unit:   unit.String,
				Labels: decodeAttributes(attributes),
			})
			current = &out[len(out)-1]
			lastKey = attributes.String
			haveTotal = false
		}

		value := avg.Float64
		if metricKindName(kind) == "histogram" {
			// No observations in this bucket means nothing happened, which is
			// a real zero rather than missing data.
			if histCount.Float64 > 0 {
				value = histSum.Float64 / histCount.Float64
			} else {
				value = 0
			}
		}
		if metricKindName(kind) == "sum" {
			// The first bucket of a counter has no predecessor to difference
			// against, so it is skipped rather than plotted as its total.
			if !haveTotal {
				lastTotal, haveTotal = last.Float64, true
				continue
			}
			delta := last.Float64 - lastTotal
			lastTotal = last.Float64
			// A counter that goes backwards means the process restarted and
			// reset it; the drop is an artefact, not a measurement.
			if delta < 0 {
				continue
			}
			value = delta
		}

		current.Points = append(current.Points, MetricPoint{Timestamp: bucket, Value: value})
	}
	return out, rows.Err()
}

// decodeAttributes reads the stored attribute blob back into a label map. A
// blob that fails to parse yields no labels rather than failing the query: the
// series is still worth charting without its labels.
func decodeAttributes(raw sql.NullString) map[string]string {
	if !raw.Valid || raw.String == "" {
		return nil
	}
	var labels map[string]string
	if err := json.Unmarshal([]byte(raw.String), &labels); err != nil {
		return nil
	}
	return labels
}

// metricKindName renders a stored kind.
func metricKindName(kind int8) string {
	switch kind {
	case 1:
		return "sum"
	case 2:
		return "histogram"
	default:
		return "gauge"
	}
}
