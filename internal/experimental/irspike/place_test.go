package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIRPlacesMatchSource is F2's agreement property for places
// (GeneratePlace): for each seed the IR path and the source path print the
// same and exit the same. Every plN and every *T function the program
// declares gets an IR body, so the stores through a pointer and into a
// local record, the op-assignments, &p of a field and an element, *i =
// v, and the *T calls on a T place all run from the IR. A failing seed
// prints the Ergo source, both outcomes and its replay command. Mutant
// ir-place-addr-copy (the receiver's address taken of a copy) fails it,
// and so do STOREMOVE's: a store through a let mut that the value's
// statements move taken as fixed (storemove-mut-pointer-fixed,
// storemove-mut-deref-fixed: moved and movedAll write the record moved
// to), and PORT-TEMP's fixPlace without its pointer binds
// (port-temp-ptr-unbound, port-temp-deref-unbound). No such store is
// refused since PORT-TEMP, so STOREMOVE's refusal mutants are gone.
//
// It builds Go for the reason needsBuild gives: the question is what the
// built program writes where, which only a run shows.
func TestIRPlacesMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GeneratePlace(seed)
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("seed %d: the generated program does not check: %v\n%s", seed, err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRPlacesMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		want := slices.Clone(placeFuncs)
		for i := 0; strings.Contains(text, fmt.Sprintf("func pl%d(", i)); i++ {
			want = append(want, fmt.Sprintf("Rec.pl%d", i))
		}
		var missing []string
		for _, f := range want {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
	}
}
