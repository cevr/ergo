package eval_test

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/eval/evalgen"
	"ergo.local/prototype/internal/sketchfront"
)

// TestPreparedEvalAgrees holds the compiled forms (compile.go, ORACLE
// O34) to the tree walk, which stays selectable as the reference
// (eval.SetWalkOnly). Each seed's evalgen program (its test over values,
// control, generic calls, literals and package lets) and each seed's
// evalgen main program run both ways: the reports must be equal in every
// field, each obligation's status, reason, site and steps, the bytes, and
// the main's streams, status and steps. In the full tier every example
// package's obligations run both ways too. The seeds must make every form
// compile.go has. A failing seed prints the source, both outcomes and the
// replay command. Every comparison is exact: the steps of an obligation
// do not depend on Go's map order.
//
// It is TESTSPEED FT4's agreement property. Mutants only it kills
// (testdata/mutants.json): ft-step-not-counted-in-fast-path and the
// oracle-o34-* rows that name it.
func TestPreparedEvalAgrees(t *testing.T) {
	n := 160
	if testing.Short() {
		n = 60
	}
	first := uint64(1)
	only := os.Getenv("ERGO_COMPILED_SEED")
	if only != "" {
		v, err := strconv.ParseUint(only, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	before := eval.FormsMade()
	defer eval.SetWalkOnly(false)
	replay := func(seed uint64) string {
		return fmt.Sprintf("replay: ERGO_COMPILED_SEED=%d go test -run TestPreparedEvalAgrees ./internal/sketch/eval", seed)
	}
	for seed := first; seed < first+uint64(n); seed++ {
		p := evalgen.Generate(seed)
		pkg := checkedSource(t, seed, p.Source)
		walk, fast := bothWays(t, func() any {
			rep, err := eval.Run(pkg, eval.DefaultBudget())
			if err != nil {
				t.Fatalf("seed %d: %v\n%s", seed, err, p.Source)
			}
			return rep
		})
		if !reflect.DeepEqual(walk, fast) {
			t.Fatalf("seed %d: the compiled forms and the walk differ:\nwalk:     %+v\ncompiled: %+v\n%s\n%s", seed, walk, fast, p.Source, replay(seed))
		}
		m := evalgen.GenerateMain(seed)
		mpkg := checkedSource(t, seed, m.Source)
		walk, fast = bothWays(t, func() any {
			got, err := eval.RunMain(mpkg, eval.DefaultBudget(), eval.Process{Args: m.Args, Stdin: m.Stdin})
			if err != nil {
				t.Fatalf("seed %d: %v\n%s", seed, err, m.Source)
			}
			return got
		})
		if !reflect.DeepEqual(walk, fast) {
			t.Fatalf("seed %d: the main's compiled forms and the walk differ:\nwalk:     %+v\ncompiled: %+v\n%s\n%s", seed, walk, fast, m.Source, replay(seed))
		}
	}
	if only != "" {
		return
	}
	made := eval.FormsMade()
	for _, form := range []string{"*syntax.BasicLit", "*syntax.Ident", "*syntax.ParenExpr", "*syntax.BinaryExpr", "*syntax.CallExpr", "*syntax.SelectorExpr", "*syntax.ExprStmt", "*syntax.AssignStmt", "*syntax.DeclStmt", "*syntax.IfStmt", "*syntax.ForStmt", "*syntax.RangeStmt", "*syntax.CompositeLit", "*syntax.ReturnStmt", "*syntax.SwitchStmt", "*syntax.TypeSwitchStmt", "instance body"} {
		if made[form] == before[form] {
			t.Errorf("no seed compiled a form of %s (made %v)", form, made)
		}
	}
	if testing.Short() {
		return // the examples are the full tier's
	}
	for _, dir := range examplePackages(t) {
		a, err := sketchfront.CheckDir(dir)
		if err != nil {
			continue // a package that does not check here runs nothing
		}
		pkgs := a.Checked()
		if len(pkgs) == 0 {
			continue
		}
		pkg := pkgs[len(pkgs)-1]
		walk, fast := bothWays(t, func() any {
			rep, err := eval.Run(pkg, eval.DefaultBudget())
			if err != nil {
				t.Fatalf("%s: %v", dir, err)
			}
			return rep
		})
		if !reflect.DeepEqual(walk, fast) {
			t.Errorf("%s: the compiled forms and the walk differ:\n%s", dir, reportDiff(walk.(eval.Report), fast.(eval.Report)))
		}
	}
}

// bothWays runs run under the walk, then with compiled forms.
func bothWays(t *testing.T, run func() any) (walk, fast any) {
	t.Helper()
	eval.SetWalkOnly(true)
	walk = run()
	eval.SetWalkOnly(false)
	return walk, run()
}

func checkedSource(t *testing.T, seed uint64, src string) *check.Package {
	t.Helper()
	a := sketchfront.Check(src)
	if len(a.Diagnostics) > 0 {
		t.Fatalf("seed %d: %v\n%s", seed, a.Diagnostics, src)
	}
	pkgs := a.Checked()
	return pkgs[len(pkgs)-1]
}

// examplePackages is every directory under examples/ that holds Ergo
// source, as the census reads them.
func examplePackages(t *testing.T) []string {
	t.Helper()
	root, err := filepath.Abs("../../../examples")
	if err != nil {
		t.Fatal(err)
	}
	var dirs []string
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		switch d.Name() {
		case "cases", "testdata", "node_modules", "editions":
			return filepath.SkipDir
		}
		ents, err := os.ReadDir(path)
		if err != nil {
			return err
		}
		if slices.ContainsFunc(ents, func(e os.DirEntry) bool { return strings.HasSuffix(e.Name(), ".ergo") }) {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dirs
}

// reportDiff names the obligations whose results differ between the walk
// and the compiled forms.
func reportDiff(walk, fast eval.Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "steps %d vs %d, bytes %d vs %d\n", walk.Steps, fast.Steps, walk.Bytes, fast.Bytes)
	if len(walk.Results) != len(fast.Results) {
		return fmt.Sprintf("%d results vs %d", len(walk.Results), len(fast.Results))
	}
	for i := range walk.Results {
		if !reflect.DeepEqual(walk.Results[i], fast.Results[i]) {
			fmt.Fprintf(&b, "walk:     %+v\ncompiled: %+v\n", walk.Results[i], fast.Results[i])
		}
	}
	return b.String()
}
