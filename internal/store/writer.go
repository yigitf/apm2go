package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/marcboeker/go-duckdb/v2"

	"github.com/apm2go/apm2go/internal/model"
)

// WriteSpans persists a batch of spans.
//
// It uses DuckDB's appender rather than INSERT statements: the appender writes
// directly into column vectors, which is roughly an order of magnitude faster
// for the batch sizes the pipeline produces and avoids re-parsing SQL per row.
//
// The appender is opened and closed per batch. Holding one open across batches
// would be faster still, but an appender pins its rows in memory until flushed,
// and a crash would lose everything buffered; per-batch keeps the exposure to
// exactly one batch.
func (s *Store) WriteSpans(ctx context.Context, spans []*model.Span) error {
	if len(spans) == 0 {
		return nil
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	conn, err := s.connector.Connect(ctx)
	if err != nil {
		return fmt.Errorf("open writer connection: %w", err)
	}
	defer conn.Close()

	appender, err := duckdb.NewAppenderFromConn(conn, "", "spans")
	if err != nil {
		return fmt.Errorf("open span appender: %w", err)
	}

	if err := appendSpans(appender, spans); err != nil {
		// Close discards the pending rows; the batch is lost either way, and
		// leaving the appender open would leak the connection.
		_ = appender.Close()
		return err
	}

	if err := appender.Close(); err != nil {
		return fmt.Errorf("flush span batch: %w", err)
	}
	return nil
}

// appendSpans writes each span as a row. The column order must match the DDL in
// schema.go exactly, because the appender is positional.
func appendSpans(appender *duckdb.Appender, spans []*model.Span) error {
	for _, span := range spans {
		attributes, err := encodeJSON(span.Attributes)
		if err != nil {
			return fmt.Errorf("encode attributes for span %s: %w", span.SpanID, err)
		}
		events, err := encodeJSON(span.Events)
		if err != nil {
			return fmt.Errorf("encode events for span %s: %w", span.SpanID, err)
		}

		err = appender.AppendRow(
			span.Timestamp.UTC(),
			span.TraceID[:],
			span.SpanID[:],
			nullableBytes(span.ParentSpanID),
			span.Service,
			span.Operation,
			int8(span.Kind),
			int64(span.Duration),
			BucketIndex(int64(span.Duration)),
			int8(span.Status),
			nullableString(span.StatusMessage),
			span.IsError(),
			span.IsRoot(),
			nullableString(span.HTTPMethod),
			nullableString(span.HTTPRoute),
			nullableInt32(span.HTTPStatus),
			nullableString(span.DBSystem),
			nullableString(span.DBStatement),
			nullableString(span.DBName),
			nullableString(span.PeerService),
			nullableString(span.HostName),
			nullableInt32(span.PID),
			nullableString(span.ExceptionType()),
			attributes,
			events,
			nullableString(span.Runtime),
		)
		if err != nil {
			return fmt.Errorf("append span %s: %w", span.SpanID, err)
		}
	}
	return nil
}

// encodeJSON renders a value as JSON, returning nil for empty input so the
// column stays NULL rather than holding "{}" or "null".
func encodeJSON(v any) (any, error) {
	switch typed := v.(type) {
	case map[string]string:
		if len(typed) == 0 {
			return nil, nil
		}
	case []model.Event:
		if len(typed) == 0 {
			return nil, nil
		}
	case nil:
		return nil, nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

// The appender maps a Go nil to SQL NULL. These helpers keep empty values out
// of the columns so that "IS NULL" means "absent" and queries do not have to
// test for empty strings as well.

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt32(n int) any {
	if n == 0 {
		return nil
	}
	return int32(n)
}

// nullableBytes renders a zero span id as NULL, which is how a root span's
// missing parent is stored.
func nullableBytes(id model.SpanID) any {
	if id.IsZero() {
		return nil
	}
	b := make([]byte, len(id))
	copy(b, id[:])
	return b
}
