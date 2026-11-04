package closure

import (
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
// the path after the run then saw the new object, not the one read. Under
// the read-only checkout, each change fails, so the sweep reports the
// package, and each sequence that only reads resolves after the run as it
// did when it was read.

// fixture is a module checkout for the sandbox tests: root holds go.mod,
// docs and the package internal/p, whose testdata has links of each kind.
// outside is a directory next to the checkout, which the sandbox leaves
// writable, with a link back into it.
type fixture struct {
	root, pkg, outside string // physical paths
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
	f := fixture{root: filepath.Join(base, "m"), outside: filepath.Join(base, "s")}
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
	env := append(sweepEnvironment(), "PWD="+dir)
	return append(env, extra...)
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
		})
	}
}

// TestSandboxRefusesWhenItCannotProtect: when the read-only mount cannot be
// made, the run is refused and the test binary never runs; with the mount,
// the same binary runs.
func TestSandboxRefusesWhenItCannotProtect(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	scratch := t.TempDir()
	marker := filepath.Join(scratch, "ran")
	op, err := json.Marshal(childOp{Op: "write", Path: marker})
	if err != nil {
		t.Fatal(err)
	}
	env := testEnv(f.pkg, childVariable+"="+string(op))

	err = runProtected(filepath.Join(f.root, "missing"), "internal/p", f.pkg, env, filepath.Join(scratch, "refused.out"), self)
	var r refusal
	if !errors.As(err, &r) {
		t.Fatalf("a run whose checkout cannot be made read-only was not refused: %v", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the test binary ran although the checkout could not be made read-only")
	}
	t.Logf("refused: %v", r)

	if err := runProtected(f.root, "internal/p", f.pkg, env, filepath.Join(scratch, "ran.out"), self); err != nil {
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
}

type childResult struct {
	Reached string // the kernel's location of the object an open reached
	Err     string
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

// do runs one operation in this process.
func do(op childOp) childResult {
	var res childResult
	var err error
	switch op.Op {
	case "open", "stat":
		res.Reached, err = reachedBy(op.Path, op.Op == "stat")
	case "retarget":
		// Replace the link at Path by one to Target, atomically.
		next := filepath.Join(filepath.Dir(op.Path), ".next")
		if err = os.Symlink(op.Target, next); err == nil {
			err = os.Rename(next, op.Path)
		}
	case "rename":
		err = os.Rename(op.Path, op.Target)
	case "remove":
		err = os.Remove(op.Path)
	case "write":
		err = os.WriteFile(op.Path, []byte("written"), 0o644)
	case "readonly":
		err = readOnly(op.Path)
	default:
		err = fmt.Errorf("unknown operation %q", op.Op)
	}
	if err != nil {
		res.Err = err.Error()
		res.EROFS = errors.Is(err, syscall.EROFS)
	}
	return res
}

// A step of a state sequence: a read (open or stat, here or in a child)
// or a change (here or in a child) of the checkout or, when Outside, of
// the directory next to it, which the sandbox leaves writable.
type step struct {
	Op      string // open, stat, retarget, rename, remove, write
	Child   bool
	Outside bool
	Path    string // relative to the package directory, or absolute
	Target  string
}

func (s step) reads() bool { return s.Op == "open" || s.Op == "stat" }

// stepResult is what the runner saw at the step: the operation's result,
// and for a read, the entries the classifier gave the path at that moment.
type stepResult struct {
	childResult
	Access []string
}

// The variables of the sequence runner: the file of sequences, and the
// file it writes the results to. Both lie outside the checkout.
const (
	sequencesVariable = "ERGO_CLOSURE_SEQUENCES"
	resultsVariable   = "ERGO_CLOSURE_RESULTS"
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
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	results := make([][]stepResult, len(seqs))
	for i, seq := range seqs {
		for _, s := range seq {
			op := childOp{Op: s.Op, Path: s.Path, Target: s.Target}
			var r stepResult
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
				r.Access = classify(t, s, pwd, root)
			}
			results[i] = append(results[i], r)
		}
	}
	out, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(resultsVariable), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// classify returns the entries the closure check gives the read s, as
// "op path" lines.
func classify(t *testing.T, s step, pwd, root string) []string {
	t.Helper()
	entries, err := parseLog([]byte("# test log\n"+s.Op+" "+s.Path+"\n"), pwd, []string{root})
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
// through each kind of link (absolute, relative, chained, climbing, local),
// lookups, and children's reads, mixed with changes of the links and the
// files they reach, here and in children. The first ones are the shapes
// the round-2 verifier named: read, then retarget, rename or remove, then
// read again; stat, retarget, open; and the same through a child.
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
	links := []string{"out", "rel", "chain", "local", "abslocal", "dot", "away", "ext"}
	targets := []string{filepath.Join(f.root, "docs"), "in", "../../../docs", "out", "sub", "/nonexistent/loop-probe-x", filepath.Join(f.pkg, "testdata", "in"), f.outside}
	back := filepath.Join(f.outside, "back")

	seqs := [][]step{
		// Out of the checkout and back in through a link the sandbox does
		// not protect, which the test then changes; and the same read
		// spelled from outside, which the recorder resolves at access.
		{{Op: "open", Path: "testdata/ext/back/tooling.md"}, {Op: "open", Path: back + "/sub/x.md"}, {Op: "stat", Path: "testdata/ext/plain.txt", Child: true}},
		{{Op: "open", Path: back + "/tooling.md", Child: true}, {Op: "retarget", Path: back, Target: filepath.Join(f.pkg, "testdata", "in"), Outside: true}, {Op: "open", Path: "testdata/ext/back/a.txt"}},
		{{Op: "open", Path: "testdata/out/tooling.md"}, {Op: "retarget", Path: "testdata/out", Target: "in"}, {Op: "open", Path: "testdata/out/tooling.md"}},
		{{Op: "stat", Path: "testdata/chain/tooling.md"}, {Op: "retarget", Path: "testdata/out", Target: "sub"}, {Op: "open", Path: "testdata/chain/tooling.md"}},
		{{Op: "open", Path: "testdata/away/tooling.md", Child: true}, {Op: "rename", Path: "testdata/away", Target: "testdata/moved"}, {Op: "open", Path: "testdata/moved/tooling.md"}},
		{{Op: "open", Path: "testdata/rel/tooling.md"}, {Op: "remove", Path: "testdata/rel"}, {Op: "stat", Path: "testdata/rel/tooling.md"}},
		{{Op: "stat", Path: filepath.Join(f.pkg, "testdata/out/tooling.md")}, {Op: "retarget", Path: "testdata/out", Target: "in", Child: true}, {Op: "open", Path: "testdata/out/tooling.md", Child: true}},
		{{Op: "open", Path: "testdata/in/new.txt"}, {Op: "write", Path: "testdata/in/new.txt"}, {Op: "open", Path: "testdata/in/new.txt"}},
	}
	rng := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	for len(seqs) < n {
		var seq []step
		for range 2 + rng.IntN(5) {
			s := step{Child: rng.IntN(4) == 0}
			switch k := rng.IntN(10); {
			case k < 4:
				s.Op, s.Path = "open", paths[rng.IntN(len(paths))]
			case k < 7:
				s.Op, s.Path = "stat", paths[rng.IntN(len(paths))]
			case k == 7:
				s.Op, s.Path, s.Target = "retarget", "testdata/"+links[rng.IntN(len(links))], targets[rng.IntN(len(targets))]
			case k == 8:
				s.Op, s.Path, s.Target = "rename", "testdata/"+links[rng.IntN(len(links))], "testdata/moved"
			default:
				if rng.IntN(2) == 0 {
					s.Op, s.Path, s.Child = "remove", "testdata/"+links[rng.IntN(len(links))], false
				} else {
					s.Op, s.Path, s.Child = "write", "testdata/in/new.txt", false
				}
			}
			seq = append(seq, s)
		}
		seqs = append(seqs, seq)
	}
	return seqs
}

// TestReadOnlyStateSequences is the property over state sequences, run as
// the sweep runs a package (runTests: read-only checkout, observed, test
// log):
//
//   - every change of the checkout fails with EROFS, here and in a child,
//     and the tree is as it was; a change outside the checkout succeeds;
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
	scratch := t.TempDir()
	const seed, n = 2026_11_04, 160
	seqs := sequences(f, n, seed)
	data, err := json.Marshal(seqs)
	if err != nil {
		t.Fatal(err)
	}
	seqFile, resFile := filepath.Join(scratch, "sequences.json"), filepath.Join(scratch, "results.json")
	if err := os.WriteFile(seqFile, data, 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshot(t, f.root)
	logged, err := runTests(f.root, []string{f.root}, "internal/p", f.pkg, filepath.Join(scratch, "internal_p"), []string{sequencesVariable + "=" + seqFile, resultsVariable + "=" + resFile}, self, "-test.run=^TestRunSequences$", "-test.v")
	if err != nil {
		t.Fatalf("the sequence runner failed: %v", err)
	}
	inLog := map[string]bool{}
	for _, e := range logged {
		inLog[e.op+" "+e.path] = true
	}
	data, err = os.ReadFile(resFile)
	if err != nil {
		t.Fatal(err)
	}
	var results [][]stepResult
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != len(seqs) {
		t.Fatalf("%d results for %d sequences", len(results), len(seqs))
	}
	if after := snapshot(t, f.root); !maps.Equal(before, after) {
		t.Errorf("the run changed the checkout:\nbefore %v\nafter  %v", before, after)
	}

	c := newClosure("internal/p", nil, nil)
	var changes, outsideChanges, reads, lost, opened int
	for i, seq := range seqs {
		for j, s := range seq {
			r := results[i][j]
			at := fmt.Sprintf("seed %d sequence %d step %d %+v", seed, i, j, s)
			switch {
			case !s.reads() && s.Outside:
				outsideChanges++
				if r.Err != "" {
					t.Errorf("%s: a change outside the checkout failed: %s", at, r.Err)
				}
				continue
			case !s.reads():
				changes++
				if !r.EROFS {
					t.Errorf("%s: the change was not refused with EROFS: %q", at, r.Err)
				}
				continue
			}
			reads++
			post := classify(t, s, f.pkg, f.root)
			isLost := slices.ContainsFunc(post, func(line string) bool { return strings.HasPrefix(line, opLost) })
			if isLost {
				lost++
			}
			want, inside := "", false
			if r.Reached != "" {
				want, inside = relative(r.Reached, []string{f.root})
			}
			// A file the kernel opened in the checkout is in the run's log.
			if s.Op == opOpen && inside && !inLog[opOpen+" "+want] && !inLog[opList+" "+want] && !isLost {
				t.Errorf("%s: the kernel opened %s, which the run's log does not record: %v", at, want, post)
			}
			if inside && s.Op == opOpen {
				opened++
			}
			if p := s.Path; filepath.IsAbs(p) && !strings.HasPrefix(p, f.root+"/") {
				// Spelled from outside the checkout: the part outside may
				// change; the recorder resolved it at access (above).
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
	if changes == 0 || outsideChanges == 0 || reads == 0 || lost == 0 || opened == 0 {
		t.Fatalf("the sequences have %d changes, %d outside, %d reads, %d lost, %d opened in the checkout", changes, outsideChanges, reads, lost, opened)
	}
	t.Logf("seed %d: %d sequences, %d reads (%d opened in the checkout, %d lost), %d refused changes, %d changes outside", seed, len(seqs), reads, opened, lost, changes, outsideChanges)
}
