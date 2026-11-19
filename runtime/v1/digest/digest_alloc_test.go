package digest

import (
	"hash/crc32"
	"runtime"
	"strings"
	"testing"
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

// TestCrc32UpdateDoesNotCopy is FD1-g3's guard on the bench's checksum:
// Crc32Update of a 1 MiB string allocates far less than the string (its
// 512-byte buffer; under -cover or -race, a few more), never a copy of it (it catches crc-copies: each chunk converted
// to a new []byte, which allocates the string's length again).
func TestCrc32UpdateDoesNotCopy(t *testing.T) {
	s := strings.Repeat("ab", 1<<19)
	if n := allocatedBy(func() { _ = Crc32Update(0, s) }); n > uint64(len(s))/16 {
		t.Errorf("Crc32Update of %d bytes allocated %d bytes, want at most %d (no copy of the string)", len(s), n, len(s)/16)
	}
	if Crc32Update(0, s) != crc32.ChecksumIEEE([]byte(s)) {
		t.Error("Crc32Update of a string longer than its buffer is not its checksum")
	}
}
