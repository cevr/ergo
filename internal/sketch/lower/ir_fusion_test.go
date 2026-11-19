package lower

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// fusionSrc is std slices' Transform and Filter, the fusion laws over
// them (std slices/laws.ergo; check's fusionFixture) and a law over a
// function and a value (ScaleFuses), with their sites.
const fusionSrc = `func Transform[T, U, E, R](xs []T, f func(x T) U fails E needs R) []U fails E needs R {
	let mut out = []U{}
	for x in xs {
		out = out.Append(try f(x))
	}
	out
}

func Filter[T, E, R](xs []T, keep func(x T) bool fails E needs R) []T fails E needs R {
	let mut out = []T{}
	for x in xs {
		if try keep(x) {
			out = out.Append(x)
		}
	}
	out
}

func Scale(xs []int, k int) []int { Transform(xs, func(x int) int { x * k }) }

law TransformFuses[T, U, V comparable](xs []T, f func(x T) U, g func(x U) V) { Transform(Transform(xs, f), g) } == { Transform(xs, func(x T) V { g(f(x)) }) }

law FilterFuses[T comparable](xs []T, p func(x T) bool, q func(x T) bool) { Filter(Filter(xs, p), q) } == { Filter(xs, func(x T) bool { p(x) && q(x) }) }

law ScaleFuses(xs []int, f func(x int) int, k int) { Scale(Transform(xs, f), k) } == { Transform(xs, func(x int) int { f(x) * k }) }

func Inc(x int) int { x + 1 }

func Width(xs []int) int { len(xs) }

func F(xs []int) []int { Transform(Transform(xs, Inc), func(x int) int { x * 3 }) }

func S(xs []int) []string { Transform(Transform(xs, Inc), func(x int) string { "s" }) }

func Two(xs []int) []string { Transform(Transform(Transform(Transform(xs, Inc), Inc), Inc), func(x int) string { "t" }) }

func G(xs []int) []int { Filter(Filter(xs, func(x int) bool { x > 1 }), func(x int) bool { x < 9 }) }

func H(xs []int) []int {
	let mut n = 0
	Transform(Transform(xs, func(x int) int {
		n = n + 1
		x
	}), Inc)
}

func K(xs []int) []int { Scale(Transform(xs, Inc), 3) }

func KCall(xs []int) []int { Scale(Transform(xs, Inc), Width(xs)) }

func main() {
	let _ = F([]int{1, 2})
	let _ = S([]int{1, 2})
	let _ = Two([]int{1, 2})
	let _ = G([]int{1, 2})
	let _ = H([]int{1, 2})
	let _ = K([]int{1, 2})
	let _ = KCall([]int{1, 2})
}
`

// TestFusionAtSites is a law over function parameters at its sites (owner
// q234; types.md, Laws 14, Over function parameters), read from LowerIR's
// IR in process: the promised fact of each site is what it calls, so no Go
// is built (runtime/v1's TestFuseOrRunsTheLeftSideOnADefect holds what
// FuseOr does with them).
//
//   - Run build, the laws validated: F, S, G and K call v1.FuseOr once;
//     its right function calls the law's root once (one loop), its left
//     function the left side's two. S binds V at string where F binds it
//     at int. Two has two sites of one law in one body (Transform four
//     deep, rewritten from the outside in): the right side's binder x is
//     declared once for each placement.
//   - H's operand writes a captured let mut, and KCall's k operand calls a
//     function after the left side's Transform ran: both keep their left
//     side, and their rows say why.
//   - -rewrites=off: no FuseOr. Test build: Shadow, no FuseOr.
//
// Mutants: fusion-lower-unapplied (fuseAt never matches),
// fusion-lower-no-fuseor (the right side placed bare, so a later stage's
// defect would be raised), fusion-lower-impure-admitted (sitePure admits
// every function operand), fusion-lower-late-operand (an operand after a
// stage may be any expression), fusion-lower-binders-kept (a side's
// binder stays declared, so Two's second placement falls back),
// fusion-lower-off-ignored.
func TestFusionAtSites(t *testing.T) {
	keys := func(string) ForeignKey { return ForeignKey{} }
	type site = struct{ right, left []string }
	lowerWith := func(step, mode string, test bool) (map[string][]site, map[string][]string, []string) {
		t.Helper()
		u := factsUnit(t, fusionSrc)
		u.Rewrites = mode
		entries := map[string]LedgerEntry{}
		for _, law := range u.Package.Laws {
			if law.FuncsRewrite == nil {
				t.Fatalf("%s is not a law over function parameters", law.Name)
			}
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
		var rows []string
		if !test {
			for _, s := range ev.Sites() {
				rows = append(rows, s.Law+": "+s.Why)
			}
		}
		return fuseOrSites(t, files), irCalls(t, files), rows
	}
	on, calls, rows := lowerWith("run build", "", false)
	for _, c := range []struct {
		fn, root string
		left     []string
	}{
		{"F", "Transform", []string{"Transform", "Transform"}},
		{"S", "Transform", []string{"Transform", "Transform"}},
		{"G", "Filter", []string{"Filter", "Filter"}},
		{"K", "Transform", []string{"Scale", "Transform"}},
	} {
		s := on[c.fn]
		if len(s) != 1 {
			t.Errorf("run build: %s has %d FuseOr sites (calls %v); want 1", c.fn, len(s), calls[c.fn])
			continue
		}
		if n := fuseCount(s[0].right, c.root); n != 1 || slices.Contains(s[0].right, "Scale") {
			t.Errorf("run build: %s's fused side calls %v; want %s once (one loop)", c.fn, s[0].right, c.root)
		}
		for _, name := range c.left {
			if fuseCount(s[0].left, name) < fuseCount(c.left, name) {
				t.Errorf("run build: %s's left side calls %v; want %v, the staged form run again on a defect", c.fn, s[0].left, c.left)
			}
		}
	}
	if len(on["Two"]) != 2 {
		t.Errorf("run build: Two has %d FuseOr sites (calls %v); want 2, one law placed twice in a body", len(on["Two"]), calls["Two"])
	}
	for _, fn := range []string{"H", "KCall"} {
		if len(on[fn]) != 0 {
			t.Errorf("run build: %s calls FuseOr (%v); want its left side kept", fn, calls[fn])
		}
	}
	for _, want := range []string{"TransformFuses: not applied: the operand of f is not a pure function", "ScaleFuses: not applied: the operand of k runs after a stage"} {
		if !slices.ContainsFunc(rows, func(r string) bool { return strings.Contains(r, want) }) {
			t.Errorf("run build: no row says %q (H's, KCall's); rows:\n\t%s", want, strings.Join(rows, "\n\t"))
		}
	}
	off, _, _ := lowerWith("-rewrites=off", "off", false)
	for fn, s := range off {
		t.Errorf("-rewrites=off: %s has %d FuseOr sites; want none", fn, len(s))
	}
	tested, tcalls, _ := lowerWith("test build", "", true)
	if len(tested) != 0 || fuseCount(tcalls["G"], "Shadow") != 1 || fuseCount(tcalls["KCall"], "Shadow") != 0 {
		t.Errorf("test build: FuseOr sites %v, G calls %v, KCall %v; want G's Shadow once, none at KCall (its operand), and no FuseOr", tested, tcalls["G"], tcalls["KCall"])
	}
}

func fuseCount(xs []string, x string) int {
	n := 0
	for _, y := range xs {
		if y == x {
			n++
		}
	}
	return n
}

// fuseOrSites are the v1.FuseOr calls of each function of the lowered IR:
// what its right function (the fused side) and its left function (the
// left side, run again on a defect) call.
func fuseOrSites(t *testing.T, files []File) map[string][]struct{ right, left []string } {
	t.Helper()
	var ir irFile
	for _, f := range files {
		if f.Path == IRFileName {
			if err := json.Unmarshal(f.Data, &ir); err != nil {
				t.Fatal(err)
			}
		}
	}
	type site = struct{ right, left []string }
	out := map[string][]site{}
	var names func(e *irExpr, list []*irStmt) []string
	names = func(e *irExpr, list []*irStmt) []string {
		var acc []string
		var expr func(e *irExpr)
		var stmts func(list []*irStmt)
		expr = func(e *irExpr) {
			if e == nil {
				return
			}
			if e.K == "call" || e.K == "pcall" {
				acc = append(acc, e.Name)
			}
			expr(e.X)
			expr(e.Y)
			for _, a := range slices.Concat(e.Args, e.Elems) {
				expr(a)
			}
			stmts(e.Body)
		}
		stmts = func(list []*irStmt) {
			for _, s := range list {
				expr(s.X)
				expr(s.Y)
				stmts(s.Then)
				stmts(s.Else)
				stmts(s.Body)
				for _, c := range s.Cases {
					stmts(c.Body)
				}
			}
		}
		expr(e)
		stmts(list)
		return acc
	}
	var walkE func(name string, e *irExpr)
	var walkS func(name string, list []*irStmt)
	walkE = func(name string, e *irExpr) {
		if e == nil {
			return
		}
		if e.K == "pcall" && e.Name == "FuseOr" && len(e.Args) == 2 {
			out[name] = append(out[name], site{right: names(e.Args[0], nil), left: names(e.Args[1], nil)})
		}
		walkE(name, e.X)
		walkE(name, e.Y)
		for _, a := range slices.Concat(e.Args, e.Elems) {
			walkE(name, a)
		}
		walkS(name, e.Body)
	}
	walkS = func(name string, list []*irStmt) {
		for _, s := range list {
			walkE(name, s.X)
			walkE(name, s.Y)
			walkS(name, s.Then)
			walkS(name, s.Else)
			walkS(name, s.Body)
			for _, c := range s.Cases {
				walkS(name, c.Body)
			}
		}
	}
	for _, f := range ir.Funcs {
		name := f.Name
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		walkS(name, f.Body)
	}
	return out
}
