package lower

import (
	"encoding/json"
	"math/rand/v2"
	"strings"
	"testing"
)

// TestIRBodiesArePlaced is the seeded property of JOBS-ICE r3: every IR
// body passes irScopesIncrease, which gives each node the lowering made
// with no position of its own (a statement, an operand, an else, a
// clause, a type switch's default, a literal) a placed one, so irPlaced
// holds for any body, whichever builder dropped a position. The bodies
// are drawn: ifs with and without an else, switches and type switches
// with and without a default clause, loops, blocks, function literals and
// calls, nested three deep, each position missing at random, the first
// one too. A failing seed prints the body as the IR file holds it.
//
// Mutants it alone catches: jobsice-r3-place-none (no unplaced position
// placed: the class gent and the L7 repro hit, an else and a default with
// none), jobsice-r3-operands-unplaced (an operand left unplaced),
// jobsice-r3-start-unused (a body whose first node has no position).
func TestIRBodiesArePlaced(t *testing.T) {
	for seed := uint64(1); seed <= 300; seed++ {
		r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
		pos := func() irPos {
			if r.IntN(3) == 0 {
				return irPos{}
			}
			return irPos{r.IntN(2), 1 + r.IntN(40), 1 + r.IntN(20)}
		}
		var stmts func(depth int) []*irStmt
		var expr func(depth int) *irExpr
		expr = func(depth int) *irExpr {
			switch k := r.IntN(4); {
			case k == 0 && depth < 3:
				return &irExpr{K: "func", Pos: pos(), Body: stmts(depth + 1)}
			case k == 1 && depth < 3:
				return &irExpr{K: "call", Pos: pos(), Name: "f", Args: []*irExpr{expr(depth + 1), expr(depth + 1)}}
			case k == 2 && depth < 3:
				return &irExpr{K: "binary", Pos: pos(), Op: "+", X: expr(depth + 1), Y: expr(depth + 1)}
			}
			return &irExpr{K: "local", Pos: pos(), I: r.IntN(4)}
		}
		stmts = func(depth int) []*irStmt {
			var out []*irStmt
			for range 1 + r.IntN(3) {
				s := &irStmt{Pos: pos(), End: pos()}
				switch k := r.IntN(6); {
				case depth >= 3 || k == 0:
					s.K, s.X = "expr", expr(depth)
				case k == 1:
					s.K, s.X, s.Then = "if", expr(depth), stmts(depth+1)
					if r.IntN(2) == 0 {
						s.Else, s.ElsePos, s.ElseEnd = stmts(depth+1), pos(), pos()
					}
				case k == 2 || k == 3:
					s.K, s.X, s.ElsePos = []string{"switch", "tswitch"}[k-2], expr(depth), pos()
					for range 1 + r.IntN(3) {
						c := &irCase{Pos: pos(), Body: stmts(depth + 1)}
						if k == 2 {
							c.Vals = []*irExpr{expr(depth + 1)}
						} else {
							c.Types = []string{"A"}
						}
						s.Cases = append(s.Cases, c)
					}
					if r.IntN(3) == 0 {
						s.Cases = append(s.Cases, &irCase{Pos: pos(), Def: true, Body: stmts(depth + 1)})
					}
				case k == 4:
					s.K, s.Body = []string{"while", "range", "block"}[r.IntN(3)], stmts(depth+1)
				default:
					s.K, s.X, s.Y = "set", expr(depth), expr(depth)
				}
				out = append(out, s)
			}
			return out
		}
		at := irPos{0, 1 + r.IntN(5), 1}
		body := irScopesIncrease(stmts(0), at)
		f := &irFile{Funcs: []irFunc{{Name: "drawn", Pos: at, End: at, Body: body}}}
		if err := irPlaced(f); err != nil {
			j, _ := json.Marshal(body)
			t.Fatalf("seed %d: %v\n%s", seed, err, j)
		}
	}
}

// TestIRPlacedNamesTheNode: a type switch with no default position, the
// codec writer's before JOBS-ICE, stops the lowering with an internal
// compiler error naming the switch and its function, not Go's compile at
// a DWARF scope. A row for the diagnostic's text.
//
// Mutant it alone catches: jobsice-placed-tswitch-default (the default's
// position is checked only when the IR has an else).
func TestIRPlacedNamesTheNode(t *testing.T) {
	at := irPos{0, 56, 25}
	sw := &irStmt{K: "tswitch", Pos: at, End: at, X: &irExpr{K: "local", Pos: at, I: 2},
		Cases: []*irCase{{Pos: at, Types: []string{"A"}}, {Pos: at, Types: []string{"B"}}}}
	f := &irFile{Funcs: []irFunc{{Name: "ergoWrite", Pos: at, Body: []*irStmt{sw}}}}
	err := irPlaced(f)
	if err == nil || !strings.Contains(err.Error(), "internal compiler error: the default or else of tswitch at [0,56,25] of ergoWrite has no position") {
		t.Fatalf("irPlaced: %v; want the tswitch's default named, in ergoWrite", err)
	}
	sw.ElsePos = at
	if err := irPlaced(f); err != nil {
		t.Fatalf("irPlaced of a placed switch: %v", err)
	}
}
