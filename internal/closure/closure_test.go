package closure

import (
	"bytes"
	"encoding/json"
	"errors"
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

	"ergo.local/prototype/internal/testinputs/testenv"
)

// allowance is one declared input of a test package outside the directories
// of its dependencies and its own testdata, with the reason it is an input.
// The path is relative to the module root, with forward slashes; a path that
// ends in "/" covers the subtree below it, and "./" covers the whole tree.
// A program entry names the directory of a package of the module whose
// executable the tests build: it covers what the go command reads to build
// it, the directories of `go list -deps` for it.
type allowance struct {
	path    string
	reason  string
	program bool
}

// declared are the inputs each test package may record outside its own
// closure, by package directory. Keep it short: an entry is a package that
// reruns when its path changes, so each one needs a reason the test cannot
// avoid. The sweep fails on an entry no recorded input uses.
var declared = map[string][]allowance{
	"internal/residue": {
		{path: "./", reason: "the residue checks read every tracked file by design: git grep, git ls-files and Go's reads of each file"},
	},
	"internal/testinputs/guard": {
		{path: "./", reason: "the guard lists every directory and reads every test file, so a test package added anywhere reruns it"},
	},
	"internal/sketch/lower": {
		{path: "cmd/ergo", reason: "the tests drive the public CLI, built once per process", program: true},
	},
}

// everyPackage are the inputs any test package may record: the go command
// reads them for every build in the module, and a test binary depends on
// them already.
var everyPackage = []allowance{
	{path: "go.mod", reason: "the go command reads the module file for every build in the module"},
	{path: "go.sum", reason: "the go command checks module sums for every build in the module"},
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
	p := e.path
	switch {
	case c.compiled(e):
		return true
	case p == c.dir+"/testdata" || strings.HasPrefix(p, c.dir+"/testdata/"):
		return true
	case e.op == "stat" && moduleLookups[path.Base(p)]:
		// The go command looks for these where it finds a module's root
		// and layout; each is absent, and adding one changes the layout.
		return true
	}
	for _, a := range everyPackage {
		if covers(a.path, p) {
			return true
		}
	}
	for _, a := range c.declared {
		if prog, ok := c.programs[a.path]; a.program && ok && prog.compiled(e) || !a.program && covers(a.path, p) {
			used[a.path] = true
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
	return c.deps[p] || c.deps[path.Dir(p)] && p != "." || c.embeds[p] || c.ancestors[p] && e.op == "stat"
}

// covers reports whether the declared path a covers the path p.
func covers(a, p string) bool {
	switch {
	case a == "./":
		return true
	case strings.HasSuffix(a, "/"):
		return p == strings.TrimSuffix(a, "/") || strings.HasPrefix(p, a)
	default:
		return p == a
	}
}

// parseLog reads a test log (cmd/go's -test.testlogfile format, with the
// lines testinputs.Main appends) and returns its inputs inside root,
// relative to it. pwd is the directory the test started in, which relative
// paths and chdir lines resolve against. A log without its final newline
// was left uncacheable on purpose (testinputs.Main), so its inputs are
// unknown and parseLog returns an error that says why.
func parseLog(data []byte, pwd string, roots []string) ([]entry, error) {
	const header = "# test log\n"
	if !bytes.HasPrefix(data, []byte(header)) {
		return nil, errors.New("no test log header; the test binary did not write a log")
	}
	if !bytes.HasSuffix(data, []byte("\n")) {
		last := data[bytes.LastIndexByte(data, '\n')+1:]
		return nil, fmt.Errorf("the log is marked uncacheable, so its inputs are unknown: %q", last)
	}
	var entries []entry
	seen := map[entry]bool{}
	body := string(data[len(header):])
	if body == "" {
		return nil, nil // the tests read nothing
	}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		op, name, ok := strings.Cut(line, " ")
		if !ok {
			return nil, fmt.Errorf("log line %q", line)
		}
		switch op {
		case "getenv":
			continue
		case "chdir", "open", "stat":
		default:
			return nil, fmt.Errorf("log line %q: unknown operation", line)
		}
		if !filepath.IsAbs(name) {
			name = filepath.Join(pwd, name)
		}
		name = filepath.Clean(name)
		if op == "chdir" {
			pwd = name
			continue
		}
		rel, inside := relative(name, roots)
		if !inside {
			continue
		}
		e := entry{op, rel}
		if !seen[e] {
			seen[e] = true
			entries = append(entries, e)
		}
	}
	return entries, nil
}

// relative returns name relative to the first root it lies in, with forward
// slashes.
func relative(name string, roots []string) (string, bool) {
	for _, root := range roots {
		rel, err := filepath.Rel(root, name)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return filepath.ToSlash(rel), true
		}
	}
	return "", false
}

// TestClosureClassifiesInputs pins the closure rule on a fixed package.
func TestClosureClassifiesInputs(t *testing.T) {
	c := newClosure("internal/a", []string{"internal/b", "cmd/c"}, []string{"internal/b/files/x.txt"})
	c.declared = []allowance{{path: "examples/", reason: "walks the examples"}, {path: "docs/one.md", reason: "reads one page"}}
	in := []entry{
		{"open", "internal/a/a_test.go"},
		{"open", "internal/a"},
		{"open", "internal/b/b.go"},
		{"stat", "cmd/c"},
		{"open", "internal/b/files/x.txt"},
		{"open", "internal/b/files"},
		{"open", "internal/a/testdata/v1/x.txt"},
		{"stat", "internal/a/testdata"},
		{"stat", "internal"},
		{"stat", "."},
		{"stat", "cmd"},
		{"open", "go.mod"},
		{"open", "examples/game/main.ergo"},
		{"stat", "examples"},
		{"open", "docs/one.md"},
		{"stat", "internal/go.work"},
		{"stat", "runtime/rt/x.go/go.mod"},
		{"stat", "vendor"},
		{"getenv", "HOME"},
	}
	out := []entry{
		{"open", "."},                       // listing the root
		{"open", "internal"},                // listing an ancestor
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
		{"open", "vendor"},                  // a vendor directory listed
	}
	used := map[string]bool{}
	if got := c.check(in, used); len(got) != 0 {
		t.Errorf("inputs inside the closure reported outside: %v", got)
	}
	if !used["examples/"] || !used["docs/one.md"] {
		t.Errorf("declared entries not marked used: %v", used)
	}
	for _, e := range out {
		if got := c.check([]entry{e}, map[string]bool{}); len(got) != 1 {
			t.Errorf("%v: reported inside the closure", e)
		}
	}

	// A declared program covers what building it reads, and nothing else.
	c.declared = []allowance{{path: "cmd/p", reason: "the tests build it", program: true}}
	c.programs = map[string]closure{"cmd/p": newClosure("cmd/p", []string{"internal/q"}, nil)}
	used = map[string]bool{}
	if got := c.check([]entry{{"open", "cmd/p/main.go"}, {"open", "internal/q/q.go"}, {"stat", "cmd"}}, used); len(got) != 0 || !used["cmd/p"] {
		t.Errorf("a program's sources reported outside the closure: %v (used %v)", got, used)
	}
	for _, e := range []entry{{"open", "cmd/p/testdata/x"}, {"open", "cmd/p/sub/x.go"}, {"open", "cmd"}, {"open", "internal/r/r.go"}} {
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
// root drop, and a log testinputs left uncacheable is refused.
func TestParseLogResolvesAndRefuses(t *testing.T) {
	root := filepath.FromSlash("/r")
	log := "# test log\ngetenv HOME\nopen x.go\nstat ../b\nchdir /r/c\nopen y\nopen /usr/lib/go/src/fmt/print.go\nopen /r/x.go\n"
	got, err := parseLog([]byte(log), filepath.FromSlash("/r/a"), []string{root})
	if err != nil {
		t.Fatal(err)
	}
	want := []entry{{"open", "a/x.go"}, {"stat", "b"}, {"open", "c/y"}, {"open", "x.go"}}
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
// relative path with a reason. The sweep checks that each one is used.
func TestDeclaredAllowanceIsWellFormed(t *testing.T) {
	check := func(owner string, a allowance) {
		p := strings.TrimSuffix(a.path, "/")
		if a.path != "./" && (p == "" || path.Clean(p) != p || path.IsAbs(p) || strings.HasPrefix(p, "../") || p == "..") {
			t.Errorf("%s: %q is not a clean path relative to the module root", owner, a.path)
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
		for _, a := range list {
			check(dir, a)
		}
	}
	for _, a := range everyPackage {
		check("every package", a)
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
	if real, err := filepath.EvalSymlinks(root); err == nil && real != root {
		roots = append(roots, real)
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
	}

	type result struct {
		outside []entry
		used    map[string]bool
		err     error
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
			entries, err := runPackage(root, roots, dir, filepath.Join(binaries, name+".test"), filepath.Join(work, name))
			if err != nil {
				results[i].err = err
				return
			}
			results[i].used = map[string]bool{}
			results[i].outside = closures[dir].check(entries, results[i].used)
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
			if !r.used[a.path] {
				t.Errorf("%s: the declared entry %q is an input of no recorded access; remove it", dir, a.path)
			}
		}
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
			if !a.program {
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

// runPackage builds the test binary of the package in dir and runs it the
// way cmd/go runs a cacheable test: from the package directory, with the
// environment a cached test may receive and a test log. It returns the
// recorded inputs inside the root. The log and the output go to stem.log
// and stem.out.
func runPackage(root string, roots []string, dir, binary, stem string) ([]entry, error) {
	log, output := stem+".log", stem+".out"
	build := exec.Command("go", "test", "-c", "-o", binary, "./"+dir)
	build.Dir = root
	if out, err := build.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("go test -c: %v\n%s", err, out)
	}
	pkgDir := filepath.Join(root, filepath.FromSlash(dir))
	run := exec.Command(binary, "-test.paniconexit0", "-test.timeout=45m", "-test.testlogfile="+log)
	run.Dir = pkgDir
	run.Env = append(testenv.Environment(os.Environ()), "PWD="+pkgDir)
	file, err := os.Create(output)
	if err != nil {
		return nil, err
	}
	run.Stdout, run.Stderr = file, file
	runErr := run.Run()
	file.Close()
	if runErr != nil {
		tail, _ := os.ReadFile(output)
		if len(tail) > 4000 {
			tail = tail[len(tail)-4000:]
		}
		return nil, fmt.Errorf("the tests failed (%v), so the inputs they record are incomplete; output %s ends:\n%s", runErr, output, tail)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		return nil, err
	}
	entries, err := parseLog(data, pkgDir, roots)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (run the sweep with -count=1, so it is not observed itself)", log, err)
	}
	return entries, nil
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
