package irspike

import (
	"strings"
	"testing"
)

// TestIRTuplesMatchSource is the seeded property over tuple lets and
// returns (FLIP F4b, GenerateTuple): each program prints the same under
// both lowerings, and every function of it has an IR body, so the
// property never passes on the source form alone. A failing seed prints
// the Ergo source, both outcomes and its replay command. Mutant
// f4b-results-swapped (the tdef's locals in reverse order) fails it.
//
// It builds Go because the tdef is decoded by the patched compile into Go's
// own multi-value assignment: only a compiled and run program shows which
// result each name holds.
func TestIRTuplesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text := GenerateTuple(seed)
		bodies := checkBoth(t, src, ir, text, func() string {
			return "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRTuplesMatchSource ./internal/experimental/irspike"
		})
		has := map[string]bool{}
		for _, f := range bodies {
			has[f] = true
		}
		for _, line := range strings.Split(text, "\n") {
			name, ok := strings.CutPrefix(line, "func ")
			if !ok {
				continue
			}
			name, _, _ = strings.Cut(name, "(")
			if name != "main" && !has[name] {
				t.Fatalf("seed %d: %s has no IR body (IR: %v)\n%s", seed, name, bodies, text)
			}
		}
	}
}
