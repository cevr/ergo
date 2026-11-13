package v1

import (
	"hash/crc32"
	"runtime"
	"strings"
	"testing"

	"ergo.local/prototype/runtime/rt"
)

// allocatedBy is the bytes the process allocated while f ran, from the
// runtime's own counter (MemStats.TotalAlloc): a count, not a time.
func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// TestRPCExitsWriteTheValueOnce is FD1-g3's guard: a call's value text is
// copied once, into the answer, for json (RPCExits) and ndjson
// (RPCExitLines). It counts the bytes allocated around one call of a
// 1 MiB value: the answer is about that size, and everything else (the
// call's task, the envelope) is a few kB. It catches rpc-exit-wrapped (a
// success first wrapped into an exit text) and rpc-exits-copied (the
// answer copied once more at the end); each adds a whole value's bytes.
func TestRPCExitsWriteTheValueOnce(t *testing.T) {
	value := `"` + strings.Repeat("x", 1<<20) + `"`
	handle := func(*rt.Task, string, string) (string, error) { return value, nil }
	envs, err := RPCParse(RPCRequest("0", "Big", `{}`), false)
	if err != nil {
		t.Fatal(err)
	}
	limit := uint64(len(value)) + uint64(len(value))/2
	var answer string
	if n := allocatedBy(func() { answer = RPCExits("rpc alloc test", rt.Root("rpc alloc test"), envs, handle) }); n > limit {
		t.Errorf("RPCExits of a %d-byte value allocated %d bytes, want at most %d (the value is copied once, into the answer)", len(value), n, limit)
	}
	if !strings.HasSuffix(answer, value+"}}]") {
		t.Fatalf("RPCExits answered %.80q…, want the value's Success Exit", answer)
	}
	var line string
	emit := func(_ *rt.Task, l string) error { line = l; return nil }
	if n := allocatedBy(func() {
		_ = RPCExitLines("rpc alloc test", rt.Root("rpc alloc test"), envs, handle, emit)
	}); n > limit {
		t.Errorf("RPCExitLines of a %d-byte value allocated %d bytes, want at most %d", len(value), n, limit)
	}
	if !strings.HasSuffix(line, value+"}}\n") {
		t.Fatalf("RPCExitLines emitted %.80q…, want the value's Success Exit line", line)
	}
}

// TestCrc32UpdateDoesNotCopy is FD1-g3's guard on the bench's checksum:
// Crc32Update of a 1 MiB string allocates at most its 512-byte buffer,
// never a copy of the string (it catches crc-copies: each chunk converted
// to a new []byte, which allocates the string's length again).
func TestCrc32UpdateDoesNotCopy(t *testing.T) {
	s := strings.Repeat("ab", 1<<19)
	if n := allocatedBy(func() { _ = Crc32Update(0, s) }); n > 4096 {
		t.Errorf("Crc32Update of %d bytes allocated %d bytes, want at most 4096 (no copy of the string)", len(s), n)
	}
	if Crc32Update(0, s) != crc32.ChecksumIEEE([]byte(s)) {
		t.Error("Crc32Update of a string longer than its buffer is not its checksum")
	}
}
