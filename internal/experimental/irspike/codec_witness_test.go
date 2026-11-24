package irspike

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRCodecWitnessesMatchSource is the seeded property over generic
// bodies whose type parameters are under the codec constraint (PORT-WITNESS,
// GenerateCodecWitness): each program prints the same under both
// lowerings, and each site has an IR body. A failing seed prints the Ergo
// source, both outcomes and its replay command.
//
// It builds Go because the codec witness locals, the generic codec
// functions that take them and the failures their Dec fields carry show
// only when the compiled program runs them.
func TestIRCodecWitnessesMatchSource(t *testing.T) {
	f7PropertyWith(t, t.Name(), GenerateCodecWitness, func(rep lower.IRReport) string {
		// viaText calls each function with P and Q. Instance suffixes
		// are derived from type arguments; don't pin the hash and don't
		// count optional projected workers as instances.
		for _, base := range []string{"enc", "dec", "back"} {
			count := 0
			for _, name := range rep.IR {
				if suffix, ok := strings.CutPrefix(name, base+"Mono_i"); ok {
					if _, err := strconv.ParseUint(suffix, 10, 64); err == nil {
						count++
					}
				}
			}
			if count != 2 {
				return fmt.Sprintf("%s: got %d IR instances, want one each for P and Q", base, count)
			}
		}
		return ""
	}, codecWitnessSites...)
}
