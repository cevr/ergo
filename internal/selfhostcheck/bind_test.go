package selfhostcheck

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"
	"unsafe"

	"ergo.local/prototype/internal/proclimit"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerBindLawsRun runs the laws of compiler/check/bind (capstone
// S3b) through `ergo test`: the parent column inverts the children, a use
// names a row of its own text, the prints ignore layout and the interface
// print cuts the bodies an importer cannot see. Mutants of
// compiler/check/bind/testdata/mutants.json it alone catches:
// release-keeps-shadow, resolve-*, parent-*, rowof-first-file, print-*,
// ifp-* and pattern-let-unprinted.
//
// It builds Go because `ergo test` lowers the laws into a test program and
// runs it: the check-time evaluator runs an operation type's laws, not a
// package's top-level laws, so no in-process check runs these.
func TestCompilerBindLawsRun(t *testing.T) {
	needsBuild(t)
	runCompilerLaws(t, "./check/bind")
}

// TestCompilerBindAgreesWithChecker is C0/plan.md §7.2's BindAgreesWithGo:
// over every package folder of examples/ and std that the Go checker
// checks alone, compiler/check/bind's declarations, uses and local
// declarations (cmd/binddump) are the Go checker's Info.Defs and
// Info.Uses, each identifier by its file and offset. A folder the Go
// checker refuses alone (an import it cannot resolve from the folder) is
// skipped; the test fails when it skips every folder. It kills the bind
// mutants the laws do not: the scope rules each name a rule the Go checker
// holds (with heads, operation laws, case alternatives, prelude types,
// predeclared formers, derive clauses, parameters).
//
// Each folder is a subtest named by its path under its root, the root a
// subtest of its own (cases, examples, std; corpusGroup), so a gate runs
// the corpus in parts by folder name, one process each: for example
// -run '^TestCompilerBindAgreesWithChecker$/^examples$/^[a-f]' (S3d8). A
// root whose run checks no folder fails.
//
// It builds Go because binddump is the Ergo program under test: bind is
// Ergo source, and only its lowered, built form runs over a corpus.
func TestCompilerBindAgreesWithChecker(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	ergo := ergoCLI(t, ctx)
	dump := filepath.Join(t.TempDir(), "binddump")
	if out, err := runCompiler(t, ctx, ergo, "build", "-o", dump, "./cmd/binddump"); err != nil {
		t.Fatalf("ergo build ./cmd/binddump: %v\n%s", err, out)
	}
	limit, err := proclimit.Limit(os.Environ(), proclimit.DefaultLimit)
	if err != nil {
		t.Fatal(err)
	}
	groups := map[string][]string{}
	for _, dir := range corpusDirs(t) {
		g, _ := corpusGroup(dir)
		groups[g] = append(groups[g], dir)
	}
	for _, g := range []string{"cases", "examples", "std"} {
		t.Run(g, func(t *testing.T) {
			checked, ran := 0, 0
			for _, dir := range groups[g] {
				_, name := corpusGroup(dir)
				t.Run(name, func(t *testing.T) {
					ran++
					if bindAgrees(t, ctx, dump, limit, dir) {
						checked++
					}
				})
			}
			if ran > 0 && checked == 0 {
				t.Fatalf("the Go checker checked no %s folder alone", g)
			}
		})
	}
}

// corpusGroup is the root a corpus folder belongs to (cases: the
// testdata/bind cases; examples; std) and its subtest's name: its path
// under the root, with dots for slashes ("." for the root itself).
func corpusGroup(dir string) (string, string) {
	r := rel(dir)
	for _, root := range []struct{ group, path string }{{"cases", "internal/selfhostcheck/testdata/bind"}, {"examples", "examples"}, {"std", "internal/sketch/std"}} {
		if r == root.path {
			return root.group, "."
		}
		if rest, ok := strings.CutPrefix(r, root.path+"/"); ok {
			return root.group, strings.ReplaceAll(rest, "/", ".")
		}
	}
	return "other", strings.ReplaceAll(r, "/", ".")
}

// bindAgrees checks one corpus folder: false when the Go checker refuses
// it alone (it is skipped).
func bindAgrees(t *testing.T, ctx context.Context, dump string, limit uint64, dir string) bool {
	got, ok := goBind(t, dir)
	if !ok {
		return false
	}
	{
		files := ergoFiles(t, dir)
		for _, mode := range []string{"collect", "uses", "locals"} {
			args := append([]string{"-std", filepath.Join(repoRoot(), "internal", "sketch", "std")}, files...)
			if mode != "collect" {
				args = append([]string{"-std", filepath.Join(repoRoot(), "internal", "sketch", "std"), "-" + mode}, files...)
			}
			cmd := exec.CommandContext(ctx, dump, args...)
			cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var out, errOut strings.Builder
			cmd.Stdout, cmd.Stderr = &out, &errOut
			if err := proclimit.Run(cmd, limit); err != nil {
				t.Fatalf("binddump -%s %s: %v\n%s", mode, dir, err, errOut.String())
			}
			if d := setDiff(got[mode], lines(out.String())); d != "" {
				t.Errorf("%s, %s: bind and the Go checker differ (go -, bind +):\n%s", rel(dir), mode, d)
			}
		}
	}
	return true
}

// corpusDirs is every folder of examples/ and std that holds .ergo files,
// testdata left out, and testdata/bind: the package-level cases the corpus
// does not reach (a member group of one name, a type that takes a
// function's name, package pattern lets).
func corpusDirs(t *testing.T) []string {
	dirs := []string{filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "bind")}
	for _, root := range []string{"examples", "internal/sketch/std"} {
		err := filepath.WalkDir(filepath.Join(repoRoot(), root), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "testdata" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasSuffix(p, ".ergo") {
				if dir := filepath.Dir(p); dirs[len(dirs)-1] != dir {
					dirs = append(dirs, dir)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return dirs
}

func ergoFiles(t *testing.T, dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, e := range ents {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".ergo") {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(files)
	return files
}

func rel(dir string) string {
	r, err := filepath.Rel(repoRoot(), dir)
	if err != nil {
		return dir
	}
	return r
}

// goBind is the Go checker's answer for dir in binddump's three forms, or
// false when the folder does not check alone:
//   - collect: "FILE OFF KIND NAME" for each package-level Def (func, type,
//     var, context; an import is no Def);
//   - uses: "FILE OFF -> FILE OFF" or "FILE OFF -> outside NAME" for each
//     use that is no selector's name and names no field;
//   - locals: collect's lines and "FILE OFF local NAME" / "FILE OFF law
//     NAME" for the body's Defs, fields left out.
//
// Identifiers inside a declaration a refinement derives reuse the source's
// positions, so only written declarations count.
func goBind(t *testing.T, dir string) (map[string][]string, bool) {
	a, err := sketchfront.CheckDir(dir)
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	root := a.Root()
	if root.Package == nil {
		return nil, false
	}
	info := root.Package.Info
	fileOf := map[*syntax.Ident]string{}
	written := map[*syntax.Ident]string{}
	sel := map[*syntax.Ident]syntax.Expr{}
	for _, f := range root.Files {
		if f.Syntax == nil {
			continue
		}
		name := filepath.Base(f.Rel)
		syntax.Inspect(f.Syntax, func(n syntax.Node) bool {
			switch x := n.(type) {
			case *syntax.Ident:
				fileOf[x] = name
			case *syntax.SelectorExpr:
				sel[x.Sel] = x.X
			}
			return true
		})
		syntax.Inspect(f.Syntax, func(n syntax.Node) bool {
			if d, ok := n.(syntax.Decl); ok && syntax.IsDerived(d) {
				return false
			}
			if id, ok := n.(*syntax.Ident); ok {
				written[id] = name
			}
			return true
		})
	}
	var collect, locals []string
	for id, o := range info.Defs {
		kind := ""
		switch v := o.(type) {
		case *check.Func:
			kind = "func"
		case *check.TypeName:
			kind = "type"
		case *check.Context:
			// Only the package's own contexts: a local `let w = context`
			// is a body's.
			if root.Package.Contexts[v.Name] == v {
				kind = "context"
			}
		case *check.Var:
			if unexported(v, "pkg").Bool() {
				kind = "var"
			}
		case *check.PkgName:
			continue
		}
		if kind != "" {
			collect = append(collect, fmt.Sprintf("%s %d %s %s", fileOf[id], id.Pos(), kind, id.Name))
			continue
		}
		if o == nil || written[id] == "" {
			continue
		}
		switch o.(type) {
		case *check.Field:
			continue
		case *check.Law:
			kind = "law"
		default:
			kind = "local"
		}
		locals = append(locals, fmt.Sprintf("%s %d %s %s", fileOf[id], id.Pos(), kind, id.Name))
	}
	defOf := map[check.Object]*syntax.Ident{}
	for id, o := range info.Defs {
		if prev, ok := defOf[o]; !ok || id.Pos() < prev.Pos() {
			defOf[o] = id
		}
	}
	var uses []string
	for id, o := range info.Uses {
		// A local's declaring identifier is also recorded as its use.
		if written[id] == "" || info.Defs[id] == o {
			continue
		}
		if _, ok := sel[id]; ok {
			continue
		}
		if _, ok := o.(*check.Field); ok {
			continue
		}
		o = canonical(o)
		to := "outside " + id.Name
		if d, ok := defOf[o]; ok && fileOf[d] != "" {
			to = fmt.Sprintf("%s %d", fileOf[d], d.Pos())
		} else if tp, ok := o.(*check.TypeParam); ok && tp.Pos > 0 {
			// A variant's own type parameter has no Def; it is the
			// union's, declared in the use's file.
			to = fmt.Sprintf("%s %d", fileOf[id], tp.Pos)
		}
		uses = append(uses, fmt.Sprintf("%s %d -> %s", fileOf[id], id.Pos(), to))
	}
	// A selector's name is a use of another package's declaration when the
	// selector reads an Ergo import (S3c's XRef). The Go checker resolves
	// it (expr.go qualified) with no Info.Uses entry, so this reads the
	// import's scope as qualified does.
	for id, x := range sel {
		if written[id] == "" {
			continue
		}
		if line, ok := importedUse(info, x, id.Name); ok {
			uses = append(uses, fmt.Sprintf("%s %d -> %s", fileOf[id], id.Pos(), line))
		}
	}
	return map[string][]string{
		"collect": collect,
		"uses":    uses,
		"locals":  append(append([]string{}, collect...), locals...),
	}, true
}

// importedUse is "import PATH NAME" when the selector's X names an Ergo
// import (no Go import) whose scope holds the exported name, as the
// checker's qualified resolves it; else false.
func importedUse(info *check.Info, x syntax.Expr, name string) (string, bool) {
	xi, ok := x.(*syntax.Ident)
	if !ok {
		return "", false
	}
	pn, ok := info.Uses[xi].(*check.PkgName)
	// go is the predeclared package of go.Is and go.As, no import.
	if !ok || pn.Pkg == nil || pn.Pkg.Host() || pn.Pkg.Path == "go" || pn.Pkg.Scope == nil || name == "" || name[0] < 'A' || name[0] > 'Z' {
		return "", false
	}
	o := unexported(pn.Pkg.Scope, "names").MapIndex(reflect.ValueOf(name))
	if !o.IsValid() || o.IsNil() {
		return "", false
	}
	return fmt.Sprintf("import %s %s", pn.Pkg.Path, name), true
}

// canonical is the object a use names as bind sees it: a capability-row
// binding names its type, and a type parameter's type name its parameter.
func canonical(o check.Object) check.Object {
	v := reflect.ValueOf(o)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return o
	}
	if v.Elem().Type().Name() == "capBinding" {
		return unexported(o, "tn").Interface().(check.Object)
	}
	if tn, ok := o.(*check.TypeName); ok {
		if tp, ok := tn.Type.(*check.TypeParam); ok {
			return tp
		}
	}
	return o
}

// unexported reads the unexported field name of the struct o points to:
// the checker's package-var bit and a capability binding's type name have
// no exported reader, and this test does not widen the checker's API.
func unexported(o any, name string) reflect.Value {
	f := reflect.ValueOf(o).Elem().FieldByName(name)
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

// lines is the non-empty lines of s.
func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// setDiff is the lines of want not in got ("- ") and of got not in want
// ("+ "), as sets, sorted; "" when they are one set.
func setDiff(want, got []string) string {
	w, g := map[string]bool{}, map[string]bool{}
	for _, l := range want {
		w[l] = true
	}
	for _, l := range got {
		g[l] = true
	}
	var out []string
	for l := range w {
		if !g[l] {
			out = append(out, "- "+l)
		}
	}
	for l := range g {
		if !w[l] {
			out = append(out, "+ "+l)
		}
	}
	sort.Strings(out)
	return strings.Join(out, "\n")
}
