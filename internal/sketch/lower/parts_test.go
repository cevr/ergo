package lower

import (
	"strings"
	"testing"
)

// partsSource holds the record shapes of the REUSE plan's §1 that consume
// by parts (cmd/ergo/testdata/reuse, rows g4, r1, r2, r3), each called by
// a rebinding in a loop.
const partsSource = `type Set { xs []int }

func InsertNew(s Set, x int) Set {
	if len(s.xs) > 0 && s.xs[len(s.xs)-1] == x {
		return s
	}
	let mut t = s
	t.xs = t.xs.Append(x)
	t
}

func Insert(s Set, x int) Set {
	let mut t = s
	t.xs = t.xs.Append(x)
	t
}

type St { set Set, n int }

func lit(s St, x int) St { St{set: Insert(s.set, x), n: s.n + 1} }

func upd(s St, x int) St { St{..s, set: Insert(s.set, x)} }

type Raw { xs []int, n int }

func updRaw(s Raw, x int) Raw { Raw{..s, xs: s.xs.Append(x)} }

func g4(n int) int {
	let mut a = Set{xs: []int{}}
	for i in 0..n { a = InsertNew(a, i) }
	len(a.xs)
}

func r1(n int) int {
	let mut s = St{set: Set{xs: []int{}}, n: 0}
	for i in 0..n { s = lit(s, i) }
	len(s.set.xs)
}

func r2(n int) int {
	let mut s = St{set: Set{xs: []int{}}, n: 0}
	for i in 0..n { s = upd(s, i) }
	len(s.set.xs)
}

func r3(n int) int {
	let mut s = Raw{xs: []int{}, n: 0}
	for i in 0..n { s = updRaw(s, i) }
	len(s.xs)
}

func main() needs Console {
	Console.Println("\(g4(3)) \(r1(3)) \(r2(3)) \(r3(3))")
}
`

// TestPartsCopyNothing is the promised form of consume by parts (REUSE
// RU1; parts.go): the shapes g4, r1, r2 and r3 copy nothing and take no
// one-extender site, in the bodies and at their callers. The one copy
// left is each owned function's wrapper, which only a caller that cannot
// hand its argument over reaches (V5). The counted rows in
// cmd/ergo/own_growth_linux_test.go show the same at n and 2n; this check
// is in process.
//
// Mutants it alone catches in process: reuse-return-off (g4's whole
// return refuses the bind), reuse-move-off (r2's update copies its base's
// part).
func TestPartsCopyNothing(t *testing.T) {
	mainGo, _, sites, _ := copiesOf(t, partsSource)
	var bad []string
	for _, s := range sites {
		switch {
		case s.Form == "copy" && strings.Contains(s.Why, "wrapper of an owned parameter"):
		case s.Form == "copy" || s.Form == "one extender":
			bad = append(bad, reportText([]CopySite{s}))
		case s.Form == "owned call" && !strings.HasSuffix(s.Why, "handed over"):
			bad = append(bad, reportText([]CopySite{s}))
		}
	}
	if len(bad) > 0 || strings.Contains(mainGo, "LineageAppend") {
		t.Errorf("the parts shapes copy (want none but the wrappers):\n%s\nfull report:\n%s", strings.Join(bad, ""), reportText(sites))
	}
}
