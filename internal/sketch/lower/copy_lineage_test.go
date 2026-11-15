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
