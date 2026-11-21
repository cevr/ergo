package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"ergo.local/prototype/scripts/gate/replay"
	"ergo.local/prototype/scripts/mutants/covfile"
)

// affectedSeedVariable replays one seed of TestAffectedAgreesWithAFullRun.
const affectedSeedVariable = "ERGO_GATE_AFFECTED_SEED"

// The round tier's selection (pickRound) holds every test whose verdict a
// change can move: a seeded model-based property over a generated module.
// The module has a test package m/a, its covered dependencies m/b and m/c,
// a dependency m/u the segment does not cover, a package m/x outside the
// closure, data files under m/a/testdata (some read by the test process,
// which its log observes, some only by a program a test starts, which it
// does not), and tests that call functions of each package and may read a
// Go file as data. A test's verdict is a digest of what it depends on: the
// text of each function it calls, every declaration of the compiled
// packages, the files it reads, and its own source. The segment is the
// model's coverage pass at the base: the body blocks of the functions each
// test calls in the covered packages, a read block for each observed read,
// some tests left out (a suite of named tests), one failed; a segment of
// mode short records only some of the calls. After a seeded edit script
// (a body, a declaration, an added function, a deleted or added file, a
// data file, a file outside the closure, the test file, a body of an
// uncovered package, a Go file read as data) the tests whose verdict
// differs between base and head, the unknown and the failed ones included,
// are a subset of the selection. A failing seed prints the module, the
// edits, both sets and the replay command.
//
// Mutants (testdata/mutants.json): round-read-ignored,
// round-deleted-file-selects-none, round-unknown-test-skipped,
// round-short-segment-used, round-uncovered-package-placed,
// round-blocks-ignored.
func TestAffectedAgreesWithAFullRun(t *testing.T) {
	seeds := 400
	first := 0
	if s := os.Getenv(affectedSeedVariable); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			t.Fatalf("%s=%q: %v", affectedSeedVariable, s, err)
		}
		first, seeds = n, 1
	}
	selective := 0
	for seed := first; seed < first+seeds; seed++ {
		m := newRoundModel(seed)
		base := maps.Clone(m.files)
		entry, seg := m.segment(base)
		edits := m.edit()
		head := m.files
		var changes []fileChange
		for _, p := range slices.Sorted(maps.Keys(unionKeys(base, head))) {
			old, was := base[p]
			now, is := head[p]
			if was && is && old == now {
				continue
			}
			changes = append(changes, fileChange{Path: p, Old: []byte(old), New: []byte(now), Was: was, Is: is})
		}
		names := m.testNames(head)
		pick := pickRound(entry, seg, m.closure(), changes, names)
		var differ []string
		for _, name := range names {
			tm := m.tests[name]
			known := seg.Lookup("a:"+name) != nil
			if !known || seg.Lookup("a:"+name).Failed || tm == nil || m.verdict(base, tm) != m.verdict(head, tm) {
				differ = append(differ, name)
			}
		}
		var missed []string
		for _, name := range differ {
			if _, ok := pick.run[name]; pick.all == "" && !ok {
				missed = append(missed, name)
			}
		}
		if pick.all == "" && len(pick.run) < len(names) {
			selective++
		}
		if len(missed) > 0 {
			var b strings.Builder
			for _, p := range slices.Sorted(maps.Keys(base)) {
				fmt.Fprintf(&b, "--- %s (base)\n%s\n", p, base[p])
			}
			t.Fatalf("seed %d: the selection misses %v\nedits: %v\nverdict differs: %v\nselected: %v (all: %q)\nsegment mode %s, known tests %v\n%s\nreplay: %s=%d go test -count=1 -run '^TestAffectedAgreesWithAFullRun$' ./scripts/gate",
				seed, missed, edits, differ, slices.Sorted(maps.Keys(pick.run)), pick.all, entry.Mode, testIDs(seg), b.String(), affectedSeedVariable, seed)
		}
	}
	// A selection that runs every test passes the subset law trivially:
	// the property must also see selections that skip.
	if seeds > 1 && selective < seeds/10 {
		t.Fatalf("only %d of %d seeds selected fewer than every test", selective, seeds)
	}
}

func unionKeys(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func testIDs(c *covfile.Coverage) []string {
	var out []string
	for _, t := range c.Tests {
		out = append(out, t.ID)
	}
	return out
}

// roundModel is a generated module and its tests' dependencies.
type roundModel struct {
	r     *rand.Rand
	files map[string]string
	tests map[string]*modelTest
	short bool
	next  int
}

type modelTest struct {
	calls    [][2]string // file, function
	reads    []string    // files the test process opens
	children []string    // files a program the test starts reads
}

var modelPackages = []string{"m/a", "m/b", "m/c", "m/u", "m/x"}

func newRoundModel(seed int) *roundModel {
	m := &roundModel{r: rand.New(rand.NewPCG(uint64(seed), 7)), files: map[string]string{}, tests: map[string]*modelTest{}}
	for _, pkg := range modelPackages {
		for f := range 1 + m.r.IntN(2) {
			var b strings.Builder
			fmt.Fprintf(&b, "package %s\n\nvar V%d = %d\n", path.Base(pkg), f, m.r.IntN(9))
			for range 2 + m.r.IntN(3) {
				b.WriteString(m.function())
			}
			m.files[fmt.Sprintf("%s/f%d.go", pkg, f)] = b.String()
		}
	}
	for d := range 3 {
		m.files[fmt.Sprintf("m/a/testdata/d%d.txt", d)] = strconv.Itoa(m.r.IntN(9))
	}
	m.files["m/x/notes.txt"] = "x"
	var funcs [][2]string
	for _, p := range slices.Sorted(maps.Keys(m.files)) {
		if strings.HasPrefix(p, "m/x/") {
			continue // outside the test's closure: it cannot call it
		}
		for _, name := range funcNames(m.files[p]) {
			funcs = append(funcs, [2]string{p, name})
		}
	}
	var src strings.Builder
	src.WriteString("package a\n\nimport \"testing\"\n")
	for i := range 4 + m.r.IntN(5) {
		name := fmt.Sprintf("Test%d", i)
		tm := &modelTest{}
		for _, f := range funcs {
			if m.r.IntN(3) == 0 {
				tm.calls = append(tm.calls, f)
			}
		}
		for d := range 3 {
			switch m.r.IntN(4) {
			case 0:
				tm.reads = append(tm.reads, fmt.Sprintf("m/a/testdata/d%d.txt", d))
			case 1:
				tm.children = append(tm.children, fmt.Sprintf("m/a/testdata/d%d.txt", d))
			}
		}
		if m.r.IntN(4) == 0 {
			tm.reads = append(tm.reads, "m/b/f0.go") // reads a Go file as data
		}
		m.tests[name] = tm
		fmt.Fprintf(&src, "\nfunc %s(t *testing.T) {\n\t_ = %d\n}\n", name, m.r.IntN(9))
	}
	m.files["m/a/a_test.go"] = src.String()
	m.short = m.r.IntN(8) == 0
	return m
}

func (m *roundModel) function() string {
	m.next++
	return fmt.Sprintf("\nfunc F%d() int {\n\tx := %d\n\treturn x\n}\n", m.next, m.r.IntN(9))
}

func (m *roundModel) closure() *replay.Closure {
	return &replay.Closure{Dir: "m/a", Deps: []string{"m/a", "m/b", "m/c", "m/u"}}
}

// funcNames are the functions a source declares, or none when it does not
// parse.
func funcNames(src string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range f.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok {
			out = append(out, fn.Name.Name)
		}
	}
	return out
}

// pieces splits a Go source into its functions' text, by name, and the
// rest (its declarations).
func pieces(src string) (map[string]string, string, bool) {
	set := token.NewFileSet()
	f, err := parser.ParseFile(set, "", src, parser.SkipObjectResolution)
	if err != nil {
		return nil, "", false
	}
	funcs := map[string]string{}
	rest := src
	for i := len(f.Decls) - 1; i >= 0; i-- {
		fn, ok := f.Decls[i].(*ast.FuncDecl)
		if !ok {
			continue
		}
		lo, hi := set.Position(fn.Pos()).Offset, set.Position(fn.End()).Offset
		funcs[fn.Name.Name] = src[lo:hi]
		rest = rest[:lo] + rest[hi:]
	}
	return funcs, rest, true
}

// verdict is the digest of what tm depends on in tree.
func (m *roundModel) verdict(tree map[string]string, tm *modelTest) string {
	h := sha256.New()
	// Every declaration and the set of files of the compiled packages.
	for _, p := range slices.Sorted(maps.Keys(tree)) {
		dir := path.Dir(p)
		if !slices.Contains(m.closure().Deps, dir) || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			continue
		}
		_, rest, ok := pieces(tree[p])
		fmt.Fprintf(h, "file %s %v %s\n", p, ok, rest)
	}
	for _, c := range tm.calls {
		src, ok := tree[c[0]]
		funcs, _, parsed := pieces(src)
		fmt.Fprintf(h, "call %s %s %v %v %s\n", c[0], c[1], ok, parsed, funcs[c[1]])
	}
	for _, p := range append(slices.Clone(tm.reads), tm.children...) {
		src, ok := tree[p]
		fmt.Fprintf(h, "read %s %v %s\n", p, ok, src)
	}
	for name, test := range m.tests {
		if test == tm {
			funcs, _, _ := pieces(tree["m/a/a_test.go"])
			fmt.Fprintf(h, "self %s\n", funcs[name])
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (m *roundModel) testNames(tree map[string]string) []string {
	return funcNames(tree["m/a/a_test.go"])
}

// segment is the model's coverage pass over tree: its index entry and
// coverage. Some tests are left out, one may fail, and a short segment
// records only some calls.
func (m *roundModel) segment(tree map[string]string) (covfile.Entry, *covfile.Coverage) {
	entry := covfile.Entry{Suite: "a", Package: "./m/a", Cover: []string{"./m/a", "./m/b", "./m/c"}, Mode: covfile.Mode, Key: "k", Commit: "c"}
	if m.short {
		entry.Mode = "short"
	}
	c := &covfile.Coverage{Files: map[string][]covfile.Block{}}
	add := func(file string, b covfile.Block) {
		for i, old := range c.Files[file] {
			if old.Start == b.Start && old.End == b.End {
				c.Files[file][i].Tests = append(c.Files[file][i].Tests, b.Tests...)
				return
			}
		}
		c.Files[file] = append(c.Files[file], b)
	}
	failed := m.r.IntN(3) == 0
	for _, name := range slices.Sorted(maps.Keys(m.tests)) {
		if m.r.IntN(6) == 0 {
			continue // not in the segment: a suite of named tests
		}
		tm := m.tests[name]
		i := len(c.Tests)
		c.Tests = append(c.Tests, &covfile.Test{ID: "a:" + name, Failed: failed})
		failed = false
		for _, call := range tm.calls {
			if !slices.Contains(entry.Cover, "./"+path.Dir(call[0])) || m.short && m.r.IntN(2) == 0 {
				continue
			}
			set := token.NewFileSet()
			f, err := parser.ParseFile(set, "", tree[call[0]], parser.SkipObjectResolution)
			if err != nil {
				continue
			}
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == call[1] {
					add(call[0], covfile.Block{Start: set.Position(fn.Body.Lbrace).Line, End: set.Position(fn.Body.Rbrace).Line, Tests: []int{i}})
				}
			}
		}
		for _, p := range tm.reads {
			add(p, covfile.Block{Start: covfile.ReadBlock, End: covfile.ReadBlock, Tests: []int{i}})
		}
	}
	for _, blocks := range c.Files {
		sort.Slice(blocks, func(i, j int) bool { return blocks[i].Start < blocks[j].Start })
	}
	c.Reindex()
	return entry, c
}

// edit applies one to three seeded edits to m.files and names them.
func (m *roundModel) edit() []string {
	var done []string
	for range 1 + m.r.IntN(3) {
		goFiles := slices.DeleteFunc(slices.Sorted(maps.Keys(m.files)), func(p string) bool {
			return !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go")
		})
		p := goFiles[m.r.IntN(len(goFiles))]
		src := m.files[p]
		switch k := m.r.IntN(10); k {
		case 0, 1, 2, 3: // a body: one function's value
			names := funcNames(src)
			if len(names) == 0 {
				continue
			}
			funcs, _, _ := pieces(src)
			name := names[m.r.IntN(len(names))]
			old := funcs[name]
			now := strings.Replace(old, "x := ", fmt.Sprintf("x := %d + ", 1+m.r.IntN(9)), 1)
			m.files[p] = strings.Replace(src, old, now, 1)
			done = append(done, "body "+p+" "+name)
		case 4: // a declaration
			m.files[p] = strings.Replace(src, " = ", fmt.Sprintf(" = %d + ", 1+m.r.IntN(9)), 1)
			done = append(done, "declaration "+p)
		case 5: // an added function, or a deleted file, or an added one
			switch m.r.IntN(3) {
			case 0:
				m.files[p] = src + m.function()
				done = append(done, "added function "+p)
			case 1:
				delete(m.files, p)
				done = append(done, "deleted "+p)
			case 2:
				dir := modelPackages[m.r.IntN(len(modelPackages))]
				name := fmt.Sprintf("%s/new%d.go", dir, m.r.IntN(100))
				m.files[name] = "package " + path.Base(dir) + "\n" + m.function()
				done = append(done, "added "+name)
			}
		case 6: // a data file
			d := fmt.Sprintf("m/a/testdata/d%d.txt", m.r.IntN(3))
			m.files[d] += "+"
			done = append(done, "data "+d)
		case 7: // a file outside the closure
			m.files["m/x/notes.txt"] += "+"
			done = append(done, "outside m/x/notes.txt")
		case 8: // the test file
			names := m.testNames(m.files)
			name := names[m.r.IntN(len(names))]
			m.files["m/a/a_test.go"] = strings.Replace(m.files["m/a/a_test.go"], "func "+name+"(t *testing.T) {\n\t_ = ", "func "+name+"(t *testing.T) {\n\t_ = 1 + ", 1)
			done = append(done, "test "+name)
		case 9: // a comment inside a body of the file read as data
			src := m.files["m/b/f0.go"]
			if src == "" {
				continue
			}
			names := funcNames(src)
			if len(names) == 0 {
				continue
			}
			funcs, _, _ := pieces(src)
			old := funcs[names[m.r.IntN(len(names))]]
			m.files["m/b/f0.go"] = strings.Replace(src, old, strings.Replace(old, "\treturn x", "\t// note\n\t// (two lines: a later body moves past the diff's one-line margin)\n\treturn x", 1), 1)
			done = append(done, "comment m/b/f0.go")
		}
	}
	return done
}
