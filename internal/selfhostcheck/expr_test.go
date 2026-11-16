package selfhostcheck

import (
	"context"
	"fmt"
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
		files := ergoFiles(t, dir)
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
				forms[key] = strings.TrimPrefix(fmt.Sprintf("%T", x), "*syntax.")
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
			if c == nil || c.Func == nil || len(c.TypeArgs) == 0 || c.Func.Pkg != pkg {
				return true
			}
			fun := unparenX(x.Fun)
			if ix, ok := fun.(*syntax.IndexExpr); ok {
				fun = unparenX(ix.X)
			}
			id, ok := fun.(*syntax.Ident)
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
