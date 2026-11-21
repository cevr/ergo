package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIRGoLibMatchSource is the FLIP F7 FORK-GOLIB agreement property for
// the Go calls a third-party library needs (GenerateGoLib): for each seed
// the IR path and the source path print the same and exit the same, and
// parse and every libN get an IR body, so a call failing with go.Error,
// a variadic spread, an Option of a go.Ref (the niche), a Go method, the
// task's context, a callback that fails and a field read through a
// go.Ref all run from the IR. A failing seed prints the Ergo source, both
// outcomes and its replay command. Mutants f7l-* (mutants.json) and the
// lower ledger's naming-goref-selector-named fail it.
//
// It builds Go for the reason needsBuild gives: the question is what the
// built program's Go calls return and report, which only a run shows.
func TestIRGoLibMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateGoLib(seed)
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
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRGoLibMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		var want []string
		if strings.Contains(text, "\tparse(s)") {
			want = append(want, "parse")
		}
		for i := 0; strings.Contains(text, fmt.Sprintf("func lib%d(", i)); i++ {
			want = append(want, fmt.Sprintf("lib%d", i))
		}
		for _, f := range want {
			if !slices.Contains(rep.IR, f) {
				t.Fatalf("seed %d: no IR body for %s (IR %v, fallback %v)\n%s\n%s", seed, f, rep.IR, rep.Fallback, replay, text)
			}
		}
	}
}
