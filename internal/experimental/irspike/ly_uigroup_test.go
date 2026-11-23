package irspike

import (
	"os"
	"path/filepath"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketch/uiskipgen"
)

// TestIRUISkipMatchesSource is the skipping property (P-ui; lower's
// TestUISkipAgrees) through the IR build (IRG-MAIN): uiskipgen's program
// of a seed prints the same under the source build, whose component calls
// go through std ui's group, and the IR build, its counted work included
// (the components each pass runs, growth, elsewhere and nested), so the
// IR skips exactly the calls the source skips. The views that make
// component calls have IR bodies, and the IR calls group and groupBy. A
// failing seed prints the Ergo source, both outcomes and its replay
// command.
//
// It builds Go because what a pass skips shows only when the compiled
// program runs its passes.
//
// Mutants (testdata/mutants.json): ly-irgroup-off, ly-ns2-ir-preset-unread,
// ly-ns2-ir-reads-whole, ly-ns2-ir-keep-aliases.
func TestIRUISkipMatchesSource(t *testing.T) {
	lyProperty(t, "TestIRUISkipMatchesSource", func(seed uint64) string { return uiskipgen.Program([]uint64{seed}) },
		uiGroupNames(t), "card", "list", "framed", "headed", "tallied", "wideView", "cardsView", "bagged")
}

// uiGroupNames is the group functions of the frame packages of
// uiskipgen's program, as the lowering finds them, by shape and law
// (lower.GroupFuncsForTest), never by their spelling.
func uiGroupNames(t *testing.T) []string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "p.ergo")
	if err := os.WriteFile(file, []byte(uiskipgen.Program([]uint64{1})), 0o644); err != nil {
		t.Fatal(err)
	}
	u, err := Load(file)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	seen := map[*check.Package]bool{}
	for _, f := range u.Package.Funcs {
		if f.Sig == nil || len(f.Sig.Params) == 0 {
			continue
		}
		p, ok := f.Sig.Params[0].Type.(*check.Pointer)
		if !ok {
			continue
		}
		n, ok := p.Elem.(*check.Named)
		if !ok || n.Obj == nil || n.Obj.Pkg == nil || seen[n.Obj.Pkg] {
			continue
		}
		seen[n.Obj.Pkg] = true
		for _, g := range lower.GroupFuncsForTest(n.Obj.Pkg) {
			names = append(names, g.Name)
		}
	}
	if len(names) == 0 {
		t.Fatal("no frame package of the program declares a lawed group")
	}
	return names
}
