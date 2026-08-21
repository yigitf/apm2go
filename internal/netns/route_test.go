package netns

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// routeHeader is the header line every /proc/net/route starts with.
const routeHeader = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT"

// writeRouteTable builds /proc/<pid>/net/route inside a temporary tree.
func writeRouteTable(t *testing.T, pid int, rows ...string) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, strconv.Itoa(pid), "net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}

	content := routeHeader + "\n"
	for _, row := range rows {
		content += row + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "route"), []byte(content), 0o644); err != nil {
		t.Fatalf("write route table: %v", err)
	}
	return root
}

func TestGatewayDecodesHostByteOrder(t *testing.T) {
	// 01001DAC is 172.29.0.1: the file prints the 32-bit value in the host's
	// byte order, so the octets read back to front. This exact value was
	// measured from a container on a user-defined bridge whose gateway was
	// 172.29.0.1, so a byte-order mistake here would be caught.
	root := writeRouteTable(t, 42,
		"eth0\t00000000\t01001DAC\t0003\t0\t0\t0\t00000000\t0\t0\t0",
		"eth0\t00001DAC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0",
	)

	gateway, err := Gateway(root, 42)
	if err != nil {
		t.Fatalf("Gateway: %v", err)
	}
	if got, want := gateway.String(), "172.29.0.1"; got != want {
		t.Errorf("Gateway = %s, want %s", got, want)
	}
}

func TestGatewayPrefersTheLowestMetric(t *testing.T) {
	// The kernel routes by the lowest metric, so a host with two default routes
	// must be answered the same way the packet would actually go.
	root := writeRouteTable(t, 7,
		"eth1\t00000000\t0100A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0",
		"eth0\t00000000\t010011AC\t0003\t0\t0\t100\t00000000\t0\t0\t0",
	)

	gateway, err := Gateway(root, 7)
	if err != nil {
		t.Fatalf("Gateway: %v", err)
	}
	if got, want := gateway.String(), "172.17.0.1"; got != want {
		t.Errorf("Gateway = %s, want %s (the lower metric)", got, want)
	}
}

func TestGatewayIgnoresNonDefaultAndAttachedRoutes(t *testing.T) {
	tests := []struct {
		name string
		rows []string
	}{
		{
			name: "only a subnet route",
			rows: []string{"eth0\t00001DAC\t00000000\t0001\t0\t0\t0\t0000FFFF\t0\t0\t0"},
		},
		{
			// A default route with a zero gateway is directly attached and
			// offers no address to export to.
			name: "default route without a gateway",
			rows: []string{"eth0\t00000000\t00000000\t0001\t0\t0\t0\t00000000\t0\t0\t0"},
		},
		{
			name: "no routes at all",
			rows: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeRouteTable(t, 1, tt.rows...)
			if _, err := Gateway(root, 1); err == nil {
				t.Error("expected an error when there is no usable default gateway")
			}
		})
	}
}

func TestGatewayReportsAMissingRoutingTable(t *testing.T) {
	// A process that exits mid-scan is the common case, not an exceptional one.
	if _, err := Gateway(t.TempDir(), 999); err == nil {
		t.Error("expected an error for a process with no routing table")
	}
}
