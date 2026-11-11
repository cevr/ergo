package check

import (
	"math/rand/v2"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/syntax"
)

// ofPatternOwners extends the ownership model at its checking seam. Its
// reference is a partition of the resource leaves: disjoint bound parts
// own, whole/part overlaps and missing parts fail, and a lent root lends.
// The random shape and consumer are independent; ERGO_SEED replays both.
// Distinct mutants and the two original repros are in po1-plan.md and
// the check ledger (po1-*); the old grammar had no destructuring lets.
func ofPatternOwners(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 0x706f31))
	pattern, value := "(a, b)", "(open(1), open(2))"
	switch r.IntN(3) {
	case 1:
		pattern, value = "Pair{R: a, S: b}", "pair()"
	case 2:
		pattern, value = "Nest{P: Pair{R: a, S: b}}", "Nest{P: pair()}"
	}
	body := "let " + pattern + " = " + value + "\nlend(b)\na.Close()\nlend(a)"
	wantBad := true
	mode := r.IntN(13)
	switch mode {
	case 1:
		body = "let " + pattern + " = " + value + "\nlend(a)\nlend(b)"
		wantBad = false
	case 2:
		body = "let Pair{R: a} = pair()\nlend(a)"
	case 3:
		body = "let Pair{R: a, S: b} as whole = pair()\nlend(a)\nlend(b)\nlendPair(whole)"
	case 4:
		body = "let Closed{R: a} = Closed{R: open(1)}\nlend(a)"
	case 5:
		body = "match p { case Pair{R: a, S: b}: lend(b); a.Close(); case Empty: }"
	case 6:
		body = "match makeHeld(n) { case Pair{R: a, S: b}: lend(a); lend(b); case Empty: }"
		wantBad = false
	case 7:
		body = "let a = match makeHeld(n) { case Pair{R: a, S: b}: lend(b); a; case Empty: open(3) }\nlend(a)"
		wantBad = false
	case 8:
		body = "match pair().R { case Res as a: keep(a) }"
	case 9:
		body = "match (open(1), open(2)) { case (a, b): lend(b); a.Close(); lend(a) }"
	case 10:
		body = "let Pair{R: _, S: b} = pair()\nlend(b)"
	case 11:
		body = "let result = plain[int]((1, 2))\nlet _ = result"
		wantBad = false
	case 12:
		body = "let Pair{R: a, S: b} = if n > 0 { keepHeld(p); pair() } else { pair() }\nlend(a)\nlend(b)"
	}
	src := ofPrelude + `type Pair { R Res, S Res }
type Nest { P Pair }
type Closed { R Res }
func Close(c Closed) {}
func pair() Pair { Pair{R: open(1), S: open(2)} }
func lendPair(p Pair) {}
type PatternHeld = Pair | Empty
func makeHeld(n int) PatternHeld { if n > 0 { return Empty }; pair() }
func plain[T](p (T, int)) int { let (_, n) = p; n }
func keepHeld(p PatternHeld) takes p {}
func Body(n int, p PatternHeld) {
` + body + "\n}\nfunc main() {}\n"
	c, ds := checkFor(src)
	bad := false
	for _, d := range ds {
		if d.Code != CodeType || (!strings.Contains(d.Message, "handed off") && !strings.Contains(d.Message, "pattern") && !strings.Contains(d.Message, "lent from") && !strings.Contains(d.Message, "lent to")) {
			t.Fatalf("pattern model seed %d: unexpected diagnostic %v\n%s", seed, d, src)
		}
		bad = true
	}
	if bad != wantBad {
		t.Fatalf("pattern model seed %d (ERGO_SEED=%d), mode %d: bad=%v want %v\n%s\n%v", seed, seed, mode, bad, wantBad, src, ds)
	}
	if wantBad {
		return
	}
	// Every resource binding of a consuming pattern must occur in the
	// release plan, unless its use transfers it to the arm's result.
	bound, planned := map[*Var]bool{}, map[*Var]bool{}
	syntax.Inspect(c.pkg.Scope.names["Body"].(*Func).Decl.Body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok {
			if v, ok := c.pkg.Info.Defs[id].(*Var); ok && (id.Name == "a" || id.Name == "b") {
				bound[v] = true
			}
		}
		return true
	})
	for _, p := range allPlans(c.pkg.Info) {
		for _, release := range p.rs {
			planned[release.Var] = true
		}
	}
	for v := range bound {
		if c.pkg.Info.OwnedVars[v] == nil || !planned[v] && mode != 7 {
			t.Fatalf("pattern model seed %d: %s has no owner/release (owned=%v planned=%v)\n%s", seed, v.Name, c.pkg.Info.OwnedVars[v] != nil, planned[v], src)
		}
	}
}

// ofPatternConsumers draws a pattern's shape, its cover and its consumer
// independently (16a fix r1, M3): one rule for every consuming pattern
// (resources R39), so a let, a match and a for-in over a queue's Values()
// (each element owned, Q7) refuse an omitted or wildcard resource part,
// and a for-in over a slice (lent, R35) admits it. Each binding of an
// admitted consuming pattern has an owner and a release. A queue's
// Values() stored in a let or passed to a Seq parameter is refused (16a
// fix r2, M1; mutant 16a-r2-values-seq-kept).
func ofPatternConsumers(t *testing.T, seed uint64) {
	r := rand.New(rand.NewPCG(seed, 0x706f32))
	type shape struct{ typ, value, full, omit, wild string }
	shapes := []shape{
		{"(Res, Res)", "(open(1), open(2))", "(a, b)", "(_, b)", "(_, b)"},
		{"Pair", "pair()", "Pair{R: a, S: b}", "Pair{S: b}", "Pair{R: _, S: b}"},
		{"Nest", "Nest{P: pair()}", "Nest{P: Pair{R: a, S: b}}", "Nest{P: Pair{S: b}}", "Nest{P: Pair{R: _, S: b}}"},
	}
	s := shapes[r.IntN(len(shapes))]
	cover := r.IntN(3)
	pattern, uses := s.full, "lend(a)\nlend(b)"
	switch cover {
	case 1:
		pattern, uses = s.omit, "lend(b)"
	case 2:
		pattern, uses = s.wild, "lend(b)"
	}
	consumer := r.IntN(6)
	var body, helper string
	switch consumer {
	case 4:
		// A stored Seq of owned values (16a fix r2, M1): refused.
		body = "let q = queue.New[" + s.typ + "](1)\nlet s = q.Values()\nfor " + pattern + " in s {\n" + uses + "\n}"
	case 5:
		// A Seq of owned values passed on (16a fix r2, M1): refused; the
		// callee's for-in lends, so its cover is not checked.
		helper = "func drain(s Seq[" + s.typ + "]) {\nfor " + pattern + " in s {\n" + uses + "\n}\n}\n"
		body = "let q = queue.New[" + s.typ + "](1)\ndrain(q.Values())"
	case 0:
		body = "let " + pattern + " = " + s.value + "\n" + uses
	case 1:
		// An owned subject: the arm's pattern consumes it. (A literal
		// subject's parts are each a temporary of the statement, and its
		// pattern lends from them.)
		body = "let s = " + s.value + "\nmatch s { case " + pattern + ": " + strings.ReplaceAll(uses, "\n", "; ") + " }"
	case 2:
		body = "let q = queue.New[" + s.typ + "](1)\nfor " + pattern + " in q.Values() {\n" + uses + "\n}"
	case 3:
		body = "for " + pattern + " in xs {\n" + uses + "\n}"
	}
	// A Seq of a queue's owned values outside the range of a for-in has
	// no owner (resources R35): consumers 4 and 5 are refused whatever
	// the cover, by that diagnostic alone.
	seqOut := consumer >= 4
	wantBad := seqOut || cover != 0 && consumer != 3
	src := ofPrelude + `type Pair { R Res, S Res }
type Nest { P Pair }
func pair() Pair { Pair{R: open(1), S: open(2)} }
` + helper + `func Body(n int, xs []` + s.typ + `) {
` + body + "\n}\nfunc main() {}\n"
	c, ds := checkFor(src)
	bad := false
	for _, d := range ds {
		want := "pattern"
		if seqOut {
			want = "q.Values() yields the queue's values"
		}
		if d.Code != CodeType || !strings.Contains(d.Message, want) {
			t.Fatalf("pattern consumer seed %d (ERGO_SEED=%d): unexpected diagnostic %v\n%s", seed, seed, d, src)
		}
		bad = true
	}
	if bad != wantBad {
		t.Fatalf("pattern consumer seed %d (ERGO_SEED=%d), cover %d consumer %d: bad=%v want %v\n%s\n%v", seed, seed, cover, consumer, bad, wantBad, src, ds)
	}
	if bad || consumer == 3 {
		return
	}
	planned := map[*Var]bool{}
	for _, p := range allPlans(c.pkg.Info) {
		for _, release := range p.rs {
			planned[release.Var] = true
		}
	}
	syntax.Inspect(c.pkg.Scope.names["Body"].(*Func).Decl.Body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && (id.Name == "a" || id.Name == "b") {
			if v, ok := c.pkg.Info.Defs[id].(*Var); ok && (c.pkg.Info.OwnedVars[v] == nil || !planned[v]) {
				t.Fatalf("pattern consumer seed %d: %s has no owner/release (owned=%v planned=%v)\n%s", seed, v.Name, c.pkg.Info.OwnedVars[v] != nil, planned[v], src)
			}
		}
		return true
	})
}
