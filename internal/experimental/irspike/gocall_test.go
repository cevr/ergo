package irspike

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIRGoCallsMatchSource is the FLIP F7 FORK-GOCALL agreement property
// for calls of Go package functions (GenerateGoCalls): for each seed the
// IR path and the source path print the same and exit the same, and every
// goN gets an IR body, so the Go call, its operands and the adapter that
// passes an Ergo function value as Go's function (sort.Search,
// strings.Map, strings.IndexFunc) all run from the IR. A failing seed
// prints the Ergo source, both outcomes and its replay command. hold, a
// frame that owns a Go resource, gets an IR body and an IR release
// helper (Go's Close). Mutants f7g-callback-refused (the adapter refused)
// and f7g-host-close-text (a Go value's Close left to Go text) fail it.
//
// It builds Go for the reason needsBuild gives: the question is what the
// built program's Go calls compute with the adapted callbacks, which only
// a run shows.
func TestIRGoCallsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		text := GenerateGoCalls(seed)
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
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRGoCallsMatchSource ./internal/experimental/irspike"
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		want := []string{"hold"}
		for i := 0; strings.Contains(text, fmt.Sprintf("func go%d(", i)); i++ {
			want = append(want, fmt.Sprintf("go%d", i))
		}
		for _, f := range want {
			if !slices.Contains(rep.IR, f) {
				t.Fatalf("seed %d: no IR body for %s (IR %v, fallback %v)\n%s\n%s", seed, f, rep.IR, rep.Fallback, replay, text)
			}
		}
		// hold's release helper, Go's Close of the *os.File, is an IR
		// function (RELEASE-IR's host Close): its Go text is a header.
		gofiles, _ := filepath.Glob(filepath.Join(dir, "ir", "*.go"))
		for _, f := range gofiles {
			if data, _ := os.ReadFile(f); strings.Contains(string(data), "static frame (IR, FLIP F7).") {
				t.Fatalf("seed %d: hold's release helper is Go text, not IR\n%s\n%s", seed, replay, text)
			}
		}
	}
}
