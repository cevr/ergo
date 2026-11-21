package v1

import "testing"

// TestCursorStringBytesAllocatesNothing: the Reader's StringBytes reads a
// string into the buffer it is lent with no allocation, escaped or not,
// when the buffer holds it (GX12): an append-form pair's decode makes no
// string of the text. It counts allocations (testing.AllocsPerRun), never
// time. The law StringBytesReadsString holds what it reads; this guard
// holds that it is read with no copy, which no law sees (mutant
// gx12-cursor-bytes-via-string, runtime/v1/testdata/mutants.json).
func TestCursorStringBytesAllocatesNothing(t *testing.T) {
	for _, text := range []string{`"2026-11-15T12:00:00Z"`, `"a\"b\\c\u00e9\n"`} {
		c, err := JSONOpen(text, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		var buf [64]byte
		n := testing.AllocsPerRun(100, func() {
			c.i = 0
			_ = c.StringBytes(buf[:0])
		})
		if n != 0 {
			t.Errorf("StringBytes of %s allocates %v times per read, want 0", text, n)
		}
	}
}
