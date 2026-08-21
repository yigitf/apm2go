package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Diagnostic is one stored dump: a diagnostic command's result for one process
// at one moment.
//
// Unlike spans and metrics these are not a stream. Each row is something an
// operator deliberately collected, which is why they are written one at a time,
// kept far longer, and never dropped to keep up with ingest.
type Diagnostic struct {
	ID   string    `json:"id"`
	TS   time.Time `json:"ts"`
	Kind string    `json:"kind"`
	PID  int       `json:"pid"`
	// StartTime is the target process's start time. Two dumps only describe the
	// same process when this matches, however equal their pids look.
	StartTime time.Time `json:"start_time,omitempty"`
	Service   string    `json:"service,omitempty"`
	HostName  string    `json:"host_name,omitempty"`
	// DurationMS is how long the command took, and so roughly how long the
	// target was paused for it.
	DurationMS int64 `json:"duration_ms"`
	SizeBytes  int64 `json:"size_bytes"`

	// Headline is a small JSON object of counts, always loaded.
	Headline json.RawMessage `json:"headline,omitempty"`
	// Summary is the parsed structure and Raw the JVM's verbatim output. Both
	// are loaded only by Diagnostic lookups, never by the list.
	Summary json.RawMessage `json:"summary,omitempty"`
	Raw     string          `json:"raw,omitempty"`
}

// WriteDiagnostic stores one dump.
//
// This takes the writer lock like every other write path: DuckDB admits one
// writer, and a dump arriving while a span batch is being flushed must queue
// rather than fail.
func (s *Store) WriteDiagnostic(ctx context.Context, d *Diagnostic) error {
	if d.ID == "" {
		return fmt.Errorf("diagnostic has no id")
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO diagnostics
			(id, ts, kind, pid, start_time, service, host_name,
			 duration_ms, size_bytes, headline, summary, raw)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.ID,
		d.TS.UTC(),
		d.Kind,
		d.PID,
		nullableTime(d.StartTime),
		nullableString(d.Service),
		nullableString(d.HostName),
		d.DurationMS,
		d.SizeBytes,
		nullableJSON(d.Headline),
		nullableJSON(d.Summary),
		nullableString(d.Raw),
	)
	if err != nil {
		return fmt.Errorf("store %s diagnostic for pid %d: %w", d.Kind, d.PID, err)
	}
	return nil
}

// DiagnosticFilter narrows a history query. A zero PID means every process,
// and an empty Kind every kind.
type DiagnosticFilter struct {
	PID   int
	Kind  string
	Limit int
}

// defaultDiagnosticLimit bounds a history query that did not ask for one.
const defaultDiagnosticLimit = 50

// ListDiagnostics returns stored dumps newest first, without their bodies.
//
// The summary and raw columns are deliberately not selected: a thread dump of a
// busy application is megabytes, and the list exists to choose between dumps,
// not to read them.
func (s *Store) ListDiagnostics(ctx context.Context, f DiagnosticFilter) ([]Diagnostic, error) {
	query := `
		SELECT id, ts, kind, pid, start_time, service, host_name,
		       duration_ms, size_bytes, headline
		FROM diagnostics
		WHERE 1 = 1`
	var args []any

	if f.PID > 0 {
		query += ` AND pid = ?`
		args = append(args, f.PID)
	}
	if f.Kind != "" {
		query += ` AND kind = ?`
		args = append(args, f.Kind)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = defaultDiagnosticLimit
	}
	query += ` ORDER BY ts DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list diagnostics: %w", err)
	}
	defer rows.Close()

	out := make([]Diagnostic, 0, limit)
	for rows.Next() {
		var (
			d         Diagnostic
			startTime sql.NullTime
			service   sql.NullString
			host      sql.NullString
			headline  sql.NullString
		)
		if err := rows.Scan(&d.ID, &d.TS, &d.Kind, &d.PID, &startTime,
			&service, &host, &d.DurationMS, &d.SizeBytes, &headline); err != nil {
			return nil, fmt.Errorf("scan diagnostic: %w", err)
		}
		d.StartTime = startTime.Time
		d.Service = service.String
		d.HostName = host.String
		if headline.Valid {
			d.Headline = json.RawMessage(headline.String)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ErrDiagnosticNotFound is returned when an id names no stored dump. Callers
// turn it into a 404 rather than a 500: a dump aged out of retention is an
// ordinary outcome, not a failure.
var ErrDiagnosticNotFound = fmt.Errorf("diagnostic not found")

// GetDiagnostic returns one stored dump in full, parsed summary and raw text.
func (s *Store) GetDiagnostic(ctx context.Context, id string) (*Diagnostic, error) {
	var (
		d         Diagnostic
		startTime sql.NullTime
		service   sql.NullString
		host      sql.NullString
		headline  sql.NullString
		summary   sql.NullString
		raw       sql.NullString
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT id, ts, kind, pid, start_time, service, host_name,
		       duration_ms, size_bytes, headline, summary, raw
		FROM diagnostics WHERE id = ?`, id).
		Scan(&d.ID, &d.TS, &d.Kind, &d.PID, &startTime, &service, &host,
			&d.DurationMS, &d.SizeBytes, &headline, &summary, &raw)

	switch {
	case err == sql.ErrNoRows:
		return nil, ErrDiagnosticNotFound
	case err != nil:
		return nil, fmt.Errorf("read diagnostic %s: %w", id, err)
	}

	d.StartTime = startTime.Time
	d.Service = service.String
	d.HostName = host.String
	d.Raw = raw.String
	if headline.Valid {
		d.Headline = json.RawMessage(headline.String)
	}
	if summary.Valid {
		d.Summary = json.RawMessage(summary.String)
	}
	return &d, nil
}

// PruneDiagnostics drops dumps past their retention and, for each process and
// kind, everything beyond the newest keepPerJVM.
//
// The per-process cap matters more than the age one here. Diagnostics are
// collected in bursts — an operator chasing a leak takes a histogram every few
// minutes — so a week-long window alone would let one afternoon's investigation
// outgrow the telemetry it sits beside.
func (s *Store) PruneDiagnostics(ctx context.Context, retention time.Duration, keepPerJVM int) (int64, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	return s.pruneDiagnosticsLocked(ctx, retention, keepPerJVM)
}

// pruneDiagnosticsLocked is the body of PruneDiagnostics for callers that
// already hold the writer lock. writeMu is not reentrant, so the retention pass
// — which holds it across every table — must take this path.
func (s *Store) pruneDiagnosticsLocked(ctx context.Context, retention time.Duration, keepPerJVM int) (int64, error) {
	var total int64

	if retention > 0 {
		res, err := s.db.ExecContext(ctx,
			`DELETE FROM diagnostics WHERE ts < ?`, time.Now().UTC().Add(-retention))
		if err != nil {
			return total, fmt.Errorf("delete expired diagnostics: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}

	if keepPerJVM > 0 {
		n, err := s.trimDiagnosticsLocked(ctx, keepPerJVM)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// trimDiagnosticsLocked drops everything beyond the newest keep dumps of each
// kind for each process.
//
// The over-quota rows are selected first and deleted by id, rather than being
// deleted through a subquery over the same table: a DELETE whose subquery reads
// the table it is deleting from matched nothing here, and silently keeping every
// dump is exactly the failure this cap exists to prevent.
func (s *Store) trimDiagnosticsLocked(ctx context.Context, keep int) (int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id FROM (
			SELECT id, row_number() OVER (
				PARTITION BY pid, start_time, kind ORDER BY ts DESC
			) AS position
			FROM diagnostics
		) AS ranked
		WHERE position > ?`, keep)
	if err != nil {
		return 0, fmt.Errorf("find over-quota diagnostics: %w", err)
	}

	var expired []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan over-quota diagnostic: %w", err)
		}
		expired = append(expired, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("find over-quota diagnostics: %w", err)
	}

	var total int64
	for _, id := range expired {
		res, err := s.db.ExecContext(ctx, `DELETE FROM diagnostics WHERE id = ?`, id)
		if err != nil {
			return total, fmt.Errorf("delete over-quota diagnostic %s: %w", id, err)
		}
		if n, err := res.RowsAffected(); err == nil {
			total += n
		}
	}
	return total, nil
}

// nullableTime keeps a zero time out of the column so "unknown" reads as NULL
// rather than the year 1.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// nullableJSON stores an empty document as NULL.
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}
