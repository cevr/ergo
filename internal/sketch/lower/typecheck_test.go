package lower

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
)

// typecheckGo is buildAll without a link, in process: it type-checks each
// program's main.go with go/types against the gc export data of the
// packages it imports, and fails the test with the Go errors and the source
// of each program that does not type-check. A test that asks only "does
// the emitted Go compile" uses it; go/types implements the language's type
// checking, and TestCorpusCompilesWithGc (full tier) builds the corpus
// with gc in one go command, so a difference between the two still fails
// a tier (test-speed plan T1; docs/decisions.md, Q2 of 2026-11-08).
func typecheckGo(t *testing.T, programs []*lowered) {
	t.Helper()
	if len(programs) == 0 {
		return
	}
	if err := unparsedGo(programs); err != nil {
		t.Error(err)
		return
	}
	failed, err := typecheckPrograms(t, programs)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for n := range failed {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		t.Errorf("go/types: %s does not type-check:\n%s", n, firstLines(strings.Join(failed[n], "\n"), 20))
		if i < 3 {
			for _, p := range programs {
				if p.name == n {
					t.Logf("source of %s:\n%s\nGo:\n%s", n, p.src, p.mainGo())
				}
			}
		}
	}
	for _, p := range programs {
		probeBuilt("", p.mainGo())
	}
}

// typecheckPrograms returns the type errors of each program that has some.
func typecheckPrograms(t *testing.T, programs []*lowered) (map[string][]string, error) {
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	var imports []string
	for _, p := range programs {
		f, err := parser.ParseFile(fset, "p/"+p.name+"/main.go", p.mainGo(), parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("%s: %v", p.name, err)
		}
		files[p.name] = f
		for _, spec := range f.Imports {
			imports = append(imports, strings.Trim(spec.Path.Value, `"`))
		}
	}
	exports, err := goExports.lookup(t, imports)
	if err != nil {
		return nil, err
	}
	imp := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, fmt.Errorf("no export data for %q", path)
		}
		return os.Open(file)
	})
	failed := map[string][]string{}
	for _, p := range programs {
		conf := types.Config{
			Importer:  imp,
			GoVersion: "go" + goVersion,
			Sizes:     types.SizesFor("gc", runtime.GOARCH),
			Error:     func(err error) { failed[p.name] = append(failed[p.name], err.Error()) },
		}
		_, _ = conf.Check(programModule+"/p/"+p.name, fset, []*ast.File{files[p.name]}, nil)
	}
	return failed, nil
}

// goExports are the gc export data files of the packages the emitted
// programs import, per test process: one go list -export -deps for the
// imports not seen yet, run in a module that holds the runtime. The files
// live in the build cache; -trimpath gives every temporary module the same
// runtime cache entries, as buildAll's builds.
var goExports = &exportTable{files: map[string]string{"unsafe": ""}}

type exportTable struct {
	mu    sync.Mutex
	files map[string]string
}

func (x *exportTable) lookup(t *testing.T, imports []string) (map[string]string, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	var missing []string
	for _, path := range imports {
		if _, ok := x.files[path]; !ok && !slices.Contains(missing, path) {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		if err := x.list(t, missing); err != nil {
			return nil, err
		}
	}
	return x.files, nil
}

// list runs one go list -export -deps of the runtime packages in paths.
// It builds Go because go/types needs the runtime's export data, and only
// the go command writes it (once per test process).
func (x *exportTable) list(t *testing.T, paths []string) error {
	root := t.TempDir()
	write := func(rel string, data []byte) error {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, data, 0o644)
	}
	if err := write("go.mod", []byte(goMod)); err != nil {
		return err
	}
	for _, f := range Runtime() {
		if err := write(f.Path, f.Data); err != nil {
			return err
		}
	}
	args := append([]string{"list", "-e", "-export", "-deps", "-trimpath", "-f", "{{.ImportPath}}\t{{.Export}}"}, paths...)
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list -export: %v\n%s", err, firstLines(stderr.String(), 20))
	}
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if path, file, ok := strings.Cut(scanner.Text(), "\t"); ok {
			x.files[path] = file
		}
	}
	for _, p := range paths {
		if _, ok := x.files[p]; !ok {
			x.files[p] = "" // not a package: the importer reports it
		}
	}
	return scanner.Err()
}

var typecheckSeed = flag.Uint64("typecheck.seed", 0, "run TestTypecheckAgreesWithGc on this seed alone")

// typecheckSeeds are the fixed seeds of TestTypecheckAgreesWithGc; each
// draws typecheckSample corpus programs.
var typecheckSeeds = []uint64{1, 2}

const typecheckSample = 24

// TestTypecheckAgreesWithGc is the property behind typecheckGo: over corpus
// programs and broken variants of them, go/types rejects a program iff gc
// (go build) rejects it. Each seed draws clean corpus programs and gives
// each one variant, or none: drop an import, rename a used local's
// declaration, change a declared type, add an unused variable, drop a
// return, duplicate a switch case. One go build compiles them all; a
// failing seed prints the Go and both verdicts, and -typecheck.seed=N
// replays it.
//
// It builds Go because its question is whether gc agrees with go/types;
// nothing in process answers that.
func TestTypecheckAgreesWithGc(t *testing.T) {
	if testing.Short() {
		t.Skip("full tier: builds Go")
	}
	seeds := typecheckSeeds
	if *typecheckSeed != 0 {
		seeds = []uint64{*typecheckSeed}
	}
	sources, err := corpusDomain()
	if err != nil {
		t.Fatal(err)
	}
	for _, seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 0x7463))
		var programs []*lowered
		kinds := map[string]string{}
		for _, i := range r.Perm(len(sources)) {
			if len(programs) == typecheckSample {
				break
			}
			o := lowerSource(fmt.Sprintf("s%dc%d", seed, i), sources[i], false)
			if o.file == nil || len(o.diags) > 0 || o.panicked != nil || o.err != nil || len(o.refused) > 0 {
				continue
			}
			kind, broken := breakGo(r, o.mainGo())
			if kind != "" {
				o.files = []File{{Path: "go.mod", Data: []byte(goMod)}, {Path: "main.go", Data: []byte(broken)}}
			}
			kinds[o.name] = kind
			programs = append(programs, o)
		}
		if len(programs) < typecheckSample {
			t.Fatalf("seed %d: only %d clean corpus programs", seed, len(programs))
		}
		failed, err := typecheckPrograms(t, programs)
		if err != nil {
			t.Fatal(err)
		}
		gc, _ := gcRejects(t, programs)
		broken := 0
		for _, p := range programs {
			_, byTypes := failed[p.name]
			if gc[p.name] {
				broken++
			}
			if byTypes != gc[p.name] {
				t.Errorf("seed %d (replay: -typecheck.seed=%d), program %s, variant %q: go/types rejects %v, gc rejects %v\ngo/types: %s\nGo:\n%s",
					seed, seed, p.name, kinds[p.name], byTypes, gc[p.name], strings.Join(failed[p.name], "; "), p.mainGo())
			}
		}
		t.Logf("seed %d: gc rejects %d of %d programs; variants %v", seed, broken, len(programs), kinds)
		if broken == 0 || broken == len(programs) {
			t.Errorf("seed %d: gc rejects %d of %d programs; the variants must make some fail and leave some clean", seed, broken, len(programs))
		}
	}
}

// gcRejects compiles the programs with gc in one go command (go build of
// many packages discards the result, and it runs no vet) and returns the
// programs it rejects. It builds Go because it is the full tier's check
// that gc agrees with go/types on the emitted Go.
func gcRejects(t *testing.T, programs []*lowered) (map[string]bool, string) {
	t.Helper()
	root := t.TempDir()
	write := func(rel string, data []byte) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte(goMod))
	for _, f := range Runtime() {
		write(f.Path, f.Data)
	}
	for _, p := range programs {
		write("p/"+p.name+"/main.go", []byte(p.mainGo()))
	}
	cmd := exec.Command("go", "build", "-trimpath", "./p/...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	rejected := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^# ergo\.local/program/p/(\S+)`).FindAllStringSubmatch(out.String(), -1) {
		rejected[m[1]] = true
	}
	if err != nil && len(rejected) == 0 {
		t.Fatalf("go build failed and named no program: %v\n%s", err, firstLines(out.String(), 40))
	}
	return rejected, out.String()
}

// breakGo applies one variant, drawn by r, to the Go source src, and names
// it; "" leaves src as it is (a clean program in the sample).
func breakGo(r *rand.Rand, src string) (string, string) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "main.go", src, parser.SkipObjectResolution)
	if err != nil {
		return "", src
	}
	off := func(p token.Pos) int { return fset.Position(p).Offset }
	splice := func(from, to int, text string) string { return src[:from] + text + src[to:] }
	var funcs []*ast.FuncDecl
	for _, d := range f.Decls {
		if fd, ok := d.(*ast.FuncDecl); ok && fd.Body != nil && len(fd.Body.List) > 0 {
			funcs = append(funcs, fd)
		}
	}
	kinds := []string{"", "drop an import", "rename a used local", "change a declared type", "add an unused variable", "drop a return", "duplicate a case"}
	for _, k := range r.Perm(len(kinds)) {
		switch kinds[k] {
		case "":
			return "", src
		case "drop an import":
			if len(f.Imports) > 0 {
				spec := f.Imports[r.IntN(len(f.Imports))]
				return kinds[k], splice(off(spec.Pos()), off(spec.End()), "")
			}
		case "rename a used local":
			var defs []*ast.Ident
			for _, fd := range funcs {
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if a, ok := n.(*ast.AssignStmt); ok && a.Tok == token.DEFINE {
						for _, l := range a.Lhs {
							if id, ok := l.(*ast.Ident); ok && id.Name != "_" {
								defs = append(defs, id)
							}
						}
					}
					return true
				})
			}
			if len(defs) > 0 {
				id := defs[r.IntN(len(defs))]
				return kinds[k], splice(off(id.Pos()), off(id.End()), id.Name+"Renamed")
			}
		case "change a declared type":
			var typed []ast.Expr
			for _, fd := range funcs {
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if vs, ok := n.(*ast.ValueSpec); ok && vs.Type != nil {
						typed = append(typed, vs.Type)
					}
					return true
				})
			}
			if len(typed) > 0 {
				x := typed[r.IntN(len(typed))]
				return kinds[k], splice(off(x.Pos()), off(x.End()), "struct{ typecheckVariant int }")
			}
		case "add an unused variable":
			if len(funcs) > 0 {
				fd := funcs[r.IntN(len(funcs))]
				at := off(fd.Body.Lbrace) + 1
				return kinds[k], splice(at, at, "\n\ttypecheckUnused := 1\n")
			}
		case "drop a return":
			var rets []*ast.ReturnStmt
			for _, fd := range funcs {
				if fd.Type.Results == nil {
					continue
				}
				if ret, ok := fd.Body.List[len(fd.Body.List)-1].(*ast.ReturnStmt); ok {
					rets = append(rets, ret)
				}
			}
			if len(rets) > 0 {
				ret := rets[r.IntN(len(rets))]
				return kinds[k], splice(off(ret.Pos()), off(ret.End()), "")
			}
		case "duplicate a case":
			var cases []*ast.CaseClause
			for _, fd := range funcs {
				ast.Inspect(fd.Body, func(n ast.Node) bool {
					if c, ok := n.(*ast.CaseClause); ok && len(c.List) > 0 && slices.IndexFunc(c.List, func(e ast.Expr) bool { _, lit := e.(*ast.BasicLit); return !lit }) < 0 {
						cases = append(cases, c)
					}
					return true
				})
			}
			if len(cases) > 0 {
				c := cases[r.IntN(len(cases))]
				at := off(c.Pos())
				return kinds[k], splice(at, at, src[at:off(c.Colon)+1]+"\n")
			}
		}
	}
	return "", src
}
