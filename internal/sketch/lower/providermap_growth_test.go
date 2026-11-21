package lower

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// providerPutSource puts base keys into a Map whose provider has no normal
// form (the prelude's slot table, types.md Maps 5), then extra more.
const providerPutSource = `type Spread {} implements Hashing[int]

func Hash(_ Spread, k int) uint64 { uint64(k) * 2654435761 }

func Equal(_ Spread, a int, b int) bool { a == b }

func more(base int, extra int) int {
	let mut m = Map[int, int, Spread]{}
	for i in 0..base {
		m[i] = i
	}
	for i in 0..extra {
		m[base + i] = i
	}
	len(m)
}

func main() needs Console {
	Console.Println("\(more(10, 10))")
}
`

// providerPutDriver logs the bytes that 100 puts allocate into a table of
// n entries, for n of 1000 and 2000: the bytes of more(n, 100) less those
// of more(n, 0). CALL is the call, with the environment when more takes
// one.
const providerPutDriver = `package main

import (
	"fmt"
	"runtime"
	"testing"

	rt "ergo.local/prototype/runtime/rt"
)

var _ = rt.Root

func bytesOf(f func()) uint64 {
	f() // the first run grows the heap
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

func TestPutBytes(t *testing.T) {
	ENV
	for _, n := range []int{1000, 2000} {
		with := bytesOf(func() { CALL(n, 100) })
		without := bytesOf(func() { CALL(n, 0) })
		fmt.Printf("PUTBYTES %d %d\n", n, int64(with)-int64(without))
	}
}
`

// TestProviderPutTouchesFewSlots is the work-counter law of the provider
// Map's Put (MAP-PROVIDER-PROBE, CONSTRAINTS C5d): a Put into a table of
// n entries writes O(1) slots, so 100 puts into a table of 2000 entries
// allocate no more, give or take a half, than 100 puts into one of 1000
// (neither crosses a growth: both tables are under three quarters full).
// It counts allocated bytes, not time. Red until OWN1: each Put now
// copies the table (Maps are values; the in-place write is OWN1's).
//
// It builds Go because the count is of the built program's allocations,
// which only a run of it shows.
func TestProviderPutTouchesFewSlots(t *testing.T) {
	needsBuild(t)
	p := mustLower(t, "providerput", providerPutSource)
	env, call := "", "more("
	if regexp.MustCompile(`(?m)^func more\(env \*Env,`).MatchString(p.mainGo()) {
		env, call = `env := &Env{task: rt.Root("putbytes")}`, "more(env, "
	}
	driver := strings.ReplaceAll(strings.Replace(providerPutDriver, "ENV", env, 1), "CALL(", call)
	out := goTestEmittedVerbose(t, p, driver)
	got := map[int]int64{}
	for _, x := range regexp.MustCompile(`PUTBYTES (\d+) (-?\d+)`).FindAllStringSubmatch(out, -1) {
		var n int
		var b int64
		fmt.Sscan(x[1], &n)
		fmt.Sscan(x[2], &b)
		got[n] = b
	}
	small, large := got[1000], got[2000]
	t.Logf("bytes of 100 puts: %d into 1000 entries, %d into 2000", small, large)
	if large > small+small/2+4096 {
		t.Errorf("a Put into the provider table grows with the table: %d bytes for 100 puts into 1000 entries, %d into 2000\n%s", small, large, out)
	}
}
