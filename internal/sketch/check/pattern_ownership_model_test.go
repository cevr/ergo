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
	for _, rs := range c.pkg.Info.Releases {
		for _, release := range rs {
			planned[release.Var] = true
		}
	}
	for v := range bound {
		if c.pkg.Info.OwnedVars[v] == nil || !planned[v] && mode != 7 {
			t.Fatalf("pattern model seed %d: %s has no owner/release (owned=%v planned=%v)\n%s", seed, v.Name, c.pkg.Info.OwnedVars[v] != nil, planned[v], src)
		}
	}
}
