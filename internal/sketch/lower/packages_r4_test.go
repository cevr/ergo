package lower

import (
	"fmt"
	"strings"
	"testing"
)

// Round 4 (196b): the lowering reads the unit's and the checker's lists
// only through the canonical unit (canon.go), and a text function's
// identity is the Go type it takes.

// TestGeneratedOrderIsTotal: the naming sort accepts one entity met twice
// and refuses, as a compiler defect, two entities under one sort string,
// whose order would be partial.
func TestGeneratedOrderIsTotal(t *testing.T) {
	one := generatedEntry{pkgKey{"text", "named main.Hold@17"}, "text main.Hold@17", "textHold"}
	if got := sortGenerated([]generatedEntry{one, one}); len(got) != 2 {
		t.Fatalf("one entity met twice: %v", got)
	}
	for _, other := range []generatedEntry{
		{pkgKey{"text", "instance main.Hold@17"}, one.sort, one.base},
		{one.key, one.sort, "textHold2"},
	} {
		func() {
			defer func() {
				r, ok := recover().(refusal)
				if !ok || !strings.Contains(r.msg, "share the sort key") {
					t.Fatalf("two entities under one sort string: recovered %#v", r)
				}
			}()
			sortGenerated([]generatedEntry{one, other})
		}()
	}
}

// The round 3 verify's probe (M1): a type generic over a failure row only
// and its instance in a record's field erase to one Go type, which has one
// text function, in both builds.
//
// It builds Go because it checks what the built program prints and how it
// exits; no in-process check observes that, and Ergo has no reference
// interpreter yet (test-speed T8).
func TestVerifyR3ErasedRows(t *testing.T) {
	needsBuild(t)
	u := checkPackage(t, "main", []pkgSource{{"/probe/rows.ergo", `type Bad {}
type Hold[E] { F func() fails E }
type Outer { V Hold[Bad] }
func bad() fails Bad { Bad }
func first() fails Hold[Bad] { Hold[Bad]{F: bad} }
func second() fails Outer { Outer{V: Hold[Bad]{F: bad}} }
func main() fails Outer { Outer{V: Hold[Bad]{F: bad}} }
test First { try first() }
test Second { try second() }
`}}, nil)
	for _, tests := range []bool{false, true} {
		t.Run(fmt.Sprint(tests), func(t *testing.T) {
			p := mustLowerUnit(t, "rows", u, tests)
			if n := strings.Count(p.mainGo(), "func textHold("); n != 1 {
				t.Fatalf("%d text functions of Hold, want 1", n)
			}
			exes := buildAll(t, []*lowered{p}, true)
			if exes == nil {
				t.Fatal("build failed")
			}
			out, stderr, code := runExe(t, exes[p.name])
			if code != 1 || !strings.Contains(out+stderr, "{F:<func>}") {
				t.Fatalf("exit %d stdout %q stderr %q: want the derived text {F:<func>}", code, out, stderr)
			}
		})
	}
}
