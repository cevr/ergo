package profmd

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"runtime"
	"runtime/pprof"
	"strings"
	"testing"
	"time"
)

//go:noinline
func busyCPUWork(d time.Duration) uint64 {
	deadline := time.Now().Add(d)
	var acc uint64 = 1
	for time.Now().Before(deadline) {
		for i := uint64(0); i < 50_000; i++ {
			acc = acc*6364136223846793005 + i + 1
		}
	}
	return acc
}

//go:noinline
func allocHeapWork() [][]byte {
	chunks := make([][]byte, 0, 16)
	for i := 0; i < 16; i++ {
		buf := make([]byte, 128*1024)
		buf[0] = byte(i + 1)
		buf[len(buf)-1] = byte(i + 2)
		chunks = append(chunks, buf)
	}
	return chunks
}

func TestRenderCPUMarkdownRealProfile(t *testing.T) {
	var buf bytes.Buffer
	if err := pprof.StartCPUProfile(&buf); err != nil {
		t.Fatalf("StartCPUProfile: %v", err)
	}
	sink := busyCPUWork(140 * time.Millisecond)
	pprof.StopCPUProfile()
	runtime.KeepAlive(sink)

	md, err := RenderCPUMarkdown(buf.Bytes(), Options{Top: 10, TopStacks: 5})
	if err != nil {
		t.Fatalf("RenderCPUMarkdown: %v", err)
	}
	for _, want := range []string{
		"# CPU Profile",
		"## Top Functions by Self Time",
		"## Top Functions by Cumulative Time",
		"## Hot Call Stacks (caller -> leaf)",
		"busyCPUWork",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("CPU markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderHeapMarkdownRealProfile(t *testing.T) {
	oldRate := runtime.MemProfileRate
	runtime.MemProfileRate = 1
	defer func() { runtime.MemProfileRate = oldRate }()

	retained := allocHeapWork()
	runtime.GC()

	var buf bytes.Buffer
	if err := pprof.WriteHeapProfile(&buf); err != nil {
		t.Fatalf("WriteHeapProfile: %v", err)
	}
	runtime.KeepAlive(retained)

	md, err := RenderHeapMarkdown(buf.Bytes(), Options{Top: 15})
	if err != nil {
		t.Fatalf("RenderHeapMarkdown: %v", err)
	}
	for _, want := range []string{
		"# Heap Profile",
		"## Top Allocators by Bytes (alloc_space)",
		"## Top Live Heap (inuse_space)",
		"allocHeapWork",
		"MiB",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("Heap markdown missing %q:\n%s", want, md)
		}
	}
}

func TestRenderCPUMarkdownSyntheticAccountingAndTopLimit(t *testing.T) {
	// Build a deterministic synthetic CPU profile with:
	// - leafHot (self=70ms, cum=70ms) called by midCaller -> rootCaller
	// - leafRec (self=20ms, cum=20ms) called recursively by leafRec -> rootCaller
	// - leafSmall (self=10ms, cum=10ms) called by rootCaller
	// Total CPU = 100ms.
	// rootCaller has self=0ms, cum=100ms (100.0%).
	// leafRec appears twice in its stack, so cum must stay 20ms (20.0%), NOT 40ms.
	raw := buildSyntheticProfile(syntheticSpec{
		sampleTypes: [][2]string{
			{"samples", "count"},
			{"cpu", "nanoseconds"},
		},
		durationNanos: 100_000_000,
		periodType:    [2]string{"cpu", "nanoseconds"},
		period:        10_000_000,
		functions: []syntheticFunc{
			{id: 1, name: "pkg.leafHot", file: "/repo/internal/hot/work.go", line: 10},
			{id: 2, name: "pkg.midCaller", file: "/repo/internal/hot/mid.go", line: 20},
			{id: 3, name: "pkg.rootCaller", file: "/repo/internal/hot/root.go", line: 30},
			{id: 4, name: "pkg.leafRec", file: "/repo/internal/hot/rec.go", line: 40},
			{id: 5, name: "pkg.leafSmall", file: "/repo/internal/hot/small.go", line: 50},
		},
		samples: []syntheticSample{
			{funcIDs: []uint64{1, 2, 3}, values: []int64{7, 70_000_000}},
			{funcIDs: []uint64{4, 4, 3}, values: []int64{2, 20_000_000}},
			{funcIDs: []uint64{5, 3}, values: []int64{1, 10_000_000}},
		},
	})

	md, err := RenderCPUMarkdown(raw, Options{Top: 2, TopStacks: 2})
	if err != nil {
		t.Fatalf("RenderCPUMarkdown: %v", err)
	}

	selfSection := extractSection(md, "## Top Functions by Self Time", "## Top Functions by Cumulative Time")
	selfRows := tableDataRows(selfSection)
	if len(selfRows) != 2 {
		t.Fatalf("expected Top=2 self rows, got %d:\n%s", len(selfRows), selfSection)
	}
	if !strings.Contains(selfRows[0], "`pkg.leafHot`") || !strings.Contains(selfRows[0], "70.0%") {
		t.Fatalf("expected rank 1 self row to be pkg.leafHot (70.0%%), got: %s", selfRows[0])
	}
	if !strings.Contains(selfRows[1], "`pkg.leafRec`") || !strings.Contains(selfRows[1], "| 20.0% | 20.00ms | 20.0% | 20.00ms |") {
		t.Fatalf("expected rank 2 self row to be pkg.leafRec with deduplicated cum=20.0%% (20.00ms), got: %s", selfRows[1])
	}

	cumSection := extractSection(md, "## Top Functions by Cumulative Time", "## Hot Call Stacks (caller -> leaf)")
	cumRows := tableDataRows(cumSection)
	if len(cumRows) != 2 {
		t.Fatalf("expected Top=2 cum rows, got %d:\n%s", len(cumRows), cumSection)
	}
	if !strings.Contains(cumRows[0], "`pkg.rootCaller`") || !strings.Contains(cumRows[0], "| 100.0% | 100.00ms | 0.0% | 0ms |") {
		t.Fatalf("expected rank 1 cum row to be pkg.rootCaller (100.0%% cum, 0ms self), got: %s", cumRows[0])
	}

	stackSection := extractSection(md, "## Hot Call Stacks (caller -> leaf)", "")
	stackRows := tableDataRows(stackSection)
	if len(stackRows) != 2 {
		t.Fatalf("expected TopStacks=2 rows, got %d:\n%s", len(stackRows), stackSection)
	}
	if !strings.Contains(stackRows[0], "`pkg.rootCaller -> pkg.midCaller -> pkg.leafHot`") {
		t.Fatalf("expected rank 1 hot stack to be root->mid->leafHot, got: %s", stackRows[0])
	}
}

func TestRenderHeapMarkdownSyntheticAllocVsInuse(t *testing.T) {
	// Function allocHeavy allocates 4 MiB transiently (0 inuse).
	// Function retainer allocates 1 MiB and keeps all 1 MiB live (inuse).
	raw := buildSyntheticProfile(syntheticSpec{
		sampleTypes: [][2]string{
			{"alloc_objects", "count"},
			{"alloc_space", "bytes"},
			{"inuse_objects", "count"},
			{"inuse_space", "bytes"},
		},
		functions: []syntheticFunc{
			{id: 1, name: "pkg.allocHeavy", file: "/repo/a/heavy.go", line: 12},
			{id: 2, name: "pkg.retainer", file: "/repo/a/retain.go", line: 34},
			{id: 3, name: "pkg.tiny", file: "/repo/a/tiny.go", line: 56},
		},
		samples: []syntheticSample{
			{funcIDs: []uint64{1}, values: []int64{400, 4 * 1024 * 1024, 0, 0}},
			{funcIDs: []uint64{2}, values: []int64{10, 1 * 1024 * 1024, 10, 1 * 1024 * 1024}},
			{funcIDs: []uint64{3}, values: []int64{2, 2048, 1, 1024}},
		},
	})

	md, err := RenderHeapMarkdown(raw, Options{Top: 2})
	if err != nil {
		t.Fatalf("RenderHeapMarkdown: %v", err)
	}

	allocSec := extractSection(md, "## Top Allocators by Bytes (alloc_space)", "## Top Live Heap (inuse_space)")
	allocRows := tableDataRows(allocSec)
	if len(allocRows) != 2 {
		t.Fatalf("expected Top=2 alloc rows, got %d:\n%s", len(allocRows), allocSec)
	}
	if !strings.Contains(allocRows[0], "`pkg.allocHeavy`") || !strings.Contains(allocRows[0], "4.00 MiB") {
		t.Fatalf("expected rank 1 allocator to be pkg.allocHeavy (4.00 MiB), got: %s", allocRows[0])
	}

	inuseSec := extractSection(md, "## Top Live Heap (inuse_space)", "")
	inuseRows := tableDataRows(inuseSec)
	if len(inuseRows) != 2 {
		t.Fatalf("expected Top=2 inuse rows, got %d:\n%s", len(inuseRows), inuseSec)
	}
	if !strings.Contains(inuseRows[0], "`pkg.retainer`") || !strings.Contains(inuseRows[0], "1.00 MiB") {
		t.Fatalf("expected rank 1 live heap retainer to be pkg.retainer (1.00 MiB), got: %s", inuseRows[0])
	}
	if strings.Contains(inuseSec, "`pkg.allocHeavy`") {
		t.Fatalf("pkg.allocHeavy has 0 inuse bytes and must not appear in live heap table:\n%s", inuseSec)
	}
}

func TestParseProfileRejectsInvalidInputs(t *testing.T) {
	// Empty input
	if _, err := ParseProfile(nil); err == nil {
		t.Fatal("expected error for nil input")
	}
	// Non-gzip garbage
	if _, err := ParseProfile([]byte("not a gzip profile")); err == nil {
		t.Fatal("expected error for non-gzip input")
	}
	// Truncated gzip stream
	valid := buildSyntheticProfile(syntheticSpec{
		sampleTypes: [][2]string{{"cpu", "nanoseconds"}},
	})
	if _, err := ParseProfile(valid[:len(valid)/2]); err == nil {
		t.Fatal("expected error for truncated gzip stream")
	}
	// Valid gzip wrapping truncated protobuf varint
	if _, err := ParseProfile(gzipBytes([]byte{0x0a, 0x10, 0x08})); err == nil {
		t.Fatal("expected error for truncated protobuf inside gzip")
	}
	// Valid gzip wrapping empty protobuf
	if _, err := ParseProfile(gzipBytes(nil)); err == nil {
		t.Fatal("expected error for empty protobuf inside gzip")
	}
	// Valid CPU profile passed to RenderHeapMarkdown must error (no alloc_space/inuse_space)
	if _, err := RenderHeapMarkdown(valid, Options{}); err == nil {
		t.Fatal("expected error passing CPU profile to RenderHeapMarkdown")
	}
}

// --- Test helpers for deterministic synthetic pprof encoding ---

type syntheticFunc struct {
	id   uint64
	name string
	file string
	line int64
}

type syntheticSample struct {
	funcIDs []uint64
	values  []int64
}

type syntheticSpec struct {
	sampleTypes   [][2]string
	durationNanos int64
	periodType    [2]string
	period        int64
	functions     []syntheticFunc
	samples       []syntheticSample
}

func buildSyntheticProfile(spec syntheticSpec) []byte {
	stringsTable := []string{""}
	strIdx := map[string]int64{"": 0}
	intern := func(s string) int64 {
		if idx, ok := strIdx[s]; ok {
			return idx
		}
		idx := int64(len(stringsTable))
		stringsTable = append(stringsTable, s)
		strIdx[s] = idx
		return idx
	}

	var msg []byte
	for _, st := range spec.sampleTypes {
		var sub []byte
		sub = appendProtoVarint(sub, 1, uint64(intern(st[0])))
		sub = appendProtoVarint(sub, 2, uint64(intern(st[1])))
		msg = appendProtoBytes(msg, 1, sub)
	}
	for _, s := range spec.samples {
		var sub []byte
		var packedLocs []byte
		for _, id := range s.funcIDs {
			packedLocs = binary.AppendUvarint(packedLocs, id)
		}
		sub = appendProtoBytes(sub, 1, packedLocs)
		var packedVals []byte
		for _, v := range s.values {
			packedVals = binary.AppendUvarint(packedVals, uint64(v))
		}
		sub = appendProtoBytes(sub, 2, packedVals)
		msg = appendProtoBytes(msg, 2, sub)
	}
	for _, fn := range spec.functions {
		// One location per function with matching ID.
		var lineMsg []byte
		lineMsg = appendProtoVarint(lineMsg, 1, fn.id)
		lineMsg = appendProtoVarint(lineMsg, 2, uint64(fn.line))
		var locMsg []byte
		locMsg = appendProtoVarint(locMsg, 1, fn.id)
		locMsg = appendProtoBytes(locMsg, 4, lineMsg)
		msg = appendProtoBytes(msg, 4, locMsg)

		var fnMsg []byte
		fnMsg = appendProtoVarint(fnMsg, 1, fn.id)
		fnMsg = appendProtoVarint(fnMsg, 2, uint64(intern(fn.name)))
		fnMsg = appendProtoVarint(fnMsg, 4, uint64(intern(fn.file)))
		fnMsg = appendProtoVarint(fnMsg, 5, uint64(fn.line))
		msg = appendProtoBytes(msg, 5, fnMsg)
	}
	for _, s := range stringsTable {
		msg = appendProtoBytes(msg, 6, []byte(s))
	}
	if spec.durationNanos != 0 {
		msg = appendProtoVarint(msg, 10, uint64(spec.durationNanos))
	}
	if spec.periodType[0] != "" {
		var sub []byte
		sub = appendProtoVarint(sub, 1, uint64(intern(spec.periodType[0])))
		sub = appendProtoVarint(sub, 2, uint64(intern(spec.periodType[1])))
		msg = appendProtoBytes(msg, 11, sub)
	}
	if spec.period != 0 {
		msg = appendProtoVarint(msg, 12, uint64(spec.period))
	}
	return gzipBytes(msg)
}

func appendProtoVarint(dst []byte, fieldNum uint64, v uint64) []byte {
	dst = binary.AppendUvarint(dst, (fieldNum<<3)|wireVarint)
	return binary.AppendUvarint(dst, v)
}

func appendProtoBytes(dst []byte, fieldNum uint64, b []byte) []byte {
	dst = binary.AppendUvarint(dst, (fieldNum<<3)|wireBytes)
	dst = binary.AppendUvarint(dst, uint64(len(b)))
	return append(dst, b...)
}

func gzipBytes(raw []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(raw)
	_ = gz.Close()
	return buf.Bytes()
}

func extractSection(md, startHeading, endHeading string) string {
	i := strings.Index(md, startHeading)
	if i < 0 {
		return ""
	}
	rest := md[i+len(startHeading):]
	if endHeading != "" {
		if j := strings.Index(rest, endHeading); j >= 0 {
			rest = rest[:j]
		}
	}
	return rest
}

func tableDataRows(section string) []string {
	var rows []string
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| ") {
			continue
		}
		if strings.HasPrefix(line, "| Rank ") || strings.HasPrefix(line, "| ---") {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}
