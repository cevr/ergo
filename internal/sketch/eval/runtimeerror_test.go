package eval

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestRuntimeErrorsAreGos holds the first line of a main's defect, for
// the operations whose defect is the Go runtime's own (an integer
// division or remainder by zero, an index or slice bound out of range, a
// negative shift), to the text Go's runtime gives for the same operation
// on the same values: the built program's first stderr line is Go's
// runtime.Error, "panic: runtime error: ...". The reference is Go itself,
// recovered in process. Seeded: a failing seed prints the source and its
// replay command (ERGO_RUNTIME_ERROR_SEED). It is the smallest repro of a
// fixed bug (ORACLE FT1/F8 probe of irspike's GenerateFail): the
// evaluator wrote these defects without "runtime error: ", so its first
// line was not the built program's.
//
// Mutant only it kills (testdata/mutants.json):
// oracle-f8-runtime-error-unprefixed.
func TestRuntimeErrorsAreGos(t *testing.T) {
	first, n := uint64(1), 40
	if s := os.Getenv("ERGO_RUNTIME_ERROR_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	kinds := map[string]bool{}
	for seed := first; seed < first+uint64(n); seed++ {
		kind, src, want := runtimeErrorProgram(seed)
		kinds[kind] = true
		out, err := RunMain(checked(t, src), DefaultBudget(), Process{})
		if err != nil {
			t.Fatal(err)
		}
		line, _, _ := strings.Cut(out.Stderr, "\n")
		if line != "panic: "+want || out.Status != 2 || out.Skipped != "" || out.Incomplete != "" {
			t.Fatalf("seed %d (%s): main gives %+v, want first stderr line %q and status 2\n%s\nreplay: ERGO_RUNTIME_ERROR_SEED=%d go test -run TestRuntimeErrorsAreGos ./internal/sketch/eval", seed, kind, out, "panic: "+want, src, seed)
		}
	}
	if n > 1 && len(kinds) < 5 {
		t.Fatalf("the seeds reached only %v", kinds)
	}
}

// runtimeErrorProgram is seed's program, its kind, and Go's text for the
// defect it ends in. The values reach the operation through parameters,
// so the checker does not fold them.
func runtimeErrorProgram(seed uint64) (kind, src, want string) {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	xs := make([]int, 1+r.IntN(4))
	list := make([]string, len(xs))
	for i := range xs {
		xs[i] = r.IntN(9)
		list[i] = fmt.Sprint(xs[i])
	}
	lit := "[]int{" + strings.Join(list, ", ") + "}"
	a := r.IntN(100) - 50
	goText := func(f func()) (s string) {
		defer func() { s = fmt.Sprint(recover()) }()
		f()
		return ""
	}
	var call string
	switch r.IntN(5) {
	case 0:
		kind, call = "quo", fmt.Sprintf("op(%d, 0)", a)
		src = "func op(a int, b int) int { a / b }\n"
		want = goText(func() { b := 0; _ = a / b })
	case 1:
		kind, call = "rem", fmt.Sprintf("op(%d, 0)", a)
		src = "func op(a int, b int) int { a % b }\n"
		want = goText(func() { b := 0; _ = a % b })
	case 2:
		i := len(xs) + r.IntN(5)
		if r.IntN(2) == 0 {
			i = -1 - r.IntN(5)
		}
		kind, call = "index", fmt.Sprintf("op(%s, %d)", lit, i)
		src = "func op(xs []int, i int) int { xs[i] }\n"
		want = goText(func() { _ = xs[i] })
	case 3:
		// Bounds within the length, inverted, or a negative low bound: a
		// bound past the length reads the capacity, which the evaluator
		// does not run (a skip).
		hi := r.IntN(len(xs))
		lo := hi + 1 + r.IntN(len(xs)-hi)
		if r.IntN(2) == 0 {
			lo = -1 - r.IntN(5)
		}
		kind, call = "slice", fmt.Sprintf("len(op(%s, %d, %d))", lit, lo, hi)
		src = "func op(xs []int, lo int, hi int) []int { xs[lo:hi] }\n"
		want = goText(func() { _ = xs[lo:hi] })
	default:
		s := -1 - r.IntN(9)
		kind, call = "shift", fmt.Sprintf("op(%d, %d)", a, s)
		src = "func op(a int, s int) int { a << s }\n"
		want = goText(func() { _ = a << s })
	}
	src += "\nfunc main() needs Console {\n\tConsole.Println(\"\\(" + call + ")\")\n}\n"
	return kind, src, want
}
