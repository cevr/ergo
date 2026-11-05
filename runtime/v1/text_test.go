package v1

import (
	"errors"
	"testing"
)

// TestStrconvAtoiFailsMalformed: every text Go's strconv.Atoi refuses,
// out-of-range included, is the typed failure Malformed with the text, and
// its Error is the record's text the lowering writes.
func TestStrconvAtoiFailsMalformed(t *testing.T) {
	for _, s := range []string{"", "x", "1x", " 1", "1.5", "99999999999999999999"} {
		n, err := StrconvAtoi(s)
		var m Malformed
		if n != 0 || !errors.As(err, &m) || m.Text != s {
			t.Errorf("StrconvAtoi(%q) = %d, %v; want 0, Malformed{Text:%q}", s, n, err, s)
		}
	}
	if n, err := StrconvAtoi("-42"); n != -42 || err != nil {
		t.Errorf("StrconvAtoi(-42) = %d, %v", n, err)
	}
	if got := (Malformed{Text: "1x"}).Error(); got != "strconv.Malformed{Text:1x}" {
		t.Errorf("Malformed.Error() = %q", got)
	}
}

// TestStringsSliceIsADefect: a bound out of range panics, as an index out
// of range does; the runtime reports it as a defect.
func TestStringsSliceIsADefect(t *testing.T) {
	if got := StringsSlice("hello", 1, 3); got != "el" {
		t.Fatalf("StringsSlice = %q", got)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("StringsSlice(hello, 2, 9) did not panic")
		}
	}()
	StringsSlice("hello", 2, 9)
}
