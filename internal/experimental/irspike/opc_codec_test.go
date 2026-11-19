package irspike

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestIRCodecsMatchSource is the seeded property over the codec built-ins
// (FLIP OPC-codec, GenerateCodec): each program prints the same under both
// lowerings, and put and back, which call encode and decode, have IR
// bodies (mutant opc-codec-encode-args-swapped: the writer and the value
// passed in the other order, which the decoder refuses, so the IR build
// fails). A failing seed prints the Ergo source, both outcomes and its
// replay command.
//
// It builds Go because the codec functions the lowering declares are Go
// text the IR body calls; only the compiled program runs the events.
func TestIRCodecsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 4)
	for seed := first; seed < first+count; seed++ {
		text := GenerateCodec(uint64(seed))
		replay := "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRCodecsMatchSource ./internal/experimental/irspike"
		dir := t.TempDir()
		file := filepath.Join(dir, "p.ergo")
		if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		u, err := Load(file)
		if err != nil {
			t.Fatalf("the program does not check: %v\n%s", err, text)
		}
		sb, ib, rep := both(t, u, src, ir, dir, BuildOptions{})
		a, b := run(t, sb), run(t, ib)
		if a != b {
			t.Fatalf("seed %d: the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", seed, a, b, replay, text)
		}
		for _, f := range []string{"put", "back"} {
			// A function whose first parameter is a record lowers as its
			// method: P.put.
			if !slices.ContainsFunc(rep.IR, func(n string) bool { return n == f || strings.HasSuffix(n, "."+f) }) {
				t.Errorf("seed %d: %s has no IR body (IR bodies: %v)\n%s\n%s", seed, f, rep.IR, replay, text)
			}
		}
	}
}
