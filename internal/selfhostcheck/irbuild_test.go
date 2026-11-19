package selfhostcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/experimental/irspike"
	"ergo.local/prototype/internal/sketch/lower"
	"ergo.local/prototype/internal/testwait"
)

// TestCompilerIRAgreesWithBuilder holds the IR builder written in Ergo
// (compiler/lower, capstone S4c) to the Go IR builder (internal/sketch/
// lower): every function compiler/lower builds over the corpus (each
// folder of the examples and of std, typed by compiler/check as
// TestCompilerExprAgreesWithChecker types them) must be, byte for byte,
// the function the Go builder writes at the same declaration in the IR of
// every example program that holds it (lower.LowerIRSplit). The two
// sides' file indices are mapped to the files' paths first; the bytes are
// otherwise the IR file's (ir.WriteFunc against encoding/json).
//
// compiler/lower stops at a function it cannot build yet: an expression
// check/expr left untyped, or a form or a decider it has not ported (the
// S4c plan's slices). The reach is logged by cause, never pinned: it
// grows as compiler/check types more and compiler/lower ports more, with
// no change here. A function built with no Go function at its position
// (the Go builder left it to the emitter, or no program reaches it) is
// logged too; a Go function of another name there is logged, and one
// whose name differs only by the Go builder's collision suffix (_N) is
// compared with that name.
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
	goFuncs := goIRFuncs(t, paths)

	// The Ergo side: exprdump's manifest, one line per corpus folder.
	tmp := t.TempDir()
	dirs := corpusDirs(t)
	type side struct {
		ok           bool
		path         string
		ext, foreign []string
	}
	sides := make([]side, len(dirs))
	next := make(chan int)
	var wg sync.WaitGroup
	for range runtime.GOMAXPROCS(0) {
		wg.Go(func() {
			for i := range next {
				pkg := typesPackage(dirs[i])
				if pkg == nil {
					continue
				}
				_, ext := goKinds(pkg)
				_, foreign := goTypes(pkg)
				sides[i] = side{ok: true, path: importPath(dirs[i], pkg), ext: ext, foreign: foreign}
			}
		})
	}
	for i := range dirs {
		next <- i
	}
	close(next)
	wg.Wait()
	var manifest []string
	for i, s := range sides {
		if !s.ok {
			continue
		}
		e, f := filepath.Join(tmp, fmt.Sprintf("ext%d", i)), filepath.Join(tmp, fmt.Sprintf("foreign%d", i))
		writeLines(t, e, s.ext)
		writeLines(t, f, s.foreign)
		manifest = append(manifest, strings.Join(append([]string{s.path, e, f}, ergoFiles(t, dirs[i])...), " "))
	}
	m := filepath.Join(tmp, "manifest")
	writeLines(t, m, manifest)

	reach := map[string]int{}
	built, failed := 0, 0
	var files []string
	for _, l := range runDump(t, ctx, bin, "-std", stdDir(), "-corpus", m) {
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
			want, cause := goFuncs.at(key, fn.Name)
			if cause != "" {
				reach["built, "+cause]++
				continue
			}
			if want.name != fn.Name {
				reach["built, compared under the Go name (_N)"]++
				fn.Name = want.name
				got, _ = json.Marshal(&fn)
			}
			if !bytes.Equal(got, want.data) {
				failed++
				if failed <= 3 {
					t.Errorf("%s:%s %s: compiler/lower builds another IR body\n ergo: %s\n   go: %s", rel(f[0]), f[1], fn.Name, got, want.data)
				}
				continue
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

type irGoFunc struct {
	name string
	data []byte
}

// irGoIndex is the Go builder's functions by declaration position, each
// the distinct texts written there across the programs.
type irGoIndex map[irKey][]irGoFunc

var irCollision = regexp.MustCompile(`_[0-9]+$`)

// at is the Go function at k that the Ergo side's name names: of that
// name, or of it with a collision suffix; cause says why there is none to
// compare.
func (x irGoIndex) at(k irKey, name string) (irGoFunc, string) {
	fs := x[k]
	if len(fs) == 0 {
		return irGoFunc{}, "no Go IR body at the position"
	}
	var same []irGoFunc
	for _, f := range fs {
		if f.name == name {
			same = append(same, f)
		}
	}
	if len(same) == 0 {
		for _, f := range fs {
			if irCollision.ReplaceAllString(f.name, "") == name {
				same = append(same, f)
			}
		}
	}
	if len(same) == 0 {
		return irGoFunc{}, "Go IR bodies of another name at the position"
	}
	for _, f := range same[1:] {
		if !bytes.Equal(f.data, same[0].data) {
			return irGoFunc{}, "Go IR body differs between programs"
		}
	}
	return same[0], ""
}

// goIRFuncs lowers each example program with the Go IR builder and
// indexes its IR files' functions by declaration position, file indices
// mapped to source paths.
func goIRFuncs(t *testing.T, paths irPathIDs) irGoIndex {
	t.Helper()
	idx := irGoIndex{}
	std := stdDir()
	for _, dir := range irPrograms(t) {
		u, err := irspike.Load(dir)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		out, _, _, err := lower.LowerIRSplit(u, false)
		if err != nil {
			t.Fatalf("%s: %v", dir, err)
		}
		module := filepath.Dir(filepath.Dir(dir)) // examples/<m>/cmd/<p>
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
					idx[k] = append(idx[k], irGoFunc{name: fn.Name, data: data})
				}
			}
		}
	}
	return idx
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
