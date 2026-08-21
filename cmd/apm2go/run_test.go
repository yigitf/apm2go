package main

import "testing"

// apiPort feeds directly into which port apm2go asks OBI to trace on itself.
// Get it wrong in the direction of "everything" and self-tracing stops being
// scoped to the REST API at all — see the comment on ebpf.WithSelf for why
// that specific mistake does not fail loudly, it loops.
func TestAPIPort(t *testing.T) {
	tests := []struct {
		addr    string
		want    int
		wantErr bool
	}{
		{addr: "0.0.0.0:18080", want: 18080},
		{addr: "127.0.0.1:8080", want: 8080},
		{addr: "[::]:18080", want: 18080},
		{addr: "not-an-address", wantErr: true},
		{addr: "", wantErr: true},
	}
	for _, tt := range tests {
		got, err := apiPort(tt.addr)
		if tt.wantErr {
			if err == nil {
				t.Errorf("apiPort(%q) = %d, <nil>, want an error", tt.addr, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("apiPort(%q) unexpected error: %v", tt.addr, err)
			continue
		}
		if got != tt.want {
			t.Errorf("apiPort(%q) = %d, want %d", tt.addr, got, tt.want)
		}
	}
}
