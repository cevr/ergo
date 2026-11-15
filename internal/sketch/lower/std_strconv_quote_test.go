package lower

import (
	"strings"
	"testing"
)

// quoteAllocSource quotes each of xs and sums the lengths: the quotes
// never leave the function.
const quoteAllocSource = `import strconv

func quoted(xs []string) int {
	let mut n = 0
	for x in xs {
		n = n + len(strconv.Quote(x))
	}
	n
}

func main() {}
`

// quoteAllocDriver logs the allocations of quoted over 10 and over 1000
// short texts. Each is plain text of at most 21 bytes, so Go's Quote
// sizes its buffer (3/2 of the text) at 32 bytes or less and never grows
// it: the size Go keeps on the stack.
const quoteAllocDriver = `package main

import (
	"runtime"
	"testing"
)

func TestQuoteAllocs(t *testing.T) {
	texts := func(n int) []string {
		xs := make([]string, n)
		for i := range xs {
			xs[i] = []string{"abcd", "hello world", "üñï", "twenty-one bytes long"}[i%4]
		}
		return xs
	}
	// quoted is pure (pure.go): Quote's body is, so it takes no Env.
	run := func(xs []string) func() {
		return func() { runtime.KeepAlive(quoted(xs)) }
	}
	a := testing.AllocsPerRun(20, run(texts(10)))
	b := testing.AllocsPerRun(20, run(texts(1000)))
	t.Logf("ALLOCS %v %v", a, b)
}
`

// TestStrconvQuoteStaysOnStack: std strconv.Quote's runtime body inlines
// into its caller, so a quote of up to 32 bytes that does not escape is
// built on the stack: quoting 1000 short texts allocates what quoting 10
// does (B1-Q; the body that called strconv.Quote cost 84 in Go's inliner,
// past its budget of 80, and allocated each quote). It kills
// strconv-quote-wrapped.
//
// It builds Go because allocation is a fact of Go's inlining and escape
// analysis of the lowered code, which no in-process check sees.
func TestStrconvQuoteStaysOnStack(t *testing.T) {
	needsBuild(t)
	out := goTestEmittedVerbose(t, mustLower(t, "quoteallocs", quoteAllocSource), quoteAllocDriver)
	_, rest, ok := strings.Cut(out, "ALLOCS ")
	if !ok {
		t.Fatalf("no ALLOCS line in:\n%s", out)
	}
	fields := strings.Fields(rest)
	if len(fields) < 2 || fields[0] != fields[1] {
		t.Fatalf("quoting 10 short texts allocates %v times, 1000 texts %v times: each quote allocates", fields[0], fields[1])
	}
}
