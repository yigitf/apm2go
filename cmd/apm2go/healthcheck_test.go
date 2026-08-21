package main

import "testing"

func TestProbeAddressResolvesWildcardBinds(t *testing.T) {
	tests := []struct {
		name       string
		listenAddr string
		want       string
	}{
		{
			// The case that shipped broken: a health check aimed at a hardcoded
			// 8080 reported a working apm2go on 9080 as unhealthy.
			name:       "non-default port is preserved",
			listenAddr: "0.0.0.0:9080",
			want:       "127.0.0.1:9080",
		},
		{
			// A wildcard says where connections are accepted, not where to
			// reach the process; loopback is always right from inside.
			name:       "wildcard becomes loopback",
			listenAddr: "0.0.0.0:8080",
			want:       "127.0.0.1:8080",
		},
		{
			name:       "IPv6 wildcard becomes loopback",
			listenAddr: "[::]:8080",
			want:       "127.0.0.1:8080",
		},
		{
			name:       "an explicit address is left alone",
			listenAddr: "127.0.0.1:8080",
			want:       "127.0.0.1:8080",
		},
		{
			name:       "a bare port binds every interface",
			listenAddr: ":8080",
			want:       "127.0.0.1:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := probeAddress(tt.listenAddr); got != tt.want {
				t.Errorf("probeAddress(%q) = %q, want %q", tt.listenAddr, got, tt.want)
			}
		})
	}
}
