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
	errs, _ := typecheckModuleInfo(t, base, files)
	return errs
}

// checkedPkg is one Go package typecheckModuleInfo checked: its files and
// their type information.
type checkedPkg struct {
	files []*ast.File
	info  *types.Info
}

// typecheckModuleInfo is typecheckModule with each package's files and
// type information, by folder.
func typecheckModuleInfo(t *testing.T, base string, files []File) ([]string, map[string]checkedPkg) {
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
			return []string{err.Error()}, nil
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
	checked := map[string]checkedPkg{}
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
		info := &types.Info{Instances: map[*ast.Ident]types.Instance{}, Uses: map[*ast.Ident]types.Object{}, Defs: map[*ast.Ident]types.Object{}}
		checked[dir] = checkedPkg{byDir[dir], info}
		pkg, _ := conf.Check(p, fset, byDir[dir], info)
		done[dir] = pkg
		return pkg, nil
	}
	for _, dir := range slices.Sorted(maps.Keys(byDir)) {
		if _, err := check(dir); err != nil {
			errs = append(errs, err.Error())
		}
	}
	return errs, checked
}

type importerFunc func(path string) (*types.Package, error)

func (f importerFunc) Import(path string) (*types.Package, error) { return f(path) }

// TestShardsTypecheck: law ShardsAreAcyclic, in process. On the generated
// programs of several Ergo packages (genSplitProgram), with every
// component its own shard (the most names that cross), the sharded module
// type-checks package by package, with no import cycle, in the run build
// and the test build; and every program is cut (the law is not vacuous).
// The IR split without shards type-checks too: a forwarder's signature
// names only exported names of another package (a law of the root, run
// from main in the test build, calls a function of the root returning its
// unexported record: JL's looseRepair, SHARD-PRIVATE).
// TestSplitLoweringAgrees runs the same shards. Mutants:
// ft9-shard-scc-split, ft9-shard-order-source,
// ft9-shard-root-reach-dropped, split-forward-signature-private.
func TestShardsTypecheck(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3, 4, 5, 6} {
		u := genSplitProgram(seed).check(t)
		for _, c := range []struct {
			tests bool
			sh    *shardOpts
		}{{false, nil}, {true, nil}, {false, everyShard}, {true, everyShard}} {
			tests := c.tests
			var files []File
			var rep SplitReport
			var err error
			if c.sh == nil {
				// The IR split: its IR bodies reach other packages
				// through forwarders (irSplit.foreignDecls).
				files, _, rep, err = lowerIRSplit(u, tests, programModule, nil)
			} else {
				files, rep, err = lowerSplit(u, tests, programModule, c.sh)
			}
			if err != nil || rep.Reason != "" {
				t.Fatalf("seed %d tests=%v shards=%v: %v %+v", seed, tests, c.sh != nil, err, rep)
			}
			if c.sh != nil && rep.Shards < 2 {
				t.Fatalf("seed %d tests=%v: %d shards, want 2 or more: %+v", seed, tests, rep.Shards, rep)
			}
			if errs := typecheckModule(t, programModule, files); len(errs) > 0 {
				var src strings.Builder
				for _, f := range files {
					if strings.HasSuffix(f.Path, ".go") {
						fmt.Fprintf(&src, "// %s\n%s\n", f.Path, f.Data)
					}
				}
				t.Fatalf("seed %d tests=%v shards=%v: the split module does not type-check:\n%s\n%s", seed, tests, c.sh != nil, firstLines(strings.Join(errs, "\n"), 20), src.String())
			}
		}
	}
}

// TestShardCutFollowsCost counts the shards of a synthetic package: 20
// functions that instantiate a 5,000-byte generic declared in another
// package, and 20 plain ones, 100 bytes each, cut at the first component
// past 1,000 bytes of cost. A package under the threshold makes no shard;
// over it, every instantiating function is a shard of its own (its cost
// counts the generic, so it ends a run alone), and no shard of plain
// functions holds more than the minimum and one function. Mutants:
// ft9-shard-pkg-threshold-ignored, ft9-shard-cost-bytes-only.
func TestShardCutFollowsCost(t *testing.T) {
	const n, small, big = 20, 100, 5000
	setup := func() (*splitPlan, []splitGroup) {
		plan := &splitPlan{pkgs: []splitPkg{{dir: sharedDir, name: "ergoshared", sees: map[int]bool{}}, {name: "main", sees: map[int]bool{0: true}}}}
		groups := []splitGroup{{pkg: 0, generic: true}} // the generic, in another package
		for i := range 2 * n {
			g := splitGroup{pkg: 1}
			if i%2 == 0 {
				g.refs = []int{0}
			}
			groups = append(groups, g)
		}
		return plan, groups
	}
	name := func(g int) string { return "g" + strconv.Itoa(g) }
	size := func(g int) int {
		if g == 0 {
			return big
		}
		return small
	}
	plan, groups := setup()
	if made := plan.shard(groups, &shardOpts{pkg: 2*n*small + 1, min: 1000, one: 1}, name, size); made != 0 {
		t.Errorf("a package under the threshold makes %d shards, want 0", made)
	}
	plan, groups = setup()
	made := plan.shard(groups, &shardOpts{pkg: 1, min: 1000, one: 1}, name, size)
	if made < 2 {
		t.Fatalf("a package over the threshold makes %d shards, want 2 or more", made)
	}
	insts, plain := map[int]int{}, map[int]int{}
	for g := 1; g < len(groups); g++ {
		if len(groups[g].refs) > 0 {
			insts[groups[g].pkg]++
		} else {
			plain[groups[g].pkg] += small
		}
	}
	for k, c := range insts {
		if c > 1 {
			t.Errorf("package %d holds %d functions that instantiate the generic: its cost passed the minimum at the first", k, c)
		}
	}
	for k, b := range plain {
		if k != 1 && b > 1000+small {
			t.Errorf("shard %d holds %d bytes of plain functions, over the minimum and one function", k, b)
		}
	}
}

// everyShard cuts after every component: each is its own shard.
var everyShard = &shardOpts{pkg: 1, min: 1, one: 1}

// genShardProgram is n functions of one Ergo package: each calls one or
// two later ones, and one in eight also the one before it, which makes
// small cycles (a random graph of more edges is one giant cycle).
func genShardProgram(r *rand.Rand, n int) []string {
	var decls []string
	for i := range n {
		var calls []string
		for range 1 + r.IntN(2) {
			if i+1 < n {
				calls = append(calls, fmt.Sprintf("f%d(n - 1)", i+1+r.IntN(n-i-1)))
			}
		}
		if i > 0 && r.IntN(8) == 0 {
			calls = append(calls, fmt.Sprintf("f%d(n - 1)", i-1))
		}
		calls = append(calls, strconv.Itoa(i))
		decls = append(decls, fmt.Sprintf("func f%d(n int) int {\n\tif n <= 0 {\n\t\treturn %d\n\t}\n\t%s\n}\n", i, i, strings.Join(calls, " + ")))
	}
	return decls
}

// shardPlacement is each declared Go function of files and the Go package
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
// next one when the bytes it adds move the next cut past min. Every other
// shard keeps its folder and its declarations, and Go reuses its compile.
// Both placements type-check (law ShardsAreAcyclic on shards of several
// components, some of them cycles). A failing seed prints both placements
// and the replay command (ERGO_SHARD_SEED=n). Mutants:
// ft9-shard-cut-count, ft9-shard-name-index, ft9-shard-scc-split,
// ft9-shard-order-source.
func TestShardsLocalUnderInsert(t *testing.T) {
	seeds := []uint64{1, 2, 3, 4, 5, 6, 7, 8}
	if s := os.Getenv("ERGO_SHARD_SEED"); s != "" {
		n, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		seeds = []uint64{n}
	}
	opts := &shardOpts{pkg: 1, min: 4000, one: 4}
	const main = "\nfunc main() needs Console { Console.Println(\"\\(f0(4))\") }\n"
	const n = 50
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x5a4d))
		decls := genShardProgram(r, n)
		// A function as big as the others (it calls two of them), so a
		// cut by size would move; its name is the seed's, so its hash
		// cuts in some seeds and not in others.
		at := r.IntN(n / 2)
		extra := fmt.Sprintf("extra%d", seed)
		edited := slices.Insert(slices.Clone(decls), at, fmt.Sprintf("func %s(n int) int {\n\tif n <= 0 {\n\t\treturn 0\n\t}\n\tf%d(n - 1) + f%d(n - 1)\n}\n", extra, at+r.IntN(n-at), at+r.IntN(n-at)))
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
		if rep.Shards < 6 {
			t.Fatalf("seed %d: %d shards, want 6 or more (the law would be vacuous): %+v", seed, rep.Shards, rep)
		}
		members := func(p map[string]string) map[string][]string {
			out := map[string][]string{}
			for name, dir := range p {
				if name != extra {
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
