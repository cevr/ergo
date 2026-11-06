package replay

import (
	"errors"
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestPackageKeyModel runs generated sequences of changes and fast, full
// and merge runs against a reference model and against the real package
// key, package store, tier key and merge tier, and requires that they agree
// after every run.
//
// The generator reaches every input class of the key rule (plan G2: the
// closure from internal/closure's table, the toolchain, the step
// environment, the tool and host fingerprints, the gate digest and the tier
// selection): edits, deletes and permission changes in each part of a
// closure (a dependency's directory, a directory on the way to one, a file
// embedded from a subdirectory, the package's testdata, the module lookups,
// a file grant, a listing that gains a name, a glob, a subtree, a program
// grant with its own closure); symbolic links created and retargeted to
// covered, uncovered and missing places; HEAD alone, the index alone and
// both, for a package that reads .git and for one that does not; the
// environment, the gate's settings, the gate executable, the checkout
// root, the tools and the host facts; a package's table entry (its
// declarations, its directory); the step's identity (its selection, with
// and without -short); an edit between a
// step's start key and its end key; an ignored input changed within
// RecentWindow.
//
// The model states, for each test package, its inputs as the closure rule
// says them (refInputs), in its own terms: it walks the tree and reads
// links through the operating system, not through Select. The oracle,
// after every run:
//
//	(1) a package whose closure, tool or host input changed since its last
//	    recorded pass never replays;
//	(2) a package with no change since a recorded pass replays;
//	(3) a fast record never satisfies --merge: the merge tier runs the
//	    inner (full) tier whenever no full run passed at these inputs;
//	(4) a failure is never recorded: a package that failed reruns;
//	(5) a pass whose inputs changed during its run is not recorded;
//	(6) a package whose ignored input changed within RecentWindow has no
//	    key, and one with no such input has one.
func TestPackageKeyModel(t *testing.T) {
	sequences, commands := 80, 50
	if testing.Short() {
		sequences = 40
	}
	offset := uint64(0)
	if text := os.Getenv(seedOffsetVariable); text != "" {
		var err error
		if offset, err = strconv.ParseUint(text, 10, 64); err != nil {
			t.Fatalf("%s=%q: %v", seedOffsetVariable, text, err)
		}
	}
	for seed := offset; seed < offset+uint64(sequences); seed++ {
		w := newPackageWorld(t, seed)
		for range commands {
			w.step()
			if t.Failed() {
				t.Fatalf("seed %d, after:\n%s", seed, strings.Join(w.log, "\n"))
			}
		}
	}
}

// seedOffsetVariable moves TestPackageKeyModel's seeds: with it set to n
// the sequences are seeds n, n+1, …, so a check of the model's margin can
// run other sequences (gate-tier round 3 TS2 ran offsets 1000 and 5000).
// The gate never sets it: its runs use seeds from 0.
const seedOffsetVariable = "ERGO_PKGKEY_SEED_OFFSET"

// modelPackages are the model's test packages:
//   - a imports b and x/y/z (so x and x/y are on the way to a dependency),
//     embeds a file from a subdirectory and one two levels down, declares a
//     file, a listing and the program p, whose closure imports q;
//   - b is a leaf;
//   - c walks the examples and globs the spec;
//   - g reads .git, so HEAD and the index are its inputs;
//   - n is a directory that holds nothing, so its table entry can move
//     ("move package") with no change of what it reads.
func modelPackages() []*Closure {
	return []*Closure{
		{Dir: "a", Deps: []string{"b", "x/y/z"}, Embeds: []string{"a/assets/e.txt", "a/assets/deep/f.txt"},
			Declared: []Allowance{{"docs/one.md", GrantFile}, {"docs/list", GrantListing}, {"p", GrantProgram}},
			Programs: map[string]*Closure{"p": {Dir: "p", Deps: []string{"q"}}}},
		{Dir: "b"},
		{Dir: "c", Declared: []Allowance{{"examples", GrantSubtree}, {"docs/spec/*.md", GrantFiles}}},
		{Dir: "g", Declared: []Allowance{{".git", GrantSubtree}}},
		{Dir: "n"},
	}
}

// modelPaths are the regular files a sequence writes and deletes, inside
// and outside each closure. "x" is a file only while the directory x is
// absent (a write that cannot happen is skipped).
var modelPaths = []string{
	"go.mod", "go.sum", "top.txt", "a/a.go", "a/a_test.go", "a/testdata/x.txt", "a/testdata/deep/y.txt",
	"a/sub/n.go", "a/assets/e.txt", "a/assets/other.txt", "a/assets/deep/f.txt", "a/assets/deep/g.txt",
	"b/b.go", "b/b_test.go", "b/sub/z.txt", "c/c.go", "c/testdata/w", "d/d.go", "d/go.mod", "d/go.work",
	"d/vendor/m.txt", "g/g.go", "p/p.go", "p/sub/s.txt", "q/q.go", "q/sub/r.txt", "x/y/z/z.go", "x/y/w.txt", "x",
	"docs/one.md", "docs/two.md", "docs/list/l1.txt", "docs/list/l2.txt", "docs/list/sub/deep.txt",
	"docs/spec/s.md", "docs/spec/sub/t.md", "docs/tree/t1.txt", "docs/tree/sub/t2.txt",
	"examples/e1/main.ergo", "examples/e2/main.ergo",
}

// ignoredPath is the one ignored file of the model: keyed by its stat
// tuple, in a's closure. Only writes name it (always new content), so its
// stat tuple changes exactly when its content does.
const ignoredPath = "a/build.log"

// linkPath is the model's symbolic link, in a's closure; linkTargets are
// where it leads: an uncovered file, an uncovered tree, a file a covers
// anyway, a file only c covers, and a missing place.
const linkPath = "a/link"

var linkTargets = []string{"../docs/two.md", "../docs/tree", "../b/b.go", "../c/c.go", "../docs/none.md"}

// identities are the step identities a package's fast step can have: its
// selection, with and without -short.
var identities = []string{"-short", "-short -run ^TestOne$", "-short -skip ^TestOne$", ""}

type packageWorld struct {
	t        *testing.T
	rng      *rand.Rand
	root     string
	packages []*Closure
	// names are the packages' step names, by slot: a package's step keeps
	// its name when its table entry moves.
	names []string
	// rootName is the checkout root the facts name.
	rootName string
	// store is a real store; the model's records are below.
	store Store
	tools map[string]string
	host  map[string]string
	// The facts that are not paths.
	env, setting, exe string
	head              int
	index             []string
	version           int
	// identity is each package step's selection (identities).
	identity map[string]string
	// passed holds the model's recorded passes: "tier package identity
	// inputs" for a package, "tier * inputs" for a whole run.
	passed map[string]bool
	log    []string
	clock  time.Time
	spool  string
}

func newPackageWorld(t *testing.T, seed uint64) *packageWorld {
	w := &packageWorld{t: t, rng: rand.New(rand.NewPCG(seed, seed^0x5eed)), root: t.TempDir(), packages: modelPackages(),
		store: Open(t.TempDir()), spool: t.TempDir(),
		tools: map[string]string{"go": "go1", "git": "git1", "sh": "sh1"}, host: map[string]string{"kernel release": "6.1", "cpu budget": "4"},
		env: "-p=4", setting: "cache0", exe: "gate0", index: []string{"100644 blob0 0\tg/g.go"},
		passed: map[string]bool{}, identity: map[string]string{}, clock: time.Now(), rootName: "/checkout0"}
	for _, c := range w.packages {
		w.names = append(w.names, c.Dir)
		w.identity[c.Dir] = identities[0]
	}
	for _, p := range modelPaths {
		if p != "x" && w.rng.IntN(4) > 0 {
			w.write(p)
		}
	}
	if w.rng.IntN(2) == 0 {
		w.link()
	}
	return w
}

func (w *packageWorld) note(format string, args ...any) {
	w.log = append(w.log, fmt.Sprintf(format, args...))
}

func (w *packageWorld) full(name string) string {
	return filepath.Join(w.root, filepath.FromSlash(name))
}

// write gives the file new content; it reports false when the path cannot
// be a file now (a directory is there, or a file is where a directory must
// be).
func (w *packageWorld) write(name string) bool {
	w.version++
	full := w.full(name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return false
	}
	if err := os.WriteFile(full, []byte(fmt.Sprintf("v%d %s", w.version, name)), 0o644); err != nil {
		return false
	}
	if name == ignoredPath {
		// Its key is its stat tuple. Two writes within one tick of the
		// kernel's coarse file clock can leave it equal (what RecentWindow
		// guards against, which facts() steps over): a distinct mtime per
		// write keeps each write a change of the tuple.
		stamp := time.Unix(1_000_000_000+int64(w.version), 0)
		if err := os.Chtimes(full, stamp, stamp); err != nil {
			w.t.Fatal(err)
		}
	}
	return true
}

// files are the regular files of the world, not the ignored one.
func (w *packageWorld) files() []string {
	var names []string
	_ = filepath.WalkDir(w.root, func(name string, entry fs.DirEntry, err error) error {
		if err == nil && entry.Type().IsRegular() {
			rel, _ := filepath.Rel(w.root, name)
			if rel = filepath.ToSlash(rel); rel != ignoredPath {
				names = append(names, rel)
			}
		}
		return nil
	})
	sort.Strings(names)
	return names
}

// modelAction is one change or run of the generator. changes is the one
// input class of the package key (packageKeyInputs) the action changes
// alone, for some package; "" for a run, or for a change of several classes
// at once.
type modelAction struct {
	name    string
	changes keyInput
	weight  int
	do      func(w *packageWorld)
}

// modelActions are the generator's actions. Each input class of the key has
// at least one action that changes it alone, so a key that drops the class
// replays a stale pass in the model
// (TestPackageKeyModelChangesEachInputAlone).
var modelActions = []modelAction{
	{"write", inputEntry, 30, func(w *packageWorld) {
		name := modelPaths[w.rng.IntN(len(modelPaths))]
		if w.rng.IntN(2) == 0 {
			// One path of a class only one rule covers: what the link can
			// reach outside a's closure, the embedded files, the
			// program's import, the name on the way to x/y/z, a listed
			// name.
			hot := []string{"docs/two.md", "docs/tree/t1.txt", "docs/tree/sub/t2.txt", "c/c.go", "docs/none.md",
				"a/assets/e.txt", "a/assets/deep/f.txt", "q/q.go", "x", "docs/list/l2.txt"}
			name = hot[w.rng.IntN(len(hot))]
		}
		w.note("write %s: %t", name, w.write(name))
	}},
	{"delete", inputEntry, 10, func(w *packageWorld) {
		if files := w.files(); len(files) > 0 {
			name := files[w.rng.IntN(len(files))]
			if err := os.Remove(w.full(name)); err != nil {
				w.t.Fatal(err)
			}
			w.note("delete %s", name)
		}
	}},
	{"remove tree", inputEntry, 10, func(w *packageWorld) {
		// A directory on the way to a dependency, or a tree, goes away.
		dir := []string{"x", "x/y", "docs/list", "docs/tree", "a/assets", "q"}[w.rng.IntN(6)]
		if _, err := os.Lstat(w.full(dir)); err != nil {
			// Absent, or below a file (x/y while x is a file).
			w.note("remove tree %s: absent", dir)
			return
		}
		if err := os.RemoveAll(w.full(dir)); err != nil {
			w.t.Fatal(err)
		}
		w.note("remove tree %s", dir)
	}},
	{"chmod", inputEntry, 10, func(w *packageWorld) {
		if files := w.files(); len(files) > 0 {
			name := files[w.rng.IntN(len(files))]
			// The owner's executable bit is keyed, group write is not.
			bit := []fs.FileMode{0o100, 0o020}[w.rng.IntN(2)]
			info, err := os.Stat(w.full(name))
			if err != nil {
				w.t.Fatal(err)
			}
			if err := os.Chmod(w.full(name), info.Mode().Perm()^bit); err != nil {
				w.t.Fatal(err)
			}
			w.note("chmod %s ^%o", name, bit)
		}
	}},
	{"link", inputEntry, 20, func(w *packageWorld) { w.link() }},
	{"ancestor", inputEntry, 8, func(w *packageWorld) { w.ancestor() }},
	{"reach", inputEntry, 8, func(w *packageWorld) { w.reach() }},
	{"write ignored", inputEntry, 10, func(w *packageWorld) {
		w.write(ignoredPath)
		w.note("write %s (ignored)", ignoredPath)
	}},
	{"tool", inputHost, 10, func(w *packageWorld) {
		tool := []string{"go", "git", "sh"}[w.rng.IntN(3)]
		w.tools[tool] = fmt.Sprintf("%s%d", tool, w.rng.IntN(3))
		w.note("tool %s = %s", tool, w.tools[tool])
	}},
	{"host fact", inputHost, 10, func(w *packageWorld) {
		fact := []string{"kernel release", "cpu budget"}[w.rng.IntN(2)]
		w.host[fact] = fmt.Sprint(w.rng.IntN(3))
		w.note("host %s = %s", fact, w.host[fact])
	}},
	{"identity", inputIdentity, 2, func(w *packageWorld) {
		// A package's step gets another selection.
		name := w.names[w.rng.IntN(len(w.names))]
		w.identity[name] = identities[w.rng.IntN(len(identities))]
		w.note("identity %s = [%s]", name, w.identity[name])
	}},
	{"environment", inputEnvironment, 2, func(w *packageWorld) {
		w.env = fmt.Sprintf("-p=%d", w.rng.IntN(3))
		w.note("environment GOFLAGS = %s", w.env)
	}},
	{"setting", inputSetting, 2, func(w *packageWorld) {
		w.setting = fmt.Sprintf("cache%d", w.rng.IntN(3))
		w.note("setting = %s", w.setting)
	}},
	{"executable", inputExecutable, 2, func(w *packageWorld) {
		w.exe = fmt.Sprintf("gate%d", w.rng.IntN(3))
		w.note("executable = %s", w.exe)
	}},
	{"root", inputRoot, 2, func(w *packageWorld) {
		// The same tree at another checkout path.
		w.rootName = fmt.Sprintf("/checkout%d", w.rng.IntN(3))
		w.note("root = %s", w.rootName)
	}},
	{"commit", "", 2, func(w *packageWorld) {
		// A commit of a staged change: HEAD moves and the index changes.
		w.head++
		w.stage()
		w.note("commit: head %d, index %v", w.head, w.index)
	}},
	{"move HEAD", inputHead, 2, func(w *packageWorld) {
		// An empty commit, or a checkout of another commit of the same
		// tree: HEAD moves, the index stays.
		w.head++
		w.note("HEAD moves to %d, index stays", w.head)
	}},
	{"stage", inputIndex, 2, func(w *packageWorld) {
		// git add or git rm --cached: the index changes, HEAD stays.
		w.stage()
		w.note("stage: index %v, head stays %d", w.index, w.head)
	}},
	{"staged rename", inputIndex, 2, func(w *packageWorld) {
		// git mv: an index entry's path changes, its mode and blob stay.
		w.rename()
		w.note("staged rename: index %v, head stays %d", w.index, w.head)
	}},
	{"regrant", inputDeclared, 2, func(w *packageWorld) {
		// b's table entry keeps the path of an input and changes its
		// grant: a file grant becomes a glob, or back. Neither names a
		// file, so only the declaration changes.
		b := w.packages[slices.Index(w.names, "b")]
		declared := slices.Clone(b.Declared)
		i := slices.IndexFunc(declared, func(a Allowance) bool { return a.Path == "docs/absent.md" })
		switch {
		case i < 0:
			declared = append(declared, Allowance{"docs/absent.md", GrantFiles})
		case declared[i].Grant == GrantFile:
			declared[i].Grant = GrantFiles
		default:
			declared[i].Grant = GrantFile
		}
		b.Declared = declared
		w.note("b declares %v", b.Declared)
	}},
	{"declare", inputDeclared, 2, func(w *packageWorld) {
		// b's table entry gains or loses an input that names no file.
		b := w.packages[slices.Index(w.names, "b")]
		extra := Allowance{"docs/absent.md", GrantFile}
		if i := slices.Index(b.Declared, extra); i >= 0 {
			b.Declared = slices.Delete(slices.Clone(b.Declared), i, i+1)
		} else {
			b.Declared = append(slices.Clone(b.Declared), extra)
		}
		w.note("b declares %v", b.Declared)
	}},
	{"move package", inputClosure, 2, func(w *packageWorld) {
		// n's table entry names another directory, which holds what n
		// held (nothing); its step keeps its name.
		i := slices.Index(w.names, "n")
		dir := map[string]string{"n": "n2", "n2": "n"}[w.packages[i].Dir]
		w.packages[i] = &Closure{Dir: dir}
		w.note("n moves to %s", dir)
	}},
	{"recent run", "", 10, func(w *packageWorld) { w.recentRun() }},
	{"fast run", "", 60, func(w *packageWorld) { w.fastRun() }},
	{"full run", "", 20, func(w *packageWorld) { w.fullRun() }},
	{"merge run", "", 10, func(w *packageWorld) { w.mergeRun() }},
}

func (w *packageWorld) step() {
	total := 0
	for _, a := range modelActions {
		total += a.weight
	}
	pick := w.rng.IntN(total)
	for _, a := range modelActions {
		if pick < a.weight {
			a.do(w)
			return
		}
		pick -= a.weight
	}
}

// stage changes the index: a new blob staged for a tracked file, a file
// added, or an entry removed.
func (w *packageWorld) stage() {
	w.version++
	switch w.rng.IntN(3) {
	case 0:
		w.index = append(slices.DeleteFunc(slices.Clone(w.index), func(e string) bool { return strings.HasSuffix(e, "\tg/g.go") }),
			fmt.Sprintf("100644 blob%d 0\tg/g.go", w.version))
	case 1:
		w.index = append(slices.Clone(w.index), fmt.Sprintf("100644 blob%d 0\tg/new%d.go", w.version, w.version))
	case 2:
		if len(w.index) > 0 {
			w.index = slices.Clone(w.index[1:])
		} else {
			w.index = []string{fmt.Sprintf("100644 blob%d 0\tg/g.go", w.version)}
		}
	}
	slices.Sort(w.index)
}

// keyParts are each package's key parts by input class, at the world's
// facts, printed.
func (w *packageWorld) keyParts() []map[keyInput]string {
	facts := w.facts("fast")
	var all []map[keyInput]string
	for i, c := range w.packages {
		parts, problem := packageKeyParts(facts, c, []string{w.stepName(i), w.identity[w.names[i]]})
		if problem != "" {
			w.t.Fatalf("%s: %s", w.stepName(i), problem)
		}
		printed := map[keyInput]string{}
		for input, part := range parts {
			printed[input] = fmt.Sprint(part)
		}
		all = append(all, printed)
	}
	return all
}

// TestPackageKeyModelChangesEachInputAlone requires that each action of the
// generator that names an input class changes that class, and no other, of
// some package's key, and that every input class of the key has such an
// action. A key input added without a model action, or an action dropped,
// fails here.
func TestPackageKeyModelChangesEachInputAlone(t *testing.T) {
	alone := map[keyInput][]string{}
	for i, action := range modelActions {
		if action.changes == "" {
			continue
		}
		// An action the generator never picks changes nothing in the
		// model's runs (gate-tier round 3 TS1, mutant s5).
		if action.weight <= 0 {
			t.Errorf("action %q has weight %d: the generator never picks it", action.name, action.weight)
			continue
		}
		w := newPackageWorld(t, uint64(i))
		found := false
		var seen []string
		for try := 0; try < 200 && !found; try++ {
			before := w.keyParts()
			action.do(w)
			after := w.keyParts()
			for p := range after {
				var changed []string
				for _, input := range packageKeyInputs {
					if before[p][input] != after[p][input] {
						changed = append(changed, string(input))
					}
				}
				if len(changed) == 1 && changed[0] == string(action.changes) {
					found = true
				} else if len(changed) > 0 {
					seen = append(seen, fmt.Sprintf("%s: %v", w.stepName(p), changed))
				}
			}
		}
		if !found {
			t.Errorf("action %q never changes the key input %q alone (it changed %v)", action.name, action.changes, seen)
			continue
		}
		alone[action.changes] = append(alone[action.changes], action.name)
	}
	for _, input := range packageKeyInputs {
		if len(alone[input]) == 0 {
			t.Errorf("the package key's input %q has no generator action that changes it alone", input)
		}
	}
}

// TestPackageKeyInputsAreTheOneList requires that the package key hashes
// only its version and the classes of packageKeyInputs: every keyInput
// constant is in the list, every part packageKeyParts adds names one, and
// PackageKey's two calls of put are the only ones in pkgkey.go.
func TestPackageKeyInputsAreTheOneList(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "pkgkey.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, input := range packageKeyInputs {
		listed[string(input)] = true
	}
	constants := map[string]bool{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			if typ, ok := value.Type.(*ast.Ident); !ok || typ.Name != "keyInput" {
				continue
			}
			for i, name := range value.Names {
				literal, _ := strconv.Unquote(value.Values[i].(*ast.BasicLit).Value)
				constants[name.Name] = true
				if !listed[literal] {
					t.Errorf("the key input %s (%q) is not in packageKeyInputs", name.Name, literal)
				}
			}
		}
	}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil || fn.Name.Name != "packageKeyParts" {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if callee, _ := call.Fun.(*ast.Ident); callee != nil && callee.Name == "add" {
				if arg, ok := call.Args[0].(*ast.Ident); !ok || !constants[arg.Name] {
					t.Errorf("packageKeyParts adds a part under %s, not a keyInput constant", types.ExprString(call.Args[0]))
				}
			}
			return true
		})
	}
	for _, problem := range packageKeyHashUses(t) {
		t.Error(problem)
	}
}

// packageKeyHashUses type-checks the package's non-test files and returns
// each use of PackageKey's hasher, or of put, that is not one of the key's
// three: put(h, "package", Version), the put of each part inside the range
// over packageKeyInputs, and h.Sum. Uses resolve through go/types, so an
// alias of put (a variable, in any file), a method value or a direct
// h.Write is a use of the same object and is reported (gate-tier round 3
// TS1, mutants e1 and e2). key.go's whole-gate key (Facts.Key) hashes
// through put on its own hasher; it is the one other function that may
// call put.
func packageKeyHashUses(t *testing.T) []string {
	t.Helper()
	fset := token.NewFileSet()
	names, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var files []*ast.File
	for _, name := range names {
		// The files this build compiles: its build constraints hold.
		if match, err := build.Default.MatchFile(".", name); err != nil || !match || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, f)
	}
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}}
	conf := types.Config{Importer: importer.ForCompiler(fset, "source", nil)}
	pkg, err := conf.Check("replay", fset, files, info)
	if err != nil {
		t.Fatalf("type-check the package: %v", err)
	}
	put, _ := pkg.Scope().Lookup("put").(*types.Func)
	if put == nil {
		return []string{"the package has no function put"}
	}
	// The functions each identifier is in, and the allowed uses.
	var problems []string
	var key *ast.FuncDecl
	allowed := map[*ast.Ident]bool{}
	var hasher types.Object
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			switch {
			case fn.Name.Name == "PackageKey" && fn.Recv == nil:
				key = fn
			case fn.Name.Name == "Key" && fn.Recv != nil && filepath.Base(fset.Position(fn.Pos()).Filename) == "key.go":
				// The whole-gate key: each direct call of put is allowed.
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					if call, ok := n.(*ast.CallExpr); ok {
						if id, ok := call.Fun.(*ast.Ident); ok && info.Uses[id] == put {
							allowed[id] = true
						}
					}
					return true
				})
			}
		}
	}
	if key == nil {
		return []string{"the package has no function PackageKey"}
	}
	// In PackageKey: h := sha256.New(), the version put, the class loop's
	// put and h.Sum.
	puts := 0
	ast.Inspect(key.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if id, ok := n.Lhs[0].(*ast.Ident); ok && id.Name == "h" && n.Tok == token.DEFINE {
				hasher = info.Defs[id]
			}
		case *ast.RangeStmt:
			if x, ok := n.X.(*ast.Ident); !ok || x.Name != "packageKeyInputs" {
				return true
			}
			ast.Inspect(n.Body, func(m ast.Node) bool {
				if call, ok := m.(*ast.CallExpr); ok && isHasherPut(info, call, put, hasher) {
					allowed[call.Fun.(*ast.Ident)] = true
					allowed[call.Args[0].(*ast.Ident)] = true
					puts++
				}
				return true
			})
			return false
		case *ast.ExprStmt:
			if call, ok := n.X.(*ast.CallExpr); ok && isHasherPut(info, call, put, hasher) && len(call.Args) == 3 && types.ExprString(call.Args[1]) == `"package"` && types.ExprString(call.Args[2]) == "Version" {
				allowed[call.Fun.(*ast.Ident)] = true
				allowed[call.Args[0].(*ast.Ident)] = true
				puts++
			}
		case *ast.SelectorExpr:
			if id, ok := n.X.(*ast.Ident); ok && hasher != nil && info.Uses[id] == hasher && n.Sel.Name == "Sum" {
				allowed[id] = true
			}
		}
		return true
	})
	if hasher == nil {
		problems = append(problems, "PackageKey defines no hasher h")
	}
	if puts != 2 {
		problems = append(problems, fmt.Sprintf("PackageKey has %d of its two puts (the version, and each part in the range over packageKeyInputs)", puts))
	}
	for id, obj := range info.Uses {
		if (obj == put || hasher != nil && obj == hasher) && !allowed[id] {
			problems = append(problems, fmt.Sprintf("%s: a use of %s outside the key's hashing: the package key hashes its inputs only through packageKeyInputs", fset.Position(id.Pos()), id.Name))
		}
	}
	sort.Strings(problems)
	return problems
}

// isHasherPut reports whether call is put(h, …) with PackageKey's hasher.
func isHasherPut(info *types.Info, call *ast.CallExpr, put, hasher types.Object) bool {
	fun, ok := call.Fun.(*ast.Ident)
	if !ok || info.Uses[fun] != put || len(call.Args) == 0 || hasher == nil {
		return false
	}
	arg, ok := call.Args[0].(*ast.Ident)
	return ok && info.Uses[arg] == hasher
}

// link creates the link, or retargets it.
func (w *packageWorld) link() {
	target := linkTargets[w.rng.IntN(len(linkTargets))]
	if err := os.Remove(w.full(linkPath)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		w.t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(w.full(linkPath)), 0o755); err != nil {
		w.t.Fatal(err)
	}
	if err := os.Symlink(target, w.full(linkPath)); err != nil {
		w.t.Fatal(err)
	}
	w.note("link %s -> %s", linkPath, target)
}

// rename moves an index entry to a new path with the same mode and blob,
// as git mv does; with no entry it stages one.
func (w *packageWorld) rename() {
	if len(w.index) == 0 {
		w.stage()
		return
	}
	w.version++
	i := w.rng.IntN(len(w.index))
	staged, _, _ := strings.Cut(w.index[i], "\t")
	index := slices.Delete(slices.Clone(w.index), i, i+1)
	w.index = append(index, fmt.Sprintf("%s\tg/moved%d.go", staged, w.version))
	slices.Sort(w.index)
}

// ancestor changes a name on the way to the dependency x/y/z (TS2 of
// gate-tier round 3): x or x/y becomes a file, or a file there gets new
// content, so a closure that drops the directories on the way to a
// dependency misses it. When the name is a file already, the rewrite
// changes that entry alone.
func (w *packageWorld) ancestor() {
	name := []string{"x", "x/y"}[w.rng.IntN(2)]
	if info, err := os.Lstat(w.full(name)); err == nil && info.Mode().IsRegular() {
		w.note("ancestor %s: rewrite %t", name, w.write(name))
		return
	}
	// The name becomes a file: what is there goes, and for x/y a file
	// at x goes too.
	if info, err := os.Lstat(w.full("x")); name == "x/y" && err == nil && !info.IsDir() {
		if err := os.Remove(w.full("x")); err != nil {
			w.t.Fatal(err)
		}
	}
	if err := os.RemoveAll(w.full(name)); err != nil {
		w.t.Fatal(err)
	}
	w.note("ancestor %s: replaced by a file %t", name, w.write(name))
}

// reachTargets are the places outside a's closure that the link can lead
// to, each with a file in it that reach writes.
var reachTargets = map[string]string{"../docs/tree": "docs/tree/sub/t2.txt", "../docs/two.md": "docs/two.md", "../c/c.go": "c/c.go"}

// reach changes what the link leads to and nothing else of a's closure
// (TS2): it writes the file the link reaches outside a's closure, after it
// points the link at such a place when it leads elsewhere. So a key that
// does not follow a covered link's reach misses the write.
func (w *packageWorld) reach() {
	target, err := os.Readlink(w.full(linkPath))
	if _, ok := reachTargets[target]; err != nil || !ok {
		targets := slices.Sorted(maps.Keys(reachTargets))
		target = targets[w.rng.IntN(len(targets))]
		if err := os.Remove(w.full(linkPath)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			w.t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(w.full(linkPath)), 0o755); err != nil {
			w.t.Fatal(err)
		}
		if err := os.Symlink(target, w.full(linkPath)); err != nil {
			w.t.Fatal(err)
		}
		w.note("reach: link %s -> %s", linkPath, target)
	}
	w.note("reach: write %s: %t", reachTargets[target], w.write(reachTargets[target]))
}

// classify is the model's Git: one ignored file, every other path tracked.
func classify(name string) Class {
	if name == ignoredPath {
		return Ignored
	}
	return Tracked
}

// collect is the real facts of the world, as package main collects them,
// with the stat-keyed changes since now-RecentWindow as Recent.
func (w *packageWorld) collect(tier string, now time.Time) *Facts {
	entries, recent, unreplayable, err := Walk(w.root, classify, now)
	if err != nil {
		w.t.Fatal(err)
	}
	links, err := NewLinks(w.root, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	unreplayable = append(unreplayable, links.FollowLinks(entries)...)
	if len(unreplayable) > 0 {
		w.t.Fatalf("unreplayable %v", unreplayable)
	}
	f := &Facts{Executable: w.exe, Root: w.rootName, Head: fmt.Sprint(w.head), Index: slices.Clone(w.index), Entries: entries,
		Environment: []Variable{{Name: "GOFLAGS", Set: true, Value: w.env}},
		Settings:    []Variable{{Name: "ERGO_GATE_CACHE", Set: true, Value: w.setting}},
		Recent:      recent}
	for name, value := range w.tools {
		f.Host = append(f.Host, Fact{Name: "program " + name, Value: value})
	}
	for name, value := range w.host {
		f.Host = append(f.Host, Fact{Name: name, Value: value})
	}
	return WithTier(f, tier)
}

// facts are the world's facts with nothing recent: every change is older
// than RecentWindow at a collection an hour on.
func (w *packageWorld) facts(tier string) *Facts {
	return w.collect(tier, w.clock.Add(time.Hour))
}

// refCovers is the model's statement of the closure rule for one path: a
// file or directory directly in a dependency's directory (the package's
// own included), a directory on the way to a dependency, an embedded file
// and the directories between it and its package, the package's testdata
// at any depth, a module lookup name anywhere, go.mod and go.sum, and each
// declared input by its grant. A program grant covers what its own closure
// compiles.
func refCovers(c *Closure, p string) bool {
	if refCompiled(c, p) {
		return true
	}
	switch {
	case p == c.Dir+"/testdata", strings.HasPrefix(p, c.Dir+"/testdata/"):
		return true
	case slices.Contains([]string{"go.mod", "go.work", "go.work.sum", "vendor"}, path.Base(p)), p == "go.sum":
		return true
	}
	for _, a := range c.Declared {
		switch a.Grant {
		case GrantFile:
			if p == a.Path || path.Dir(p) == a.Path {
				return true
			}
		case GrantListing:
			if p == a.Path || path.Dir(p) == a.Path {
				return true
			}
		case GrantFiles:
			if ok, _ := path.Match(a.Path, p); ok {
				return true
			}
		case GrantSubtree:
			if a.Path == "." || p == a.Path || strings.HasPrefix(p, a.Path+"/") {
				return true
			}
		case GrantProgram:
			if refCompiled(c.Programs[a.Path], p) {
				return true
			}
		}
	}
	return false
}

// refCompiled: what the go command reads to build the closure's packages.
func refCompiled(c *Closure, p string) bool {
	deps := append([]string{c.Dir}, c.Deps...)
	for _, d := range deps {
		if p == d || path.Dir(p) == d || strings.HasPrefix(d, p+"/") {
			return true
		}
	}
	for _, e := range c.Embeds {
		if p == e || strings.HasPrefix(e, p+"/") {
			return true
		}
	}
	return false
}

// refReadsGit: a declared input that reaches .git makes HEAD and the index
// inputs.
func refReadsGit(c *Closure) bool {
	for _, a := range c.Declared {
		if a.Grant == GrantSubtree && (a.Path == "." || a.Path == ".git") || a.Grant == GrantFile && a.Path == ".git" {
			return true
		}
	}
	return false
}

// describe states one path of the world: a directory by its name, a file
// by its keyed permission bits and content (the ignored one by content too:
// it only ever changes content), a link by its target text and what it
// leads to, read through the operating system.
func (w *packageWorld) describe(b *strings.Builder, rel string, follow bool) {
	full := w.full(rel)
	info, err := os.Lstat(full)
	if err != nil {
		fmt.Fprintf(b, "absent %s\n", rel)
		return
	}
	switch {
	case info.IsDir():
		fmt.Fprintf(b, "dir %s\n", rel)
	case info.Mode()&fs.ModeSymlink != 0:
		target, _ := os.Readlink(full)
		fmt.Fprintf(b, "link %s %s\n", rel, target)
		if !follow {
			return
		}
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil {
			fmt.Fprintf(b, "  leads nowhere\n")
			return
		}
		root, _ := filepath.EvalSymlinks(w.root)
		place, _ := filepath.Rel(root, resolved)
		_ = filepath.WalkDir(resolved, func(name string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			sub, _ := filepath.Rel(root, name)
			fmt.Fprintf(b, "  via %s: ", place)
			w.describe(b, filepath.ToSlash(sub), false)
			return nil
		})
	default:
		content, _ := os.ReadFile(full)
		fmt.Fprintf(b, "file %s %o %q\n", rel, info.Mode().Perm()&KeyedPermissions, content)
	}
}

// refInputs is the model's statement of a package's inputs: its table
// entry, each path its closure covers, described, HEAD and the index when
// it reads .git, and every fact that is not a path.
func (w *packageWorld) refInputs(c *Closure) string {
	var b strings.Builder
	if c != nil {
		fmt.Fprintf(&b, "package %s declares %v\n", c.Dir, c.Declared)
	}
	_ = filepath.WalkDir(w.root, func(name string, entry fs.DirEntry, err error) error {
		if err != nil || name == w.root {
			return err
		}
		rel, _ := filepath.Rel(w.root, name)
		rel = filepath.ToSlash(rel)
		if c == nil || refCovers(c, rel) {
			w.describe(&b, rel, c != nil)
		}
		return nil
	})
	if c == nil || refReadsGit(c) {
		fmt.Fprintf(&b, "head %d index %v\n", w.head, w.index)
	}
	return b.String() + fmt.Sprint(w.tools, w.host, w.env, w.setting, w.exe, w.rootName)
}

// stepName is the name of the fast step of the package in slot i.
func (w *packageWorld) stepName(i int) string { return "go test ./" + w.names[i] + " (fast)" }

// refWhole is the model's statement of the whole run's inputs: everything.
func (w *packageWorld) refWhole() string { return w.refInputs(nil) }

// lookup reports whether the package store replays the key.
func (w *packageWorld) lookup(key string) bool {
	recorded, err := w.store.Packages().Lookup(key, func() (*os.File, error) { return os.CreateTemp(w.spool, "spool") })
	if recorded != nil {
		recorded.Close()
	}
	return err == nil
}

func (w *packageWorld) fastRun() {
	facts := w.facts("fast")
	packages := w.store.Packages()
	allPassed := true
	for i, c := range w.packages {
		identity := w.identity[w.names[i]]
		name := w.stepName(i)
		key, problem := PackageKey(facts, c, name, identity)
		if problem != "" {
			w.t.Fatalf("%s: %s", name, problem)
		}
		ref := "fast " + name + " " + identity + " " + w.refInputs(c)
		replayed := w.lookup(key)
		w.note("fast %s [%s]: replayed %t, model %t", c.Dir, identity, replayed, w.passed[ref])
		if replayed != w.passed[ref] {
			w.t.Errorf("fast %s [%s]: replayed %t, but the model has a pass at these inputs: %t", c.Dir, identity, replayed, w.passed[ref])
		}
		if replayed {
			continue
		}
		pass := w.rng.IntN(5) > 0
		allPassed = allPassed && pass
		object, err := w.store.NewObject()
		if err != nil {
			w.t.Fatal(err)
		}
		fmt.Fprintf(object, "ok %s\n", c.Dir)
		endKey := key
		// An edit of one of its files while it runs: its end key differs,
		// so its pass is not recorded; the edit is then undone, so the
		// next run sees the inputs of this one.
		var undo func()
		if own := w.ownFiles(c); pass && len(own) > 0 && w.rng.IntN(4) == 0 {
			edited := own[w.rng.IntN(len(own))]
			before, err := os.ReadFile(w.full(edited))
			if err != nil {
				w.t.Fatal(err)
			}
			w.write(edited)
			endKey, _ = PackageKey(w.facts("fast"), c, name, identity)
			if endKey == key {
				w.t.Errorf("fast %s: an edit of %s during the run left its key unchanged", c.Dir, edited)
			}
			undo = func() {
				if err := os.WriteFile(w.full(edited), before, 0o644); err != nil {
					w.t.Fatal(err)
				}
			}
			w.note("fast %s: %s edited during the run", c.Dir, edited)
		}
		line := packages.RecordPackage(key, endKey, pass, Step{Name: name, Outcome: "ran", Passed: w.clock}, object, w.clock)
		object.Abort()
		if undo != nil {
			undo()
			if pass && !strings.Contains(line, "not recorded") {
				w.t.Errorf("fast %s: a pass whose inputs changed during the run: %q, want not recorded", c.Dir, line)
			}
			w.passed[ref] = false
			w.note("fast %s ran: passed %t, not recorded", c.Dir, pass)
			continue
		}
		if strings.Contains(line, "not recorded") {
			w.t.Fatalf("%s: %s", name, line)
		}
		w.passed[ref] = pass
		w.note("fast %s ran: passed %t", c.Dir, pass)
	}
	// The fast run's whole record, under the fast tier's key.
	w.wholeRun(facts, "fast", allPassed)
}

// ownFiles are the regular files directly in the package's directory.
func (w *packageWorld) ownFiles(c *Closure) []string {
	var own []string
	for _, f := range w.files() {
		if path.Dir(f) == c.Dir {
			own = append(own, f)
		}
	}
	return own
}

// recentRun writes the ignored file and collects at once: the file changed
// within RecentWindow, so each package whose closure covers it has no key
// (and so neither replays nor records), and each other package has one.
func (w *packageWorld) recentRun() {
	w.write(ignoredPath)
	facts := w.collect("fast", time.Now())
	if !slices.Contains(facts.Recent, ignoredPath) {
		w.t.Fatalf("Walk does not name %s, written now, as recent: %v", ignoredPath, facts.Recent)
	}
	for i, c := range w.packages {
		_, problem := PackageKey(facts, c, w.stepName(i), identities[0])
		covered := refCovers(c, ignoredPath)
		w.note("recent %s: covered %t, problem %q", c.Dir, covered, problem)
		if covered != (problem != "") {
			w.t.Errorf("recent %s: %s changed now, covered %t, but the key's problem is %q", c.Dir, ignoredPath, covered, problem)
		}
	}
}

// wholeRun records or withdraws a whole-gate run at the facts' key.
func (w *packageWorld) wholeRun(facts *Facts, tier string, pass bool) {
	run := w.store.Begin(facts)
	object, err := run.Output()
	if err != nil {
		w.t.Fatal(err)
	}
	fmt.Fprintf(object, "%s run\n", tier)
	run.Passed("step", object, "ran", 1, w.clock)
	line := run.Finish(pass, func() (*Facts, error) { return w.facts(tier), nil }, Meta{Now: w.clock})
	if pass && !strings.Contains(line, "recorded") {
		w.t.Fatalf("%s run not recorded: %s", tier, line)
	}
	w.passed[tier+" * "+w.refWhole()] = pass
}

func (w *packageWorld) fullRun() {
	pass := w.rng.IntN(5) > 0
	w.wholeRun(w.facts("full"), "full", pass)
	w.note("full run: passed %t", pass)
}

func (w *packageWorld) mergeRun() {
	full := "full * " + w.refWhole()
	innerRan := false
	tier := MergeTier{
		Store:   w.store,
		Collect: func() (*Facts, error) { return w.facts("full"), nil },
		Inner: func() bool {
			innerRan = true
			w.wholeRun(w.facts("full"), "full", true)
			return true
		},
		Uncached: func() ([]string, map[string]Verdict) {
			return []string{"step"}, map[string]Verdict{"step": Pass}
		},
	}
	expectInner := !w.passed[full]
	fastOnly := w.passed["fast * "+w.refWhole()] && expectInner
	code := tier.Run(&strings.Builder{})
	w.note("merge: inner ran %t (model: %t, a fast record only: %t), exit %d", innerRan, expectInner, fastOnly, code)
	if innerRan != expectInner {
		w.t.Errorf("merge: the inner tier ran %t; the model has a full pass at these inputs: %t (a fast record only: %t)", innerRan, w.passed[full], fastOnly)
	}
	if code != 0 {
		w.t.Errorf("merge: exit %d with equal verdicts", code)
	}
}
