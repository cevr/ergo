package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// genericFuncs are GenerateGeneric's functions: each gets an IR body in
// every seed. sameS, an == of two generic flat unions, is not one yet
// (the IR's == takes basic operands).
var genericFuncs = []string{"e3", "e3name", "mkG", "showG", "mkS", "swapS", "showT", "countB", "msName", "mi", "mstep", "scan"}

// TestIRGenericUnionsMatchSource is MSB1's agreement property through the
// IR path: for generated programs of a generic union of the program at
// three instances, an enum and a machine step that returns the prelude's
// Decision (GenerateGeneric), the IR path and the source path print the
// same and exit the same. Every function but main gets an IR body, and
// the IR spells the generic unions in the form the shared lowerer decided
// (lower/genericunion.go): one generic Go struct whose variants go in by
// its of methods and whose matches switch on its tag, never a sealed
// interface. Its mutant: the IR builder deciding its own form
// (ir-own-representation, which makes every union an interface). A failing
// seed prints the Ergo source, both outcomes and its replay command. It
// builds Go for the reason needsBuild gives.
func TestIRGenericUnionsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateGeneric(seed)
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
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRGenericUnionsMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var missing []string
		for _, f := range genericFuncs {
			if !slices.Contains(rep.IR, f) {
				missing = append(missing, f)
			}
		}
		if len(missing) > 0 {
			t.Fatalf("seed %d: no IR body for %v (fallback %v)\n%s\n%s", seed, missing, rep.Fallback, replay, text)
		}
		seen := map[string]bool{}
		irFile := filepath.Join(dir, "ir", "ergoir.json")
		irKinds(t, irFile, seen)
		of, tag := unionForms(t, irFile, seen)
		if of == 0 || tag == 0 || seen["tswitch"] || seen["toiface"] {
			t.Fatalf("seed %d: the IR's unions are not the flat form (of calls %d, tag switches %d, type switches %v, interface conversions %v)\n%s\n%s", seed, of, tag, seen["tswitch"], seen["toiface"], replay, text)
		}
		mainGo, err := os.ReadFile(filepath.Join(dir, "ir", "main.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range []string{"type G[A any, B any] struct", "type Decision[S any, O any] struct"} {
			if !strings.Contains(string(mainGo), decl) {
				t.Fatalf("seed %d: the IR path's Go declares no %q: the shared lowerer's generic union form\n%s", seed, decl, replay)
			}
		}
	}
}
