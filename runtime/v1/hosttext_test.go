package v1

import (
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"io/fs"
	"path"
	"slices"
	"testing"
)

// TestHostTextUsersAreListed: hostTextUsers is the set of exported
// functions of runtime/v1 whose calls reach rt.TypedText, which writes a
// machine state with fmt only when the gofmt leaf is linked (binsize BS2).
// The lowering imports that leaf into a program that names one of them
// (NeedsHostText), so a missing name would write a program's machine
// state as the core's placeholder.
// Mutant: bs2-host-text-user-missing (MachineStart is left out of the list).
func TestHostTextUsersAreListed(t *testing.T) {
	want := exportedReaching(t, "TypedText")
	if len(want) == 0 {
		t.Fatal("no function of runtime/v1 reaches rt.TypedText; the scan no longer sees it")
	}
	if !slices.Equal(want, hostTextUsers) {
		t.Errorf("the exported functions that reach rt.TypedText are %v; hostTextUsers lists %v", want, hostTextUsers)
	}
}

// TestHostWalkUsersAreListed: hostWalkUsers is the set of exported
// functions of runtime/v1 whose calls reach takeHost, which keeps a host
// a step or a recover makes. A durable start's or a restart's recover may
// make hosts, and the recovered actor's first entry walks its state for
// them (heldHosts), which needs the hostwalk leaf (binsize BS8). The
// lowering imports that leaf into a program that names one of them
// (NeedsHostWalk); a missing name would leave a program that recovers
// hosts without the walk, and its first entry would raise the lowering's
// internal error.
// Mutant: bs8-host-walk-user-missing (NewHost is left out of the list).
func TestHostWalkUsersAreListed(t *testing.T) {
	want := exportedReaching(t, "takeHost")
	if len(want) == 0 {
		t.Fatal("no function of runtime/v1 reaches takeHost; the scan no longer sees it")
	}
	if !slices.Equal(want, hostWalkUsers) {
		t.Errorf("the exported functions that reach takeHost are %v; hostWalkUsers lists %v", want, hostWalkUsers)
	}
}

// exportedReaching returns, sorted, the exported top-level functions of
// runtime/v1 whose code reaches a function or method named target, in the
// host's default build (the explored build, ergoexplore, is the explorer's
// own binary, which links fmt for its report). The call graph is
// typed (go/types): an edge is a use of a function or of a concrete
// method, called or passed as a value (rt.Go(&s, name, a.loop)); a use of
// an interface's method is an edge to every method of runtime/v1 with
// that name, so the list errs toward a longer one, never a shorter one.
// The leaves are not in the graph: none starts an actor or hands one
// hosts.
func exportedReaching(t *testing.T, target string) []string {
	t.Helper()
	ctxt := build.Default
	ctxt.JoinPath = path.Join
	ctxt.OpenFile = func(p string) (io.ReadCloser, error) { return Source.Open(p) }
	names, err := fs.Glob(Source, "*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, name := range names {
		if ok, err := ctxt.MatchFile(".", name); err != nil || !ok {
			if err != nil {
				t.Fatal(err)
			}
			continue
		}
		data, err := fs.ReadFile(Source, name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	if _, err := conf.Check(Path, fset, files, info); err != nil {
		t.Fatal(err)
	}
	methods := map[string][]*types.Func{} // runtime/v1's methods by name
	var decls []*ast.FuncDecl
	for _, f := range files {
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
				decls = append(decls, fn)
				if fn.Recv != nil {
					obj := info.Defs[fn.Name].(*types.Func)
					methods[obj.Name()] = append(methods[obj.Name()], obj)
				}
			}
		}
	}
	calls := map[*types.Func][]*types.Func{}
	for _, fn := range decls {
		from := info.Defs[fn.Name].(*types.Func)
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			id, ok := n.(*ast.Ident)
			if !ok {
				return true
			}
			to, ok := info.Uses[id].(*types.Func)
			if !ok {
				return true
			}
			to = to.Origin()
			if recv := to.Signature().Recv(); recv != nil && types.IsInterface(recv.Type()) {
				calls[from] = append(calls[from], methods[to.Name()]...)
				return true
			}
			calls[from] = append(calls[from], to)
			return true
		})
	}
	reaches := map[*types.Func]bool{}
	var reach func(f *types.Func, seen map[*types.Func]bool) bool
	reach = func(f *types.Func, seen map[*types.Func]bool) bool {
		if done, ok := reaches[f]; ok {
			return done
		}
		if seen[f] {
			return false
		}
		seen[f] = true
		for _, callee := range calls[f] {
			if callee.Name() == target || reach(callee, seen) {
				reaches[f] = true
				return true
			}
		}
		return false
	}
	var want []string
	for _, fn := range decls {
		if fn.Recv == nil && fn.Name.IsExported() && reach(info.Defs[fn.Name].(*types.Func), map[*types.Func]bool{}) {
			want = append(want, fn.Name.Name)
		}
	}
	slices.Sort(want)
	return want
}
