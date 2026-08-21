// Package netns answers one question about a process: from inside its network
// namespace, what address reaches apm2go?
//
// A containerized application cannot export to 127.0.0.1, because its loopback
// is its own. What it can reach is the gateway of the network it is attached
// to, which on a bridge network is an address the host itself owns.
//
// The target's routing table is read straight from /proc/<pid>/net/route. That
// file is namespace-aware per process: opened from the host it shows the
// routing table of that pid's network namespace, not the reader's. Measured on
// a container attached to a user-defined bridge, it reported that bridge's
// gateway while the reading process saw the default bridge's — so no setns and
// no privileged helper is needed here.
package netns

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Column positions in /proc/net/route. The file has a header line and then one
// space-separated row per route.
const (
	colIface = iota
	colDestination
	colGateway
	colFlags
	colRefCnt
	colUse
	colMetric
	colMask
	minColumns // every row must have at least this many fields
)

// zeroAddress is how the file spells 0.0.0.0, both as a destination (meaning
// the default route) and as a gateway (meaning the route is directly attached
// rather than via a router).
const zeroAddress = "00000000"

// Gateway returns the default-route gateway of the network namespace pid
// belongs to.
//
// When several default routes exist the one with the lowest metric wins, which
// is the same choice the kernel makes when routing a packet.
func Gateway(procRoot string, pid int) (netip.Addr, error) {
	path := filepath.Join(procRoot, strconv.Itoa(pid), "net", "route")

	file, err := os.Open(path)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("read routing table of pid %d: %w", pid, err)
	}
	defer file.Close()

	var (
		best       netip.Addr
		bestMetric = int64(-1)
	)

	scanner := bufio.NewScanner(file)
	for lineNumber := 0; scanner.Scan(); lineNumber++ {
		if lineNumber == 0 {
			// Header row.
			continue
		}
		fields := strings.Fields(scanner.Text())
		if len(fields) < minColumns {
			continue
		}
		// A default route goes everywhere: destination and mask are both zero.
		if fields[colDestination] != zeroAddress || fields[colMask] != zeroAddress {
			continue
		}
		// A default route with no gateway is directly attached and gives us no
		// address to export to.
		if fields[colGateway] == zeroAddress {
			continue
		}

		addr, err := parseHexAddress(fields[colGateway])
		if err != nil {
			continue
		}
		metric, err := strconv.ParseInt(fields[colMetric], 10, 64)
		if err != nil {
			metric = 0
		}
		if bestMetric < 0 || metric < bestMetric {
			best, bestMetric = addr, metric
		}
	}
	if err := scanner.Err(); err != nil {
		return netip.Addr{}, fmt.Errorf("read routing table of pid %d: %w", pid, err)
	}

	if !best.IsValid() {
		return netip.Addr{}, fmt.Errorf(
			"pid %d has no default route with a gateway; its network namespace has no route to this host", pid)
	}
	return best, nil
}

// parseHexAddress decodes an address as /proc/net/route writes it: the 32-bit
// value in the host's byte order, printed as eight hex digits. On every
// platform apm2go targets that is little endian, so 0x01001DAC is 172.29.0.1 —
// the bytes read back to front.
func parseHexAddress(hex string) (netip.Addr, error) {
	value, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("malformed address %q: %w", hex, err)
	}

	var octets [4]byte
	binary.LittleEndian.PutUint32(octets[:], uint32(value))
	return netip.AddrFrom4(octets), nil
}
