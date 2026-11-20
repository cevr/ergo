package residue_test

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// preludeUnionNames are the prelude union and its variants: Option, Some
// and None. ADT-NOPRIV design A (approved, owner q270, 2026-11-20): Option
// is one instance of rules any package's union gets, its layout from the
// declaration's shape, a combinator's speed from its definition and a
// fusion from a law; and the Go boundary takes any union of Option's shape
// (owner q271: "Any union of the shape"). So the compiler names none of
// the three. Timeout, the prelude record a machine's After gives, is read
// the same way from Machine's declared field (orchestrator, 2026-11-20).
var preludeUnionNames = []string{"Option", "Some", "None", "Timeout"}

// preludeFieldNames are the fields of those prelude records that the
// compiler reads by name: Some's payload Value, and Timeout's (and the
// Every timer's) In and Signal. A field name is a privilege only where an
// accessor reads it, so the scan counts one of them as the argument of a
// field accessor (preludeFieldAccessors) or compared with a .Name
// selector. Value is counted in check and lower only: the evaluator's
// "Value" accessors read std records (an arena cell, url's query pair),
// which std's own inventory holds, not Some.
var preludeFieldNames = map[string][]string{
	"Value":  {"internal/sketch/check", "internal/sketch/lower"},
	"In":     {"internal/sketch/check", "internal/sketch/lower", "internal/sketch/eval"},
	"Signal": {"internal/sketch/check", "internal/sketch/lower", "internal/sketch/eval"},
}

// preludeFieldAccessors are the functions that read or write a field by
// its name in check, lower and eval.
var preludeFieldAccessors = []string{"memberName", "recordFieldType", "recordField", "recordFieldLit", "fieldIndex", "field"}

// preludeUnionDirs are the compiler packages the rule reads: the checker,
// the lowering (its IR half included) and the evaluator, and the IR
// patch's Go glue (toolchain/ir).
var preludeUnionDirs = []string{"internal/sketch/check", "internal/sketch/lower", "internal/sketch/eval", "toolchain/ir"}

// preludeUnionUnits are the units of ADT-NOPRIV that remove a row:
// OPTION-SHAPE (Option through the general layout), SHAPE-BOUNDARY (the
// pointer form and the Go boundary by shape), KNOWN-VARIANT (the
// known-variant jump that replaces the comma-ok Get special case) and
// ADT-LANG (where the compiler itself produces the value, go.As, the
// any-cast, a machine's After, Work and Exit and the evaluator's Some and
// None, it reads the type from the prelude declaration it is given and
// builds the variant through the general union path). GEN9-GENERAL
// (MODES) is not ADT-NOPRIV's: the type parameter of Option and Some admits
// an unmarked resource (types.md, Generics 9) by a rule read from the
// declaration, so a user's generic union admits it too (orchestrator,
// 2026-11-20: the named rule does not stay).
var preludeUnionUnits = []string{"OPTION-SHAPE", "SHAPE-BOUNDARY", "KNOWN-VARIANT", "ADT-LANG", "GEN9-GENERAL"}

// TestLoweringNamesNoPreludeUnion is ADT-NOPRIV's residue rule (§4 step 1,
// acceptance AC6): no non-test Go file of preludeUnionDirs names the
// prelude's Option, Some, None or Timeout, or reads one of their fields by
// name (preludeFieldNames), outside testdata/prelude-unions.txt. A name is
// a string literal whose text is one of them, so a helper such as
// isOptionType or nicheElem holds the row and its callers do not.
// A row is a function (a method as Recv.Name; a package-level declaration
// by its first name) with its count of such literals, its verdict (by
// name: a decision keyed on the name; language: the compiler builds the
// type there; debt: a row another inventory already holds as debt) and
// the unit that removes it.
//
// The list only shrinks. A mention in a function the list does not hold,
// or more mentions than its row counts, fails: decide by the union's
// shape, its definition or a law. A row whose function names fewer than
// it counts fails as stale, so a removed site takes its row (or its count)
// with it. A row outside preludeUnionBaseline, or above its count there,
// fails too. The rule is done when the list is empty.
//
// Mutants (testdata/mutants.json): prelude-union-new-mention,
// prelude-union-stale-row.
func TestLoweringNamesNoPreludeUnion(t *testing.T) {
	top, err := exec.Command("git", "--no-optional-locks", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("git rev-parse: %v", err)
	}
	repo := strings.TrimSpace(string(top))
	found := preludeUnionSites(t, repo)
	listed := readPreludeUnions(t)
	baseline := map[string]int{}
	for _, b := range preludeUnionBaseline {
		key, n, _ := strings.Cut(b, " ")
		baseline[key], _ = strconv.Atoi(n)
	}
	for key, n := range found {
		row, ok := listed[key]
		switch {
		case !ok:
			t.Errorf("%s names a prelude union, record or field (Option, Some, None, Timeout; Value, In, Signal) %d time(s) and is not in testdata/prelude-unions.txt: decide by the union's shape, its definition or a law (ADT-NOPRIV)", key, n)
		case n > row.count:
			t.Errorf("%s names a prelude union, record or field %d times; testdata/prelude-unions.txt counts %d: the list only shrinks", key, n, row.count)
		}
	}
	for key, row := range listed {
		if n := found[key]; n < row.count {
			t.Errorf("testdata/prelude-unions.txt counts %d mention(s) in %s, which has %d: lower the count or drop the line (stale)", row.count, key, n)
		}
		if max, ok := baseline[key]; !ok || row.count > max {
			t.Errorf("testdata/prelude-unions.txt lists %s with %d, which the list did not hold when the rule landed: the list only shrinks", key, row.count)
		}
	}
}

// preludeUnionRow is one line of testdata/prelude-unions.txt.
type preludeUnionRow struct {
	count         int
	verdict, unit string
}

// readPreludeUnions reads the list: one
// `dir/file.go:function<TAB>count<TAB>verdict<TAB>unit<TAB>reason` line
// per row; # starts a comment.
func readPreludeUnions(t *testing.T) map[string]preludeUnionRow {
	t.Helper()
	f, err := os.Open("testdata/prelude-unions.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out := map[string]preludeUnionRow{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := strings.Split(line, "\t")
		n, err := strconv.Atoi(strings.TrimSpace(preludeField(p, 1)))
		if len(p) != 5 || err != nil || n < 1 || !slices.Contains([]string{"by name", "language", "debt"}, p[2]) || !slices.Contains(preludeUnionUnits, p[3]) || strings.TrimSpace(p[4]) == "" {
			t.Errorf("testdata/prelude-unions.txt: %q is not `dir/file.go:function<TAB>count<TAB>by name|language|debt<TAB>%s<TAB>reason`", line, strings.Join(preludeUnionUnits, "|"))
			continue
		}
		if _, dup := out[p[0]]; dup {
			t.Errorf("testdata/prelude-unions.txt lists %s twice", p[0])
		}
		out[p[0]] = preludeUnionRow{count: n, verdict: p[2], unit: p[3]}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func preludeField(p []string, i int) string {
	if i < len(p) {
		return p[i]
	}
	return ""
}

// preludeUnionSites counts, per function of the non-test Go files of
// preludeUnionDirs (git ls-files, the files directly in each directory),
// the string literals whose text is Option, Some or None. A key is
// `dir/file.go:function`, dir the directory's last element.
func preludeUnionSites(t *testing.T, repo string) map[string]int {
	t.Helper()
	var globs []string
	for _, d := range preludeUnionDirs {
		globs = append(globs, d+"/*.go")
	}
	ls := exec.Command("git", append([]string{"--no-optional-locks", "ls-files", "-z", "--"}, globs...)...)
	ls.Dir = repo
	list, err := ls.Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	out := map[string]int{}
	fset := token.NewFileSet()
	files := 0
	for _, name := range strings.Split(strings.TrimSuffix(string(list), "\x00"), "\x00") {
		if name == "" || strings.HasSuffix(name, "_test.go") || !slices.Contains(preludeUnionDirs, filepath.ToSlash(filepath.Dir(name))) {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(repo, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		files++
		prefix := filepath.Base(filepath.Dir(name)) + "/" + filepath.Base(name) + ":"
		for _, d := range f.Decls {
			if n := preludeUnionLiterals(d, filepath.ToSlash(filepath.Dir(name))); n > 0 {
				out[prefix+preludeDeclName(d)] += n
			}
		}
	}
	if files == 0 {
		t.Fatalf("git ls-files found no Go file in %v", preludeUnionDirs)
	}
	return out
}

// preludeUnionLiterals counts, in n of a file of dir, the string literals
// whose text is a preludeUnionNames name, and those whose text is a
// preludeFieldNames field of dir that a field accessor takes or that is
// compared with a .Name selector.
func preludeUnionLiterals(n ast.Node, dir string) int {
	count := 0
	text := func(e ast.Expr) (string, bool) {
		lit, ok := e.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(lit.Value)
		return s, err == nil
	}
	field := func(e ast.Expr) bool {
		s, ok := text(e)
		return ok && slices.Contains(preludeFieldNames[s], dir)
	}
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BasicLit:
			if s, ok := text(n); ok && slices.Contains(preludeUnionNames, s) {
				count++
			}
		case *ast.CallExpr:
			name := ""
			switch f := n.Fun.(type) {
			case *ast.Ident:
				name = f.Name
			case *ast.SelectorExpr:
				name = f.Sel.Name
			}
			if slices.Contains(preludeFieldAccessors, name) {
				for _, a := range n.Args {
					if field(a) {
						count++
					}
				}
			}
		case *ast.BinaryExpr:
			if (n.Op == token.EQL || n.Op == token.NEQ) && (nameSel(n.X) && field(n.Y) || nameSel(n.Y) && field(n.X)) {
				count++
			}
		}
		return true
	})
	return count
}

// preludeDeclName is a function's name (a method's as Recv.Name), or a
// package-level declaration's first name.
func preludeDeclName(d ast.Decl) string {
	switch d := d.(type) {
	case *ast.FuncDecl:
		if d.Recv != nil && len(d.Recv.List) == 1 {
			typ := d.Recv.List[0].Type
			if s, ok := typ.(*ast.StarExpr); ok {
				typ = s.X
			}
			if ix, ok := typ.(*ast.IndexExpr); ok {
				typ = ix.X
			}
			if id, ok := typ.(*ast.Ident); ok {
				return id.Name + "." + d.Name.Name
			}
		}
		return d.Name.Name
	case *ast.GenDecl:
		for _, s := range d.Specs {
			switch s := s.(type) {
			case *ast.ValueSpec:
				return s.Names[0].Name
			case *ast.TypeSpec:
				return s.Name.Name
			}
		}
	}
	return fmt.Sprintf("decl@%T", d)
}

// preludeUnionBaseline is testdata/prelude-unions.txt as the rule landed
// (`dir/file.go:function count`). A row may leave or count fewer; none may
// join or count more.
var preludeUnionBaseline = []string{
	"check/cells.go:optionParams 2",
	"check/decl.go:checker.resolveNamed 2",
	"check/entry.go:checker.machineAfterShape 2",
	"check/entry.go:checker.machineAfterEvery 1",
	"check/entry.go:IsEvery 2",
	"check/entry.go:EveryClock 1",
	"check/exit.go:checker.machineExitRow 1",
	"check/extern.go:optionElem 1",
	"check/goany.go:checker.goAnyAs 1",
	"check/goerror.go:checker.goErrorCall 1",
	"check/work.go:checker.machineWorkRow 1",
	"eval/machinevalue.go:interp.everyTimeout 4",
	"eval/machinevalue.go:interp.machineOption 2",
	"eval/machinevalue.go:optionOf 2",
	"eval/machinevalue.go:sigParam 1",
	"lower/decls.go:lowerer.intrinsic 1",
	"lower/decls.go:optionVariants 2",
	"lower/flatunion.go:flatUnion.flatNarrow 1",
	"lower/flatunion.go:flatUnion.flatValue 1",
	"lower/flatunion.go:funcCtx.optionSome 1",
	"lower/flatunion.go:lowerer.flatUnionSet 1",
	"lower/flatunion.go:lowerer.layoutOf 1",
	"lower/flatunion.go:lowerer.lowerAgain 1",
	"lower/flatunion.go:lowerer.nicheElem 1",
	"lower/genericunion.go:lowerer.genericUnions 1",
	"lower/getmatch.go:funcCtx.getArmsLower 2",
	"lower/getmatch.go:getPattern 4",
	"lower/getmatch.go:preludeGet 1",
	"lower/gocall.go:isOptionType 1",
	"lower/ir_get.go:irBuilder.getArm 2",
	"lower/ir_golib.go:irBuilder.goBuiltin 1",
	"lower/ir_golib.go:irBuilder.hostResult 1",
	"lower/ir_golib.go:irBuilder.nicheNarrow 1",
	"lower/ir_golib.go:irBuilder.nicheValue 1",
	"lower/ir_machine.go:irBuilder.machineAfter 4",
	"lower/ir_machine.go:irBuilder.machineOption 3",
	"lower/machine.go:funcCtx.everyTimers 2",
	"lower/script.go:funcCtx.scriptOperations 1",
	"lower/std.go:funcCtx.stdResult 1",
}
