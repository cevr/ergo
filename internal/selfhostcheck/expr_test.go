package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerExprLawsRun runs the laws of compiler/check/expr (capstone
// S3d) through `ergo test`: the counted guard
// TyperWorkPerFunctionIsConstant.
//
// It builds Go because `ergo test` lowers the laws into a test program and
// runs it: the check-time evaluator runs an operation type's laws, not a
// package's top-level laws, so no in-process check runs these.
func TestCompilerExprLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/expr")
}

// TestCompilerExprAgreesWithChecker is S3d's expression agreement: over
// every package folder of examples/ and std the Go checker checks, each
// expression compiler/check/expr types (cmd/exprdump) has the type the Go
// checker's Info.Types gives it, printed as its Type.String; and each
// generic call it infers (exprdump -calls) has the Go checker's
// Info.Calls TypeArgs. An expression the Ergo side leaves untyped is not
// compared: the run logs, by syntax form, how many of the Go checker's
// typed expressions the Ergo side types. The types of other packages come
// from the Go side, as in the types agreement.
//
// It builds Go because exprdump is the Ergo program under test:
// compiler/check/expr is Ergo source, and only its lowered, built form
// runs over a corpus.
func TestCompilerExprAgreesWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := exprDump(t, ctx)
	tmp := t.TempDir()
	e, f := filepath.Join(tmp, "ext"), filepath.Join(tmp, "foreign")
	reach := map[string][2]int{}
	calls := [2]int{}
	folders := 0
	for _, dir := range corpusDirs(t) {
		pkg := typesPackage(dir)
		if pkg == nil {
			continue
		}
		folders++
		want, forms := goExprTypes(pkg, "")
		wantCalls := goCallArgs(pkg, "")
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		files := append(ergoFiles(t, dir), depArgs(t, tmp, dir, pkg)...)
		want = dropChargedTypes(want)
		got := dropChargedTypes(runDump(t, ctx, bin, append([]string{e, f}, files...)...))
		if d := subsetDiff(want, got, 3); d != "" {
			t.Errorf("%s: expression types differ (go -, ergo +):\n%s", rel(dir), d)
		}
		have := map[string]bool{}
		for _, l := range got {
			have[keyOf(l, 3)] = true
		}
		for key, form := range forms {
			r := reach[form]
			r[1]++
			if have[key] {
				r[0]++
			}
			reach[form] = r
		}
		gotCalls := runDump(t, ctx, bin, append([]string{"-calls", e, f}, files...)...)
		if d := subsetDiff(wantCalls, gotCalls, 2); d != "" {
			t.Errorf("%s: inferred type arguments differ (go -, ergo +):\n%s", rel(dir), d)
		}
		calls[0] += len(gotCalls)
		calls[1] += len(wantCalls)
	}
	var lines []string
	all := [2]int{}
	for form, r := range reach {
		all[0] += r[0]
		all[1] += r[1]
		lines = append(lines, fmt.Sprintf("%-28s %7d of %7d", form, r[0], r[1]))
	}
	sort.Strings(lines)
	t.Logf("typed %d of %d expressions in %d folders; inferred %d of %d generic calls:\n%s", all[0], all[1], folders, calls[0], calls[1], strings.Join(lines, "\n"))
	if all[0] == 0 || calls[0] == 0 {
		t.Fatal("the Ergo side typed no expression or inferred no call")
	}
	// The imports' members are typed from their packages' stores (S3d2):
	// a run that types none has lost them, which the subset check alone
	// would not see.
	if reach["SelectorExpr pkg"][0] == 0 || reach["CallExpr pkg"][0] == 0 {
		t.Fatal("the Ergo side typed no member of an import")
	}
}

// exprDump builds compiler/cmd/exprdump.
func exprDump(t *testing.T, ctx context.Context) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "exprdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/exprdump"); err != nil {
		t.Fatalf("ergo build ./cmd/exprdump: %v\n%s", err, o)
	}
	return bin
}

// dropChargedTypes drops go.Foreign from the function types of "FILE
// START END TYPE" lines, as dropCharged does from signatures: the body
// check charges it to a function whose body calls Go, which this pass
// does not compute.
func dropChargedTypes(ls []string) []string {
	out := make([]string, len(ls))
	for i, l := range ls {
		if strings.Contains(l, "func(") {
			for _, c := range []string{"go.Foreign | ", " | go.Foreign", " needs go.Foreign"} {
				l = strings.ReplaceAll(l, c, "")
			}
		}
		out[i] = l
	}
	return out
}

// keyOf is a line's first n fields: the expression or call it names.
func keyOf(l string, n int) string {
	fs := strings.SplitN(l, " ", n+1)
	if len(fs) < n {
		return l
	}
	return strings.Join(fs[:n], " ")
}

// subsetDiff is the lines of got whose key (first n fields) want has with
// another text, or does not have: the Ergo side may leave an expression
// out, never type it otherwise or type what the Go checker does not.
func subsetDiff(want, got []string, n int) string {
	byKey := map[string]string{}
	for _, l := range want {
		byKey[keyOf(l, n)] = l
	}
	var b strings.Builder
	for _, l := range got {
		w, ok := byKey[keyOf(l, n)]
		switch {
		case !ok:
			fmt.Fprintf(&b, "+ %s (no Go entry)\n", l)
		case w != l:
			fmt.Fprintf(&b, "- %s\n+ %s\n", w, l)
		}
	}
	return b.String()
}

// pkgFiles is pkg's files, by their syntax.
func pkgFiles(pkg *check.Package, unnamed string) map[*syntax.File]string {
	out := map[*syntax.File]string{}
	files := fieldOf(reflect.ValueOf(pkg), "files")
	for i := 0; i < files.Len(); i++ {
		fi := files.Index(i)
		if fi.IsNil() {
			continue
		}
		sf, _ := fieldOf(fi, "syntax").Interface().(*syntax.File)
		if sf == nil {
			continue
		}
		name := unnamed
		if sf.Name != "" {
			name = filepath.Base(sf.Name)
		}
		out[sf] = name
	}
	return out
}

// goExprTypes is the Go checker's Info.Types of pkg's function bodies as
// "FILE START END TYPE" lines, and each line's key ("FILE START END")
// mapped to its syntax form.
func goExprTypes(pkg *check.Package, unnamed string) ([]string, map[string]string) {
	var out []string
	forms := map[string]string{}
	for sf, name := range pkgFiles(pkg, unnamed) {
		syntax.Inspect(sf, func(n syntax.Node) bool {
			var body *syntax.BlockStmt
			switch d := n.(type) {
			case *syntax.FuncDecl:
				if d.Recv == nil {
					body = d.Body
				}
			case *syntax.LawDecl:
				body = d.Body
			case *syntax.TestDecl:
				body = d.Body
			default:
				return true
			}
			if body == nil {
				return false
			}
			syntax.Inspect(body, func(n syntax.Node) bool {
				x, ok := n.(syntax.Expr)
				if !ok {
					return true
				}
				ty, ok := pkg.Info.Types[x]
				if !ok || ty == nil {
					return true
				}
				key := fmt.Sprintf("%s %d %d", name, x.Pos(), x.End())
				out = append(out, key+" "+ty.String())
				forms[key] = formOf(pkg, x)
				return true
			})
			return false
		})
	}
	return out, forms
}

// goCallArgs is the Go checker's type arguments of each call by name to a
// generic function of pkg, "FILE OFFSET T1 ; T2 …".
func goCallArgs(pkg *check.Package, unnamed string) []string {
	var out []string
	for sf, name := range pkgFiles(pkg, unnamed) {
		syntax.Inspect(sf, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			c := pkg.Info.Calls[x]
			if c == nil || c.Func == nil || len(c.TypeArgs) == 0 {
				return true
			}
			fun := unparenX(x.Fun)
			if ix, ok := fun.(*syntax.IndexExpr); ok {
				fun = unparenX(ix.X)
			}
			// A function of the package by its name, or of an import
			// by pkg.F's name (S3d2).
			id, ok := fun.(*syntax.Ident)
			if ok && c.Func.Pkg != pkg {
				return true
			}
			if se, isSel := fun.(*syntax.SelectorExpr); isSel && selectorKind(pkg, se) == "pkg" {
				id, ok = se.Sel, true
			}
			if !ok {
				return true
			}
			ts := make([]string, len(c.TypeArgs))
			for i, a := range c.TypeArgs {
				ts[i] = a.String()
			}
			out = append(out, fmt.Sprintf("%s %d %s", name, id.Pos(), strings.Join(ts, " ; ")))
			return true
		})
	}
	return out
}

// formOf is x's syntax form for the reach log; a selector and a call say
// what they name, so the log shows which gap each miss is in: "pkg" a
// member of an import, "field" or "method" one of a value, and a call's
// callee "own" (a function of the package), "pkg", "method", "builtin",
// "value" (a function value) or "conv" (a conversion).
func formOf(pkg *check.Package, x syntax.Expr) string {
	form := strings.TrimPrefix(fmt.Sprintf("%T", x), "*syntax.")
	switch e := x.(type) {
	case *syntax.SelectorExpr:
		return form + " " + selectorKind(pkg, e)
	case *syntax.CallExpr:
		fun := unparenX(e.Fun)
		if ix, ok := fun.(*syntax.IndexExpr); ok {
			fun = unparenX(ix.X)
		}
		switch f := fun.(type) {
		case *syntax.SelectorExpr:
			return form + " " + selectorKind(pkg, f)
		case *syntax.Ident:
			switch o := pkg.Info.Uses[f].(type) {
			case *check.Func:
				if o.Pkg == pkg {
					return form + " own"
				}
				return form + " pkg"
			case *check.Builtin:
				return form + " builtin"
			case *check.TypeName:
				return form + " conv"
			}
			return form + " value"
		}
		return form + " value"
	}
	return form
}

// selectorKind is what the selector e names: "pkg", "field", "method" or
// "other".
func selectorKind(pkg *check.Package, e *syntax.SelectorExpr) string {
	if id, ok := unparenX(e.X).(*syntax.Ident); ok {
		if _, ok := pkg.Info.Uses[id].(*check.PkgName); ok {
			return "pkg"
		}
	}
	switch pkg.Info.Uses[e.Sel].(type) {
	case *check.Var:
		return "field"
	case *check.Func:
		return "method"
	}
	return "other"
}

// depArgs is exprdump's groups for pkg's imports, each
// "-- EXT FOREIGN FILE...", the tables written under tmp: every package
// pkg names through an import whose folder is in the repository (std, or
// the module of dir).
func depArgs(t *testing.T, tmp, dir string, pkg *check.Package) []string {
	var deps []*check.Package
	seen := map[*check.Package]bool{}
	for _, o := range pkg.Info.Uses {
		pn, ok := o.(*check.PkgName)
		if !ok || pn.Pkg == nil || pn.Pkg == pkg || seen[pn.Pkg] {
			continue
		}
		seen[pn.Pkg] = true
		deps = append(deps, pn.Pkg)
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].Path < deps[j].Path })
	var out []string
	for i, d := range deps {
		ddir := depDir(dir, d.Path)
		if ddir == "" {
			continue
		}
		files := ergoFiles(t, ddir)
		if len(files) == 0 {
			continue
		}
		_, ext := goKinds(d)
		_, foreign := goTypes(d)
		e, f := filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, e, ext)
		writeLines(t, f, foreign)
		out = append(append(out, "--", e, f), files...)
	}
	return out
}

// depDir is the folder of the package at import path path, imported from
// dir: a package of dir's module (the nearest ergo.mod above it), or of
// std; "" when neither holds it.
func depDir(dir, path string) string {
	for root := dir; root != filepath.Dir(root); root = filepath.Dir(root) {
		mod, err := os.ReadFile(filepath.Join(root, "ergo.mod"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(mod), "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				if rest, ok := strings.CutPrefix(path, strings.TrimSpace(name)+"/"); ok {
					if st, err := os.Stat(filepath.Join(root, rest)); err == nil && st.IsDir() {
						return filepath.Join(root, rest)
					}
				}
			}
		}
		break
	}
	std := filepath.Join(repoRoot(), "internal", "sketch", "std", filepath.FromSlash(path))
	if st, err := os.Stat(std); err == nil && st.IsDir() {
		return std
	}
	return ""
}
