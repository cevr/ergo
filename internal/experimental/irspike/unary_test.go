package irspike

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIRUnaryOperandsMatchSource is the seeded property over unary
// operators with binary operands (MISCOMPILE-1, GenerateUnary): each
// program prints the same under both lowerings. A failing seed prints
// the Ergo source, both outcomes and its replay command. Mutant M7 (the
// derivative's un case without the parenthesized operand) fails it.
//
// It builds Go because the defect is in the patched compile's writer
// (staticBool reads the operand's syntax), after the IR text is checked:
// only a compiled and run program shows the branch it dropped.
func TestIRUnaryOperandsMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	first, count := envInt("ERGO_IRSPIKE_SEED", 1), envInt("ERGO_IRSPIKE_SEEDS", 6)
	for seed := first; seed < first+count; seed++ {
		text := GenerateUnary(seed)
		checkBoth(t, src, ir, text, func() string {
			return "replay: ERGO_IRSPIKE_SEED=" + itoa(seed) + " ERGO_IRSPIKE_SEEDS=1 go test -run TestIRUnaryOperandsMatchSource ./internal/experimental/irspike"
		})
	}
}

// TestIRReprosMatchSource runs the smallest repros of fixed miscompiles
// under both lowerings, each with the function it needs an IR body of:
//   - notand (MISCOMPILE-1): `if !(p && false)` was dropped as never
//     taken (derivative mutant M7).
//
// It builds Go for the reason TestIRUnaryOperandsMatchSource does.
func TestIRReprosMatchSource(t *testing.T) {
	src, ir := needsBuild(t)
	for _, r := range []struct{ name, fn string }{{"notand", "pick"}} {
		t.Run(r.name, func(t *testing.T) {
			text, err := os.ReadFile(filepath.Join("testdata", r.name+".ergo"))
			if err != nil {
				t.Fatal(err)
			}
			rep := checkBoth(t, src, ir, string(text), func() string {
				return "replay: go test -run 'TestIRReprosMatchSource/" + r.name + "' ./internal/experimental/irspike"
			})
			found := false
			for _, f := range rep {
				found = found || f == r.fn
			}
			if !found {
				t.Fatalf("%s: %s has no IR body", r.name, r.fn)
			}
		})
	}
}

// checkBoth builds the program text by both paths, runs both and fails
// when their outcomes differ; it returns the functions with IR bodies.
func checkBoth(t *testing.T, src, ir Toolchain, text string, replay func() string) []string {
	t.Helper()
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
		t.Fatalf("the paths differ\nsource: %+v\nir:     %+v\n%s\n%s", a, b, replay(), text)
	}
	return rep.IR
}
