package irspike

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/lower"
)

// TestIRLibraryBodiesLowerAsCensused: on every example binary, a std body
// the census finds no reason in and the build declares (a pure body may be
// inlined at every call instead) is a body the build lowers through IR,
// and at least one std body is lowered. A library body is positioned in its embedded file as a
// program's body is in its source (lower/ir.go pos), in a census and in a
// build alike (mutant opc-lib-gate-in-builds: the library-body gate kept
// outside a census, so the census reports bodies the build never lowers).
// The check is in process: the census and the IR report of one lowering
// each, with no Go build.
func TestIRLibraryBodiesLowerAsCensused(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: lowers every example binary twice")
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	mains, _, err := ExampleTargets(root)
	if err != nil {
		t.Fatal(err)
	}
	stdLowered := 0
	for _, dir := range mains {
		u, err := Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		c, err := lower.CensusIR(u, false)
		if err != nil {
			t.Fatalf("%s: census: %v", dir, err)
		}
		files, rep, err := lower.LowerIR(u)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		var text strings.Builder
		for _, f := range files {
			text.Write(f.Data)
		}
		// A body the build never declares (inlined at each call) has no
		// body to lower.
		declared := func(name string) bool {
			head := "func " + name
			if i := strings.LastIndex(name, "."); i >= 0 {
				head = ") " + name[i+1:]
			}
			return strings.Contains(text.String(), head+"(") || strings.Contains(text.String(), head+"[")
		}
		var clean, reasoned []string
		for _, f := range c.Funcs {
			if !f.Std {
				continue
			}
			if len(f.Reasons) == 0 {
				clean = append(clean, f.Name)
			} else {
				reasoned = append(reasoned, f.Name)
			}
		}
		for _, name := range clean {
			if !slices.Contains(rep.IR, name) && !slices.Contains(reasoned, name) && declared(name) {
				t.Errorf("%s: the census lowers the std body %s and the build does not\nreplay: go test -run TestIRLibraryBodiesLowerAsCensused ./internal/experimental/irspike", dir, name)
			}
		}
		for _, name := range rep.IR {
			if slices.Contains(clean, name) {
				stdLowered++
			}
		}
	}
	if stdLowered == 0 {
		t.Fatal("no example lowers a std body through IR: the property does not reach a library body")
	}
}
