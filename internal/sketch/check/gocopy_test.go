package check_test

import (
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
)

// TestGoCopyWriteIsReported holds H5's diagnostic text and position: a Go
// method with no result on a let that holds Go's map and that the program
// also uses as an Ergo value writes only the let's copy, so it is S108 at
// the receiver. A Go-only let (Go's map, shared) and a reading method
// (h.Get, a result) are clean. Mutants: gocopy-ergo-use (the Ergo use is
// not looked for: every Go-held let with a writing method is reported, the
// Go-only one too), gocopy-writes (a method with a result counts as a
// write: h.Get is reported).
func TestGoCopyWriteIsReported(t *testing.T) {
	_, diags := check.CheckSource(`import go net/http

func writes(w http.ResponseWriter) int needs go.Foreign {
	let h = w.Header()
	h.Set("A", "1")
	len(h)
}

func reads(w http.ResponseWriter) int needs go.Foreign {
	let h = w.Header()
	let n = len(h)
	n + len(h.Get("A"))
}

func only(w http.ResponseWriter) needs go.Foreign {
	let h = w.Header()
	h.Set("A", "1")
	h.Del("B")
}
`)
	if len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != 5 || diags[0].Column != 2 || !strings.HasPrefix(diags[0].Message, "h is a copy of the Map or slice Go gave it, because the program also uses h as an Ergo value") {
		t.Fatalf("want one S108 at 5:2, got\n\t%s", describe(diags))
	}
}
