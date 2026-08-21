package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/apm2go/apm2go/internal/model"
)

// candidateMultiplier decides how many traces the matching stage may collect
// before the root-span filters run. Filters such as minimum duration apply to
// the root span, which is not necessarily the span that matched, so the
// matching stage has to over-collect and let the final stage narrow down.
const candidateMultiplier = 10

// SearchTraces returns trace summaries matching a filter, newest first.
//
// The query runs in three stages: find the traces containing a matching span,
// aggregate each of those traces, and describe each one by its root span. The
// split matters because a filter like "service = orders" is about any span in
// the trace, while "duration > 2s" is about the trace as a whole.
func (s *Store) SearchTraces(ctx context.Context, f TraceFilter) ([]TraceSummary, error) {
	matchClause, matchArgs := traceMatchClause(f)

	query := `
		WITH matched AS (
			SELECT DISTINCT trace_id
			FROM spans
			WHERE ` + matchClause + `
			LIMIT ` + itoa(f.limit()*candidateMultiplier) + `
		),
		agg AS (
			SELECT
				s.trace_id,
				count(*)                          AS span_count,
				count(*) FILTER (WHERE s.is_error) AS error_count,
				count(DISTINCT s.service)         AS service_count,
				min(s.ts)                         AS start_time
			FROM spans s
			JOIN matched m ON s.trace_id = m.trace_id
			GROUP BY s.trace_id
		),
		root AS (
			SELECT
				s.trace_id, s.service, s.operation, s.duration_ns,
				s.http_method, s.http_route, s.http_status, s.exception_type,
				row_number() OVER (
					PARTITION BY s.trace_id
					-- Prefer the declared root; fall back to the earliest span,
					-- because a trace whose root was sampled away still needs a
					-- representative row in the list.
					ORDER BY s.is_root DESC, s.ts ASC, s.duration_ns DESC
				) AS rn
			FROM spans s
			JOIN matched m ON s.trace_id = m.trace_id
		)
		SELECT
			agg.trace_id, root.service, root.operation, agg.start_time,
			root.duration_ns, agg.span_count, agg.error_count, agg.service_count,
			root.http_method, root.http_route, root.http_status, root.exception_type
		FROM agg
		JOIN root ON root.trace_id = agg.trace_id AND root.rn = 1`

	args := matchArgs
	var conditions []string
	if f.MinDuration > 0 {
		conditions = append(conditions, "root.duration_ns >= ?")
		args = append(args, int64(f.MinDuration))
	}
	if f.MaxDuration > 0 {
		conditions = append(conditions, "root.duration_ns <= ?")
		args = append(args, int64(f.MaxDuration))
	}
	if f.HTTPStatus > 0 {
		conditions = append(conditions, "root.http_status = ?")
		args = append(args, f.HTTPStatus)
	}
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	query += " ORDER BY agg.start_time DESC LIMIT " + itoa(f.limit())

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search traces: %w", err)
	}
	defer rows.Close()

	var out []TraceSummary
	for rows.Next() {
		var (
			t          TraceSummary
			traceID    []byte
			httpMethod sql.NullString
			httpRoute  sql.NullString
			httpStatus sql.NullInt32
			exception  sql.NullString
		)
		if err := rows.Scan(
			&traceID, &t.RootService, &t.RootOperation, &t.StartTime,
			&t.DurationNanos, &t.SpanCount, &t.ErrorCount, &t.ServiceCount,
			&httpMethod, &httpRoute, &httpStatus, &exception,
		); err != nil {
			return nil, fmt.Errorf("scan trace summary: %w", err)
		}
		t.TraceID = hexEncode(traceID)
		t.HTTPMethod = httpMethod.String
		t.HTTPRoute = httpRoute.String
		t.HTTPStatus = int(httpStatus.Int32)
		t.ExceptionType = exception.String
		out = append(out, t)
	}
	return out, rows.Err()
}

// traceMatchClause builds the span-level predicate that selects candidate
// traces, along with its arguments.
func traceMatchClause(f TraceFilter) (string, []any) {
	conditions := []string{"ts >= ?", "ts < ?"}
	args := []any{f.Range.From, f.Range.To}

	if f.Service != "" {
		conditions = append(conditions, "service = ?")
		args = append(args, f.Service)
	}
	if f.Operation != "" {
		conditions = append(conditions, "operation = ?")
		args = append(args, f.Operation)
	}
	if f.OnlyErrors {
		conditions = append(conditions, "is_error")
	}
	if f.Search != "" {
		// One search box across the three fields an operator actually searches:
		// what ran, which endpoint, and which query.
		conditions = append(conditions,
			"(operation ILIKE ? OR http_route ILIKE ? OR db_statement ILIKE ?)")
		pattern := "%" + f.Search + "%"
		args = append(args, pattern, pattern, pattern)
	}
	return strings.Join(conditions, " AND "), args
}

// GetTrace loads every span of a trace, ordered for waterfall rendering.
func (s *Store) GetTrace(ctx context.Context, traceID model.TraceID) (*Trace, error) {
	const query = `
		SELECT
			ts, trace_id, span_id, parent_span_id, service, operation, kind,
			duration_ns, status, status_message,
			http_method, http_route, http_status,
			db_system, db_statement, db_name,
			peer_service, host_name, pid, attributes, events
		FROM spans
		WHERE trace_id = ?
		ORDER BY ts ASC, duration_ns DESC
		LIMIT 10000`

	rows, err := s.db.QueryContext(ctx, query, traceID[:])
	if err != nil {
		return nil, fmt.Errorf("load trace %s: %w", traceID, err)
	}
	defer rows.Close()

	trace := &Trace{TraceID: traceID.String()}
	services := make(map[string]struct{})
	var end time.Time

	for rows.Next() {
		span, err := scanSpan(rows)
		if err != nil {
			return nil, err
		}
		trace.Spans = append(trace.Spans, span)
		services[span.Service] = struct{}{}

		if trace.StartTime.IsZero() || span.Timestamp.Before(trace.StartTime) {
			trace.StartTime = span.Timestamp
		}
		if spanEnd := span.EndTime(); spanEnd.After(end) {
			end = spanEnd
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(trace.Spans) == 0 {
		return nil, ErrTraceNotFound
	}

	// The trace's length is the span of its wall clock extent, not the root
	// span's duration: an async trace can outlive its own root.
	trace.DurationNanos = int64(end.Sub(trace.StartTime))
	for service := range services {
		trace.Services = append(trace.Services, service)
	}
	return trace, nil
}

// ErrTraceNotFound is returned when a trace id matches no stored spans.
var ErrTraceNotFound = fmt.Errorf("trace not found")

// scanSpan reads one span row.
func scanSpan(rows *sql.Rows) (*model.Span, error) {
	var (
		span                             model.Span
		traceID, spanID, parentID        []byte
		kind, status                     int8
		durationNs                       int64
		statusMessage, httpMethod        sql.NullString
		httpRoute, dbSystem, dbStatement sql.NullString
		dbName, peerService, hostName    sql.NullString
		attributes, events               sql.NullString
		httpStatus, pid                  sql.NullInt32
	)

	if err := rows.Scan(
		&span.Timestamp, &traceID, &spanID, &parentID,
		&span.Service, &span.Operation, &kind, &durationNs,
		&status, &statusMessage,
		&httpMethod, &httpRoute, &httpStatus,
		&dbSystem, &dbStatement, &dbName,
		&peerService, &hostName, &pid, &attributes, &events,
	); err != nil {
		return nil, fmt.Errorf("scan span: %w", err)
	}

	copy(span.TraceID[:], traceID)
	copy(span.SpanID[:], spanID)
	copy(span.ParentSpanID[:], parentID)

	span.Kind = model.SpanKind(kind)
	span.Status = model.StatusCode(status)
	span.Duration = time.Duration(durationNs)
	span.StatusMessage = statusMessage.String
	span.HTTPMethod = httpMethod.String
	span.HTTPRoute = httpRoute.String
	span.HTTPStatus = int(httpStatus.Int32)
	span.DBSystem = dbSystem.String
	span.DBStatement = dbStatement.String
	span.DBName = dbName.String
	span.PeerService = peerService.String
	span.HostName = hostName.String
	span.PID = int(pid.Int32)

	if attributes.Valid {
		// A span whose attribute blob failed to parse is still worth showing;
		// losing its attributes is better than losing the span.
		_ = json.Unmarshal([]byte(attributes.String), &span.Attributes)
	}
	if events.Valid {
		_ = json.Unmarshal([]byte(events.String), &span.Events)
	}
	return &span, nil
}

const hexDigits = "0123456789abcdef"

// hexEncode renders an id blob as lowercase hex.
func hexEncode(b []byte) string {
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hexDigits[c>>4]
		out[i*2+1] = hexDigits[c&0x0f]
	}
	return string(out)
}

// itoa renders an int for inlining into SQL. Only used for values apm2go
// computes itself, never for user input.
func itoa(n int) string {
	if n <= 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
