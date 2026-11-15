package lower

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// typecheckModule type-checks a split module in process: files are its
// Go files, with paths relative to the module root at the import path
// base. Each Go package of files is checked from source with go/types, in
// import order, and every other import against gc export data. An import
// cycle among the module's packages is an error, as it is for Go. It
// returns the errors, nil when the module type-checks.
func typecheckModule(t *testing.T, base string, files []File) []string {
	t.Helper()
	fset := token.NewFileSet()
	byDir := map[string][]*ast.File{}
	var imports []string
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		af, err := parser.ParseFile(fset, f.Path, f.Data, parser.SkipObjectResolution)
		if err != nil {
			return []string{err.Error()}
		}
		d := path.Dir(f.Path)
		byDir[d] = append(byDir[d], af)
		for _, s := range af.Imports {
			imports = append(imports, strings.Trim(s.Path.Value, `"`))
		}
	}
	dirOf := func(p string) (string, bool) {
		if p == base {
			return ".", true
		}
		if rest, ok := strings.CutPrefix(p, base+"/"); ok {
			_, has := byDir[rest]
			return rest, has
		}
		return "", false
	}
	var external []string
	for _, p := range imports {
		if _, ok := dirOf(p); !ok {
			external = append(external, p)
		}
	}
	exports, err := goExports.lookup(t, external)
	if err != nil {
		t.Fatal(err)
	}
	gc := importer.ForCompiler(fset, "gc", func(p string) (io.ReadCloser, error) {
		file := exports[p]
		if file == "" {
			return nil, fmt.Errorf("no export data for %q", p)
		}
		return os.Open(file)
	})
	var errs []string
	done := map[string]*types.Package{}
	checking := map[string]bool{}
	var check func(dir string) (*types.Package, error)
	imp := importerFunc(func(p string) (*types.Package, error) {
		if dir, ok := dirOf(p); ok {
			return check(dir)
		}
		return gc.Import(p)
	})
	check = func(dir string) (*types.Package, error) {
		if pkg, ok := done[dir]; ok {
			return pkg, nil
		}
		if checking[dir] {
			return nil, fmt.Errorf("import cycle through %s", dir)
		}
		checking[dir] = true
		defer delete(checking, dir)
		conf := types.Config{
			Importer:  imp,
			GoVersion: "go" + goVersion,
			Sizes:     types.SizesFor("gc", runtime.GOARCH),
			Error:     func(err error) { errs = append(errs, dir+": "+err.Error()) },
		}
		p := base
		if dir != "." {
			p = base + "/" + dir
		}
		pkg, _ := conf.Check(p, fset, byDir[dir], nil)
		done[dir] = pkg
		return pkg, nil
	}
	for _, dir := range slices.Sorted(maps.Keys(byDir)) {
		if _, err := check(dir); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return errs
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// TestShardsTypecheck: law ShardsAreAcyclic, in process. On the generated
// programs of several Ergo packages (genSplitProgram), with every
// component its own shard (the most names that cross), the sharded module
// type-checks package by package, with no import cycle, in the run build
// and the test build; and every program is cut (the law is not vacuous).
// TestSplitLoweringAgrees runs the same shards. Mutants:
// ft9-shard-scc-split, ft9-shard-order-source,
// ft9-shard-root-reach-dropped.
func TestShardsTypecheck(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3, 4, 5, 6} {
		u := genSplitProgram(seed).check(t)
		for _, tests := range []bool{false, true} {
			files, rep, err := lowerSplit(u, tests, programModule, everyShard)
			if err != nil || rep.Reason != "" {
				t.Fatalf("seed %d tests=%v: %v %+v", seed, tests, err, rep)
			}
			if rep.Shards < 2 {
				t.Fatalf("seed %d tests=%v: %d shards, want 2 or more: %+v", seed, tests, rep.Shards, rep)
			}
			if errs := typecheckModule(t, programModule, files); len(errs) > 0 {
				var src strings.Builder
				for _, f := range files {
					if strings.HasSuffix(f.Path, ".go") {
						fmt.Fprintf(&src, "// %s\n%s\n", f.Path, f.Data)
					}
				}
				t.Fatalf("seed %d tests=%v: the sharded module does not type-check:\n%s\n%s", seed, tests, firstLines(strings.Join(errs, "\n"), 20), src.String())
			}
		}
	}
}

// everyShard cuts after every component: each is its own shard.
var everyShard = &shardOpts{min: 1, max: 1}

// genShardProgram is one Ergo package of n functions that call one
// another, some of them in cycles, and a main.
func genShardProgram(r *rand.Rand, n int) []string {
	var decls []string
	for i := range n {
		var calls []string
		for range 1 + r.IntN(2) {
			if j := r.IntN(n); j != i {
				calls = append(calls, fmt.Sprintf("f%d(n - 1)", j))
			}
		}
		calls = append(calls, strconv.Itoa(i))
		decls = append(decls, fmt.Sprintf("func f%d(n int) int {\n\tif n <= 0 {\n\t\treturn %d\n\t}\n\t%s\n}\n", i, i, strings.Join(calls, " + ")))
	}
	return decls
}

// shardPlacement is each declared Go name of files and the Go package
// folder that declares it, the exported spelling (X+name) undone.
func shardPlacement(t *testing.T, files []File) map[string]string {
	t.Helper()
	out := map[string]string{}
	fset := token.NewFileSet()
	for _, f := range files {
		if !strings.HasSuffix(f.Path, ".go") {
			continue
		}
		af, err := parser.ParseFile(fset, f.Path, f.Data, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range af.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Recv == nil {
				out[strings.TrimLeft(fd.Name.Name, "X")] = path.Dir(f.Path)
			}
		}
	}
	return out
}

// TestShardsLocalUnderInsert: law ShardsLocalUnderInsert. The cuts are
// content-defined and a shard is named by its first name, so a new
// function with no callers, inserted at a seeded place of a generated
// package of many shards, changes the declarations of at most three Go
// packages: the shard that takes it, one its hashed cut makes, and the
// next one when the insert moves a cut at the maximum size. Every other
// shard keeps its folder and its declarations, and Go reuses its compile.
// A failing seed prints both placements and the replay command
// (ERGO_SHARD_SEED=n). Mutants: ft9-shard-cut-count, ft9-shard-name-index.
func TestShardsLocalUnderInsert(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if s := os.Getenv("ERGO_SHARD_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	opts := &shardOpts{min: 200, max: 800}
	const main = "\nfunc main() needs Console { Console.Println(\"\\(f0(4))\") }\n"
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x5a4d))
		decls := genShardProgram(r, 60)
		at := r.IntN(len(decls) + 1)
		edited := slices.Insert(slices.Clone(decls), at, "func extra(n int) int { n + 1 }\n")
		place := func(decls []string) (map[string]string, SplitReport) {
			src := strings.Join(decls, "\n") + main
			u := checkPackage(t, "main", []pkgSource{{"/shard/main.ergo", src}}, pkgImporter{})
			files, rep, err := lowerSplit(u, false, programModule, opts)
			if err != nil || rep.Reason != "" {
				t.Fatalf("seed %d: %v %+v\n%s", seed, err, rep, src)
			}
			// Its cycles are what ShardsAreAcyclic is about.
			if errs := typecheckModule(t, programModule, files); len(errs) > 0 {
				t.Fatalf("seed %d: the sharded module does not type-check:\n%s\nsource:\n%s", seed, firstLines(strings.Join(errs, "\n"), 20), src)
			}
			return shardPlacement(t, files), rep
		}
		before, rep := place(decls)
		after, _ := place(edited)
		if rep.Shards < 8 {
			t.Fatalf("seed %d: %d shards, want 8 or more (the law would be vacuous): %+v", seed, rep.Shards, rep)
		}
		members := func(p map[string]string) map[string][]string {
			out := map[string][]string{}
			for name, dir := range p {
				if name != "extra" {
					out[dir] = append(out[dir], name)
				}
			}
			for _, names := range out {
				slices.Sort(names)
			}
			return out
		}
		b, a := members(before), members(after)
		var changed []string
		for dir := range b {
			if !slices.Equal(b[dir], a[dir]) {
				changed = append(changed, dir)
			}
		}
		for dir := range a {
			if _, ok := b[dir]; !ok {
				changed = append(changed, dir)
			}
		}
		slices.Sort(changed)
		t.Logf("seed %d: insert at %d of %d changes %d of %d shards", seed, at, len(decls), len(changed), rep.Shards)
		if len(changed) > 3 {
			t.Errorf("seed %d: inserting a function at declaration %d changes %d of %d shards: %v\nbefore: %v\nafter: %v\nreplay: ERGO_SHARD_SEED=%d go test -run TestShardsLocalUnderInsert ./internal/sketch/lower", seed, at, len(changed), rep.Shards, changed, b, a, seed)
		}
	}
}
