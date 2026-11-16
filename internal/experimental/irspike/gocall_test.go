package irspike

import (
	"strings"
	"testing"
)

// TestIRGoCallsMatchSource is the seeded property over Go calls (SP4,
// GenerateGoCall): user externs and import go functions, inside
// expressions and failing go.Error, print the same under both lowerings,
// and every function of the program has an IR body, so the property never
// passes on the source form alone. A failing seed prints the Ergo source,
// both outcomes and its replay command. Mutants sp4-host-pure-is-effect
// (a pure-tier call is an effect, so a call inside an expression drops
// its body) and sp4-host-error-unwrapped (a Go error fails without
// becoming go.Error, so its catch misses) fail it.
//
// It builds Go because the Go calls are decoded by the patched compile
// and the failure crosses Go's error into Ergo's go.Error: only a compiled
// and run program shows which arm catches it and what text it keeps.
func TestIRGoCallsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text := GenerateGoCall(seed)
		bodies := checkBoth(t, src, ir, text, func() string {
			return "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRGoCallsMatchSource ./internal/experimental/irspike"
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
