package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"ergo.local/prototype/internal/sketchfront"
	"ergo.local/prototype/internal/stage"
	"ergo.local/prototype/internal/task"
)

// gateWork are the stage counters of work a replayed verdict must not
// start: a parse, a check, a lowering, a compile, a link, a go command or
// a process.
var gateWork = []string{
	"files_parsed", "packages_checked", "modules_lowered", "go_compiles",
	"links", "go_commands", "processes",
}

// taskResult is one `ergo task --json` run: its steps and summary.
type taskResult struct {
	steps   []stepLine
	summary taskSummary
	failed  bool
}

// verdict is the verdict of step at member, "" when the run has none.
func (r taskResult) verdict(step, member string) (stepLine, bool) {
	for _, s := range r.steps {
		if s.Step == step && s.Member == member {
			return s, true
		}
	}
	return stepLine{}, false
}

// runTaskJSON runs `ergo task ARGS... --json` in process and reads its
// lines.
func runTaskJSON(t *testing.T, args ...string) taskResult {
	t.Helper()
	var out bytes.Buffer
	err := taskMain(context.Background(), append(args, "--json"), &out)
	var exit exitStatusError
	res := taskResult{}
	if errors.As(err, &exit) && exit.code == 1 && exit.handled {
		res.failed = true
	} else if err != nil {
		t.Fatalf("ergo task %v: %v", args, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if strings.HasPrefix(line, `{"summary"`) {
			var s map[string]taskSummary
			if err := json.Unmarshal([]byte(line), &s); err != nil {
				t.Fatal(err)
			}
			res.summary = s["summary"]
			continue
		}
		var s stepLine
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		res.steps = append(res.steps, s)
	}
	return res
}

// TestTaskGateReplaysWhatHolds runs ergo task gate over a module of a
// contract package and the hub ergo gen api writes for it (its laws run
// at check time, so nothing is built). A second run on the unchanged
// module starts no work: every work counter stays, and every step with a
// key replays its verdict. A new file the packages do not read misses
// check, lint and test (the module key holds every file) but not fmt
// --check (its key is the .ergo files). A changed contract fails gen
// --check, and a file out of format fails fmt --check, though both passed
// before.
//
// It kills task-fmt-key-omits-bytes, task-fmt-key-parses-graph,
// task-gen-check-not-check and ledger-library-resolves-toolchain.
func TestTaskGateReplaysWhatHolds(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("ERGO_CACHE", "")
	t.Setenv("ERGO_LAWCACHE", "")
	t.Setenv(sketchfront.WarmVariable, "")
	mod := t.TempDir()
	smokeWrite(t, mod, "ergo.mod", "module mod\n")
	smokeWrite(t, mod, "api/api.ergo", "// Package api is the gate test's contract.\n\n// Item is what Read answers.\ntype Item { Count int }\n\n// API reads items.\ntype API { Read(key string) Item }\n")
	t.Chdir(mod)
	if err := genCommand([]string{"api", "api.API"}); err != nil {
		t.Fatal(err)
	}
	if err := formatCommand([]string{"./..."}); err != nil {
		t.Fatal(err)
	}

	before := stage.Snapshot()
	first := runTaskJSON(t, "gate", mod)
	after := stage.Snapshot()
	if first.failed {
		t.Fatalf("first run failed: %+v", first.summary.Failed)
	}
	// No package has a test, a law or a main, so the first run builds
	// nothing: not even the patched compile a build would take
	// (GATE-CORE-COST: the law ledger's write resolved the toolchain for a
	// library, a go build of cmd/compile into a cold cache).
	for _, name := range []string{"go_commands", "processes"} {
		if after[name] != before[name] {
			t.Errorf("first run: %s went from %d to %d, want no build", name, before[name], after[name])
		}
	}
	for _, s := range []string{"check", "fmt --check", "lint", "test", "gen --check"} {
		member := "."
		if v, ok := first.verdict(s, member); !ok || v.Replay != "miss" || v.Verdict != task.Pass {
			t.Errorf("first run: %s at %s is %+v, want a pass that ran", s, member, v)
		}
	}

	before = stage.Snapshot()
	second := runTaskJSON(t, "gate", mod)
	after = stage.Snapshot()
	if second.failed {
		t.Fatalf("second run failed: %+v", second.summary.Failed)
	}
	for _, name := range gateWork {
		if after[name] != before[name] {
			t.Errorf("second run on an unchanged module: %s went from %d to %d", name, before[name], after[name])
		}
	}
	for _, s := range second.steps {
		if s.Step != "gen --check" && s.Replay != "hit" {
			t.Errorf("second run: %s at %s %s (%s), want a replay", s.Step, s.Member, s.Replay, s.Why)
		}
	}

	// A file no package reads: the module key misses, fmt's does not.
	smokeWrite(t, mod, "NOTES.md", "notes\n")
	third := runTaskJSON(t, "gate", mod)
	if third.failed {
		t.Fatalf("third run failed: %+v", third.summary.Failed)
	}
	for step, want := range map[string]string{"check": "miss", "lint": "miss", "test": "miss", "fmt --check": "hit"} {
		if v, _ := third.verdict(step, "."); v.Replay != want {
			t.Errorf("a new NOTES.md: %s %s (%s), want %s", step, v.Replay, v.Why, want)
		}
	}
	if v, _ := third.verdict("check", "."); v.Why != "new input NOTES.md" {
		t.Errorf("a new NOTES.md: check's miss says %q", v.Why)
	}

	// A changed contract: the hub is stale.
	smokeWrite(t, mod, "api/api.ergo", "// Package api is the gate test's contract.\n\n// Item is what Read answers.\ntype Item { Count int, Note string }\n\n// API reads items.\ntype API { Read(key string) Item }\n")
	if err := formatCommand([]string{"./..."}); err != nil {
		t.Fatal(err)
	}
	if v, _ := runTaskJSON(t, "gate", mod).verdict("gen --check", "."); v.Verdict != task.Fail || !strings.Contains(v.First, "apiapi") {
		t.Errorf("a changed contract: gen --check is %+v, want a failure naming apiapi", v)
	}
	if err := genCommand([]string{"api", "api.API"}); err != nil {
		t.Fatal(err)
	}

	// A file out of format.
	path := filepath.Join(mod, "api", "api.ergo")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Replace(string(data), "Item {", "Item  {", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if v, _ := runTaskJSON(t, "gate", mod).verdict("fmt --check", "."); v.Verdict != task.Fail || !strings.Contains(v.First, "api.ergo") {
		t.Errorf("a file out of format: fmt --check is %+v, want a failure naming api.ergo", v)
	}
}

// TestTaskPlanOrder: a task's plan runs the tasks of its after lines
// first, a step naming a task in its place, each task once, and a
// built-in step at each member in the workspace's order. It kills
// task-after-ignored and task-step-task-ignored.
func TestTaskPlanOrder(t *testing.T) {
	root := t.TempDir()
	smokeWrite(t, root, sketchfront.WorkFile, "version 3\ntask lints lint\ntask ci gate\ntask ci check\nafter ci lints\ntask gate test\n")
	work, err := sketchfront.ReadWork(root)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := taskPlan(work, "ci", []int{2, 0, 1})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, p := range plan {
		got = append(got, fmt.Sprintf("%s:%s@%d", p.task, strings.Join(p.step.Words, " "), p.member))
	}
	want := "lints:lint@2 lints:lint@0 lints:lint@1 gate:test@2 gate:test@0 gate:test@1 ci:check@2 ci:check@0 ci:check@1"
	if strings.Join(got, " ") != want {
		t.Errorf("ci's plan is\n%s\nwant\n%s", strings.Join(got, " "), want)
	}
}

// taskEdit is one member's source as the property writes it.
type taskEdit struct {
	c        int    // V()'s own constant
	note     string // a comment in the source
	extra    int    // a count of exported functions besides V (a signature)
	breakLaw bool   // the law pins a value V() no longer has
	layout   bool   // a line out of ergo fmt's layout
	notes    int    // a NOTES.md beside the source, not read by any package
}

// writeTaskWorkspace writes member i of a workspace whose member i's
// package vi has V() = c[i] plus V() of each member it imports (deps[i],
// all below i), and a law that V() is what the generator computed (or,
// with breakLaw, one more). Its go.mod replaces every member it reaches.
func writeTaskWorkspace(t *testing.T, root string, ed []taskEdit, deps [][]int) {
	t.Helper()
	n := len(ed)
	reach := taskReach(deps)
	v := make([]int, n)
	for i := range n {
		v[i] = ed[i].c
		for _, j := range deps[i] {
			v[i] += v[j]
		}
	}
	for i := range n {
		mod := fmt.Sprintf("ws.test/w%d", i)
		smokeWrite(t, root, fmt.Sprintf("w%d/ergo.mod", i), "module "+mod+"\n")
		gomod := "module " + mod + "\n\ngo 1.27\n"
		for j := range n {
			if reach[i][j] && j != i {
				gomod += fmt.Sprintf("\nrequire ws.test/w%d v0.0.0\n\nreplace ws.test/w%d => ../w%d\n", j, j, j)
			}
		}
		smokeWrite(t, root, fmt.Sprintf("w%d/go.mod", i), gomod)
		var src strings.Builder
		for _, j := range deps[i] {
			fmt.Fprintf(&src, "import ws.test/w%d/v%d\n", j, j)
		}
		if len(deps[i]) > 0 {
			src.WriteString("\n")
		}
		if ed[i].note != "" {
			fmt.Fprintf(&src, "// %s\n\n", ed[i].note)
		}
		fmt.Fprintf(&src, "// V is this member's value.\nfunc V() int { %d", ed[i].c)
		for _, j := range deps[i] {
			fmt.Fprintf(&src, " + v%d.V()", j)
		}
		want := v[i]
		if ed[i].breakLaw {
			want++
		}
		fmt.Fprintf(&src, " }\n\n// VHolds holds while V is what the generator computed.\nlaw VHolds(n int) { V() + n == %d + n }\n", want)
		for k := range ed[i].extra {
			fmt.Fprintf(&src, "\n// X%d is one more export.\nfunc X%d() int { %d }\n", k, k, k)
		}
		file := fmt.Sprintf("w%d/v%d/v%d.ergo", i, i, i)
		smokeWrite(t, root, file, src.String())
		if err := formatCommand([]string{fmt.Sprintf("%s/w%d/...", root, i)}); err != nil {
			t.Fatal(err)
		}
		if ed[i].layout {
			p := filepath.Join(root, file)
			data, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			smokeWrite(t, root, file, strings.Replace(string(data), "func V() int {", "func V()  int {", 1))
		}
		if ed[i].notes > 0 {
			smokeWrite(t, root, fmt.Sprintf("w%d/NOTES.md", i), fmt.Sprintf("notes %d\n", ed[i].notes))
		}
	}
}

// taskReach is, per member, the members it reaches, itself included.
func taskReach(deps [][]int) []map[int]bool {
	reach := make([]map[int]bool, len(deps))
	for i := range deps {
		reach[i] = map[int]bool{i: true}
		for _, j := range deps[i] {
			for k := range reach[j] {
				reach[i][k] = true
			}
		}
	}
	return reach
}

// treeFiles are the files below root by content, .git left out.
func treeFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// gitIn runs git in dir.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// TestTaskGateAgreesWithRunningEverything is TASK-GATE's model-based
// property: over seeded workspaces of 3 to 6 members with random edges
// and seeded edit scripts (a layout break, a comment, a body, a
// signature, a law broken in a reached member, a file no package reads),
// after each edit:
//
//   - ergo task gate --json gives each step the verdict of the reference
//     "run every step on every member with no store" (ERGO_LAWCACHE=off);
//   - the steps that run are exactly those whose inputs moved since they
//     last passed (a file of a member they reach for check, lint and
//     test; one of the member's .ergo files for fmt --check), and those
//     that did not pass;
//   - ergo task gate --affected BASE (BASE the commit of the tree as the
//     last run left it) selects exactly the steps whose inputs hold a
//     changed path: a superset of the steps whose key moved.
//
// It kills gate-module-key-omits-tree, task-module-key-reach-dropped, task-affected-by-member,
// task-why-dropped and the runner's verdict-failure-kept over real steps.
func TestTaskGateAgreesWithRunningEverything(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("ERGO_CACHE", "")
	t.Setenv(sketchfront.WarmVariable, "")
	for seed := uint64(1); seed <= 2; seed++ {
		r := rand.New(rand.NewPCG(seed, 23))
		n := 3 + r.IntN(4)
		deps := make([][]int, n)
		for i := 1; i < n; i++ {
			for j := range i {
				if r.IntN(3) == 0 || j == i-1 && len(deps[i]) == 0 && r.IntN(2) == 0 {
					deps[i] = append(deps[i], j)
				}
			}
		}
		reach := taskReach(deps)
		ed := make([]taskEdit, n)
		for i := range ed {
			ed[i].c = 1 + r.IntN(5)
		}
		root := t.TempDir()
		writeTaskWorkspace(t, root, ed, deps)
		gitIn(t, root, "init", "-q")
		// ok[step@member]: the step passed and none of its inputs moved
		// since, so the next run replays it.
		ok := map[string]bool{}
		// Two warm-up runs: the first writes each member's law ledger (test
		// writes it after check and lint read it), the second keeps every
		// verdict over the ledger as it then stays.
		t.Setenv("ERGO_LAWCACHE", "")
		runTaskJSON(t, "gate", root)
		for _, s := range runTaskJSON(t, "gate", root).steps {
			ok[s.Step+"@"+s.Member] = s.Verdict == task.Pass
		}
		snap := treeFiles(t, root)
		gitIn(t, root, "add", "-A")
		gitIn(t, root, "commit", "-q", "-m", "base")
		var script []string
		kinds := []string{"layout", "comment", "body", "signature", "law", "notes"}
		r.Shuffle(len(kinds), func(i, j int) { kinds[i], kinds[j] = kinds[j], kinds[i] })
		for step, kind := range append([]string{"none"}, kinds...) {
			k := r.IntN(n)
			switch kind {
			case "layout":
				ed[k].layout = !ed[k].layout
			case "comment":
				ed[k].note = fmt.Sprintf("note %d", step)
			case "body":
				ed[k].c += 1 + r.IntN(3)
			case "signature":
				ed[k].extra++
			case "law":
				ed[k].breakLaw = !ed[k].breakLaw
			case "notes":
				ed[k].notes = step
			}
			script = append(script, fmt.Sprintf("%s w%d", kind, k))
			writeTaskWorkspace(t, root, ed, deps)
			now := treeFiles(t, root)
			var changed []string
			for p, c := range now {
				if snap[p] != c {
					changed = append(changed, p)
				}
			}
			for p := range snap {
				if _, ok := now[p]; !ok {
					changed = append(changed, p)
				}
			}
			slices.Sort(changed)
			moved := func(stepName string, m int) bool {
				for _, p := range changed {
					for j := range n {
						dir := fmt.Sprintf("w%d/", j)
						if !strings.HasPrefix(p, dir) {
							continue
						}
						if stepName == "fmt --check" {
							if j == m && strings.HasSuffix(p, ".ergo") {
								return true
							}
						} else if reach[m][j] {
							return true
						}
					}
				}
				return false
			}
			where := fmt.Sprintf("seed %d, deps %v, edits %v, changed %v", seed, deps, script, changed)
			affected := step%2 == 1 || kind == "notes"
			args := []string{"gate", root}
			if affected {
				args = append(args, "--affected", "HEAD")
			}
			t.Setenv("ERGO_LAWCACHE", "")
			got := runTaskJSON(t, args...)
			t.Setenv("ERGO_LAWCACHE", "off")
			ref := runTaskJSON(t, "gate", root)
			for _, s := range got.steps {
				if s.Step == "gen --check" {
					continue
				}
				m := -1
				fmt.Sscanf(s.Member, "w%d", &m)
				id := s.Step + "@" + s.Member
				mv := moved(s.Step, m)
				if mv {
					ok[id] = false
				}
				if affected {
					if (s.Verdict != task.Unaffected) != mv {
						t.Fatalf("%s: --affected: %s is %s, its inputs moved %v", where, id, s.Verdict, mv)
					}
					if !mv {
						continue
					}
				}
				want, _ := ref.verdict(s.Step, s.Member)
				if s.Verdict != want.Verdict {
					t.Fatalf("%s: %s is %s, the reference %s (%s)", where, id, s.Verdict, want.Verdict, want.First)
				}
				if ran := s.Replay == "miss"; ran == ok[id] {
					t.Fatalf("%s: %s ran %v (%s), but it replays exactly when it passed with these inputs (%v)", where, id, ran, s.Why, ok[id])
				}
				if s.Replay == "miss" && s.Why == "" {
					t.Fatalf("%s: %s ran with no reason", where, id)
				}
				ok[id] = s.Verdict == task.Pass
			}
			snap = treeFiles(t, root)
			gitIn(t, root, "add", "-A")
			gitIn(t, root, "commit", "-q", "--allow-empty", "-m", "edit")
		}
	}
}
