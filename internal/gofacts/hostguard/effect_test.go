package hostguard

import (
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/gofacts"
)

// TestEffectsHold is the guard of the memory-only rows (GBX6): every row
// of gofacts.Effects has a guarded call, every guarded call has a row, and
// none gives other results or leaves another state under the two host
// states; a planted row whose call reads the working directory into its
// state is refuted.
//
// Mutants it alone catches: ffi-effect-guard-one-state (the two states are
// one), ffi-effect-row-unguarded (a row whose guarded call is gone).
func TestEffectsHold(t *testing.T) {
	calls := effectCalls()
	for _, e := range gofacts.Effects {
		if _, ok := calls[e.Target]; !ok {
			t.Errorf("%s: a row of gofacts.Effects with no guarded call", e.Target)
		}
	}
	for target := range calls {
		if _, ok := gofacts.EffectOf(target); !ok {
			t.Errorf("%s: a guarded call with no row of gofacts.Effects", target)
		}
	}
	for target, err := range Effects() {
		t.Errorf("%s: a memory-only row reads host state: %v", target, err)
	}
	planted := map[string]func(r *rand.Rand) effectRun{"fixture.Cwd": func(*rand.Rand) effectRun {
		var dir string
		return effectRun{&dir, func() []any { dir, _ = os.Getwd(); return nil }}
	}}
	if d := effects(planted); len(d) != 1 || !strings.Contains(d[0].Error(), "fixture.Cwd case 0") {
		t.Errorf("a call that reads the working directory passes the memory-only guard: %v", d)
	}
	var keys []string
	for _, e := range Entries("go1.0") {
		if strings.HasSuffix(e.Key, " effect none") {
			keys = append(keys, e.Key)
			if e.Verdict != "validated" {
				t.Errorf("%s is %s", e.Key, e.Verdict)
			}
		}
	}
	if len(keys) != len(gofacts.Effects) || !slices.IsSorted(keys) {
		t.Errorf("Entries gives the effect lines %q, want one per row of gofacts.Effects", keys)
	}
}
