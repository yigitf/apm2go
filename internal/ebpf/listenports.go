package ebpf

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// listenPorts returns the TCP ports a process is listening on, deduplicated
// and ascending.
//
// OBI's discovery.instrument selects a target by the port it owns, not by pid
// — there is no pid selector in that mechanism, only glob-based ones (exe path,
// port, namespace). A glob on the executable would lump every Node process on
// the host under one name; the port is what actually distinguishes one running
// service from another; this is where that port comes from.
//
// The ordering is part of the contract, not a convenience. A socket bound on
// both IPv4 and IPv6 appears in the tcp and tcp6 tables alike and is one port
// either way, and the tables themselves are in no particular order — so
// returning them raw makes a target's ports differ between two scans that found
// exactly the same sockets, which reads as a changed target set and restarts
// OBI for nothing.
func listenPorts(procRoot string, pid int) ([]int, error) {
	inodes, err := socketInodes(procRoot, pid)
	if err != nil {
		return nil, err
	}
	if len(inodes) == 0 {
		return nil, nil
	}

	seen := make(map[int]bool)
	var ports []int
	for _, table := range []string{"tcp", "tcp6"} {
		// /proc/<pid>/net/tcp, not /proc/net/tcp: the socket table is
		// per-network-namespace, and the inodes above came out of the target's
		// own file descriptors. Read through apm2go's own /proc/net instead and
		// a process in a different network namespace — every containerized one,
		// once apm2go runs anywhere but inside that same container — matches no
		// row at all, so it is reported as listening on nothing and never
		// handed to OBI. Nothing errors; the service is simply never traced.
		p, err := listeningPortsInTable(filepath.Join(procRoot, strconv.Itoa(pid), "net", table), inodes)
		if err != nil {
			// A container's network namespace can make /proc/net unreadable or
			// absent; that is not this process's fault, so move on rather than
			// failing the whole lookup over one address family.
			continue
		}
		for _, port := range p {
			if !seen[port] {
				seen[port] = true
				ports = append(ports, port)
			}
		}
	}
	sort.Ints(ports)
	return ports, nil
}

// socketInodes reads the inode number encoded in every socket fd a process
// holds, e.g. /proc/<pid>/fd/12 -> "socket:[123456]".
func socketInodes(procRoot string, pid int) (map[uint64]bool, error) {
	fdDir := filepath.Join(procRoot, strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", fdDir, err)
	}

	inodes := make(map[uint64]bool)
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, e.Name()))
		if err != nil {
			// A fd can close between the readdir and the readlink; that is
			// ordinary and not a reason to abandon the rest of the directory.
			continue
		}
		if inode, ok := parseSocketLink(target); ok {
			inodes[inode] = true
		}
	}
	return inodes, nil
}

// parseSocketLink reads the inode out of a "socket:[12345]" symlink target.
func parseSocketLink(target string) (uint64, bool) {
	if !strings.HasPrefix(target, "socket:[") || !strings.HasSuffix(target, "]") {
		return 0, false
	}
	n, err := strconv.ParseUint(target[len("socket:["):len(target)-1], 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// tcpListenState is the "st" field /proc/net/tcp uses for a listening socket.
const tcpListenState = "0A"

// listeningPortsInTable scans one /proc/net/tcp{,6} table for rows in the
// listening state whose inode belongs to the target process.
func listeningPortsInTable(path string, ownedInodes map[uint64]bool) ([]int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var ports []int
	scanner := bufio.NewScanner(f)
	scanner.Scan() // header line
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		// local_address rem_address st ... inode is field index 9, but the
		// leading "sl:" column is not separated by the same spacing as the
		// rest, so field count rather than fixed offsets is what is trustworthy
		// here: inode is always the last field this format defines before the
		// kernel-internal columns some kernels append.
		if len(fields) < 10 || fields[3] != tcpListenState {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || !ownedInodes[inode] {
			continue
		}
		if port, ok := parseLocalPort(fields[1]); ok {
			ports = append(ports, port)
		}
	}
	return ports, scanner.Err()
}

// parseLocalPort reads the port out of the "local_address" column, which is
// hex address and hex port joined by ':', e.g. "0100007F:1F90".
func parseLocalPort(localAddress string) (int, bool) {
	_, hexPort, found := strings.Cut(localAddress, ":")
	if !found {
		return 0, false
	}
	port, err := strconv.ParseUint(hexPort, 16, 32)
	if err != nil {
		return 0, false
	}
	return int(port), true
}
