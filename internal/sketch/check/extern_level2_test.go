package check_test

import (
	"flag"
	"fmt"
	"go/ast"
	"go/build"
	"go/parser"
	"go/printer"
	"go/token"
	"math/rand/v2"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std/gostd"
	v1 "ergo.local/prototype/runtime/v1"
)

var level2Seed = flag.Uint64("level2.seed", 0, "run TestLevel2AdapterRelation on this seed alone")

// level2Packages are the Go packages whose functions the property draws:
// both tiers (strconv, strings, path/filepath with its host members,
// unicode/utf8, math; net/url, flag, os, regexp, html, mime), pointer
// results with and without an error, (T, bool) results and variadics.
var level2Packages = []string{"strconv", "strings", "path/filepath", "unicode/utf8", "math", "net/url", "flag", "os", "regexp", "html", "mime"}

// goTarget is a Go function and its Level 1 signature, written by the
// reference below straight from Go's declaration (docs/spec/interop.md,
// Type mapping), independently of the bindings the checker reads.
type goTarget struct {
	path, name, goText string
	params             []string // Ergo type texts
	vals               []string // Go results but a last error, mapped
	errs, foreign      bool
}

// level2Ref maps a Go type expression of package pn to the Ergo type text
// of Level 1, or "" when the reference does not cover it (the function is
// then not drawn). result says a result position, errs that Go's last
// result is error.
func level2Ref(e ast.Expr, pn string, structs map[string]bool, result, errs bool) string {
	switch e := e.(type) {
	case *ast.Ident:
		switch e.Name {
		case "bool", "string", "int", "int64", "uint64", "float64", "rune":
			return e.Name
		}
	case *ast.ArrayType:
		if e.Len == nil {
			if inner := level2Ref(e.Elt, pn, structs, false, false); inner != "" && !strings.Contains(inner, ".") {
				return "[]" + inner
			}
		}
	case *ast.StarExpr:
		id, ok := e.X.(*ast.Ident)
		if !ok || !structs[id.Name] {
			return ""
		}
		switch {
		case !result:
			return "*" + pn + "." + id.Name
		case errs:
			return "go.Ref[" + pn + "." + id.Name + "]"
		}
		return "Option[go.Ref[" + pn + "." + id.Name + "]]"
	}
	return ""
}

// level2Targets reads the exported, non-generic functions of path from the
// toolchain's sources whose types the reference maps, and that the
// embedded binding imports (a declaration it refuses, as platform-specific,
// is not drawn).
func level2Targets(t *testing.T, path string) []goTarget {
	t.Helper()
	p, err := build.Default.Import(path, "", 0)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	binding, ok := gostd.Binding(path)
	if !ok {
		t.Fatalf("no binding for %s", path)
	}
	pn := check.GoPackageName(path)
	fset := token.NewFileSet()
	var files []*ast.File
	structs := map[string]bool{}
	for _, name := range p.GoFiles {
		f, err := parser.ParseFile(fset, filepath.Join(p.Dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok {
				for _, s := range gd.Specs {
					if ts, ok := s.(*ast.TypeSpec); ok && ts.Name.IsExported() && ts.TypeParams == nil {
						if _, ok := ts.Type.(*ast.StructType); ok {
							structs[ts.Name.Name] = true
						}
					}
				}
			}
		}
	}
	var out []goTarget
	for _, f := range files {
	decls:
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.TypeParams != nil ||
				!strings.Contains(binding, "\nextern go "+path+"."+fd.Name.Name+" func ") {
				continue
			}
			tg := goTarget{path: path, name: fd.Name.Name}
			tg.foreign = !slices.Contains(v1.PureImports, path) || slices.Contains(v1.PureImportsExcept[path], fd.Name.Name)
			var results []ast.Expr
			if fd.Type.Results != nil {
				for _, r := range fd.Type.Results.List {
					for range max(1, len(r.Names)) {
						results = append(results, r.Type)
					}
				}
			}
			if n := len(results); n > 0 {
				if id, ok := results[n-1].(*ast.Ident); ok && id.Name == "error" {
					tg.errs, results = true, results[:n-1]
				}
			}
			for _, prm := range fd.Type.Params.List {
				typ := prm.Type
				if el, ok := typ.(*ast.Ellipsis); ok {
					typ = &ast.ArrayType{Elt: el.Elt}
				}
				m := level2Ref(typ, pn, structs, false, false)
				if m == "" {
					continue decls
				}
				for range max(1, len(prm.Names)) {
					tg.params = append(tg.params, m)
				}
			}
			for _, r := range results {
				m := level2Ref(r, pn, structs, true, tg.errs)
				if m == "" {
					continue decls
				}
				tg.vals = append(tg.vals, m)
			}
			// Only (*S, error) promises a value with a nil error: beside
			// other results, a pointer is nullable again (os.Pipe).
			for i, v := range tg.vals {
				if len(tg.vals) > 1 && strings.HasPrefix(v, "go.Ref[") {
					tg.vals[i] = "Option[" + v + "]"
				}
			}
			tg.goText = fmt.Sprintf("%s.%s%s", path, fd.Name.Name, goSigText(fset, fd))
			out = append(out, tg)
		}
	}
	return out
}

func goSigText(fset *token.FileSet, fd *ast.FuncDecl) string {
	var b strings.Builder
	printer.Fprint(&b, fset, fd.Type)
	return strings.TrimPrefix(b.String(), "func")
}

// written is a generated user extern signature.
type written struct {
	params, results []string
	fails, needs    bool
}

func (w written) text(name, target string) string {
	var ps []string
	for i, p := range w.params {
		ps = append(ps, fmt.Sprintf("p%d %s", i, p))
	}
	s := "extern go " + target + " func " + name + "(" + strings.Join(ps, ", ") + ")"
	switch len(w.results) {
	case 0:
	case 1:
		s += " " + w.results[0]
	default:
		s += " (" + strings.Join(w.results, ", ") + ")"
	}
	if w.fails {
		s += " fails go.Error"
	}
	if w.needs {
		s += " needs go.Foreign"
	}
	return s
}

// level2Admits is the reference relation of the Level 2 adapters
// (docs/spec/interop.md, Level 2 adapters; plan §1.6), written from the
// table: identical parameters, the binding's tier row, and one of Same,
// Non-nil, Option or Partial for the results and the fails row.
func level2Admits(g goTarget, w written) bool {
	if !slices.Equal(g.params, w.params) || w.needs != g.foreign {
		return false
	}
	inner := func(s string) (string, bool) {
		if rest, ok := strings.CutPrefix(s, "Option["); ok && strings.HasSuffix(rest, "]") {
			return rest[:len(rest)-1], true
		}
		return "", false
	}
	switch {
	case slices.Equal(w.results, g.vals) && w.fails == g.errs:
		return true // Same
	case w.fails == g.errs && len(g.vals) == 1 && len(w.results) == 1:
		in, ok := inner(g.vals[0])
		return ok && in == w.results[0] // Non-nil
	case w.fails == g.errs && len(g.vals) == 2 && g.vals[1] == "bool" && len(w.results) == 1:
		in, ok := inner(w.results[0])
		return ok && in == g.vals[0] // Option
	case g.errs && !w.fails && len(w.results) == len(g.vals)+1 && slices.Equal(w.results[:len(g.vals)], g.vals) && w.results[len(g.vals)] == "Option[go.Error]":
		for _, v := range g.vals {
			if strings.HasPrefix(v, "go.Ref[") {
				return false // a nil pointer beside an error
			}
		}
		return true // Partial
	}
	return false
}

// level2Write is a written signature drawn from g's Level 1 signature: one
// of the adapters, or a near miss of one (a parameter, a row or a result
// changed), so the relation is probed on both sides.
func level2Write(r *rand.Rand, g goTarget) written {
	w := written{params: slices.Clone(g.params), results: slices.Clone(g.vals), fails: g.errs, needs: g.foreign}
	others := []string{"string", "int", "bool", "int64", "[]string"}
	for range 1 + r.IntN(2) {
		switch r.IntN(12) {
		case 0: // Same
		case 1: // Non-nil, where it applies
			if len(g.vals) == 1 && strings.HasPrefix(g.vals[0], "Option[") {
				w.results = []string{strings.TrimSuffix(strings.TrimPrefix(g.vals[0], "Option["), "]")}
			}
		case 2: // Option, where it applies; else wrap a result
			if len(w.results) == 2 {
				w.results = []string{"Option[" + w.results[0] + "]"}
			} else if len(w.results) == 1 {
				w.results = []string{"Option[" + w.results[0] + "]"}
			}
		case 3: // Partial
			w.results = append(w.results, "Option[go.Error]")
			w.fails = r.IntN(4) == 0
		case 4:
			w.fails = !w.fails
		case 5:
			w.needs = !w.needs
		case 6:
			if len(w.params) > 0 {
				w.params[r.IntN(len(w.params))] = others[r.IntN(len(others))]
			}
		case 7:
			if len(w.params) > 0 && r.IntN(2) == 0 {
				w.params = w.params[:len(w.params)-1]
			} else {
				w.params = append(w.params, others[r.IntN(len(others))])
			}
		case 8: // T for (T, bool): the first of two results alone
			if len(w.results) > 1 {
				w.results = w.results[:1]
			}
		case 9:
			if len(w.results) > 0 {
				w.results[r.IntN(len(w.results))] = others[r.IntN(len(others))]
			} else {
				w.results = []string{"int"}
			}
		case 10: // a result dropped
			if len(w.results) > 0 {
				w.results = w.results[:len(w.results)-1]
			}
		case 11: // Partial with the row kept
			if g.errs {
				w.results = append(w.results, "Option[go.Error]")
			}
		}
	}
	return w
}

// TestLevel2AdapterRelation is IG4's acceptance property: for Go functions
// read from the toolchain's sources and written signatures drawn around
// their Level 1 signatures, the checker admits a user extern exactly when
// the reference relation does, and refuses it with one S108 at the
// declaration. A failing case prints the replay seed, the Go function, the
// program and both verdicts. Fixed rows cover the generic instance, which
// the reference does not draw.
func TestLevel2AdapterRelation(t *testing.T) {
	var targets []goTarget
	for _, path := range level2Packages {
		targets = append(targets, level2Targets(t, path)...)
	}
	kinds := map[string]int{}
	for _, g := range targets {
		switch {
		case g.errs:
			kinds["error"]++
		case len(g.vals) == 2 && g.vals[1] == "bool":
			kinds["(T, bool)"]++
		case len(g.vals) == 1 && strings.HasPrefix(g.vals[0], "Option["):
			kinds["nullable"]++
		}
		if g.foreign {
			kinds["foreign"]++
		} else {
			kinds["pure"]++
		}
	}
	for _, k := range []string{"error", "(T, bool)", "nullable", "foreign", "pure"} {
		if kinds[k] == 0 {
			t.Fatalf("no drawn target is %s: the property misses that side (%v)", k, kinds)
		}
	}
	seeds := []uint64{1, 2, 3}
	if *level2Seed != 0 {
		seeds = []uint64{*level2Seed}
	}
	verdicts := map[bool]int{}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x1e4))
		for _, g := range targets {
			w := level2Write(r, g)
			decl := w.text("f", g.path+"."+g.name)
			src := ""
			if strings.Contains(decl, check.GoPackageName(g.path)+".") && strings.Contains(decl, "go.Ref[") || strings.Contains(decl, " *") {
				src = "import go " + g.path + "\n"
			}
			line := strings.Count(src, "\n") + 1
			src += decl + "\n"
			want := level2Admits(g, w)
			verdicts[want]++
			_, diags := check.CheckSource(src)
			got := len(diags) == 0
			if got != want || !got && (len(diags) != 1 || diags[0].Code != "S108" || diags[0].Line != line) {
				t.Errorf("seed %d (replay with -level2.seed=%d): Go %s; Level 1 %v %v fails=%v foreign=%v\nadmits: checker %v, reference %v\n%s\t%s",
					seed, seed, g.goText, g.params, g.vals, g.errs, g.foreign, got, want, src, describe(diags))
			}
		}
	}
	if verdicts[true] == 0 || verdicts[false] == 0 {
		t.Fatalf("the draw reached one verdict only: %v", verdicts)
	}
	for _, tc := range []struct {
		decl string
		ok   bool
	}{
		// Go infers T from the parameters; the constraint holds.
		{"extern go cmp.Compare func f(a int, b int) int needs go.Foreign", true},
		{"extern go slices.Index func f(s []string, v string) int", true},
		// T bound by the first parameter; the second is not T.
		{"extern go cmp.Compare func f(a int, b string) int needs go.Foreign", false},
		// The constraint fails: a slice is not ordered.
		{"extern go cmp.Compare func f(a []int, b []int) int needs go.Foreign", false},
		{"extern go slices.Index func f(s []func(), v func()) int", false},
		// A written type parameter.
		{"extern go slices.Index func f[T comparable](s []T, v T) int", false},
	} {
		_, diags := check.CheckSource(tc.decl + "\n")
		if (len(diags) == 0) != tc.ok || !tc.ok && (len(diags) != 1 || diags[0].Code != "S108") {
			t.Errorf("%s: want admitted %v, got\n\t%s", tc.decl, tc.ok, describe(diags))
		}
	}
}
