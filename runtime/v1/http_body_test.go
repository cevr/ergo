package v1

import (
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

// TestReadBodyAllocates counts what a server's read of a small request
// body allocates (GX8): the text's own allocation and the budget reader,
// not io.Copy's 32 KB buffer per request, and, with a declared length,
// no growth of the text. TestHTTPServerMatchesModel holds what the read
// returns; this holds its cost, counted by the Go runtime, not timed.
// The least of 20 reads is checked: a collection between two reads may
// empty the chunk pool, and then that one read makes a new chunk.
//
// Mutants it kills (testdata/mutants.json): gx8-body-copy-buffer,
// gx8-body-ungrown.
func TestReadBodyAllocates(t *testing.T) {
	body := strings.Repeat("j", 5000)
	w := httptest.NewRecorder()
	least := [2]uint64{^uint64(0), ^uint64(0)}
	for range 20 {
		r := httptest.NewRequest("POST", "/jobs", strings.NewReader(body))
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		got, err := readBody(w, r, 1<<20)
		runtime.ReadMemStats(&after)
		if err != nil || got != body {
			t.Fatalf("readBody: %q, %v", got, err)
		}
		least[0] = min(least[0], after.Mallocs-before.Mallocs)
		least[1] = min(least[1], after.TotalAlloc-before.TotalAlloc)
	}
	// The text (5000 bytes, a 5376-byte class) and the budget reader;
	// io.Copy's buffer alone is 32768 bytes, and a text grown chunk by
	// chunk (2048, 4096, 8192) is 14336.
	if least[0] > 3 || least[1] > 7168 {
		t.Fatalf("a 5000-byte body read allocates %d objects, %d bytes at least; want at most 3 and 7168", least[0], least[1])
	}
}
