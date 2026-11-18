package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"sync"
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
// typed expressions the Ergo side types. The Go side writes the
// prelude's, the universe's and the Go imports' types; an Ergo import's
// types are XRefs into its interface, which exprdump computes from the
// import's source (a corpus package, else -std), as in the types
// agreement.
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
	// One manifest line per folder, and one exprdump run over them all:
	// the Ergo side binds the packages in import waves and loads each once,
	// so a folder's imports are the corpus's own packages (S3d3).
	type folder struct {
		dir             string
		want, wantCalls []string
		forms           map[string]string
	}
	// The Go checker's side of each folder, the folders on GOMAXPROCS
	// workers (the checker and its std importer are safe for concurrent
	// use); the files are written after, in corpus order.
	type goSide struct {
		ok              bool
		path            string
		want, wantCalls []string
		forms           map[string]string
		ext, foreign    []string
	}
	dirs := corpusDirs(t)
	sides := make([]goSide, len(dirs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range next {
				pkg := typesPackage(dirs[i])
				if pkg == nil {
					continue
				}
				want, forms := goExprTypes(pkg, "")
				_, ext := goKinds(pkg)
				_, foreign := goTypes(pkg)
				sides[i] = goSide{ok: true, path: importPath(dirs[i], pkg), want: dropChargedTypes(want), wantCalls: goCallArgs(pkg, ""), forms: forms, ext: ext, foreign: foreign}
			}
		})
	}
	for i := range dirs {
		next <- i
	}
	close(next)
	wg.Wait()
	var folders []folder
	var manifest []string
	for i, g := range sides {
		if !g.ok {
			continue
		}
		n := len(folders)
		e, f := filepath.Join(tmp, fmt.Sprintf("ext%d", n)), filepath.Join(tmp, fmt.Sprintf("foreign%d", n))
		writeLines(t, e, g.ext)
		writeLines(t, f, g.foreign)
		manifest = append(manifest, strings.Join(append([]string{g.path, e, f}, ergoFiles(t, dirs[i])...), " "))
		folders = append(folders, folder{dir: dirs[i], want: g.want, wantCalls: g.wantCalls, forms: g.forms})
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	gotTypes, gotCalls, gotDiags := make([][]string, len(folders)), make([][]string, len(folders)), make([][]string, len(folders))
	at := -1
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-corpus", m) {
		switch {
		case strings.HasPrefix(l, "@pkg "):
			at++
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "E "):
			gotTypes[at] = append(gotTypes[at], l[2:])
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "C "):
			gotCalls[at] = append(gotCalls[at], l[2:])
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "D "):
			gotDiags[at] = append(gotDiags[at], l[2:])
		}
	}
	if at != len(folders)-1 {
		t.Fatalf("exprdump -corpus wrote %d packages, want %d", at+1, len(folders))
	}
	reach := map[string][2]int{}
	calls := [2]int{}
	for i, fo := range folders {
		got := dropChargedTypes(gotTypes[i])
		if d := subsetDiff(fo.want, got, 3); d != "" {
			t.Errorf("%s: expression types differ (go -, ergo +):\n%s", rel(fo.dir), d)
		}
		have := map[string]bool{}
		for _, l := range got {
			have[keyOf(l, 3)] = true
		}
		for key, form := range fo.forms {
			r := reach[form]
			r[1]++
			if have[key] {
				r[0]++
			}
			reach[form] = r
		}
		// The Go checker accepts every folder it types, so a diagnostic
		// of the Ergo side is a false one (S3d5).
		if len(gotDiags[i]) > 0 {
			t.Errorf("%s: the Go checker accepts the package, the Ergo side reports %d type errors: %s", rel(fo.dir), len(gotDiags[i]), strings.Join(gotDiags[i][:min(3, len(gotDiags[i]))], "; "))
		}
		if d := subsetDiff(fo.wantCalls, gotCalls[i], 2); d != "" {
			t.Errorf("%s: inferred type arguments differ (go -, ergo +):\n%s", rel(fo.dir), d)
		}
		calls[0] += len(gotCalls[i])
		calls[1] += len(fo.wantCalls)
	}
	var lines []string
	all := [2]int{}
	for form, r := range reach {
		all[0] += r[0]
		all[1] += r[1]
		lines = append(lines, fmt.Sprintf("%-28s %7d of %7d", form, r[0], r[1]))
	}
	sort.Strings(lines)
	t.Logf("typed %d of %d expressions in %d folders; inferred %d of %d generic calls:\n%s", all[0], all[1], len(folders), calls[0], calls[1], strings.Join(lines, "\n"))
	if all[0] == 0 || calls[0] == 0 {
		t.Fatal("the Ergo side typed no expression or inferred no call")
	}
	// The imports' members are typed from their packages' stores (S3d2):
	// a run that types none has lost them, which the subset check alone
	// would not see.
	if reach["SelectorExpr pkg"][0] == 0 || reach["CallExpr pkg"][0] == 0 {
		t.Fatal("the Ergo side typed no member of an import")
	}
	// Functions on a value's type, the prelude's on slices and Maps,
	// operations, and fields through a pointer (S3d3), and the builtin
	// len (S3d5): each kind typed
	// somewhere, since the subset check alone would not see one lost.
	for _, kind := range []string{"CallExpr method", "CallExpr prelude", "CallExpr op", "CallExpr capability", "SelectorExpr ptrfield", "CallExpr builtin"} {
		if reach[kind][0] == 0 {
			t.Errorf("the Ergo side typed no %s", kind)
		}
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
			// A function of the package by its name, of an import by
			// pkg.F's name (S3d2), or on x's type by x.f's (S3d3).
			id, ok := fun.(*syntax.Ident)
			if ok && c.Func.Pkg != pkg {
				return true
			}
			if se, isSel := fun.(*syntax.SelectorExpr); isSel {
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

// selectorKind is what the selector e names: "pkg", "field", a field
// through a pointer ("ptrfield"), an operation on a value ("op") or on
// the capability named ("capability"), the prelude's
// function on a slice or Map ("prelude"), another function on the value's
// type ("method"), or "other".
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
	// The checker records no use of a field's or method's name: a
	// selector of a function type on a value with no field of the name
	// calls a function on the value's type or an operation (S3d3).
	xt := pkg.Info.Types[e.X]
	if hasField(xt, e.Sel.Name) {
		if _, ok := xt.(*check.Pointer); ok {
			return "ptrfield"
		}
		return "other"
	}
	if _, isSig := pkg.Info.Types[e].(*check.Signature); isSig {
		u := xt
		if n, ok := u.(*check.Named); ok {
			u = n.Underlying()
		}
		switch u.(type) {
		case *check.Interface:
			// The capability itself named as the receiver
			// (Console.Println, io.Stdout.Print), or a value of the
			// operation type. The checker binds a needed capability's
			// name to its unexported capBinding, and records no use of
			// an import's capability (pkg.C).
			switch x := unparenX(e.X).(type) {
			case *syntax.Ident:
				if fmt.Sprintf("%T", pkg.Info.Uses[x]) == "*check.capBinding" {
					return "capability"
				}
			case *syntax.SelectorExpr:
				if selectorKind(pkg, x) == "pkg" {
					return "capability"
				}
			}
			return "op"
		case *check.Slice, *check.Map:
			return "prelude"
		}
		return "method"
	}
	return "other"
}

// hasField is whether a value of type t, or of the type a pointer t
// points to, has the field name.
func hasField(t check.Type, name string) bool {
	if p, ok := t.(*check.Pointer); ok {
		t = p.Elem
	}
	if n, ok := t.(*check.Named); ok {
		t = n.Underlying()
	}
	st, ok := t.(*check.Struct)
	if !ok {
		return false
	}
	for _, f := range st.Fields {
		if f.Name == name {
			return true
		}
	}
	return false
}

// importPath is the path the folder dir's package is imported by: its
// module's name and its folder in the module (the nearest ergo.mod above
// dir), or the package's own path (std, or a folder with no module). A
// package checked as a root has the path main, which no import names.
func importPath(dir string, pkg *check.Package) string {
	for root := dir; root != filepath.Dir(root); root = filepath.Dir(root) {
		mod, err := os.ReadFile(filepath.Join(root, "ergo.mod"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(mod), "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				r, err := filepath.Rel(root, dir)
				if err != nil {
					return pkg.Path
				}
				if r == "." {
					return strings.TrimSpace(name)
				}
				return strings.TrimSpace(name) + "/" + filepath.ToSlash(r)
			}
		}
		break
	}
	return pkg.Path
}
