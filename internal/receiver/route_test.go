package receiver

import "testing"

func TestNormalizeRoute(t *testing.T) {
	tests := []struct {
		raw  string
		want string
	}{
		// Identifiers become placeholders so one endpoint is one row.
		{"/api/orders/8123/items", "/api/orders/{id}/items"},
		{"/users/550e8400-e29b-41d4-a716-446655440000", "/users/{id}"},
		{"/files/a1b2c3d4e5f60718293a4b5c6d7e8f90", "/files/{id}"},
		{"/api/user-48213/profile", "/api/{id}/profile"},

		// Real path names must survive, including ones that look hexadecimal
		// or contain a digit.
		{"/api/orders", "/api/orders"},
		{"/api/v1/feed", "/api/v1/feed"},
		{"/oauth2/token", "/oauth2/token"},
		{"/health", "/health"},

		// Absolute URLs are reduced to their path, and queries are dropped.
		{"http://svc.internal:8080/api/orders?page=2", "/api/orders"},
		{"https://example.com/a/1?x=y#frag", "/a/{id}"},

		{"/", "/"},
		{"", ""},
	}

	for _, tt := range tests {
		if got := NormalizeRoute(tt.raw); got != tt.want {
			t.Errorf("NormalizeRoute(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
}

func TestNormalizeRouteCapsDepth(t *testing.T) {
	// A deep generated path is pure cardinality past a point.
	got := NormalizeRoute("/a/b/c/d/e/f/g/h/i/j/k")
	if want := "/a/b/c/d/e/f/g/h/*"; got != want {
		t.Errorf("NormalizeRoute deep path = %q, want %q", got, want)
	}
}
