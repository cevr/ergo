package closure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"ergo.local/prototype/internal/testinputs"
	"ergo.local/prototype/internal/testinputs/testenv"
)

// allowance is one declared input of a test package outside the directories
// of its dependencies and its own testdata, with the reason it is an input.
// The path is clean, relative to the module root, with forward slashes. The
// grant says which accesses at that path it covers; each grant is as narrow
// as the measured reads allow, so a listing never grants the files below
// it and a flat pattern never grants a subdirectory.
type allowance struct {
	path   string
	grant  grant
	reason string
}

// grant is the kind of access an allowance covers.
type grant int

const (
	// file covers any access to exactly this path.
	file grant = iota + 1
	// listing covers a lookup (stat) or a listing (open) of this
	// directory, and nothing in it.
	listing
	// files covers any access to a path the pattern matches (path.Match:
	// "*" and "?" never match "/"), so docs/x/*.ergo covers docs/x/a.ergo
	// and neither docs/x/a.txt, docs/x/sub/a.ergo nor the directory docs/x.
	files
	// subtree covers this directory and every path below it, at any depth,
	// with any access; "." is the whole tree. Only a measured recursive
	// walk earns it.
	subtree
	// program names the directory of a package of the module whose
	// executable the tests build: it covers what the go command reads to
	// build it, the directories of `go list -deps` for it.
	program
)

func (g grant) String() string {
	switch g {
	case file:
		return "file"
	case listing:
		return "listing"
	case files:
		return "files"
	case subtree:
		return "subtree"
	case program:
		return "program"
	}
	return fmt.Sprintf("grant(%d)", int(g))
}

// declared are the inputs each test package may record outside its own
// closure, by package directory. Keep it short: an entry is a package that
// reruns when its path changes, so each one needs a reason the test cannot
// avoid. The sweep fails on an entry no recorded input uses.
//
// Each entry has the narrowest grant the measured reads allow (sweep logs):
// a glob is declared as glob declares it, a read by name as a file, and a
// subtree only where the test walks the tree and the reads go deeper than
// one level.
var declared = map[string][]allowance{
	"internal/residue": {
		{".", subtree, "the residue checks read every tracked file by design: git grep, git ls-files and Go's reads of each file"},
	},
	"internal/testinputs/guard": {
		{".", subtree, "the guard lists every directory and reads every test file, so a test package added anywhere reruns it"},
	},
	"internal/sketch/check": declare(
		glob("internal/sketch/testdata/*.ergo", "the corpus globs (check_test.go corpus, info_test.go, round19_test.go) check every source of the shared corpus"),
		glob("internal/sketch/testdata/*/*.ergo", "the corpus globs check every source of the shared corpus one directory down"),
		glob("internal/sketch/syntax/testdata/corpus/*/*.ergo", "the corpus globs check every sketch-eval source"),
		glob("internal/sketch/syntax/testdata/corpus/*/*/*.ergo", "the corpus globs check every sketch-eval source one directory down"),
		glob("examples/*/*/*.ergo", "round19 checks every example program"),
		glob("docs/research/ef-migrate/*.ergo", "round19 checks the migrated examples"),
		[]allowance{{"docs/spec/diagnostics.md", file, "TestRegistry: the spec's code rows and the code table agree"}},
		glob("internal/sketch/lower/*.go", "TestTupleAssertionsUnwrap (C45) parses the lowering's sources for type assertions to *Tuple"),
	),
	"internal/sketch/lower": declare(
		[]allowance{
			{"cmd/ergo", program, "the tests drive the public CLI, built once per process"},
			{"internal/sketch/check/testdata", subtree, "the accepted and refusal inventories walk the checker's fixtures (filepath.WalkDir), lowering each"},
			{"internal/sketch/testdata", subtree, "the accepted and refusal inventories and TestCorpusProbesRun walk the shared corpus (filepath.WalkDir), lowering each"},
			{"examples", subtree, "the accepted and refusal inventories walk the example programs (filepath.WalkDir), lowering each"},
			{"docs/spec/support.md", file, "TestSupportTableIsCurrent compares the support table with the one the family inventory generates"},
			// TestNoStageERefusal (C43) walks internal/sketch, skipping
			// testdata, and parses each source: the packages lower builds
			// on are its inputs already; these are the rest, at the depths
			// the walk reads them.
			{"internal/sketch", listing, "TestNoStageERefusal (C43) walks the compiler's code below internal/sketch"},
		},
	),
	"internal/sketch/syntax": declare(
		glob("internal/sketch/testdata/*.ergo", "decidedInputs parses every source of the shared corpus"),
		glob("internal/sketch/testdata/*/*.ergo", "decidedInputs parses every source of the shared corpus one directory down"),
		[]allowance{{"internal/migrate196/testdata/old", subtree, "the old spellings must stop parsing: decided_test.go walks them (filepath.WalkDir)"}},
		glob("docs/spec/*.md", "every go fence of the language spec parses (sketchtest.SpecFences); grammar.md is read by name too"),
		glob("docs/research/ef-migrate/*.ergo", "the migrated examples parse (sketchtest.Repository)"),
		[]allowance{
			{"examples/editions/sketch/main.ergo", file, "the 2026 example program parses (sketchtest.Repository)"},
			{"docs/previews/2026-10-28-without-go-syntax.md", file, "the decided syntax matches the preview it was decided from"},
		},
	),
	"internal/sketch/format": declare(
		[]allowance{{"internal/sketch/syntax/testdata/corpus", subtree, "the formatter round-trips the parser's corpus: sketchtest.Corpus walks it (filepath.WalkDir)"}},
		glob("internal/sketch/syntax/testdata/decided/*.ergo", "the formatter round-trips the decided-syntax fixtures"),
		glob("internal/sketch/syntax/testdata/edge/*.ergo", "the formatter round-trips the edge fixtures"),
		glob("docs/spec/*.md", "the formatter round-trips every go fence of the language spec (sketchtest.SpecFences)"),
		glob("docs/research/ef-migrate/*.ergo", "the formatter round-trips the migrated examples (sketchtest.Repository)"),
		[]allowance{{"examples/editions/sketch/main.ergo", file, "the formatter round-trips the 2026 example program (sketchtest.Repository)"}},
	),
	"cmd/ergo": {
		{"README.md", file, "the README smoke test runs the README's go blocks and commands"},
		{"examples/checkout", subtree, "the README smoke test compares the README's first two go blocks with the checkout example's files and copies the program, a module of packages, into a workspace (smokeWorkspace walks it)"},
		{"examples/editions/sketch", listing, "the process and README smoke tests copy the 2026 example into a workspace (smokeWorkspace)"},
		{"examples/editions/sketch/*", files, "smokeWorkspace copies each file of the 2026 example; it has no subdirectory, so the copy reads one level"},
	},
	"internal/sketchfront": {
		{"examples/editions/sketch/main.ergo", file, "TestSketchBuildRunsProgram and the lease test build the 2026 example program"},
		{"examples/editions/sketch/expected.out", file, "TestSketchBuildRunsProgram compares the run with the expected output"},
	},
	"internal/testinputs": declare(
		[]allowance{
			{"internal/testinputs/guard/guard.go", file, "the end-to-end test copies the guard into a scratch module"},
			{"internal/testinputs/guard/guard_test.go", file, "the end-to-end test copies the guard into a scratch module"},
		},
		exactly("the observer's own tests run unobserved (unobservable), so what the end-to-end test's scratch modules read through their link to the package has no identity",
			"internal/testinputs", "internal/testinputs/guard", "internal/testinputs/testenv",
			"internal/testinputs/identity.go", "internal/testinputs/identity_linux.go",
			"internal/testinputs/observe_linux.go", "internal/testinputs/observe_other.go",
			"internal/testinputs/recorder.go", "internal/testinputs/sys_linux_amd64.go",
			"internal/testinputs/sys_linux_arm64.go", "internal/testinputs/testinputs.go",
			"internal/testinputs/testenv/testenv.go"),
	),
	"scripts/conformance": {
		{"conformance/effect-cases.json", file, "the mapping reads the Effect case list"},
		{"conformance/upstream/foldkit-d21db423", subtree, "the mapping walks the pinned foldkit evidence at every depth (follow-up: outside this unit's Owns)"},
		{".git", file, "the mapping asks git whether evidence is tracked (follow-up: outside this unit's Owns)"},
	},
	"scripts/gate": {
		{".", subtree, "gate-speed2 owns scripts/gate (do not touch); its tests build in the module with VCS stamping and walk the tree (follow-up)"},
	},
}

// glob declares what filepath.Glob(pattern) reads, as Go's Glob reads it:
// a lookup and a listing of the directory above the first element with a
// pattern; a lookup of each match of that element and of each further one
// but the last, and a listing of each that is a directory; and the files
// the whole pattern matches, which the test then reads. Each pattern
// matches at one depth, so nothing below the last element is granted.
func glob(pattern, reason string) []allowance {
	elems := strings.Split(pattern, "/")
	first := slices.IndexFunc(elems, func(e string) bool { return strings.ContainsAny(e, `*?[\`) })
	if first < 1 {
		panic("glob " + pattern + ": want a pattern below a directory")
	}
	out := []allowance{{strings.Join(elems[:first], "/"), listing, reason}}
	for j := first; j < len(elems)-1; j++ {
		out = append(out, allowance{strings.Join(elems[:j+1], "/"), listing, reason})
	}
	return append(out, allowance{pattern, files, reason})
}

// declare joins entries and drops a repeat of an entry (the same grant and
// path), keeping the first reason.
// exactly declares one file grant per path, with one reason: the exact
// entries that cover an input with no identity.
func exactly(reason string, paths ...string) []allowance {
	out := make([]allowance, len(paths))
	for i, p := range paths {
		out[i] = allowance{p, file, reason}
	}
	return out
}

// unobservable are the test packages the observer cannot observe, with the
// reason: the sweep gives them no identity file, so each recorded input of
// theirs in the checkout fails closed (unidentified) unless an exact
// declared entry covers it.
var unobservable = map[string]string{
	// Measured: under testinputs.Main, its tests' own observers fail with
	// "seccomp(SECCOMP_SET_MODE_FILTER): device or resource busy" (Linux
	// allows one user-notification listener in a filter chain).
	"internal/testinputs": "the observer's own tests install seccomp listeners, and a second listener in one filter chain is refused (EBUSY)",
}

func declare(parts ...[]allowance) []allowance {
	var out []allowance
	seen := map[string]bool{}
	for _, part := range parts {
		for _, a := range part {
			if !seen[a.key()] {
				seen[a.key()] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// everyPackage are the inputs any test package may record: the go command
// reads them for every build in the module, and a test binary depends on
// them already.
var everyPackage = []allowance{
	{"go.mod", file, "the go command reads the module file for every build in the module"},
	{"go.sum", file, "the go command checks module sums for every build in the module"},
}

// moduleLookups are the names the go command (and a test that finds the
// module root) looks up in a directory to find a module's root and layout.
// A lookup of one, anywhere, records only whether it exists.
var moduleLookups = map[string]bool{"go.mod": true, "go.work": true, "go.work.sum": true, "vendor": true}

// closure is what one test package may record.
type closure struct {
	dir       string          // the package directory, relative to the module root
	deps      map[string]bool // the directories of its dependencies, its own included
	ancestors map[string]bool // every directory above a dependency, "." included
	embeds    map[string]bool // embedded files of its dependencies and their directories
	declared  []allowance
	programs  map[string]closure // the closure of each declared program, by its path
}

// entry is one recorded input: an operation and a path relative to the
// module root.
type entry struct{ op, path string }

// newClosure builds the closure of the package in dir from the directories
// of its dependencies and the files they embed, all relative to the root.
func newClosure(dir string, deps []string, embeds []string) closure {
	c := closure{dir: dir, deps: map[string]bool{dir: true}, ancestors: map[string]bool{}, embeds: map[string]bool{}, declared: declared[dir]}
	for _, d := range deps {
		c.deps[d] = true
	}
	for d := range c.deps {
		for a := path.Dir(d); ; a = path.Dir(a) {
			c.ancestors[a] = true
			if a == "." {
				break
			}
		}
	}
	for _, e := range embeds {
		c.embeds[e] = true
		for a := path.Dir(e); !c.deps[a] && a != "."; a = path.Dir(a) {
			c.embeds[a] = true
		}
	}
	return c
}

// check returns the recorded inputs outside the closure, and marks in used
// the declared entries an input falls under.
func (c closure) check(entries []entry, used map[string]bool) []entry {
	var outside []entry
	for _, e := range entries {
		if e.op == "getenv" || e.op == "chdir" {
			continue
		}
		if c.inside(e, used) {
			continue
		}
		outside = append(outside, e)
	}
	return outside
}

func (c closure) inside(e entry, used map[string]bool) bool {
	if strings.HasPrefix(e.op, opRaced) {
		return false // the access may have reached another object than the one named
	}
	if failsClosed(e.op) {
		// The walk could not tell what the path reached, or what it
		// reached changed: only a declared file entry that names it
		// exactly covers it.
		for _, a := range c.declared {
			if a.grant == file && a.path == e.path {
				used[a.key()] = true
				return true
			}
		}
		return false
	}
	p := e.path
	switch {
	case c.compiled(e):
		return true
	case p == c.dir+"/testdata" || strings.HasPrefix(p, c.dir+"/testdata/"):
		return true
	case e.op == opStat && moduleLookups[path.Base(p)]:
		// The go command looks for these where it finds a module's root
		// and layout; each is absent, and adding one changes the layout.
		return true
	}
	for _, a := range everyPackage {
		if covers(a, e) {
			return true
		}
	}
	for _, a := range c.declared {
		if prog, ok := c.programs[a.path]; a.grant == program && ok && prog.compiled(e) || covers(a, e) {
			used[a.key()] = true
			return true
		}
	}
	return false
}

// compiled reports whether the input is a file the go command reads to
// build the package's test binary: a file of a dependency's directory, the
// directory itself, a file a dependency embeds, or a lookup of a directory
// on the way to a dependency. Listing such a directory would be an input
// of every entry in it, so only a lookup passes.
func (c closure) compiled(e entry) bool {
	p := e.path
	return c.deps[p] || c.deps[path.Dir(p)] && p != "." || c.embeds[p] || c.ancestors[p] && e.op == opStat
}

// covers reports whether the declared entry a covers the resolved input e.
// A program entry covers nothing here: closure.inside asks its closure.
func covers(a allowance, e entry) bool {
	p := e.path
	match := func() bool {
		ok, err := path.Match(a.path, p)
		return ok && err == nil
	}
	switch a.grant {
	case file:
		return p == a.path
	case listing:
		return (e.op == opList || e.op == opStat) && match()
	case files:
		return (e.op == opOpen || e.op == opStat || e.op == opLink) && match()
	case subtree:
		return a.path == "." || p == a.path || strings.HasPrefix(p, a.path+"/")
	}
	return false
}

// key names the entry in the set of entries a sweep used.
func (a allowance) key() string { return a.grant.String() + " " + a.path }

// TestClosureClassifiesInputs pins the closure rule on a fixed package.
func TestClosureClassifiesInputs(t *testing.T) {
	c := newClosure("internal/a", []string{"internal/b", "cmd/c"}, []string{"internal/b/files/x.txt"})
	c.declared = []allowance{{"examples", subtree, "walks the examples"}, {"docs/one.md", file, "reads one page"}}
	in := []entry{
		{"open", "internal/a/a_test.go"},
		{"list", "internal/a"},
		{"open", "internal/b/b.go"},
		{"stat", "cmd/c"},
		{"open", "internal/b/files/x.txt"},
		{"list", "internal/b/files"},
		{"open", "internal/a/testdata/v1/x.txt"},
		{"stat", "internal/a/testdata"},
		{"stat", "internal"},
		{"stat", "."},
		{"stat", "cmd"},
		{"open", "go.mod"},
		{"open", "examples/game/main.ergo"},
		{"stat", "examples"},
		{"open", "docs/one.md"},
		{"unresolved open", "docs/one.md"},
		{"link", "internal/a/testdata/link"},
		{"stat", "internal/go.work"},
		{"stat", "runtime/rt/x.go/go.mod"},
		{"stat", "vendor"},
		{"getenv", "HOME"},
	}
	out := []entry{
		{"list", "."},                       // listing the root
		{"list", "internal"},                // listing an ancestor
		{"open", "internal/b/testdata/y"},   // another package's testdata
		{"open", "internal/b/files/z.txt"},  // not embedded
		{"open", "internal/ab/x.go"},        // a sibling whose name shares a prefix
		{"stat", "internal/a/testdatax/y"},  // not the testdata directory
		{"open", "internal/a/sub/y"},        // a subdirectory that is no dependency
		{"open", "docs/one.md.bak"},         // not the declared file
		{"open", "examplesx/a"},             // not the declared subtree
		{"stat", ".git"},                    // version-control state
		{"open", "conformance/upstream/x"},  // a tree walk
		{"stat", "scripts/gate/budget.go"},  // a git grep
		{"open", "internal/sketch/check/x"}, // an unrelated package
		{"open", "internal/go.work"},        // a module file read, not looked up
		{"list", "vendor"},                  // a vendor directory listed
		{"link", "internal/link"},           // a link in an ancestor
		// A lost walk, even where the package may read: only an exact
		// declared file covers it.
		{"unresolved open", "internal/a/testdata/x"},
		{"unresolved stat", "internal/a/testdata/gone/../x"},
		{"unresolved stat", "examples/game/main.ergo"},
	}
	used := map[string]bool{}
	if got := c.check(in, used); len(got) != 0 {
		t.Errorf("inputs inside the closure reported outside: %v", got)
	}
	if !used["subtree examples"] || !used["file docs/one.md"] {
		t.Errorf("declared entries not marked used: %v", used)
	}
	for _, e := range out {
		if got := c.check([]entry{e}, map[string]bool{}); len(got) != 1 {
			t.Errorf("%v: reported inside the closure", e)
		}
	}

	// A declared program covers what building it reads, and nothing else.
	c.declared = []allowance{{"cmd/p", program, "the tests build it"}}
	c.programs = map[string]closure{"cmd/p": newClosure("cmd/p", []string{"internal/q"}, nil)}
	used = map[string]bool{}
	if got := c.check([]entry{{"open", "cmd/p/main.go"}, {"open", "internal/q/q.go"}, {"stat", "cmd"}}, used); len(got) != 0 || !used["program cmd/p"] {
		t.Errorf("a program's sources reported outside the closure: %v (used %v)", got, used)
	}
	for _, e := range []entry{{"open", "cmd/p/testdata/x"}, {"open", "cmd/p/sub/x.go"}, {"list", "cmd"}, {"open", "internal/r/r.go"}} {
		if got := c.check([]entry{e}, map[string]bool{}); len(got) != 1 {
			t.Errorf("%v: a declared program covers it", e)
		}
	}
	c.programs = nil
	if got := c.check([]entry{{"open", "cmd/p/main.go"}}, map[string]bool{}); len(got) != 1 {
		t.Error("a program entry with no computed closure covers its path")
	}
}

// TestParseLogResolvesAndRefuses pins the log reading: relative paths
// resolve against the start directory and each chdir, paths outside the
// root drop, and a log testinputs left uncacheable is refused. The symbolic
// link cases are properties (resolve_property_test.go).
func TestParseLogResolvesAndRefuses(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "r")
	for _, f := range []string{"a/x.go", "b/b.go", "c/y", "x.go", "outside/f"} {
		name := filepath.Join(root, filepath.FromSlash(f))
		if f == "outside/f" {
			name = filepath.Join(base, "outside", "f")
		}
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	log := "# test log\ngetenv HOME\nopen x.go\nstat ../b\nchdir " + filepath.Join(root, "c") + "\nopen y\nopen " + filepath.Join(base, "outside", "f") + "\nopen " + filepath.Join(root, "x.go") + "\nstat absent\n"
	got, err := parseLog([]byte(log), filepath.Join(root, "a"), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	want := []entry{{"open", "a/x.go"}, {"stat", "b"}, {"open", "c/y"}, {"open", "x.go"}, {"stat", "c/absent"}}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if _, err := parseLog([]byte("# test log\nopen x\ntestinputs: unobserved: tracing is unavailable"), root, []string{root}); err == nil || !strings.Contains(err.Error(), "unobserved") {
		t.Errorf("an uncacheable log was read: %v", err)
	}
	if got, err := parseLog([]byte("# test log\n"), root, []string{root}); err != nil || len(got) != 0 {
		t.Errorf("a log with no input: %v, %v", got, err)
	}
	if _, err := parseLog([]byte("# test log\nopen x\n\n"), root, []string{root}); err == nil {
		t.Error("an empty line was read")
	}
	if _, err := parseLog(nil, root, []string{root}); err == nil {
		t.Error("an empty log was read")
	}
	if _, err := parseLog([]byte("# test log\nrename x\n"), root, []string{root}); err == nil {
		t.Error("an unknown operation was read")
	}
}

// TestDeclaredAllowanceIsWellFormed: every declared entry is a clean
// relative path with a known grant and a reason. Only a listing or a files
// entry has a pattern, and a pattern is path.Match's, so it matches at one
// depth only; a files entry has one in its last element. Only a subtree is
// the whole tree, and no entry is declared twice. The sweep checks that
// each one is used.
func TestDeclaredAllowanceIsWellFormed(t *testing.T) {
	check := func(owner string, a allowance) {
		p := a.path
		if p == "" || path.Clean(p) != p || path.IsAbs(p) || strings.HasPrefix(p, "../") || p == ".." || p == "." && a.grant != subtree {
			t.Errorf("%s: %q is not a clean path relative to the module root", owner, a.path)
		}
		const meta = `*?[\`
		switch {
		case a.grant < file || a.grant > program:
			t.Errorf("%s: %q has an unknown grant %v", owner, p, a.grant)
		case a.grant == files && !strings.ContainsAny(path.Base(p), meta):
			t.Errorf("%s: the files entry %q has no pattern in its last element; declare a file", owner, p)
		case a.grant != files && a.grant != listing && strings.ContainsAny(p, meta):
			t.Errorf("%s: the %v entry %q has a pattern", owner, a.grant, p)
		}
		if _, err := path.Match(p, ""); err != nil {
			t.Errorf("%s: %q: %v", owner, p, err)
		}
		if strings.TrimSpace(a.reason) == "" {
			t.Errorf("%s: %q has no reason", owner, a.path)
		}
	}
	for dir, list := range declared {
		if path.Clean(dir) != dir || path.IsAbs(dir) || strings.HasPrefix(dir, "..") {
			t.Errorf("declared package %q is not a clean relative directory", dir)
		}
		if len(list) == 0 {
			t.Errorf("declared package %s has no entry", dir)
		}
		seen := map[string]bool{}
		for _, a := range list {
			check(dir, a)
			if seen[a.key()] {
				t.Errorf("%s: %s is declared twice", dir, a.key())
			}
			seen[a.key()] = true
		}
	}
	for _, a := range everyPackage {
		check("every package", a)
	}
	// The checker and the lowerer must stay cached across an edit of an
	// unrelated document (speed plan U2): no entry of theirs covers it, nor
	// a listing of its directory, which an atomic save changes.
	for _, dir := range []string{"internal/sketch/check", "internal/sketch/lower"} {
		for _, a := range append(slices.Clone(everyPackage), declared[dir]...) {
			for _, e := range []entry{{opOpen, "docs/tooling.md"}, {opStat, "docs/tooling.md"}, {opOpen, "docs"}} {
				if covers(a, e) {
					t.Errorf("%s: %q (%v) covers %s %s, so an edit of docs/tooling.md reruns the package", dir, a.path, a.grant, e.op, e.path)
				}
			}
		}
	}
}

// Variables that drive the sweep. The sweep runs every test binary of the
// module, so the gate does not run it; it is the acceptance check of the
// closure rule (docs/tooling.md).
const (
	// sweepVariable selects the packages: "all", or a comma list of package
	// directories relative to the module root.
	sweepVariable = "ERGO_CLOSURE_SWEEP"
	// jobsVariable bounds how many test binaries build or run at once.
	jobsVariable = "ERGO_CLOSURE_JOBS"
	// keepVariable names a directory that keeps the binaries' logs and
	// output, and each package's inputs outside its closure.
	keepVariable = "ERGO_CLOSURE_KEEP"
)

// listed is the part of `go list -json` the sweep reads.
type listed struct {
	ImportPath      string
	Dir             string
	ForTest         string
	Deps            []string
	TestGoFiles     []string
	XTestGoFiles    []string
	EmbedFiles      []string
	TestEmbedFiles  []string
	XTestEmbedFiles []string
}

// TestEveryPackageRecordsOnlyItsClosure is the closure rule: it builds the
// test binary of every test package of the module, runs it as cmd/go does
// with a test log, and fails on each recorded input inside the checkout
// that lies outside the package's closure, and on each declared entry no
// input uses. Run it unobserved and uncached, so each binary observes its
// own child processes:
//
//	ERGO_CLOSURE_SWEEP=all go test -count=1 -run TestEveryPackageRecordsOnlyItsClosure ./internal/closure
func TestEveryPackageRecordsOnlyItsClosure(t *testing.T) {
	selection := os.Getenv(sweepVariable)
	if selection == "" {
		t.Skipf("set %s=all (or a comma list of package directories) to run every test binary; see the doc comment", sweepVariable)
	}
	jobs := max(1, runtime.NumCPU()/4)
	if v := os.Getenv(jobsVariable); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			t.Fatalf("%s=%q: want a positive number", jobsVariable, v)
		}
		jobs = n
	}
	binaries := t.TempDir()
	work := binaries
	if keep := os.Getenv(keepVariable); keep != "" {
		work = keep
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		t.Fatalf("go list -m: %v", err)
	}
	root := strings.TrimSpace(string(out))
	roots := []string{root}
	protect, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if protect != root {
		roots = append(roots, protect)
	}
	// Each test binary runs in the sandbox, so the tree the classifier
	// resolves after the run is the tree the tests read (sandbox_test.go).
	// The sweep first checks the sandbox as a test sees it (S1 to S3);
	// without it the sweep does not run.
	if err := sandboxSupported(); err != nil {
		t.Fatalf("the sweep cannot run: %v", err)
	}
	if err := checkSandboxAsATest(protect, root, filepath.Join(work, "sandbox.out")); err != nil {
		t.Fatalf("the sweep cannot run: %v", err)
	}
	closures, targets := modulePackages(t, root)
	if selection != "all" {
		targets = nil
		for _, dir := range strings.Split(selection, ",") {
			if _, ok := closures[dir]; !ok {
				t.Fatalf("%s: %q is not a test package of the module", sweepVariable, dir)
			}
			targets = append(targets, dir)
		}
	} else {
		for dir := range declared {
			if _, ok := closures[dir]; !ok {
				t.Errorf("declared package %s is not a test package of the module", dir)
			}
		}
		for dir := range declaredKernelWrites {
			if _, ok := closures[dir]; !ok {
				t.Errorf("the package %s of a declared kernel write is not a test package of the module", dir)
			}
		}
	}

	type result struct {
		outside  []entry
		used     map[string]bool
		identity string
		err      error
	}
	results := make([]result, len(targets))
	sem := make(chan struct{}, jobs)
	var wg sync.WaitGroup
	for i, dir := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			name := strings.ReplaceAll(dir, "/", "_")
			entries, err := runPackage(root, protect, roots, dir, filepath.Join(binaries, name+".test"), filepath.Join(work, name))
			if err != nil {
				results[i].err = err
				return
			}
			results[i].used = map[string]bool{}
			results[i].outside = closures[dir].check(entries, results[i].used)
			results[i].identity = identityCounts(entries)
		}()
	}
	wg.Wait()

	for i, dir := range targets {
		r := results[i]
		if r.err != nil {
			t.Errorf("%s: %v", dir, r.err)
			continue
		}
		for _, a := range declared[dir] {
			if !r.used[a.key()] {
				t.Errorf("%s: the declared entry %s is an input of no recorded access; remove it", dir, a.key())
			}
		}
		// The counts show whether a package's inputs depend on a change by a
		// process outside the sandbox (docs/tooling.md, the closure rule's
		// limit).
		t.Logf("%s: identity: %s", dir, r.identity)
		if len(r.outside) == 0 {
			t.Logf("%s: every recorded input lies in its closure", dir)
			continue
		}
		if os.Getenv(keepVariable) != "" {
			var b strings.Builder
			for _, e := range r.outside {
				fmt.Fprintf(&b, "%s %s\n", e.op, e.path)
			}
			name := filepath.Join(work, strings.ReplaceAll(dir, "/", "_")+".outside")
			if err := os.WriteFile(name, []byte(b.String()), 0o644); err != nil {
				t.Error(err)
			}
		}
		t.Errorf("%s: %d recorded inputs lie outside its closure:\n%s", dir, len(r.outside), summary(r.outside))
	}
}

// identityCounts counts the entries that fail closed on identity: those that
// met a change during the run, those whose object changed after it, and those
// with no identity. opRaced starts with opChanged, so it is tested first.
func identityCounts(entries []entry) string {
	var raced, changed, unidentified int
	for _, e := range entries {
		switch {
		case strings.HasPrefix(e.op, opRaced):
			raced++
		case strings.HasPrefix(e.op, opChanged):
			changed++
		case strings.HasPrefix(e.op, opUnidentified):
			unidentified++
		}
	}
	return fmt.Sprintf("%d changed during, %d changed, %d unidentified, of %d entries", raced, changed, unidentified, len(entries))
}

// modulePackages returns the closure of every test package of the module,
// by package directory, and the directories of the packages with tests.
func modulePackages(t *testing.T, root string) (map[string]closure, []string) {
	t.Helper()
	cmd := exec.Command("go", "list", "-e", "-deps", "-test", "-json=ImportPath,Dir,ForTest,Deps,TestGoFiles,XTestGoFiles,EmbedFiles,TestEmbedFiles,XTestEmbedFiles", "./...")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	byPath := map[string]listed{}
	var order []listed
	for dec := json.NewDecoder(bytes.NewReader(out)); ; {
		var p listed
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		byPath[p.ImportPath] = p
		order = append(order, p)
	}
	rel := func(dir string) (string, bool) { return relative(dir, []string{root}) }
	// closureOf builds the closure of the package in dir from the packages
	// named by imports.
	closureOf := func(dir string, imports []string) closure {
		var deps, embeds []string
		for _, imp := range imports {
			d, ok := byPath[imp]
			if !ok {
				continue
			}
			ddir, inside := rel(d.Dir)
			if !inside {
				continue
			}
			deps = append(deps, ddir)
			for _, files := range [][]string{d.EmbedFiles, d.TestEmbedFiles, d.XTestEmbedFiles} {
				for _, f := range files {
					embeds = append(embeds, path.Join(ddir, f))
				}
			}
		}
		return newClosure(dir, deps, embeds)
	}
	byDir := map[string]listed{}
	for _, p := range order {
		if dir, inside := rel(p.Dir); inside && p.ForTest == "" && !strings.Contains(p.ImportPath, " ") && !strings.HasSuffix(p.ImportPath, ".test") {
			byDir[dir] = p
		}
	}
	closures := map[string]closure{}
	var targets []string
	for dir, p := range byDir {
		if len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 {
			continue
		}
		main, ok := byPath[p.ImportPath+".test"]
		if !ok {
			t.Fatalf("go list has no test main for %s", p.ImportPath)
		}
		c := closureOf(dir, append([]string{p.ImportPath + " [" + p.ImportPath + ".test]", p.ImportPath + "_test [" + p.ImportPath + ".test]"}, main.Deps...))
		c.programs = map[string]closure{}
		for _, a := range c.declared {
			if a.grant != program {
				continue
			}
			prog, ok := byDir[a.path]
			if !ok {
				t.Fatalf("%s: the declared program %s is not a package of the module", dir, a.path)
			}
			c.programs[a.path] = closureOf(a.path, append([]string{prog.ImportPath}, prog.Deps...))
		}
		closures[dir] = c
		targets = append(targets, dir)
	}
	slices.Sort(targets)
	return closures, targets
}

// runPackage builds the test binary of the package in dir (outside the
// sandbox, which leaves the checkout writable for the go command) and
// runs it (runTests). The log and the output go to stem.log and stem.out.
func runPackage(root, protect string, roots []string, dir, binary, stem string) ([]entry, error) {
	build := exec.Command("go", "test", "-c", "-o", binary, "./"+dir)
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go test -c: %v\n%s", err, out)
	}
	return runTests(protect, roots, dir, filepath.Join(root, filepath.FromSlash(dir)), stem, nil, binary)
}

// sweepEnvironment is the environment a cached test may receive.
func sweepEnvironment() []string { return testenv.Environment(os.Environ()) }

// runTests runs a test binary the way cmd/go runs a cacheable test: from
// the package directory, with the environment a cached test may receive
// (and extra) and a test log. It runs it in a sandbox (runProtected) whose
// checkout is protect and whose scratch directories are fresh for this
// run, and returns the recorded inputs inside the roots. pkg names the
// package in a violation. The test log and the output are kept in
// stem.log and stem.out.
//
// It classifies the log before it removes the run's scratch directories:
// a path the tests recorded through a link they left in scratch then
// resolves through that link, as the read did if the link is unchanged (a
// run with identity replays the access-time walk instead). Classified after the
// removal, such a path would name an absent place in scratch, which is no
// input, and the read would be classified by nothing.
func runTests(protect string, roots []string, pkg, pkgDir, stem string, extra []string, binary string, args ...string) ([]entry, error) {
	var entries []entry
	classify := func(run observedRun) (err error) {
		entries, err = run.entries(roots)
		return err
	}
	if _, err := runObserved(protect, pkg, pkgDir, stem, extra, classify, binary, args...); err != nil {
		return nil, err
	}
	return entries, nil
}

// observedRun is one run of a test binary as the sweep runs it: the test log
// and what its observer saw at access time.
type observedRun struct {
	pkgDir, stem string
	log          []byte
	id           *accessIdentity
}

// entries classifies the run's recorded inputs inside the roots (readLog).
func (o observedRun) entries(roots []string) ([]entry, error) {
	entries, err := readLog(o.log, o.pkgDir, roots, os.TempDir(), o.id)
	if err != nil {
		return nil, fmt.Errorf("%s.log: %w (run the sweep with -count=1, so it is not observed itself)", o.stem, err)
	}
	return entries, nil
}

// runObserved is runTests up to the classification. classify, if not nil,
// runs while the run's scratch directories still exist.
func runObserved(protect, pkg, pkgDir, stem string, extra []string, classify func(observedRun) error, binary string, args ...string) (observedRun, error) {
	fail := func(err error) (observedRun, error) { return observedRun{}, err }
	s, err := newScratch()
	if err != nil {
		return fail(err)
	}
	defer s.remove()
	log := filepath.Join(s.log, "test.log")
	identityFile := filepath.Join(s.log, "identity.json")
	args = append([]string{"-test.paniconexit0", "-test.timeout=45m", "-test.testlogfile=" + log}, args...)
	// The observer of the test binary (testinputs.Main) records the
	// identity of each access at access time (testinputs.IdentityVariable).
	// A package the observer cannot observe gets no identity file to write.
	_, unobserved := unobservable[pkg]
	vars := []string{"PWD=" + pkgDir}
	if !unobserved {
		vars = append(vars, testinputs.IdentityVariable+"="+identityFile)
	}
	env := withEnv(sweepEnvironment(), append(append(vars, s.env()...), extra...)...)
	sb := sandbox{Root: protect, Scratch: s.dirs()}
	for _, k := range declaredKernelWrites[pkg] {
		sb.Kernel = append(sb.Kernel, k.point)
	}
	if err := runProtected(sb, pkg, pkgDir, env, stem+".out", binary, args...); err != nil {
		return fail(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		return fail(err)
	}
	if err := os.WriteFile(stem+".log", data, 0o644); err != nil {
		return fail(err)
	}
	id, err := testinputs.ReadIdentity(identityFile)
	switch {
	case unobserved:
		// No access has an identity, so each recorded input in the
		// checkout fails closed: only an exact declared entry covers it.
		id = testinputs.Identity{}
	case err != nil:
		if bytes.HasSuffix(data, []byte("\n")) {
			return fail(fmt.Errorf("%s: the run has no access-time identity of its inputs (it did not run under testinputs.Main): %w", pkg, err))
		}
		id = testinputs.Identity{} // the log says why it is unobserved
	}
	if raw, err := os.ReadFile(identityFile); err == nil && !unobserved {
		if err := os.WriteFile(stem+".identity.json", raw, 0o644); err != nil {
			return fail(err)
		}
	}
	writable := append(append([]string{"/dev/shm"}, s.dirs()...), sb.Kernel...)
	run := observedRun{pkgDir: pkgDir, stem: stem, log: data, id: newAccessIdentity(id, writable)}
	if classify != nil {
		if err := classify(run); err != nil {
			return fail(err)
		}
	}
	return run, nil
}

// summary groups inputs by their first two path elements, with a count and
// up to three examples each.
func summary(entries []entry) string {
	groups := map[string][]entry{}
	var keys []string
	for _, e := range entries {
		parts := strings.SplitN(e.path, "/", 3)
		key := strings.Join(parts[:min(2, len(parts))], "/")
		if _, ok := groups[key]; !ok {
			keys = append(keys, key)
		}
		groups[key] = append(groups[key], e)
	}
	slices.Sort(keys)
	var b strings.Builder
	for _, k := range keys {
		g := groups[k]
		fmt.Fprintf(&b, "  %s: %d", k, len(g))
		for _, e := range g[:min(3, len(g))] {
			fmt.Fprintf(&b, " [%s %s]", e.op, e.path)
		}
		b.WriteString("\n")
	}
	return b.String()
}
