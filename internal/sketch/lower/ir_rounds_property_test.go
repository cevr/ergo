package lower

import (
	"bytes"
	"flag"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
)

var roundsSeed = flag.Uint64("rounds.seed", 0, "replay one seed of TestRoundsEmitWhatOneRoundEmits")

// roundsProgram is a test build whose laws draw a Map[int, int] (its
// generator makes Go's map, an int-map conflict, so the IR lowering takes a
// second round: intmap.go) beside parameters of other generated kinds, a
// record, a union, a slice of the union and a Map[string, int], picked by
// the seed; each law's failure shows its arguments, so it takes their
// literal functions too.
func roundsProgram(seed uint64) string {
	r := rand.New(rand.NewPCG(seed, 0x60d5))
	var b strings.Builder
	b.WriteString("type Set { Key int, Value int }\n\ntype Drop { Key int }\n\ntype Edit = Set | Drop\n\ntype Pair { A int, B string }\n\n")
	b.WriteString("func size(m Map[int, int]) int { len(m) }\n\n")
	kinds := []string{"Edit", "[]Edit", "Pair", "Map[string, int]", "[]int"}
	for i := range 1 + r.IntN(3) {
		var params []string
		params = append(params, "units Map[int, int]")
		for j := range r.IntN(3) {
			params = append(params, fmt.Sprintf("p%d %s", j, kinds[r.IntN(len(kinds))]))
		}
		fmt.Fprintf(&b, "law L%d(%s) {\n\tsize(units) >= 0\n}\n\n", i, strings.Join(params, ", "))
	}
	b.WriteString("func main() needs Console {\n\tConsole.Println(\"\\(size(Map[int, int]{}))\")\n}\n")
	return b.String()
}

// TestRoundsEmitWhatOneRoundEmits (LAW-GEN-HELPERS) is lowerIRRounds'
// property over generated test builds that take a second round: the last
// round's main.go is the one a single round with the last round's int-map
// facts emits, so nothing a round emits once (a generator, a literal
// function, a state key function) is left to an earlier, discarded round.
// A failing seed prints the program and the replay command.
//
// Mutant (lower mutants.json): lgh-record-shared.
func TestRoundsEmitWhatOneRoundEmits(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if *roundsSeed != 0 {
		seeds = []uint64{*roundsSeed}
	}
	for _, seed := range seeds {
		src := roundsProgram(seed)
		u := factsUnit(t, src)
		replay := fmt.Sprintf("replay: go test -run TestRoundsEmitWhatOneRoundEmits ./internal/sketch/lower -rounds.seed=%d", seed)
		l, code, err := lowerIRRounds(u, &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}}, false, nil)
		if err != nil {
			t.Fatalf("seed %d: %v\n%s\n%s", seed, err, replay, src)
		}
		if l.ir.report.MapRounds < 2 {
			t.Fatalf("seed %d: the program took %d round; the property needs a second\n%s\n%s", seed, l.ir.report.MapRounds, replay, src)
		}
		facts := intMapState{off: l.ir.maps.off, allGo: l.ir.maps.allGo}
		_, once, err := lowerIRRound(u, &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}}, false, facts, nil)
		if err != nil {
			t.Fatalf("seed %d: one round: %v\n%s", seed, err, replay)
		}
		if !bytes.Equal(code, once) {
			t.Fatalf("seed %d: %d rounds emit %d bytes, one round with their facts %d; the first difference is %s\n%s\n%s", seed, l.ir.report.MapRounds, len(code), len(once), firstDiff(string(code), string(once)), replay, src)
		}
	}
}
