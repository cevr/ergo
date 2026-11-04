package closure

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// The round-2 verifier's temporal probes (round-2-receipts: switch-probe,
// TestVerifyRemovedInModuleLink, TestVerifyRemovedPathSpelling) and the
// property over state sequences. A test read something in the checkout
// and then changed what the same path names; a classifier that resolves
// the path after the run then saw the new object, not the one read. In the
// sandbox each change fails, so the sweep reports the package, and each
// sequence that only reads resolves after the run as it did when it was
// read.

// fixture is a module checkout for the sandbox tests: root holds go.mod,
// docs and the package internal/p, whose testdata has links of each kind.
// outside is a directory next to the checkout, with a link back into it;
// base holds both. other, elsewhere, is a replacement for base: the round-3
// verifier's tree, whose link reaches docs in the checkout once base has
// moved to base-moved.
type fixture struct {
	base, root, pkg, outside, other string // physical paths
}

// fixtureLinks are the links in internal/p/testdata, by name, with their
// targets; "$root" is the module root and "$outside" the directory next to
// it.
var fixtureLinks = [][2]string{
	{"out", "$root/docs"},    // absolute, out of the closure
	{"rel", "../../../docs"}, // relative, out of the closure
	{"chain", "out"},         // a link to a link
	{"local", "in"},          // relative, inside testdata
	{"abslocal", "$root/internal/p/testdata/in"},
	{"dot", "out/.."},      // a link whose target climbs
	{"away", "$root/docs"}, // renamed away by a probe
	{"ext", "$outside"},    // out of the checkout, and back in through outside/back
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	other, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{base: base, root: filepath.Join(base, "m"), outside: filepath.Join(base, "s"), other: other}
	f.pkg = filepath.Join(f.root, "internal", "p")
	for name, data := range map[string]string{
		"m/go.mod":                           "module fixture\n\ngo 1.27\n",
		"m/docs/tooling.md":                  "the test package reads this document",
		"m/docs/sub/x.md":                    "a page below docs",
		"m/internal/p/p.go":                  "package p\n",
		"m/internal/p/testdata/in/a.txt":     "own testdata",
		"m/internal/p/testdata/in/sub/b.txt": "own testdata below",
		"m/internal/p/testdata/sub/c.txt":    "own testdata",
		"s/plain.txt":                        "outside the checkout",
	} {
		name = filepath.Join(base, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, l := range fixtureLinks {
		target := strings.NewReplacer("$root", f.root, "$outside", f.outside).Replace(l[1])
		if err := os.Symlink(target, filepath.Join(f.pkg, "testdata", l[0])); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(f.root, "docs"), filepath.Join(f.outside, "back")); err != nil {
		t.Fatal(err)
	}
	// The round-3 verifier's replacement for the checkout's parent: the
	// same package path, whose link reaches docs in the moved checkout.
	if err := os.MkdirAll(filepath.Join(other, "m/internal/p/testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(base+"-moved/m/docs", filepath.Join(other, "m/internal/p/testdata/local")); err != nil {
		t.Fatal(err)
	}
	return f
}

// snapshot describes every entry below root without following links: its
// type, its link target and its content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		desc := d.Type().String()
		switch {
		case d.Type()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			desc += " -> " + target
		case d.Type().IsRegular():
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			desc += " " + string(data)
		}
		out[p] = desc
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// testEnv is the environment the sweep gives a test binary, with extra.
func testEnv(dir string, extra ...string) []string {
	return withEnv(sweepEnvironment(), append([]string{"PWD=" + dir}, extra...)...)
}

// probeVariable selects the probe TestTemporalProbe runs.
const probeVariable = "ERGO_CLOSURE_PROBE"

// TestTemporalProbe runs one probe in the package directory of a fixture.
// It runs only as the target of TestTemporalProbesFailTheSweep.
func TestTemporalProbe(t *testing.T) {
	probe := os.Getenv(probeVariable)
	if probe == "" {
		t.Skip("the target of TestTemporalProbesFailTheSweep")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	switch probe {
	case "switch":
		// switch-probe.patch: read docs through an absolute link, by
		// lookup and by a child, then replace the link with one to the
		// package's own testdata and read the same spelling again.
		must(os.MkdirAll("testdata/review-safe", 0o755))
		must(os.WriteFile("testdata/review-safe/tooling.md", []byte("safe local data"), 0o600))
		must(os.Symlink(filepath.Join(root, "docs"), "testdata/review-switch"))
		raw := "testdata/review-switch/tooling.md"
		_, err := os.Stat(raw)
		must(err)
		out, err := exec.Command("cat", raw).Output()
		if err != nil || !strings.Contains(string(out), "test package") {
			t.Fatalf("child read docs/tooling.md: %v", err)
		}
		must(os.Symlink("review-safe", "testdata/review-next"))
		must(os.Rename("testdata/review-next", "testdata/review-switch"))
		b, err := os.ReadFile(raw)
		if err != nil || string(b) != "safe local data" {
			t.Fatalf("new target: %q %v", b, err)
		}
	case "removed-link":
		// TestVerifyRemovedInModuleLink: read docs through a link made
		// in testdata, then point the link at the package's own testdata.
		must(os.Symlink(filepath.Join(root, "docs"), "testdata/next"))
		must(os.Rename("testdata/next", "testdata/swap"))
		_, err := os.Stat("testdata/swap/tooling.md")
		must(err)
		_, err = os.ReadFile("testdata/swap/tooling.md")
		must(err)
		must(os.Symlink("in", "testdata/next"))
		must(os.Rename("testdata/next", "testdata/swap"))
	case "removed-spelling":
		// TestVerifyRemovedPathSpelling: read docs through a link of the
		// fixture, then move the link away, so the spelling names nothing.
		_, err := os.ReadFile("testdata/away/tooling.md")
		must(err)
		must(os.Rename("testdata/away", "testdata/renamed-away"))
	default:
		t.Fatalf("%s=%q: no such probe", probeVariable, probe)
	}
}

// TestTemporalProbesFailTheSweep: each probe reads docs/tooling.md, which
// lies outside internal/p's closure, and then changes the checkout so that
// the path it read names something inside it. Under the sweep the change
// fails with EROFS, so the sweep reports a closure violation that names
// the package. Red when the sweep runs with a writable checkout (round 2
// head 9f8a6b79): the probes pass and the read is classified by the final
// tree.
func TestTemporalProbesFailTheSweep(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, probe := range []string{"switch", "removed-link", "removed-spelling"} {
		t.Run(probe, func(t *testing.T) {
			f := newFixture(t)
			stem := filepath.Join(t.TempDir(), "internal_p")
			entries, err := runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{probeVariable + "=" + probe}, self, "-test.run=^TestTemporalProbe$")
			wantViolation(t, err, entries)
		})
	}
}

// wantViolation fails unless err is a closure violation of internal/p that
// quotes the refused write.
func wantViolation(t *testing.T, err error, entries []entry) {
	t.Helper()
	var v violation
	if !errors.As(err, &v) {
		outside := newClosure("internal/p", nil, nil).check(entries, map[string]bool{})
		t.Fatalf("the probe passed the sweep (err %v); recorded %v, outside the closure %v", err, entries, outside)
	}
	if v.pkg != "internal/p" || !strings.Contains(v.Error(), "internal/p") {
		t.Errorf("the violation does not name the package: %v", v)
	}
	if !strings.Contains(v.Error(), syscall.EROFS.Error()) {
		t.Errorf("the violation does not show the refused write: %v", v)
	}
	t.Log(v)
}

// TestSandboxRefusesWhenItCannotProtect (S4): when the sandbox cannot be
// built or its scratch directories break S2, the run is refused, says why,
// and the test binary never runs; with a right sandbox the same binary
// runs.
func TestSandboxRefusesWhenItCannotProtect(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	report := t.TempDir()
	fresh := func() string {
		dir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	full := fresh()
	if err := os.WriteFile(filepath.Join(full, "old"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(f.pkg, "testdata", "in", "sub")
	cases := []struct {
		name string
		sb   sandbox
		want string
	}{
		{"no checkout", sandbox{Root: filepath.Join(f.root, "missing"), Scratch: []string{fresh()}}, "no such file or directory"},
		{"no scratch", sandbox{Root: f.root}, "no scratch directory"},
		{"scratch is the checkout's parent", sandbox{Root: f.root, Scratch: []string{f.base}}, "is an ancestor of the checkout"},
		{"scratch is the checkout", sandbox{Root: f.root, Scratch: []string{f.root}}, "lies in the checkout"},
		{"scratch in the checkout", sandbox{Root: f.root, Scratch: []string{inside}}, "lies in the checkout"},
		{"scratch not fresh", sandbox{Root: f.root, Scratch: []string{full}}, "is not empty"},
		{"scratch by a link", sandbox{Root: f.root, Scratch: []string{filepath.Join(f.pkg, "testdata", "ext")}}, "physical path"},
		{"a kernel write that is not cgroup2", sandbox{Root: f.root, Scratch: []string{fresh()}, Kernel: []string{f.base}}, "is not a cgroup2 file system"},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			marker := filepath.Join(report, fmt.Sprintf("ran-%d", i))
			op, err := json.Marshal(childOp{Op: "write", Path: marker})
			if err != nil {
				t.Fatal(err)
			}
			err = runProtected(c.sb, "internal/p", f.pkg, testEnv(f.pkg, childVariable+"="+string(op)), filepath.Join(report, fmt.Sprintf("refused-%d.out", i)), self)
			var r refusal
			if !errors.As(err, &r) || !strings.Contains(r.Error(), c.want) {
				t.Fatalf("got %v, want a refusal that says %q", err, c.want)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("the test binary ran although the sandbox was refused")
			}
			t.Log(r)
		})
	}

	// The control: a fresh scratch directory, and the binary writes in it.
	scratchDir := fresh()
	marker := filepath.Join(scratchDir, "ran")
	op, err := json.Marshal(childOp{Op: "write", Path: marker})
	if err != nil {
		t.Fatal(err)
	}
	if err := runProtected(sandbox{Root: f.root, Scratch: []string{scratchDir}}, "internal/p", f.pkg, testEnv(f.pkg, childVariable+"="+string(op)), filepath.Join(report, "ran.out"), self); err != nil {
		t.Fatalf("the control run failed: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("the control run did not run the binary: %v", err)
	}
}

// childVariable makes this test binary run one file system operation
// (childOp) and print its result (childResult) as JSON: the child process
// of the sequences.
const childVariable = "ERGO_CLOSURE_CHILD"

type childOp struct {
	Op, Path, Target string
	Scratch          []string `json:",omitempty"`
}

type childResult struct {
	Reached string    // the kernel's location of the object an open reached
	Ident   [2]uint64 // its device and inode
	Err     string
	Errno   string // the error's name: EROFS, EXDEV, EPERM, ENOENT...
	EROFS   bool
}

func childMain(spec string) int {
	var op childOp
	if err := json.Unmarshal([]byte(spec), &op); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	res := do(op)
	out, err := json.Marshal(res)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	os.Stdout.Write(append(out, '\n'))
	return 0
}

// errnoNames are the error numbers the sequences tell apart.
var errnoNames = map[syscall.Errno]string{
	syscall.EROFS: "EROFS", syscall.EXDEV: "EXDEV", syscall.EPERM: "EPERM",
	syscall.EACCES: "EACCES", syscall.ENOENT: "ENOENT", syscall.EEXIST: "EEXIST",
	syscall.ENOTDIR: "ENOTDIR", syscall.ENOSPC: "ENOSPC",
}

// do runs one operation in this process.
func do(op childOp) childResult {
	var res childResult
	var err error
	switch op.Op {
	case "open", "stat":
		res.Reached, res.Ident, err = reachedBy(op.Path, op.Op == "stat")
	case "retarget":
		// Replace the link at Path by one to Target, atomically.
		next := filepath.Join(filepath.Dir(op.Path), ".next")
		if err = os.Symlink(op.Target, next); err == nil {
			err = os.Rename(next, op.Path)
		}
	case "rename":
		// The system call itself: os.Rename looks up both names first and
		// answers EEXIST or ENOENT before the kernel can refuse.
		if err = syscall.Rename(op.Path, op.Target); err != nil {
			err = &os.LinkError{Op: "rename", Old: op.Path, New: op.Target, Err: err}
		}
	case "remove":
		err = os.Remove(op.Path)
	case "write":
		err = os.WriteFile(op.Path, []byte("written"), 0o644)
	case "link":
		// A hard link: Target becomes another name of Path.
		err = os.Link(op.Path, op.Target)
	case "symlink":
		// A symbolic link at Path, to Target.
		err = os.Symlink(op.Target, op.Path)
	case "mount":
		err = bindMount(op.Path, op.Target)
	case "sandbox":
		sb := sandbox{Root: op.Path, Scratch: op.Scratch}
		if err = checkSandbox(sb); err == nil {
			var self string
			if self, err = os.Executable(); err == nil {
				err = checkPrivileges(sb, self)
			}
		}
	case "noop":
	default:
		err = fmt.Errorf("unknown operation %q", op.Op)
	}
	if err != nil {
		res.Err = err.Error()
		res.EROFS = errors.Is(err, syscall.EROFS)
		var errno syscall.Errno
		if errors.As(err, &errno) {
			res.Errno = errnoNames[errno]
			if res.Errno == "" {
				res.Errno = errno.Error()
			}
		}
	}
	return res
}

// A step of a state sequence: a read (open or stat, here or in a child)
// or a change (here or in a child). A path is relative to the package
// directory or absolute; "$tmp" at its start is the run's TMPDIR, a
// scratch directory, which the runner fills before the first sequence
// (scratchSetup).
type step struct {
	Op     string // open, stat, retarget, rename, remove, write, link, symlink, mount
	Child  bool
	Path   string
	Target string
}

func (s step) reads() bool { return s.Op == "open" || s.Op == "stat" }

// tmpPrefix marks a path in the run's TMPDIR.
const tmpPrefix = "$tmp"

func inScratch(p string) bool { return strings.HasPrefix(p, tmpPrefix) }

// refusedWith is the error the sandbox must give a change, or "" for a
// change in the scratch directory only, which the sandbox allows. Linux
// checks a cross-mount hard link or rename (EXDEV) before the read-only
// mount (EROFS) for a rename, and after the new name's mount for a link.
// (A mount is not a step: testinputs refuses the log of a run that tries
// one, TestMountAttemptsFailTheSweep, and checkPrivileges tries each.)
func (s step) refusedWith() string {
	switch s.Op {
	case "link":
		switch {
		case !inScratch(s.Target):
			return "EROFS"
		case !inScratch(s.Path):
			return "EXDEV"
		}
		return ""
	case "rename":
		switch {
		case inScratch(s.Path) != inScratch(s.Target):
			return "EXDEV"
		case !inScratch(s.Path):
			return "EROFS"
		}
		return ""
	}
	if inScratch(s.Path) {
		return ""
	}
	return "EROFS"
}

// scratchSetup is what the runner makes in TMPDIR before the sequences:
// a file to write, a file no step removes, and links into the checkout.
func scratchSetup(tmp, root, pkg string) error {
	for _, name := range []string{"w", "keep"} {
		if err := os.WriteFile(filepath.Join(tmp, name), []byte("scratch"), 0o644); err != nil {
			return err
		}
	}
	if err := os.Symlink(filepath.Join(root, "docs"), filepath.Join(tmp, "l")); err != nil {
		return err
	}
	return os.Symlink(filepath.Join(pkg, "testdata"), filepath.Join(tmp, "d"))
}

// stepResult is what the runner saw at the step: the operation's result,
// the path with "$tmp" replaced, and for a read, the entries the
// classifier gave the path at that moment.
type stepResult struct {
	childResult
	Path   string
	Access []string
}

// The variable of the sequence runner: the file of sequences, outside the
// checkout. The runner prints its results on one line of its output, after
// resultsMarker, since everything outside its scratch directories is
// read-only.
const (
	sequencesVariable = "ERGO_CLOSURE_SEQUENCES"
	resultsMarker     = "ergo-closure-results "
)

// TestRunSequences runs the sequences of TestReadOnlyStateSequences in the
// package directory of a fixture. It runs only as that test's target.
func TestRunSequences(t *testing.T) {
	file := os.Getenv(sequencesVariable)
	if file == "" {
		t.Skip("the target of TestReadOnlyStateSequences")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var seqs [][]step
	if err := json.Unmarshal(data, &seqs); err != nil {
		t.Fatal(err)
	}
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(pwd))
	tmp := os.TempDir()
	if err := scratchSetup(tmp, root, pwd); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	expand := func(p string) string {
		if rest, ok := strings.CutPrefix(p, tmpPrefix); ok {
			return tmp + rest
		}
		return p
	}
	results := make([][]stepResult, len(seqs))
	for i, seq := range seqs {
		for _, s := range seq {
			s.Path, s.Target = expand(s.Path), expand(s.Target)
			op := childOp{Op: s.Op, Path: s.Path, Target: s.Target}
			r := stepResult{Path: s.Path}
			if s.Child {
				spec, _ := json.Marshal(op)
				cmd := exec.Command(self)
				cmd.Env = append(os.Environ(), childVariable+"="+string(spec))
				out, err := cmd.Output()
				if err != nil {
					t.Fatalf("child %v: %v", op, err)
				}
				if err := json.Unmarshal(out, &r.childResult); err != nil {
					t.Fatalf("child %v: %q: %v", op, out, err)
				}
			} else {
				r.childResult = do(op)
			}
			if s.reads() {
				r.Access = classify(t, s.Op, s.Path, pwd, root)
			}
			results[i] = append(results[i], r)
		}
	}
	out, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.Write(append(append([]byte("\n"+resultsMarker), out...), '\n'))
}

// classify returns the entries the closure check gives a read of p, as
// "op path" lines.
func classify(t *testing.T, op, p, pwd, root string) []string {
	t.Helper()
	entries, err := parseLog([]byte("# test log\n"+op+" "+p+"\n"), pwd, []string{root})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.op+" "+e.path)
	}
	return out
}

// sequences generates n state sequences over the fixture: reads of paths
// through each kind of link (absolute, relative, chained, climbing, local,
// and links in the scratch directory into the checkout), lookups, and
// children's reads, mixed with changes: of the links and the files they
// reach, of the checkout's ancestors (rename, replacement, restore), hard
// links of checkout files into the scratch directory and next to the
// checkout, and the scratch directory's own links, here and in children.
// The first ones are the shapes the verifiers named. Bind mounts made
// before the run are TestWritableAliasesFailTheSweep's.
func sequences(f fixture, n int, seed uint64) [][]step {
	rel := []string{
		"testdata/in/a.txt", "testdata/out/tooling.md", "testdata/rel/tooling.md",
		"testdata/chain/sub/x.md", "testdata/local/sub/b.txt", "testdata/abslocal/a.txt",
		"testdata/dot/docs/tooling.md", "testdata/out/../go.mod", "testdata/chain/../docs/sub",
		"testdata/missing/x", "testdata/in/new.txt", "testdata/moved/tooling.md",
		"testdata/out", "testdata/in", "testdata/away/tooling.md", "testdata/local/../sub/c.txt",
		"testdata/ext/back/tooling.md", "testdata/ext/plain.txt",
	}
	var paths []string
	for _, p := range rel {
		paths = append(paths, p, filepath.Join(f.pkg, p))
	}
	moved := f.base + "-moved"
	paths = append(paths,
		tmpPrefix+"/l/tooling.md", tmpPrefix+"/l/sub", tmpPrefix+"/l", tmpPrefix+"/d/in/a.txt",
		tmpPrefix+"/d/out/tooling.md", tmpPrefix+"/w", tmpPrefix+"/l2/a.txt",
		filepath.Join(f.outside, "back/tooling.md"), filepath.Join(moved, "m/docs/tooling.md"),
	)
	links := []string{"out", "rel", "chain", "local", "abslocal", "dot", "away", "ext"}
	targets := []string{filepath.Join(f.root, "docs"), "in", "../../../docs", "out", "sub", "/nonexistent/loop-probe-x", filepath.Join(f.pkg, "testdata", "in"), f.outside}
	scratchLinks := []string{tmpPrefix + "/l", tmpPrefix + "/d", tmpPrefix + "/l2"}
	back := filepath.Join(f.outside, "back")
	other := f.other
	// Ancestor changes: the round-3 verifier's move of the checkout's
	// parent, its replacement and its restore, and a move of the checkout.
	ancestors := []step{
		{Op: "rename", Path: f.base, Target: moved},
		{Op: "rename", Path: other, Target: f.base},
		{Op: "rename", Path: f.base, Target: other},
		{Op: "rename", Path: moved, Target: f.base},
		{Op: "rename", Path: f.root, Target: f.root + "-moved"},
		{Op: "rename", Path: filepath.Join(f.root, "docs"), Target: tmpPrefix + "/docs"},
	}
	// Aliases: hard links of checkout objects, into the scratch directory
	// (EXDEV) and next to the checkout (EROFS), and symbolic links.
	aliases := []step{
		{Op: "link", Path: "testdata/in/a.txt", Target: tmpPrefix + "/h"},
		{Op: "link", Path: filepath.Join(f.root, "docs/tooling.md"), Target: tmpPrefix + "/h"},
		{Op: "link", Path: filepath.Join(f.root, "docs/tooling.md"), Target: filepath.Join(f.outside, "h")},
		{Op: "link", Path: tmpPrefix + "/keep", Target: "testdata/in/h"},
		{Op: "link", Path: tmpPrefix + "/keep", Target: tmpPrefix + "/h2"},
		{Op: "symlink", Path: tmpPrefix + "/l2", Target: filepath.Join(f.pkg, "testdata", "in")},
		{Op: "symlink", Path: "testdata/new", Target: "in"},
	}

	seqs := [][]step{
		// Out of the checkout and back in through a link next to it, which
		// the test then tries to change; and the same read spelled from
		// outside, which the recorder resolves at access.
		{{Op: "open", Path: "testdata/ext/back/tooling.md"}, {Op: "open", Path: back + "/sub/x.md"}, {Op: "stat", Path: "testdata/ext/plain.txt", Child: true}},
		{{Op: "open", Path: back + "/tooling.md", Child: true}, {Op: "retarget", Path: back, Target: filepath.Join(f.pkg, "testdata", "in")}, {Op: "open", Path: "testdata/ext/back/a.txt"}},
		{{Op: "open", Path: "testdata/out/tooling.md"}, {Op: "retarget", Path: "testdata/out", Target: "in"}, {Op: "open", Path: "testdata/out/tooling.md"}},
		{{Op: "stat", Path: "testdata/chain/tooling.md"}, {Op: "retarget", Path: "testdata/out", Target: "sub"}, {Op: "open", Path: "testdata/chain/tooling.md"}},
		{{Op: "open", Path: "testdata/away/tooling.md", Child: true}, {Op: "rename", Path: "testdata/away", Target: "testdata/moved"}, {Op: "open", Path: "testdata/moved/tooling.md"}},
		{{Op: "open", Path: "testdata/rel/tooling.md"}, {Op: "remove", Path: "testdata/rel"}, {Op: "stat", Path: "testdata/rel/tooling.md"}},
		{{Op: "stat", Path: filepath.Join(f.pkg, "testdata/out/tooling.md")}, {Op: "retarget", Path: "testdata/out", Target: "in", Child: true}, {Op: "open", Path: "testdata/out/tooling.md", Child: true}},
		{{Op: "open", Path: "testdata/in/new.txt"}, {Op: "write", Path: "testdata/in/new.txt"}, {Op: "open", Path: "testdata/in/new.txt"}},
		// Round 3, M2: move the checkout's parent, put a replacement at its
		// name, read through the replacement by an absolute path, restore.
		{ancestors[0], ancestors[1], {Op: "open", Path: filepath.Join(f.pkg, "testdata/local/tooling.md")}, ancestors[2], ancestors[3]},
		{withChild(ancestors[0]), withChild(ancestors[1]), {Op: "open", Path: filepath.Join(f.pkg, "testdata/local/tooling.md"), Child: true}, withChild(ancestors[2]), withChild(ancestors[3])},
		{ancestors[4], {Op: "open", Path: filepath.Join(f.root, "docs/tooling.md")}, ancestors[5], {Op: "stat", Path: filepath.Join(moved, "m/docs/tooling.md")}},
		// Round 3, M1: a writable alias of the checkout. The scratch
		// directory's link reaches docs, is read, then retargeted to the
		// package's own testdata and read again; hard links of checkout
		// files are refused.
		{{Op: "open", Path: tmpPrefix + "/l/tooling.md"}, {Op: "retarget", Path: tmpPrefix + "/l", Target: filepath.Join(f.pkg, "testdata", "in")}, {Op: "open", Path: tmpPrefix + "/l/a.txt", Child: true}, {Op: "retarget", Path: tmpPrefix + "/l", Target: filepath.Join(f.root, "docs")}},
		{aliases[0], aliases[1], withChild(aliases[2]), aliases[3], aliases[4], {Op: "open", Path: "testdata/in/a.txt"}},
		{aliases[5], {Op: "open", Path: tmpPrefix + "/l2/a.txt"}, withChild(aliases[6]), {Op: "stat", Path: tmpPrefix + "/l/tooling.md", Child: true}},
		{{Op: "open", Path: "testdata/away/tooling.md"}, {Op: "retarget", Path: "testdata/away", Target: "in"}, {Op: "open", Path: tmpPrefix + "/d/away/tooling.md"}},
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for len(seqs) < n {
		var seq []step
		for range 2 + rng.IntN(5) {
			s := step{Child: rng.IntN(4) == 0}
			switch k := rng.IntN(14); {
			case k < 5:
				s.Op, s.Path = "open", paths[rng.IntN(len(paths))]
			case k < 8:
				s.Op, s.Path = "stat", paths[rng.IntN(len(paths))]
			case k == 8:
				s.Op, s.Path, s.Target = "retarget", "testdata/"+links[rng.IntN(len(links))], targets[rng.IntN(len(targets))]
				if rng.IntN(2) == 0 {
					s.Path = scratchLinks[rng.IntN(len(scratchLinks))]
				}
			case k == 9:
				s.Op, s.Path, s.Target = "rename", "testdata/"+links[rng.IntN(len(links))], "testdata/moved"
				if rng.IntN(2) == 0 {
					s.Path, s.Target = scratchLinks[rng.IntN(len(scratchLinks))], tmpPrefix+"/moved"
				}
			case k == 10:
				a := ancestors[rng.IntN(len(ancestors))]
				a.Child = s.Child
				s = a
			case k == 11:
				a := aliases[rng.IntN(len(aliases))]
				a.Child = s.Child
				s = a
			default:
				switch rng.IntN(4) {
				case 0:
					s.Op, s.Path, s.Child = "remove", "testdata/"+links[rng.IntN(len(links))], false
				case 1:
					s.Op, s.Path, s.Child = "write", "testdata/in/new.txt", false
				case 2:
					s.Op, s.Path = "write", tmpPrefix+"/w"
				default:
					s.Op, s.Path = "remove", scratchLinks[rng.IntN(len(scratchLinks))]
				}
			}
			seq = append(seq, s)
		}
		seqs = append(seqs, seq)
	}
	return seqs
}

func withChild(s step) step {
	s.Child = true
	return s
}

// TestReadOnlyStateSequences is the property over state sequences, run as
// the sweep runs a package (runTests: the sandbox, observed, test log):
//
//   - every change outside the scratch directory fails, with EROFS, or
//     EXDEV for a hard link or rename across mounts, here and in a child:
//     of the checkout, its ancestors, the directory
//     next to it, and of checkout files through every alias the test can
//     reach or make; the checkout, its parent and the directory next to it
//     are as they were, with the same identity; a change in the scratch
//     directory is not refused;
//   - every object of the checkout the kernel reached, by any spelling, it
//     reached at its checkout path: no other name of it exists;
//   - every read of a path that starts in the checkout has after the run
//     the classification it had at access time, and that names the object
//     the kernel reached, or the path is lost (fail closed);
//   - every file the kernel opened in the checkout, by any spelling, is an
//     entry of the run's log as the sweep reads it, or its read is lost.
//
// So the sweep's post-run classification is the access-time one.
func TestReadOnlyStateSequences(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	scratchDir := t.TempDir()
	const seed, n = 2026_11_04, 160
	seqs := sequences(f, n, seed)
	data, err := json.Marshal(seqs)
	if err != nil {
		t.Fatal(err)
	}
	seqFile := filepath.Join(scratchDir, "sequences.json")
	if err := os.WriteFile(seqFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	// The checkout's objects, by device and inode.
	objects := map[[2]uint64]string{}
	if err := filepath.WalkDir(f.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&fs.ModeSymlink != 0 {
			return err
		}
		id, err := identity(p)
		objects[id] = p
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, f.base)
	maps.Copy(before, snapshot(t, f.other))
	baseID, err := identity(f.base)
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := identity(f.root)
	if err != nil {
		t.Fatal(err)
	}
	stem := filepath.Join(scratchDir, "internal_p")
	logged, err := runTests(f.root, []string{f.root}, "internal/p", f.pkg, stem, []string{sequencesVariable + "=" + seqFile}, self, "-test.run=^TestRunSequences$", "-test.v")
	if err != nil {
		t.Fatalf("the sequence runner failed: %v", err)
	}
	inLog := map[string]bool{}
	for _, e := range logged {
		inLog[e.op+" "+e.path] = true
		// The observer's walk at access time and the sweep's replay of it
		// agree (no lookup missing, every line has a walk), and no process
		// outside the tree changed a looked-up location during the run.
		if strings.HasPrefix(e.op, opUnidentified) || strings.HasPrefix(e.op, opRaced) {
			t.Errorf("the run's log has %v", e)
		}
	}
	results, err := readResults(stem + ".out")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != len(seqs) {
		t.Fatalf("%d results for %d sequences", len(results), len(seqs))
	}
	after := snapshot(t, f.base)
	maps.Copy(after, snapshot(t, f.other))
	if !maps.Equal(before, after) {
		t.Errorf("the run changed the checkout, the directory next to it or the parent's replacement:\nbefore %v\nafter  %v", before, after)
	}
	if id, err := identity(f.base); err != nil || id != baseID {
		t.Errorf("the checkout's parent is not the one it was: %v", err)
	}
	if id, err := identity(f.root); err != nil || id != rootID {
		t.Errorf("the checkout is not the one it was: %v", err)
	}

	c := newClosure("internal/p", nil, nil)
	counts := map[string]int{}
	for i, seq := range seqs {
		for j, s := range seq {
			r := results[i][j]
			at := fmt.Sprintf("seed %d sequence %d step %d %+v", seed, i, j, s)
			if !s.reads() {
				want := s.refusedWith()
				switch {
				case want == "" && slices.Contains([]string{"EROFS", "EXDEV", "EPERM"}, r.Errno):
					t.Errorf("%s: a change in the scratch directory was refused: %s", at, r.Err)
				case want != "" && r.Errno != want:
					t.Errorf("%s: the change was not refused with %s: %q", at, want, r.Err)
				}
				counts["changes "+s.Op+" "+want]++
				continue
			}
			counts["reads"]++
			spelledInside := !inScratch(s.Path) && (!filepath.IsAbs(s.Path) || strings.HasPrefix(s.Path, f.root+"/"))
			post := r.Access
			if !inScratch(s.Path) {
				post = classify(t, s.Op, s.Path, f.pkg, f.root)
			}
			isLost := slices.ContainsFunc(post, func(line string) bool { return strings.HasPrefix(line, opLost) })
			if isLost {
				counts["lost"]++
			}
			want, inside := "", false
			if r.Reached != "" {
				want, inside = relative(r.Reached, []string{f.root})
			}
			// No other name: a checkout object is reached at its path.
			if p, ok := objects[r.Ident]; ok && r.Reached != "" {
				if r.Reached != p {
					t.Errorf("%s: the kernel reached the checkout's %s by another name, %s", at, p, r.Reached)
				}
				if !spelledInside {
					counts["checkout objects read by a spelling from outside"]++
				}
			}
			// A file the kernel opened in the checkout is in the run's log.
			if s.Op == opOpen && inside && !inLog[opOpen+" "+want] && !inLog[opList+" "+want] && !isLost {
				t.Errorf("%s: the kernel opened %s, which the run's log does not record: %v", at, want, post)
			}
			if inside && s.Op == opOpen {
				counts["opened in the checkout"]++
			}
			if !spelledInside {
				// Spelled from outside the checkout: the part outside is
				// resolved by the recorder at access (above).
				continue
			}
			if !slices.Equal(post, r.Access) {
				t.Errorf("%s: classified %v after the run, %v at access", at, post, r.Access)
			}
			if !inside || isLost {
				continue
			}
			// The entry of the object (the last one; links come first)
			// names what the kernel reached.
			if len(post) == 0 || !strings.HasSuffix(post[len(post)-1], " "+want) {
				t.Errorf("%s: the kernel reached %s, the classifier %v", at, want, post)
			}
			op := s.Op
			if info, err := os.Stat(r.Reached); err == nil && info.IsDir() && op == opOpen {
				op = opList
			}
			wantOutside := len(c.check([]entry{{op, want}}, map[string]bool{})) > 0
			var es []entry
			for _, line := range post {
				o, p, _ := strings.Cut(line, " ")
				es = append(es, entry{o, p})
			}
			if gotOutside := len(c.check(es, map[string]bool{})) > 0; gotOutside != wantOutside {
				t.Errorf("%s: outside the closure %v after the run, %v for the object reached", at, gotOutside, wantOutside)
			}
		}
	}
	for _, key := range []string{
		"reads", "lost", "opened in the checkout", "checkout objects read by a spelling from outside",
		"changes rename EROFS", "changes rename EXDEV", "changes rename ", "changes link EXDEV", "changes link EROFS",
		"changes link ", "changes symlink ", "changes retarget EROFS", "changes retarget ", "changes write EROFS",
		"changes write ", "changes symlink EROFS", "changes remove EROFS",
	} {
		if counts[key] == 0 {
			t.Errorf("the sequences have no %q: %v", key, counts)
		}
	}
	t.Logf("seed %d: %d sequences: %v", seed, len(seqs), counts)
}

// readResults reads the sequence runner's results from its output.
func readResults(output string) ([][]stepResult, error) {
	data, err := os.ReadFile(output)
	if err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		if rest, ok := bytes.CutPrefix(sc.Bytes(), []byte(resultsMarker)); ok {
			var results [][]stepResult
			err := json.Unmarshal(rest, &results)
			return results, err
		}
	}
	return nil, fmt.Errorf("%s has no results line", output)
}
