package receiver

import (
	"strings"
)

// maxRouteSegments caps how much of a path is kept. Anything deeper is almost
// always generated, and keeping it would inflate cardinality without helping
// anyone read the trace list.
const maxRouteSegments = 8

// NormalizeRoute turns a concrete request path into a template suitable for
// grouping, e.g. "/api/orders/8123/items" becomes "/api/orders/{id}/items".
//
// This only runs when the instrumentation did not supply an http.route. Without
// it, every distinct id would become its own operation, and a single endpoint
// under load would fill the operation table on its own.
func NormalizeRoute(raw string) string {
	if raw == "" {
		return ""
	}

	// An absolute URL arrives from HTTP clients; keep only the path.
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.IndexByte(rest, '/'); j >= 0 {
			raw = rest[j:]
		} else {
			raw = "/"
		}
	}
	// Query strings and fragments are pure cardinality.
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if raw == "" || raw == "/" {
		return "/"
	}

	segments := strings.Split(strings.Trim(raw, "/"), "/")
	if len(segments) > maxRouteSegments {
		segments = append(segments[:maxRouteSegments], "*")
	}

	for i, seg := range segments {
		if isIdentifierSegment(seg) {
			segments[i] = "{id}"
		}
	}
	return "/" + strings.Join(segments, "/")
}

// isIdentifierSegment reports whether a path segment looks like a value rather
// than a name.
//
// The rules are deliberately conservative: a false positive collapses two real
// endpoints into one row, which is worse than leaving a little extra
// cardinality behind.
func isIdentifierSegment(seg string) bool {
	if seg == "" {
		return false
	}
	switch {
	case isAllDigits(seg):
		return true
	case isUUID(seg):
		return true
	case isLongHex(seg):
		return true
	case hasLongDigitRun(seg):
		// Catches mixed identifiers such as "user-48213" or "v2_9981224".
		return true
	default:
		return false
	}
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// isUUID matches the canonical 8-4-4-4-12 form.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !isHexDigit(r) {
				return false
			}
		}
	}
	return true
}

// isLongHex matches hashes, object ids and tokens: 16 hex characters or more.
// Shorter runs are left alone because real path names such as "feed" and
// "added" are valid hex too.
func isLongHex(s string) bool {
	if len(s) < 16 {
		return false
	}
	for _, r := range s {
		if !isHexDigit(r) {
			return false
		}
	}
	return true
}

// minDigitRun is how many consecutive digits mark a segment as an identifier.
//
// Four is the threshold because it is above every digit run that appears in a
// real path name — "oauth2", "v1", "s3", "utf8", "sha256" all top out at three —
// and below the length of any counter, timestamp or year that would otherwise
// multiply into thousands of operations.
const minDigitRun = 4

// hasLongDigitRun matches segments carrying a run of consecutive digits, which
// is how slug-style identifiers such as "user-48213" or "order_2024" look.
func hasLongDigitRun(s string) bool {
	run := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			run++
			if run >= minDigitRun {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

func isHexDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}
