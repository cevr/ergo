package selfhostcheck

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
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
// Info.Calls TypeArgs. Each package let's name, and each if, match and
// catch whose value is used, has the Go checker's type, none missed
// (CHECK-VALUE-TYPES, values_test.go). Any other expression the Ergo
// side leaves untyped is not compared: the run logs, by syntax form, how many of the Go checker's
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
		values          []goValue
		forms           map[string]string
	}
	// The Go checker's side of each folder, one folder at a time
	// (eachGoPackage); the files are written after, in corpus order.
	type goSide struct {
		ok              bool
		path            string
		want, wantCalls []string
		values          []goValue
		forms           map[string]string
		ext, foreign    []string
	}
	dirs := corpusDirs(t)
	sides := make([]goSide, len(dirs))
	eachGoPackage(dirs, func(i int, pkg *check.Package) {
		want, forms := goExprTypes(pkg, "")
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		values := goValues(pkg, "")
		sides[i] = goSide{ok: true, path: importPath(dirs[i], pkg), want: dropChargedTypes(want), wantCalls: goCallArgs(pkg, ""), values: values, forms: forms, ext: ext, foreign: foreign}
	})
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
		folders = append(folders, folder{dir: dirs[i], want: g.want, wantCalls: g.wantCalls, values: g.values, forms: g.forms})
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	gotTypes, gotCalls, gotDiags, gotOwn, gotFlow, gotLets := make([][]string, len(folders)), make([][]string, len(folders)), make([][]string, len(folders)), make([][]string, len(folders)), make([][]string, len(folders)), make([][]string, len(folders))
	gotOutside := make([]map[string]bool, len(folders))
	// The Ergo side's cause of each call it left untyped, by "FILE START
	// END" (exprdump's U lines).
	gotCauses := make([]map[string]string, len(folders))
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
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "O "):
			gotOwn[at] = append(gotOwn[at], l[2:])
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "U "):
			if f := strings.SplitN(l[2:], " ", 4); len(f) == 4 {
				if gotCauses[at] == nil {
					gotCauses[at] = map[string]string{}
				}
				gotCauses[at][strings.Join(f[:3], " ")] = f[3]
			}
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "W "):
			gotFlow[at] = append(gotFlow[at], l[2:])
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "L "):
			gotLets[at] = append(gotLets[at], l[2:])
		case at >= 0 && at < len(folders) && strings.HasPrefix(l, "X "):
			if gotOutside[at] == nil {
				gotOutside[at] = map[string]bool{}
			}
			gotOutside[at][l[2:]] = true
		}
	}
	if at != len(folders)-1 {
		t.Fatalf("exprdump -corpus wrote %d packages, want %d", at+1, len(folders))
	}
	reach := map[string][2]int{}
	missed := map[string][]string{} // a few untyped sites of each form
	// Each call form's untyped sites by the Ergo side's cause, logged and
	// never pinned (the reach grows by unit).
	causes := map[string]map[string]int{}
	kindNames := syntaxKindNames(t)
	calls := [2]int{}
	values := 0
	excluded := map[string]int{}
	for i, fo := range folders {
		got := dropChargedTypes(gotTypes[i])
		if d := subsetDiff(fo.want, got, 3); d != "" {
			t.Errorf("%s: expression types differ (go -, ergo +):\n%s", rel(fo.dir), d)
		}
		d, ex := valueDiff(fo.values, gotTypes[i], gotLets[i], gotOutside[i], fo.forms)
		if d != "" {
			t.Errorf("%s: value types differ (go -, ergo +):\n%s", rel(fo.dir), d)
		}
		values += len(fo.values)
		for c, n := range ex {
			excluded[c] += n
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
			} else {
				if strings.HasPrefix(form, "CallExpr ") {
					by := causes[form]
					if by == nil {
						by = map[string]int{}
						causes[form] = by
					}
					by[kindNames.Replace(cmp.Or(gotCauses[i][key], "no cause line"))]++
				}
				if len(missed[form]) < 8 {
					missed[form] = append(missed[form], rel(fo.dir)+"/"+key)
				}
			}
			reach[form] = r
			// The fixture writes each short form where its context fixes
			// it: every such site of it is typed.
			if group, _ := corpusGroup(fo.dir); group == "cases" && !have[key] && (form == "CompositeLit elided" || form == "Ident patbinder") {
				t.Errorf("%s/%s: the fixture's %s is untyped", rel(fo.dir), key, form)
			}
		}
		// The Go checker accepts every folder it types, so a diagnostic
		// of the Ergo side is a false one (S3d5).
		if len(gotDiags[i]) > 0 {
			t.Errorf("%s: the Go checker accepts the package, the Ergo side reports %d type errors: %s", rel(fo.dir), len(gotDiags[i]), strings.Join(gotDiags[i][:min(3, len(gotDiags[i]))], "; "))
		}
		// And no own-mark S108 (MODES-OWN-CHECK).
		if len(gotOwn[i]) > 0 {
			t.Errorf("%s: the Go checker accepts the package, the Ergo side reports %d own-mark errors: %s", rel(fo.dir), len(gotOwn[i]), strings.Join(gotOwn[i][:min(3, len(gotOwn[i]))], "; "))
		}
		// And no ownership flow S108 (OWN-WALK).
		if len(gotFlow[i]) > 0 {
			t.Errorf("%s: the Go checker accepts the package, the Ergo side reports %d ownership flow errors: %s", rel(fo.dir), len(gotFlow[i]), strings.Join(gotFlow[i][:min(3, len(gotFlow[i]))], "; "))
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
	type count struct {
		form, cause string
		n           int
	}
	var counts []count
	for form, by := range causes {
		for cause, n := range by {
			counts = append(counts, count{form, cause, n})
		}
	}
	slices.SortFunc(counts, func(a, b count) int {
		return cmp.Or(strings.Compare(a.form, b.form), cmp.Compare(b.n, a.n), strings.Compare(a.cause, b.cause))
	})
	var byCause []string
	for _, c := range counts {
		byCause = append(byCause, fmt.Sprintf("%-20s %6d  %s", c.form, c.n, c.cause))
	}
	t.Logf("untyped calls by the Ergo side's cause (exprdump U lines):\n%s", strings.Join(byCause, "\n"))
	// The excluded values by cause, logged and never pinned: each value
	// under one cause, so the lines add up to the total.
	var byExcluded []string
	total := 0
	for c, n := range excluded {
		total += n
		byExcluded = append(byExcluded, fmt.Sprintf("%7d %s", n, c))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(byExcluded)))
	t.Logf("typed %d of %d expressions in %d folders; inferred %d of %d generic calls; %d package lets and value ifs, matches and catches, %d excluded:\n%s\n%s", all[0], all[1], len(folders), calls[0], calls[1], values, total, strings.Join(byExcluded, "\n"), strings.Join(lines, "\n"))
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
	// len (S3d5), and a tuple a result's type fixes (TUPLE-WANT): each
	// kind typed somewhere, since the subset check alone would not see
	// one lost.
	for _, kind := range []string{"CallExpr method", "CallExpr prelude", "CallExpr op", "CallExpr capability", "CallExpr preludecap", "SelectorExpr ptrfield", "CallExpr builtin", "TupleExpr"} {
		if reach[kind][0] == 0 {
			t.Errorf("the Ergo side typed no %s", kind)
		}
	}
	// Short spellings (OPTION-ONE): a site the Ergo side leaves untyped is
	// not compared, so typing the short form less often than the rest
	// would silently shrink the agreement as the corpus moves to it.
	for _, lag := range shortFormLags(reach, all) {
		t.Errorf("%s; untyped short-form sites include: elided %s; patbinder %s", lag, strings.Join(missed["CompositeLit elided"], ", "), strings.Join(missed["Ident patbinder"], ", "))
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
// syntaxKindNames turns exprdump's #N syntax kinds into compiler/syntax's
// names, read from its K… lets in tree.ergo (the one place that names
// them).
func syntaxKindNames(t *testing.T) *strings.Replacer {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(), "compiler", "syntax", "tree.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	ms := regexp.MustCompile(`(?m)^let K(\w+) = uint8\((\d+)\)$`).FindAllStringSubmatch(string(src), -1)
	// The Replacer tries its pairs in order: the longer number first, so
	// #3 never matches the start of #34.
	slices.SortFunc(ms, func(a, b []string) int { return cmp.Compare(len(b[2]), len(a[2])) })
	var pairs []string
	for _, m := range ms {
		pairs = append(pairs, "#"+m[2], m[1])
	}
	return strings.NewReplacer(pairs...)
}

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

// goExprTypes is the Go checker's Info.Types of pkg's function bodies and
// package lets' values as
// "FILE START END TYPE" lines, and each line's key ("FILE START END")
// mapped to its syntax form.
func goExprTypes(pkg *check.Package, unnamed string) ([]string, map[string]string) {
	var out []string
	forms := map[string]string{}
	binders := patternBinders(pkg)
	for sf, name := range pkgFiles(pkg, unnamed) {
		syntax.Inspect(sf, func(n syntax.Node) bool {
			var body syntax.Node
			switch d := n.(type) {
			case *syntax.FuncDecl:
				if d.Recv == nil && d.Body != nil {
					body = d.Body
				}
			case *syntax.LawDecl:
				body = d.Body
			case *syntax.TestDecl:
				body = d.Body
			case *syntax.VarDecl:
				// A package let's value (CHECK-VALUE-TYPES).
				body = d
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
				if id, ok := x.(*syntax.Ident); ok && binders[pkg.Info.Uses[id]] != "" {
					forms[key] = "Ident " + binders[pkg.Info.Uses[id]]
				}
				return true
			})
			return false
		})
	}
	return out, forms
}

// patternBinders is each variable a case's or catch arm's pattern
// declares: "patbinder" for a name a record pattern's field binds
// (`case Some{Value: v}`), "asbinder" for an as-pattern's name (`case
// Some as s`).
func patternBinders(pkg *check.Package) map[check.Object]string {
	out := map[check.Object]string{}
	var walk func(x syntax.Expr)
	walk = func(x syntax.Expr) {
		switch p := x.(type) {
		case *syntax.AsPattern:
			if o := pkg.Info.Defs[p.Name]; o != nil {
				out[o] = "asbinder"
			}
			walk(p.X)
		case *syntax.CompositeLit:
			for _, el := range p.Elts {
				kv, ok := el.(*syntax.KeyValueExpr)
				if !ok {
					continue
				}
				if id, ok := kv.Value.(*syntax.Ident); ok {
					if o := pkg.Info.Defs[id]; o != nil {
						out[o] = "patbinder"
					}
					continue
				}
				walk(kv.Value)
			}
		}
	}
	for sf := range pkgFiles(pkg, "") {
		syntax.Inspect(sf, func(n syntax.Node) bool {
			switch c := n.(type) {
			case *syntax.CaseClause:
				for _, x := range c.List {
					walk(x)
				}
			case *syntax.CatchArm:
				for _, x := range c.Types {
					walk(x)
				}
			}
			return true
		})
	}
	return out
}

// shortFormLags is the guard against a short spelling the Ergo side types
// less often than the corpus it stands in: its share of typed sites must
// not fall below its counterpart's in the same run. A field pattern's
// binder is held to an as-pattern's. An elided literal is held to the
// whole run's expressions, not to the generic literals written with their
// arguments: those are typed wherever they stand (the written type), an
// elided one only where the typer reads its context, and the contexts it
// does not read yet (an operation's argument, a constant beside an
// untyped operand of ==) are the expression typer's gaps, not the spelling's. all is
// the run's typed and total expressions. A short form the run does not
// reach is a failure too: the fixture testdata/bind/elide.ergo writes
// each.
func shortFormLags(reach map[string][2]int, all [2]int) []string {
	var out []string
	reach = maps.Clone(reach)
	reach["all expressions"] = all
	for _, p := range [][2]string{{"CompositeLit elided", "all expressions"}, {"Ident patbinder", "Ident asbinder"}} {
		short, long := reach[p[0]], reach[p[1]]
		switch {
		case short[1] == 0 || long[1] == 0:
			out = append(out, fmt.Sprintf("the corpus reaches no %q or no %q site", p[0], p[1]))
		case short[0]*long[1] < long[0]*short[1]:
			// short[0]/short[1] < long[0]/long[1], without division.
			out = append(out, fmt.Sprintf("%s typed %d of %d, below %s's %d of %d", p[0], short[0], short[1], p[1], long[0], long[1]))
		}
	}
	return out
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
	case *syntax.CompositeLit:
		// A generic record or variant literal, written with its type
		// arguments or without them (the one spelling, OPTION-ONE).
		if n, ok := pkg.Info.Types[x].(*check.Named); ok && len(n.TArgs) > 0 && e.Type != nil {
			if _, written := unparenX(e.Type).(*syntax.IndexExpr); written {
				return form + " generic"
			}
			return form + " elided"
		}
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
// the capability named ("capability"; "preludecap" for a prelude
// operation type's, Random.IntN), the prelude's
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
					if n, ok := xt.(*check.Named); ok && n.Obj != nil && n.Obj.Pkg != nil && n.Obj.Pkg.Path == "prelude" {
						return "preludecap"
					}
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

// importPath keeps a checked package's resolved identity, even when its
// source lies under another module. Only a package checked as a program
// root has the synthetic path main; recover its import path from its module.
func importPath(dir string, pkg *check.Package) string {
	return sourceImportPath(dir, pkg.Path)
}

func sourceImportPath(dir, path string) string {
	if path != "main" {
		return path
	}
	for root := dir; root != filepath.Dir(root); root = filepath.Dir(root) {
		mod, err := os.ReadFile(filepath.Join(root, "ergo.mod"))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(mod), "\n") {
			if name, ok := strings.CutPrefix(strings.TrimSpace(line), "module "); ok {
				r, err := filepath.Rel(root, dir)
				if err != nil {
					return path
				}
				if r == "." {
					return strings.TrimSpace(name)
				}
				return strings.TrimSpace(name) + "/" + filepath.ToSlash(r)
			}
		}
		break
	}
	return path
}
