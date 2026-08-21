package jvmdiag

import "testing"

func TestParseHeapInfoG1(t *testing.T) {
	const raw = ` garbage-first heap   total 262144K, used 76800K [0x00000000f0000000, 0x0000000100000000)
  region size 1024K, 25 young (25600K), 3 survivors (3072K)
 Metaspace       used 45678K, committed 46208K, reserved 1114112K
  class space    used 5678K, committed 6016K, reserved 1048576K
`

	info := ParseHeapInfo(raw)

	if info.Collector != "garbage-first heap" {
		t.Errorf("Collector = %q", info.Collector)
	}
	if info.TotalBytes != 262144*1024 || info.UsedBytes != 76800*1024 {
		t.Errorf("heap = %d used of %d, want bytes not kilobytes", info.UsedBytes, info.TotalBytes)
	}
	if info.Metaspace == nil {
		t.Fatal("Metaspace not parsed")
	}
	if info.Metaspace.UsedBytes != 45678*1024 || info.Metaspace.ReservedBytes != 1114112*1024 {
		t.Errorf("Metaspace = %+v", info.Metaspace)
	}
	if info.ClassSpace == nil || info.ClassSpace.CommittedBytes != 6016*1024 {
		t.Errorf("ClassSpace = %+v", info.ClassSpace)
	}
}

// The generational collectors print named spaces instead of regions, and the
// per-generation "total ... used ..." lines must not overwrite the heap's own.
func TestParseHeapInfoGenerational(t *testing.T) {
	const raw = ` PSYoungGen      total 76288K, used 12345K [0x00000000eab00000, 0x00000000f0000000)
  eden space 65536K, 15% used [0x00000000eab00000,0x00000000eb6f2778)
  from space 10752K, 0% used [0x00000000ef580000,0x00000000ef580000)
  to   space 10752K, 0% used [0x00000000eeb00000,0x00000000eeb00000)
 ParOldGen       total 175104K, used 30000K [0x00000000c0000000, 0x00000000cab00000)
  object space 175104K, 17% used [0x00000000c0000000,0x00000000c1d4c010)
 Metaspace       used 20000K, committed 20480K, reserved 1114112K
`

	info := ParseHeapInfo(raw)

	if info.Collector != "PSYoungGen" || info.TotalBytes != 76288*1024 {
		t.Errorf("first heap line = %q / %d", info.Collector, info.TotalBytes)
	}
	if len(info.Regions) != 4 {
		t.Fatalf("parsed %d regions, want 4: %+v", len(info.Regions), info.Regions)
	}

	eden := info.Regions[0]
	if eden.Name != "eden" || eden.TotalBytes != 65536*1024 || eden.UsedPercent != 15 {
		t.Errorf("eden = %+v", eden)
	}
	if info.Regions[3].Name != "object" {
		t.Errorf("old generation space = %q", info.Regions[3].Name)
	}
}

// An unfamiliar collector must yield an empty struct, never a panic: the raw
// output travels with it and stays readable.
func TestParseHeapInfoUnknownFormat(t *testing.T) {
	info := ParseHeapInfo("some future collector says something else entirely\n")
	if info == nil {
		t.Fatal("ParseHeapInfo returned nil")
	}
	if info.TotalBytes != 0 || len(info.Regions) != 0 {
		t.Errorf("invented structure from unrecognised text: %+v", info)
	}
}
