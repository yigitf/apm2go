package discovery

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// HotSpot writes a memory-mapped performance data file per JVM under
// /tmp/hsperfdata_<user>/<pid>. Parsing it is the only way to learn a JVM's
// exact version, main class and full VM arguments from the outside, without
// executing anything in the target process.
//
// Layout (PerfDataBuffer prologue, format 2.0):
//
//	 0  u32 magic = 0xcafec0c0 (always big endian)
//	 4  u8  byte order (0 = big endian, 1 = little endian)
//	 5  u8  major version
//	 6  u8  minor version
//	 7  u8  accessible
//	 8  u32 used
//	12  u32 overflow
//	16  u64 mod timestamp
//	24  u32 entry offset
//	28  u32 num entries
//
// Each entry, starting at entry offset:
//
//	 0  u32 entry length
//	 4  u32 name offset (relative to entry start)
//	 8  u32 vector length (0 = scalar)
//	12  u8  data type ('J' = long, 'B' = byte)
//	13  u8  flags
//	14  u8  data units
//	15  u8  data variability
//	16  u32 data offset (relative to entry start)
const (
	hsperfMagic     = 0xcafec0c0
	hsperfPrologLen = 32
	hsperfEntryHdr  = 20
	// maxPerfFileSize guards against a corrupt or hostile file; real ones are
	// tens of kilobytes.
	maxPerfFileSize = 4 << 20
)

// perfData holds the counters we care about from a JVM's hsperfdata file.
type perfData struct {
	// JavaVersion is java.property.java.version, e.g. "21.0.11".
	JavaVersion string
	// JavaCommand is sun.rt.javaCommand: the main class or jar plus its args.
	JavaCommand string
	// VMArgs is java.rt.vmArgs: the JVM flags, space separated.
	VMArgs string
	// VMVersion is java.property.java.vm.version.
	VMVersion string
	// VMName is java.property.java.vm.name, e.g. "OpenJDK 64-Bit Server VM".
	VMName string
}

// hsperfPath locates the perf file for a process as seen from our mount
// namespace. The file lives in the target's /tmp and is named after the pid
// inside the target's pid namespace, so containers resolve correctly.
func hsperfPath(procRoot string, pid, nspid int, user string) []string {
	root := filepath.Join(procRoot, strconv.Itoa(pid), "root")
	names := []string{strconv.Itoa(nspid)}
	if nspid != pid {
		names = append(names, strconv.Itoa(pid))
	}

	// The owning user's directory is the normal case, but a JVM started with
	// -XX:+PerfDisableSharedMem writes nothing and one started under a
	// different accounting name may land elsewhere, so glob as a fallback.
	var dirs []string
	if user != "" {
		dirs = append(dirs, filepath.Join(root, "tmp", "hsperfdata_"+user))
	}
	if matches, err := filepath.Glob(filepath.Join(root, "tmp", "hsperfdata_*")); err == nil {
		dirs = append(dirs, matches...)
	}

	var out []string
	seen := map[string]bool{}
	for _, d := range dirs {
		for _, n := range names {
			p := filepath.Join(d, n)
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	return out
}

// readPerfData parses the first readable hsperfdata file for a process.
// A missing or unreadable file is not an error: many JVMs run with perf data
// disabled, and discovery falls back to /proc/<pid>/cmdline in that case.
func readPerfData(procRoot string, pid, nspid int, user string) (*perfData, error) {
	var lastErr error
	for _, path := range hsperfPath(procRoot, pid, nspid, user) {
		fi, err := os.Stat(path)
		if err != nil {
			lastErr = err
			continue
		}
		if fi.Size() < hsperfPrologLen || fi.Size() > maxPerfFileSize {
			lastErr = fmt.Errorf("implausible perf file size %d", fi.Size())
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			lastErr = err
			continue
		}
		pd, err := parsePerfData(data)
		if err != nil {
			lastErr = err
			continue
		}
		return pd, nil
	}
	if lastErr == nil {
		lastErr = os.ErrNotExist
	}
	return nil, lastErr
}

// parsePerfData decodes the counters of interest from a raw hsperfdata buffer.
func parsePerfData(buf []byte) (*perfData, error) {
	if len(buf) < hsperfPrologLen {
		return nil, fmt.Errorf("perf buffer too short: %d bytes", len(buf))
	}
	// The magic is always stored big endian, regardless of the byte order flag.
	if binary.BigEndian.Uint32(buf[0:4]) != hsperfMagic {
		return nil, fmt.Errorf("bad perf magic %#x", binary.BigEndian.Uint32(buf[0:4]))
	}

	var order binary.ByteOrder = binary.BigEndian
	if buf[4] == 1 {
		order = binary.LittleEndian
	}
	if major := buf[5]; major < 2 {
		return nil, fmt.Errorf("unsupported perf format version %d.%d", buf[5], buf[6])
	}

	entryOffset := int(order.Uint32(buf[24:28]))
	numEntries := int(order.Uint32(buf[28:32]))
	if entryOffset < hsperfPrologLen || entryOffset > len(buf) || numEntries < 0 {
		return nil, fmt.Errorf("perf header out of range (offset %d, entries %d)", entryOffset, numEntries)
	}

	out := &perfData{}
	pos := entryOffset
	for i := 0; i < numEntries; i++ {
		if pos+hsperfEntryHdr > len(buf) {
			break
		}
		entryLen := int(order.Uint32(buf[pos : pos+4]))
		if entryLen <= 0 || pos+entryLen > len(buf) {
			break
		}
		entry := buf[pos : pos+entryLen]
		pos += entryLen

		nameOff := int(order.Uint32(entry[4:8]))
		vectorLen := int(order.Uint32(entry[8:12]))
		dataType := entry[12]
		dataOff := int(order.Uint32(entry[16:20]))
		if nameOff < hsperfEntryHdr || nameOff >= entryLen || dataOff <= 0 || dataOff > entryLen {
			continue
		}

		name := cstring(entry[nameOff:])
		// Only the string ('B' vectors) counters carry what we need; the
		// numeric ones are GC and JIT statistics we do not use here.
		if vectorLen == 0 || dataType != 'B' {
			continue
		}
		end := dataOff + vectorLen
		if end > entryLen {
			end = entryLen
		}
		value := strings.TrimRight(cstring(entry[dataOff:end]), " ")

		switch name {
		case "java.property.java.version":
			out.JavaVersion = value
		case "sun.rt.javaCommand":
			out.JavaCommand = value
		case "java.rt.vmArgs":
			out.VMArgs = value
		case "java.property.java.vm.version":
			out.VMVersion = value
		case "java.property.java.vm.name":
			out.VMName = value
		}
	}
	return out, nil
}

// cstring reads a NUL-terminated string from the front of b.
func cstring(b []byte) string {
	if i := indexByte(b, 0); i >= 0 {
		return string(b[:i])
	}
	return string(b)
}

func indexByte(b []byte, c byte) int {
	for i, v := range b {
		if v == c {
			return i
		}
	}
	return -1
}

// majorJavaVersion extracts the feature version from a java.version string.
// It understands both the legacy "1.8.0_402" and the modern "21.0.11" forms,
// returning 0 when the string is unparseable.
func majorJavaVersion(v string) int {
	if v == "" {
		return 0
	}
	parts := strings.FieldsFunc(v, func(r rune) bool {
		return r == '.' || r == '_' || r == '-' || r == '+'
	})
	if len(parts) == 0 {
		return 0
	}
	first, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	// "1.8.0" means Java 8; from Java 9 on the first component is the version.
	if first == 1 && len(parts) > 1 {
		if second, err := strconv.Atoi(parts[1]); err == nil {
			return second
		}
		return 0
	}
	return first
}
