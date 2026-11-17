package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// foldProgram writes GenerateFold(seed) into dir and loads it.
func foldProgram(t *testing.T, seed uint64, dir string) (lower.Unit, string) {
	t.Helper()
	text := GenerateFold(seed)
	file := filepath.Join(dir, "p.ergo")
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatalf("seed %d: the program does not check: %v\n%s", seed, err, text)
	}
	return u, text
}

// emitFold lowers u by the IR path, the compiler seen on the Go release
// goVersion, with the fold pass on or off (lower.SetPureFold) into dir.
func emitFold(t *testing.T, u lower.Unit, on bool, goVersion, dir string) lower.IRReport {
	t.Helper()
	defer lower.SetPureFold(on)()
	defer lower.SetCompilerGo(goVersion)()
	rep, err := Emit(u, IR, dir)
	if err != nil {
		t.Fatal(err)
	}
	return rep
}

// TestPureFoldCounts: the IR of the fold programs computes some Go calls
// at compile time when the compiler runs on the pinned Go release, and
// none with the pass off or on another release (LAWFFI LF3c). In
// process: the IR report counts them.
//
// Mutants it alone catches: ffi-fold-other-toolchain (the pass folds on a
// Go release other than the pinned one; the values agree on this host, so
// the agreement property cannot see it), ffi-fold-never (no call folds),
// ffi-fold-off-ignored (the switch off still folds).
func TestPureFoldCounts(t *testing.T) {
	pinned, other, off := 0, 0, 0
	for seed := uint64(1); seed <= 4; seed++ {
		dir := t.TempDir()
		u, _ := foldProgram(t, seed, dir)
		pinned += emitFold(t, u, true, toolchain.Version, filepath.Join(dir, "pinned")).Fold
		other += emitFold(t, u, true, "go1.26.0", filepath.Join(dir, "other")).Fold
		off += emitFold(t, u, false, toolchain.Version, filepath.Join(dir, "off")).Fold
	}
	if pinned == 0 || other != 0 || off != 0 {
		t.Errorf("folded calls: %d on the pinned Go, %d on go1.26.0, %d with the pass off; want some, then none, then none", pinned, other, off)
	}
}

// TestPureFoldAgrees is LF3c's fold-agrees property: each fold program,
// built with the fold pass on (the calls computed by the compiler's
// natives) and off (the reference: Go computes them when the program
// runs), prints the same and exits the same. A failing seed prints the
// Ergo source, both outcomes and its replay command.
//
// Mutants it alone catches: ffi-fold-wrong-native (a native calls another
// Go function), ffi-fold-operand-misread (an operand constant is read
// without its sign), ffi-fold-written-local (a let mut bound to a constant
// and written after counts as that constant).
//
// It builds Go because the claim is that a constant the compiler computed
// equals what the built program's own Go call returns, which only running
// that program shows.
func TestPureFoldAgrees(t *testing.T) {
	_, ir := needsBuild(t)
	first, count := envInt("ERGO_FOLD_SEED", 1), envInt("ERGO_FOLD_SEEDS", 8)
	folded := 0
	for seed := first; seed < first+count; seed++ {
		dir := t.TempDir()
		u, text := foldProgram(t, seed, dir)
		folded += emitFold(t, u, true, toolchain.Version, filepath.Join(dir, "on")).Fold
		emitFold(t, u, false, toolchain.Version, filepath.Join(dir, "off"))
		onBin, offBin := filepath.Join(dir, "on.bin"), filepath.Join(dir, "off.bin")
		for _, b := range [][2]string{{"on", onBin}, {"off", offBin}} {
			if _, err := Build(ir, IR, filepath.Join(dir, b[0]), b[1], BuildOptions{}); err != nil {
				t.Fatalf("seed %d: the %s build: %v\n%s", seed, b[0], err, text)
			}
		}
		if a, b := run(t, onBin), run(t, offBin); a != b {
			t.Errorf("seed %d: the folded calls change the program\non:  %+v\noff: %+v\nreplay: ERGO_FOLD_SEED=%d ERGO_FOLD_SEEDS=1 go test -run TestPureFoldAgrees ./internal/experimental/irspike\n%s", seed, a, b, seed, text)
		}
	}
	if folded == 0 {
		t.Errorf("no seed folded a call: the property compares two equal lowerings")
	}
}
