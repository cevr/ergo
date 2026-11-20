package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/toolchain"
)

// TestEmitRunsTheBodies: a main that calls a function with constants
// keeps the call through Emit (the fold off), and Runs refuses the same
// program lowered with the build-time fold (EmitFold, fold on). In
// process: it reads the lowering's report, no Go build. Mutants
// (testdata/mutants.json): vacuous-emit-folds (Emit lowers with the fold
// on, so every property compares folded constants) and
// vacuous-guard-silent (Runs passes a folded program, so both() never
// fails).
func TestEmitRunsTheBodies(t *testing.T) {
	defer lower.SetConstFoldBuildGoForTest(toolchain.Version)()
	defer lower.SetConstFoldCompilerGoForTest(toolchain.Version)()
	dir := t.TempDir()
	file := filepath.Join(dir, "p.ergo")
	text := "func twice(n int) int { n + n }\n\nfunc main() needs Console {\n\tConsole.Println(\"\\(twice(21))\")\n}\n"
	if err := os.WriteFile(file, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Emit(u, IR, filepath.Join(dir, "ir"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Runs(rep); err != nil {
		t.Errorf("Emit: %v", err)
	}
	rep, err = EmitFold(u, filepath.Join(dir, "irfold"), "")
	if err != nil {
		t.Fatal(err)
	}
	if Runs(rep) == nil {
		t.Errorf("EmitFold with the fold on: Runs passes a program whose call of twice is folded (folds %+v)", rep.Folds)
	}
}
