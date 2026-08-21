package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/apm2go/apm2go/internal/store"
)

// defaultRange is what a request with no time parameters means: the last hour,
// which is the window an operator investigating a live problem wants.
const defaultRange = time.Hour

// maxRange caps how much history one query may span, so a mistyped parameter
// cannot ask the store to aggregate the entire retention window.
const maxRange = 30 * 24 * time.Hour

// parseTimeRange resolves the from/to parameters.
//
// Three spellings are accepted because each is natural somewhere: RFC 3339 for
// generated links, Unix seconds for scripts, and a relative duration such as
// "-15m" for the UI's range picker.
func parseTimeRange(r *http.Request) store.TimeRange {
	now := time.Now().UTC()
	query := r.URL.Query()

	to := parseTime(query.Get("to"), now, now)
	from := parseTime(query.Get("from"), now, to.Add(-defaultRange))

	if !from.Before(to) {
		from = to.Add(-defaultRange)
	}
	if to.Sub(from) > maxRange {
		from = to.Add(-maxRange)
	}
	return store.TimeRange{From: from, To: to}
}

// parseTime decodes one timestamp parameter, falling back to fallback.
func parseTime(raw string, now, fallback time.Time) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}

	// Relative offsets are written as "-15m" or "now-15m".
	if offset, ok := strings.CutPrefix(raw, "now"); ok {
		raw = offset
		if raw == "" {
			return now
		}
	}
	if strings.HasPrefix(raw, "-") || strings.HasPrefix(raw, "+") {
		if d, err := time.ParseDuration(raw); err == nil {
			return now.Add(d)
		}
	}

	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t.UTC()
	}
	if secs, err := strconv.ParseInt(raw, 10, 64); err == nil {
		// Values large enough to be milliseconds are treated as such, since
		// JavaScript's Date.now() is the most common source of this parameter.
		if secs > 1e12 {
			return time.UnixMilli(secs).UTC()
		}
		return time.Unix(secs, 0).UTC()
	}
	return fallback
}

// parseDuration decodes a duration parameter, returning fallback when absent
// or malformed.
func parseDuration(raw string, fallback time.Duration) time.Duration {
	if raw == "" {
		return fallback
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	// Bare numbers are read as milliseconds, matching how the UI sends them.
	if ms, err := strconv.ParseInt(raw, 10, 64); err == nil && ms > 0 {
		return time.Duration(ms) * time.Millisecond
	}
	return fallback
}

// parseTraceFilter builds a trace search from the query parameters.
func parseTraceFilter(r *http.Request) store.TraceFilter {
	query := r.URL.Query()

	filter := store.TraceFilter{
		Range:       parseTimeRange(r),
		Service:     query.Get("service"),
		Operation:   query.Get("operation"),
		Search:      query.Get("search"),
		MinDuration: parseDuration(query.Get("min_duration"), 0),
		MaxDuration: parseDuration(query.Get("max_duration"), 0),
	}

	if v := query.Get("status"); v == "error" {
		filter.OnlyErrors = true
	}
	if v, err := strconv.Atoi(query.Get("http_status")); err == nil {
		filter.HTTPStatus = v
	}
	if v, err := strconv.Atoi(query.Get("limit")); err == nil {
		filter.Limit = v
	}
	return filter
}

// contextWithTimeout bounds a request's own context, so a slow operation is
// abandoned when either the client disconnects or the deadline passes.
func contextWithTimeout(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}
