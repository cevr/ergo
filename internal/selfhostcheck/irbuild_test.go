package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerIRAgreesWithBuilder holds the IR builder written in Ergo
// (compiler/lower, capstone S4c) to the Go IR builder (internal/sketch/
// lower): every function compiler/lower builds over the corpus (each
// folder of the examples and of std, typed by compiler/check as
// TestCompilerExprAgreesWithChecker types them) must be, byte for byte,
// the function the Go builder writes at the same declaration. The Go side
// is every example program lowered with every function of its own
// packages reached (lower.LowerIRSplitReaching: called by its main or
// not), the test build of each example package no program imports,
// reached alike, and one empty program that reaches every std function,
// as the IR census reaches them; so the Go builder lowers every function
// the Ergo side can build. A body the programs lower differently agrees
// with one of them. A test build the Go lowerer refuses is logged, and
// its functions are not compared. The two sides' file indices are mapped
// to the files' paths first; the bytes are otherwise the IR file's
// (ir.WriteFunc against encoding/json).
//
// compiler/lower stops at a function it cannot build yet: an expression
// check/expr left untyped, or a form or a decider it has not ported (the
// S4c plan's slices). The reach is logged by cause, never pinned: it
// grows as compiler/check types more and compiler/lower ports more, with
// no change here. A function built where the Go builder wrote no IR body
// (it left the body to the emitter: a gate compiler/lower lacks) fails:
// the Ergo builder builds only what the Go builder builds. A Go function
// is named per declaration and package (NAME-LOCAL, lower's pkgnames.go):
// bare in a build's root package, alias__name in any other, so irdump is
// told the roots (-roots). A Go function whose name differs only by the
// split's X export (irSplitName) is compared under its unexported name;
// that export is the program's, not the function's (NL-3).
//
// It builds compiler/cmd/irdump and runs Go binaries because the Ergo
// side is a whole compiled program over the corpus (as the other
// agreement tests of this package); full tier only.
//
// Mutants it alone catches: testdata/mutants.json, rows lower-*.
func TestCompilerIRAgreesWithBuilder(t *testing.T) {
	needsBuild(t)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	bin := filepath.Join(t.TempDir(), "irdump")
	if o, err := runCompiler(t, ctx, ergoCLI(t, ctx), "build", "-o", bin, "./cmd/irdump"); err != nil {
		t.Fatalf("ergo build ./cmd/irdump: %v\n%s", err, o)
	}
	paths := irPathIDs{}
	goFuncs, unlowered, rootDirs := goIRFuncs(t, paths)
	for dir, err := range unlowered {
		t.Logf("%s: the Go builder does not lower its test build (its functions are not compared): %s", rel(dir), err)
	}

	// The Ergo side: exprdump's manifest, one line per corpus folder.
	tmp := t.TempDir()
	dirs := corpusDirs(t)
	type side struct {
		ok           bool
		path         string
		ext, foreign []string
	}
	sides := make([]side, len(dirs))
	eachGoPackage(dirs, func(i int, pkg *check.Package) {
		_, ext := goKinds(pkg)
		_, foreign := goTypes(pkg)
		sides[i] = side{ok: true, path: importPath(dirs[i], pkg), ext: ext, foreign: foreign}
	})
	var manifest, roots []string
	for i, s := range sides {
		if !s.ok {
			continue
		}
		if rootDirs[dirs[i]] {
			roots = append(roots, s.path)
		}
		e, f := filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, e, s.ext)
		writeLines(t, f, s.foreign)
		manifest = append(manifest, strings.Join(append([]string{s.path, e, f}, ergoFiles(t, dirs[i])...), " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)
	rf := filepath.Join(tmp, "roots")
	writeLines(t, rf, roots)

	reach := map[string]int{}
	built, failed := 0, 0
	var files []string
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-roots", rf, "-corpus", m) {
		kind, rest, _ := strings.Cut(l, " ")
		switch kind {
		case "@pkg":
			files = nil
		case "P":
			_, path, _ := strings.Cut(rest, " ")
			files = append(files, path)
		case "S":
			f := strings.SplitN(rest, " ", 3)
			if len(f) == 3 {
				reach["stop: "+f[2]]++
			}
		case "F":
			f := strings.SplitN(rest, " ", 3)
			if len(f) != 3 {
				t.Fatalf("irdump line %q", l)
			}
			var fn irFunc
			if err := json.Unmarshal([]byte(f[2]), &fn); err != nil {
				t.Fatalf("%s:%s: irdump wrote no IR function: %v\n%s", rel(f[0]), f[1], err, f[2])
			}
			if err := irMapFiles(&fn, files, paths); err != nil {
				t.Fatalf("%s:%s: %v", rel(f[0]), f[1], err)
			}
			got, err := json.Marshal(&fn)
			if err != nil {
				t.Fatal(err)
			}
			key := irKey{file: fn.Pos[0], line: fn.Pos[1], col: fn.Pos[2]}
			wants, cause := goFuncs.at(key, fn.Name)
			if cause != "" && unlowered[filepath.Dir(f[0])] != "" {
				reach["built, its package's test build the Go builder does not lower"]++
				continue
			}
			if cause != "" {
				failed++
				if failed <= 10 {
					t.Errorf("%s:%s %s: compiler/lower builds a body where the Go builder writes none (%s; Go names there: %s)\n ergo: %s", rel(f[0]), f[1], fn.Name, cause, goFuncs.names(key), got)
				}
				continue
			}
			// A body the programs lower differently (its callees'
			// forms differ by program) agrees with one of them.
			i := slices.IndexFunc(wants, func(w irGoFunc) bool { return bytes.Equal(got, w.data) })
			if i < 0 {
				failed++
				if failed <= 10 {
					t.Errorf("%s:%s %s: compiler/lower builds another IR body\n ergo: %s\n   go: %s", rel(f[0]), f[1], fn.Name, got, wants[0].data)
				}
				continue
			}
			if wants[i].raw != fn.Name {
				reach["built, the Go name exported by the split (X)"]++
			}
			if len(wants) > 1 {
				reach["built, one of the Go bodies the programs differ in"]++
			}
			built++
			reach["built, equal"]++
		}
	}
	var lines []string
	for cause, n := range reach {
		lines = append(lines, fmt.Sprintf("%7d %s", n, cause))
	}
	sort.Strings(lines)
	t.Logf("compiler/lower built %d functions equal to the Go builder's; %d differ:\n%s", built, failed, strings.Join(lines, "\n"))
	if failed > 0 {
		t.Fatalf("%d functions differ", failed)
	}
}

// irPathIDs gives each source path one id, the file index both sides'
// positions are mapped to.
type irPathIDs map[string]int

func (p irPathIDs) id(path string) int {
	if i, ok := p[path]; ok {
		return i
	}
	p[path] = len(p)
	return p[path]
}

type irKey struct{ file, line, col int }

// irGoFunc is one Go function: its name unexported (irSplitName), its
// name in the program, and its text under the unexported names.
type irGoFunc struct {
	name, raw string
	data      []byte
}

// irGoIndex is the Go builder's functions by declaration position, each
// the distinct texts written there across the programs.
type irGoIndex map[irKey][]irGoFunc

// irProgramName is the Go builder's names that are program-wide rather
// than the function's: the split's export of a name two Go packages use
// (pkgsplit.go: X before an unexported name, again while taken) and a
// collision suffix (_N) of a field or a literal's key (irBaseFields).
// Declarations' names follow NAME-LOCAL (S4c/naming.md) and compare
// under irSplitName alone.
var irProgramName = regexp.MustCompile(`^X+([a-z_])|_[0-9]+$|_[0-9]+(\.)`)

var irMethodExport = regexp.MustCompile(`\.X+([a-z_])`)

// irBaseName is name without its program-wide parts.
func irBaseName(name string) string {
	return irMethodExport.ReplaceAllString(irProgramName.ReplaceAllString(name, "${1}${2}"), ".${1}")
}

var irSplitExport = regexp.MustCompile(`^X+([a-z_])`)

// irSplitName is a declaration's name without the split's export (X
// before a name two Go packages use, pkgsplit.go), the one program-wide
// part of a declaration's Go name NAME-LOCAL leaves (NL-3).
func irSplitName(name string) string {
	return irMethodExport.ReplaceAllString(irSplitExport.ReplaceAllString(name, "${1}"), ".${1}")
}

// irBaseFields gives each field the Go builder reads in fn its base name:
// the split exports an unexported field two Go packages read (X + name),
// a program-wide decision too; so are a record literal's keys, and its
// type's export (irSplitName).
func irBaseFields(fn *irFunc) {
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Struct:
			if e, ok := v.Addr().Interface().(*irExpr); ok && e.K == "field" {
				e.Name = irBaseName(e.Name)
			}
			if e, ok := v.Addr().Interface().(*irExpr); ok && e.K == "lit" {
				// A literal's record type and keys (the type's Go name is
				// the program's too: pkgName).
				e.T = irSplitName(e.T)
				for i, k := range e.Keys {
					e.Keys[i] = irBaseName(k)
				}
			}
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		}
	}
	walk(reflect.ValueOf(fn))
}

// names is the names of the Go functions at k.
func (x irGoIndex) names(k irKey) string {
	var ns []string
	for _, f := range x[k] {
		ns = append(ns, f.raw)
	}
	return strings.Join(ns, ", ")
}

// at is the Go functions at k of the Ergo side's name (its base name),
// one per distinct text the programs gave it; cause says why there is
// none to compare.
func (x irGoIndex) at(k irKey, name string) ([]irGoFunc, string) {
	fs := x[k]
	if len(fs) == 0 {
		return nil, "no Go IR body at the position"
	}
	var same []irGoFunc
	for _, f := range fs {
		if f.name == name {
			same = append(same, f)
		}
	}
	if len(same) == 0 {
		return nil, "Go IR bodies of another name at the position"
	}
	return same, ""
}

// goIRFuncs lowers each example program with the Go IR builder and
// indexes its IR files' functions by declaration position, file indices
// mapped to source paths.
// The folders of the builds' root packages are the third result.
func goIRFuncs(t *testing.T, paths irPathIDs) (irGoIndex, map[string]string, map[string]bool) {
	t.Helper()
	idx := irGoIndex{}
	std := stdDir()
	type program struct {
		u      lower.Unit
		tests  bool
		dir    string
		module string
		reach  []*check.Func
	}
	unlowered := map[string]string{}
	var programs []program
	covered := map[string]bool{}
	for _, dir := range irPrograms(t) {
		u, err := irspike.Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		programs = append(programs, program{u: u, module: moduleRoot(dir), reach: ownFuncs(u, covered)})
	}
	// A package no program imports is lowered in its test build (the IR
	// census's test targets), its functions reached alike.
	_, targets, err := irspike.ExampleTargets(repoRoot())
	if err != nil {
		t.Fatal(err)
	}
	// The corpus's own fixture (testdata/bind) is lowered alike; a folder
	// the Go front end does not load is logged as one it does not lower.
	fixture := filepath.Join(repoRoot(), "internal", "selfhostcheck", "testdata", "bind")
	for _, dir := range append(targets, fixture) {
		if covered[dir] || filepath.Base(filepath.Dir(dir)) == "cmd" {
			continue
		}
		u, err := irspike.Load(dir)
		if err != nil && dir == fixture {
			unlowered[dir] = err.Error()
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		programs = append(programs, program{u: u, tests: true, dir: dir, module: moduleRoot(dir), reach: ownFuncs(u, covered)})
	}
	// Every std body, reached by a program that calls none (the IR
	// census's std domain).
	funcs, err := irspike.StdFuncs()
	if err != nil {
		t.Fatal(err)
	}
	a := sketchfront.Check("func main() {}\n")
	su, err := sketchfront.IRSpikeUnit(a, filepath.Join(t.TempDir(), "std.ergo"))
	if err != nil {
		t.Fatal(err)
	}
	programs = append(programs, program{u: su, module: filepath.Dir(su.Sources[0].Path), reach: funcs})
	roots := map[string]bool{}
	for _, pr := range programs[:len(programs)-1] { // the std program's root is no corpus folder
		roots[filepath.Dir(pr.u.Sources[0].Path)] = true
	}
	for _, pr := range programs {
		out, _, _, err := lower.LowerIRSplitReaching(pr.u, pr.tests, pr.reach)
		if err != nil && pr.tests {
			// A test build the Go lowerer refuses (an internal compiler
			// error of its own) has no IR to compare; it is logged.
			unlowered[pr.dir] = err.Error()
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", pr.module, err)
		}
		module := pr.module
		for _, file := range out {
			if filepath.Base(file.Path) != lower.IRFileName {
				continue
			}
			var f irFile
			if err := json.Unmarshal(file.Data, &f); err != nil {
				t.Fatalf("%s: %v", file.Path, err)
			}
			abs := make([]string, len(f.Files))
			for i, p := range f.Files {
				if rest, ok := strings.CutPrefix(p, "std/"); ok {
					abs[i] = filepath.Join(std, rest)
				} else {
					abs[i] = filepath.Join(module, p)
				}
			}
			for i := range f.Funcs {
				fn := &f.Funcs[i]
				if err := irMapFiles(fn, abs, paths); err != nil {
					t.Fatalf("%s: %s: %v", file.Path, fn.Name, err)
				}
				raw := fn.Name
				fn.Name = irSplitName(raw)
				irBaseFields(fn)
				data, err := json.Marshal(fn)
				if err != nil {
					t.Fatal(err)
				}
				k := irKey{file: fn.Pos[0], line: fn.Pos[1], col: fn.Pos[2]}
				seen := false
				for _, g := range idx[k] {
					seen = seen || g.name == fn.Name && bytes.Equal(g.data, data)
				}
				if !seen {
					idx[k] = append(idx[k], irGoFunc{name: fn.Name, raw: raw, data: data})
				}
			}
		}
	}
	return idx, unlowered, roots
}

// ownFuncs is every function with a body of u's packages and their
// imports that are not std, in import order; covered records their
// folders.
func ownFuncs(u lower.Unit, covered map[string]bool) []*check.Func {
	var out []*check.Func
	seen := map[*check.Package]bool{}
	var walk func(u lower.Unit)
	walk = func(u lower.Unit) {
		for _, i := range u.Imports {
			walk(i)
		}
		p := u.Package
		if p == nil || p.Std() || seen[p] {
			return
		}
		seen[p] = true
		for _, s := range u.Sources {
			covered[filepath.Dir(s.Path)] = true
		}
		for _, f := range p.Funcs {
			if f.Decl != nil && f.Decl.Body != nil {
				out = append(out, f)
			}
		}
	}
	walk(u)
	return out
}

// moduleRoot is the folder of the ergo.mod that holds dir.
func moduleRoot(dir string) string {
	for d := dir; d != filepath.Dir(d); d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, "ergo.mod")); err == nil {
			return d
		}
	}
	return dir
}

// irMapFiles sets the file index of every position of fn to its path's id.
func irMapFiles(fn *irFunc, files []string, paths irPathIDs) error {
	var err error
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem())
			}
		case reflect.Slice:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Array:
			if v.Type() != reflect.TypeFor[irPos]() {
				return
			}
			p := v.Addr().Interface().(*irPos)
			if *p == (irPos{}) {
				return // Go's zero position names no file
			}
			if p[0] < 0 || p[0] >= len(files) {
				err = fmt.Errorf("position %v names no file of %d", *p, len(files))
				return
			}
			p[0] = paths.id(files[p[0]])
		}
	}
	walk(reflect.ValueOf(fn))
	if err != nil {
		return err
	}
	return nil
}
