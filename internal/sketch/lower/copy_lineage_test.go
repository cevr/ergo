package lower

import (
	"strings"
	"testing"
)

// TestChainSkipsLineage: an Append on a one-extender site's result
// (handover.go, own-lineage) is not a chain link written in place (V8,
// inPlaceChain): the site may extend that array in place later, past the
// result's length, where the chain would have written. The promised fact
// is the outer Append's clipped source. Mutant: inPlaceChain takes a
// lineage receiver (mapcopy-chain-lineage).
func TestChainSkipsLineage(t *testing.T) {
	src := "type State { Log []int }\n\nfunc f(s State, a int) State {\n\tState{Log: s.Log.Append(a).Append(a + 1)}\n}\n\n" +
		"func main() needs Console {\n\tConsole.Println(\"\\(len(f(State{Log: []int{}}, 1).Log))\")\n}\n"
	body := goFuncBody(t, mustLower(t, "chainlineage", src).mainGo(), "f")
	if !strings.Contains(body, "LineageAppend(") {
		t.Fatalf("the inner Append is not a one-extender site:\n%s", body)
	}
	if !strings.Contains(body, "slices.Clip(") {
		t.Errorf("the outer Append writes the lineage result in place:\n%s", body)
	}
}

// TestNamedTailsSettle: a function passed by name (here to maps.Update)
// whose tail is a one-extender Append takes the plain clipped Append
// (settleNamedTails), so its result is fresh; a function only called
// keeps the one-extender site (own-lineage), its promised form. Mutants:
// mapcopy-named-lineage-off (TestCopyCountsMatchModel also kills it),
// mapcopy-named-lineage-callee.
func TestNamedTailsSettle(t *testing.T) {
	src := "import maps\n\ntype State { Log []int }\n\nfunc keep(s State) []int { s.Log.Append(1) }\n\n" +
		"func cb(o Option[[]int]) []int {\n\tmatch o {\n\tcase Some[[]int] as v:\n\t\tv.Value.Append(9)\n\tcase None:\n\t\t[]int{}\n\t}\n}\n\n" +
		"func main() needs Console {\n\tlet mut m = Map[string, []int]{\"a\": []int{1}}\n\tm = maps.Update(m, \"a\", cb)\n\tConsole.Println(\"\\(len(m)) \\(len(keep(State{Log: []int{}})))\")\n}\n"
	mainGo := mustLower(t, "namedtails", src).mainGo()
	if body := goFuncBody(t, mainGo, "keep"); !strings.Contains(body, "LineageAppend(") {
		t.Errorf("a function only called lost its one-extender site:\n%s", body)
	}
	if body := goFuncBody(t, mainGo, "cb"); strings.Contains(body, "LineageAppend(") {
		t.Errorf("a callback by name keeps its one-extender site, so its result is not fresh:\n%s", body)
	}
}
