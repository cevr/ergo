package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower/provgen"
)

// TestIRProvidersMatchSource is the agreement property of the copies for
// known providers on IR (PORT-INSTCOPY; lower/ir_known.go): each program
// of the known-provider property's generator (lower/provgen,
// TestKnownProvidersAgree's) prints, exits and fails the same on both
// paths. Its functions on an operation type Sink call its operations,
// fail, loop, pass the Sink on and in some keep it an interface; main
// passes three providers. Every copy the generator's model names is an
// IR body (its Go name ends in the adapter's, AsSink), and no body falls
// back on "copy for an instance". A failing seed prints the Ergo source,
// both outcomes and its replay command. It builds Go because the question
// is what the copies do at run time (each operation call reaches its
// provider's function, a failure and the defect leave as written), and
// because a copy's parameter is the adapter's pointer only in the Go the
// patched compile types. It catches the ledger's mutants
// ic-provider-copy-refused, ic-provider-read-not-converted and
// ic-provider-arg-converted.
func TestIRProvidersMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	copies := 0
	for seed := first; seed < first+count; seed++ {
		text, want, _ := provgen.Program(seed, map[string]bool{})
		replay := fmt.Sprintf("replay: ERGO_IRSPIKE_SEED=%d ERGO_IRSPIKE_SEEDS=1 go test -run '^TestIRProvidersMatchSource$' ./internal/experimental/irspike", seed)
		dir := t.TempDir()
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
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var built []string
		for _, f := range rep.IR {
			if strings.Contains(f, "AsSink") {
				built = append(built, f)
			}
		}
		if len(built) != want {
			t.Errorf("seed %d: %d copies for a known provider are IR bodies (%v), the model names %d (fallbacks %v)\n%s\n%s", seed, len(built), built, want, rep.Fallback, replay, text)
		}
		for r := range rep.Fallback {
			if strings.Contains(r, "copy for an instance") {
				t.Errorf("seed %d: a body falls back on %q\n%s\n%s", seed, r, replay, text)
			}
		}
		copies += len(built)
	}
	t.Logf("%d copies for known providers built on IR", copies)
}
