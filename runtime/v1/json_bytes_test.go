package v1

import "testing"

// TestCursorStringBytesAllocatesNothing: the Reader's StringBytes reads a
// string into the buffer it is lent with no allocation, escaped or not,
// when the buffer holds it (GX12): an append-form pair's decode makes no
// string of the text. It counts allocations (testing.AllocsPerRun), never
// time. The law StringBytesReadsString holds what it reads; this guard
// holds that it is read with no copy, which no law sees (mutant
// gx12-cursor-bytes-via-string, runtime/v1/testdata/mutants.json).
// TestCursorKeysAllocateNothing: a record's read on a Cursor, opened and
// nested two deep, allocates only the Cursor itself, checked or not
// (JSON-CURSOR-ALLOC): a member's key is a window of the text, compared
// in place, and the frames of the lists and objects it opens are the
// Cursor's own. It counts allocations (testing.AllocsPerRun), never time.
// No law sees a copy that changes no value (mutants jca-record-key-cloned
// and jca-frames-not-held, runtime/v1/testdata/mutants.json).
func TestCursorKeysAllocateNothing(t *testing.T) {
	const text = `{"id": 7, "name": "ada", "tags": [{"k": 1}, {"k": 2}], "ok": true}`
	for _, checked := range []bool{false, true} {
		read := func() {
			var c *Cursor
			var err error
			if checked {
				c, err = JSONOpenChecked(text, 0, 0)
			} else {
				c, err = JSONOpen(text, 0, 0)
			}
			if err != nil || !c.BeginObject() {
				t.Fatalf("open %v: %v", checked, err)
			}
			for c.More() {
				if c.Key() != "tags" {
					c.Skip()
					continue
				}
				c.BeginList()
				for c.More() {
					c.BeginObject()
					for c.More() {
						_ = c.Key() == "k" && c.Int() > 0
					}
					c.End()
				}
				c.End()
			}
			c.End()
			if c.bad || c.wrong {
				t.Fatalf("read %v: bad %v, wrong %v", checked, c.bad, c.wrong)
			}
		}
		if n := testing.AllocsPerRun(100, read); n != 1 {
			t.Errorf("opening and reading %s (checked %v) allocates %v times, want 1 (the Cursor)", text, checked, n)
		}
	}
}

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
