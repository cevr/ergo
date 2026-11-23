package lower

import (
	"slices"
	"strings"
	"testing"
)

// unfoldSrc is a fallible inverse law (ReadShow, std strconv's
// AtoiInvertsItoa in small) and its sites: written whole, through the
// one-expression functions enc and dec, through a let, and under catch;
// and three that keep their left side.
const unfoldSrc = `type Bad { At int }

func show(n int) []int { []int{n, n} }

func read(xs []int) int fails Bad {
	if len(xs) != 2 || xs[0] != xs[1] {
		return Bad{At: len(xs)}
	}
	xs[0]
}

law ReadShow(n int) { try read(show(n)) } == { n }

func enc(n int) []int { show(n) }

func dec(xs []int) int fails Bad { try read(xs) }

func Direct(n int) int fails Bad { try read(show(n)) } // site Direct

func Nested(n int) int fails Bad { try dec(enc(n)) } // site Nested

func Shared(n int) int fails Bad {
	let xs = enc(n)
	let k = len(xs)
	k + try dec(xs) // site Shared
}

func Caught(n int) int {
	dec(enc(n)) catch { // site Caught
	case Bad:
		-1
	}
}

func Mut(n int) int fails Bad {
	let mut m = n
	let xs = enc(m)
	m = m + 1
	m + try dec(xs) // site Mut
}

func Again(n int) int fails Bad {
	let xs = enc(n + 1)
	try dec(xs) // site Again
}

func Other(n int) int fails Bad { try dec([]int{n, n}) } // site Other

func main() {
	let _ = Direct(1) catch { case Bad: 0 }
	let _ = Nested(1) catch { case Bad: 0 }
	let _ = Shared(1) catch { case Bad: 0 }
	let _ = Caught(1)
	let _ = Mut(1) catch { case Bad: 0 }
	let _ = Again(1) catch { case Bad: 0 }
	let _ = Other(1) catch { case Bad: 0 }
}
`

// TestUnfoldAtSites is a fallible rewrite law over values at its sites
// (ENCDEC; ir_unfold.go), read from LowerIR's rows and IR in process: the
// promised fact of a site is whether it is rewritten and why not, and of
// an applied one that its function calls the law's left side no more, so
// no Go is built.
//
//   - Run build, the law validated: Direct, Nested (through enc and dec),
//     Shared (through the let xs) and Caught (a catch's call) are applied,
//     and none of their functions calls read or dec.
//   - Mut's operand is a let mut, which changes between the let and the
//     site, and Again's is n + 1, which the right side would evaluate a
//     second time: each keeps its left side and its row says why. Other
//     decodes a literal, no show: no site.
//   - -rewrites=off applies none. The test build rewrites none.
//
// Mutants: unfold-no-let (ufResolve never reads a let: Shared keeps its
// left side), unfold-no-body (ufBody unfolds nothing: Nested keeps it),
// unfold-mut-admitted (a let mut operand admitted), unfold-again-admitted
// (an operand expression through a let admitted), unfold-catch-unseen
// (bind does not try unfoldAt: Caught keeps it), unfold-off-ignored (the
// run build's decision skipped), unfold-test-build (the test build
// rewrites).
func TestUnfoldAtSites(t *testing.T) {
	keys := func(string) ForeignKey { return ForeignKey{} }
	lineOf := func(marker string) int {
		for i, l := range strings.Split(unfoldSrc, "\n") {
			if strings.HasSuffix(l, "// site "+marker) || strings.Contains(l, "// site "+marker+"\n") || strings.HasSuffix(l, "{ // site "+marker) {
				return i + 1
			}
		}
		t.Fatalf("no site %s", marker)
		return 0
	}
	lowerWith := func(step, mode string, test bool) (map[int]RewriteSite, map[string][]string) {
		t.Helper()
		u := factsUnit(t, unfoldSrc)
		u.Rewrites = mode
		entries := map[string]LedgerEntry{}
		for _, law := range u.Package.Laws {
			e := FuncLawEntry(law, LedgerNames{}, "validated", keys)
			entries[e.Key()] = e
		}
		ev := NewLedgerEvidence(u.Package, LedgerNames{}, entries, keys)
		u.Rewrite = ev
		lower := LowerIR
		if test {
			lower = LowerIRTests
		}
		files, rep, err := lower(u)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		if len(rep.Fallback) > 0 {
			t.Fatalf("%s: bodies keep their source body: %v", step, rep.Fallback)
		}
		rows := map[int]RewriteSite{}
		for _, s := range ev.Sites() {
			if prev, seen := rows[s.Line]; !seen || s.Applied && !prev.Applied {
				rows[s.Line] = s
			}
		}
		return rows, irCalls(t, files)
	}
	rows, calls := lowerWith("run build", "", false)
	for _, fn := range []string{"Direct", "Nested", "Shared", "Caught"} {
		r, ok := rows[lineOf(fn)]
		if !ok || !r.Applied {
			t.Errorf("run build: %s's site is %+v; want ReadShow applied", fn, r)
		}
		// The left side's calls, also through their flat workers
		// (IRF-2): a row applied while the call is built as written
		// is no rewrite.
		for _, left := range []string{"read", "dec", "readFlat", "decFlat"} {
			if slices.Contains(calls[fn], left) {
				t.Errorf("run build: %s calls %v; want no read and no dec, the right side n", fn, calls[fn])
			}
		}
	}
	for fn, why := range map[string]string{"Mut": "is a let mut", "Again": "would evaluate it again"} {
		r, ok := rows[lineOf(fn)]
		if !ok || r.Applied || !strings.Contains(r.Why, why) {
			t.Errorf("run build: %s's site is %+v; want not applied, %q", fn, r, why)
		}
	}
	if r, ok := rows[lineOf("Other")]; ok {
		t.Errorf("run build: Other has a site %+v; want none (no show)", r)
	}
	off, _ := lowerWith("-rewrites=off", "off", false)
	for line, r := range off {
		if r.Applied {
			t.Errorf("-rewrites=off: line %d applied %s", line, r.Law)
		}
	}
	_, tcalls := lowerWith("test build", "", true)
	// Unrewritten, Direct keeps its fallible body, which IRF-2 builds as
	// its flat worker DirectFlat (ir_forms_flat.go flatSplit): Direct is
	// the wrapper that calls it, and the left side is the worker's.
	direct := append(tcalls["Direct"], tcalls["DirectFlat"]...)
	if !slices.Contains(direct, "read") && !slices.Contains(direct, "show") {
		t.Errorf("test build: Direct and its flat worker call %v; want the left side as written", direct)
	}
}
