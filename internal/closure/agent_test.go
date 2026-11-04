//go:build linux

package closure

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// The property over changes a test asks an agent outside its process tree
// to make: a service of the host the tree reaches by a socket, such as the
// systemd user manager. The sandbox stops the tree's own writes, not the
// agent's. Identity mode (testinputs) records what each access reached at
// access time, and the sweep fails closed when a location an access looked
// up changed, during the run or after it.

// The variables of TestAgentTarget: the agent's socket and the file of
// steps.
const (
	agentVariable      = "ERGO_CLOSURE_AGENT"
	agentStepsVariable = "ERGO_CLOSURE_AGENT_STEPS"
)

// An agentStep is a read by the test (open or stat, of a path relative to
// the package directory or absolute), or a request to the agent: a change
// (retarget, rename, remove: childOp), "flap", which starts changing the
// link at Path between Target and Other until "unflap", or "unflap".
type agentStep struct {
	Op                  string
	Path, Target, Other string
}

func (s agentStep) reads() bool { return s.Op == "open" || s.Op == "stat" }

// agentRead is what the test saw at a read: the object the kernel reached
// (from the opened descriptor) and the links the path passed then.
type agentRead struct {
	childResult
	Links []string
}

// TestAgentTarget runs the steps of TestOutsideAgentChangesFailClosed in the
// package directory of a fixture, in the sandbox. It runs only as that
// test's target.
func TestAgentTarget(t *testing.T) {
	sock := os.Getenv(agentVariable)
	if sock == "" {
		t.Skip("the target of TestOutsideAgentChangesFailClosed")
	}
	data, err := os.ReadFile(os.Getenv(agentStepsVariable))
	if err != nil {
		t.Fatal(err)
	}
	var steps []agentStep
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatal(err)
	}
	c, err := net.DialTimeout("unix", sock, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// The agent stops answering when a step fails: the deadline turns that
	// into a failure that names the step, not a hang until the test timeout.
	if err := c.SetDeadline(time.Now().Add(60 * time.Second)); err != nil {
		t.Fatal(err)
	}
	in := bufio.NewReader(c)
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	var results []agentRead
	for _, s := range steps {
		if !s.reads() {
			req, _ := json.Marshal(s)
			if _, err := c.Write(append(req, '\n')); err != nil {
				t.Fatal(err)
			}
			line, err := in.ReadString('\n')
			if err != nil || strings.TrimSpace(line) != "ok" {
				t.Fatalf("the agent's answer to %+v: %q %v", s, line, err)
			}
			results = append(results, agentRead{})
			continue
		}
		var r agentRead
		r.childResult = do(childOp{Op: s.Op, Path: s.Path})
		abs := s.Path
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(pwd, abs)
		}
		r.Links = newResolver().walk(abs).links
		results = append(results, r)
	}
	out, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout.Write(append(append([]byte("\n"+resultsMarker), out...), '\n'))
}

// startAgent serves one connection on a socket outside the sandbox: each
// request is a step, made before the answer "ok".
func startAgent(t *testing.T) (sock string, stop func() error) {
	t.Helper()
	sock = filepath.Join(t.TempDir(), "a")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	l.SetDeadline(time.Now().Add(60 * time.Second))
	done := make(chan error, 1)
	go func() {
		c, err := l.AcceptUnix()
		if err != nil {
			done <- err
			return
		}
		defer c.Close()
		var flapping sync.WaitGroup
		var quit chan struct{}
		sc := bufio.NewScanner(c)
		for sc.Scan() {
			var s agentStep
			if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
				done <- err
				return
			}
			switch s.Op {
			case "flap":
				quit = make(chan struct{})
				flapping.Add(1)
				go func(q chan struct{}) {
					defer flapping.Done()
					for i := 0; ; i++ {
						select {
						case <-q:
							return
						default:
						}
						target := s.Target
						if i%2 == 1 {
							target = s.Other
						}
						do(childOp{Op: "retarget", Path: s.Path, Target: target})
					}
				}(quit)
			case "unflap":
				close(quit)
				flapping.Wait()
			default:
				if r := do(childOp{Op: s.Op, Path: s.Path, Target: s.Target}); r.Err != "" {
					done <- fmt.Errorf("the agent's %+v: %s", s, r.Err)
					return
				}
			}
			if _, err := c.Write([]byte("ok\n")); err != nil {
				done <- err
				return
			}
		}
		done <- sc.Err()
	}()
	return sock, func() error {
		l.Close()
		return <-done
	}
}

// agentSteps generates the steps of one run over the fixture f. The first
// shapes are the verifier's probe, a change undone before the test exits,
// a rename of a directory the test read in, and a flapping link; the rest
// are random.
func agentSteps(f fixture, run int, rng *rand.Rand) []agentStep {
	links := []string{"away", "out", "rel", "local", "abslocal", "chain"}
	targets := []string{"in", f.root + "/docs", f.pkg + "/testdata/in", "../../../docs"}
	spellings := []string{
		"testdata/away/tooling.md", "testdata/out/tooling.md", "testdata/rel/tooling.md",
		"testdata/local/a.txt", "testdata/abslocal/a.txt", "testdata/chain/sub/x.md",
		"testdata/ext/back/tooling.md", "testdata/in/a.txt", "testdata/in/sub/b.txt",
		"testdata/local/sub/b.txt", f.pkg + "/testdata/away/tooling.md", f.pkg + "/testdata/local/a.txt",
		"testdata/away", "testdata/local",
	}
	link := func(name string) string { return filepath.Join(f.pkg, "testdata", name) }
	read := func(p string) agentStep {
		op := "open"
		if rng.IntN(3) == 0 {
			op = "stat"
		}
		return agentStep{Op: op, Path: p}
	}
	switch run {
	case 0: // the probe
		return []agentStep{{Op: "open", Path: "testdata/away/tooling.md"}, {Op: "retarget", Path: link("away"), Target: "in"}}
	case 1: // undone before the test exits
		return []agentStep{
			{Op: "open", Path: "testdata/local/a.txt"}, {Op: "open", Path: "testdata/in/sub/b.txt"},
			{Op: "retarget", Path: link("local"), Target: f.root + "/docs"},
			{Op: "retarget", Path: link("local"), Target: "in"},
		}
	case 2: // a directory renamed away and back
		return []agentStep{
			{Op: "open", Path: "testdata/out/tooling.md"}, {Op: "open", Path: "testdata/in/a.txt"},
			{Op: "rename", Path: f.root + "/docs", Target: f.root + "/docs-moved"},
			{Op: "rename", Path: f.root + "/docs-moved", Target: f.root + "/docs"},
		}
	case 3: // outside the checkout: the directory next to it
		return []agentStep{
			{Op: "open", Path: "testdata/ext/back/tooling.md"},
			{Op: "retarget", Path: filepath.Join(f.outside, "back"), Target: f.pkg + "/testdata/in"},
		}
	}
	var steps []agentStep
	if run%3 == 0 {
		// Concurrent edits: reads while the agent flips a link.
		name := links[rng.IntN(len(links))]
		steps = append(steps, read("testdata/in/a.txt"), agentStep{Op: "flap", Path: link(name), Target: "in", Other: f.root + "/docs"})
		for range 20 {
			steps = append(steps, read("testdata/"+name+"/a.txt"), read("testdata/"+name+"/tooling.md"))
		}
		return append(steps, agentStep{Op: "unflap"}, read("testdata/in/a.txt"))
	}
	for range 4 + rng.IntN(5) {
		if rng.IntN(4) == 0 {
			name := links[rng.IntN(len(links))]
			switch rng.IntN(3) {
			case 0:
				steps = append(steps, agentStep{Op: "retarget", Path: link(name), Target: targets[rng.IntN(len(targets))]})
			case 1:
				steps = append(steps, agentStep{Op: "rename", Path: link(name), Target: link(name + "-gone")})
				links = slices.DeleteFunc(links, func(l string) bool { return l == name })
			default:
				steps = append(steps, agentStep{Op: "remove", Path: link(name)})
				links = slices.DeleteFunc(links, func(l string) bool { return l == name })
			}
			if len(links) == 0 {
				break
			}
			continue
		}
		steps = append(steps, read(spellings[rng.IntN(len(spellings))]))
	}
	return steps
}

// TestOutsideAgentChangesFailClosed is the property: over runs of reads and
// changes an outside agent makes at the test's request (acknowledged before
// the test goes on) or concurrently (a flapping link), as the sweep runs a
// package:
//
//   - (a) each object in the checkout a read reached (the kernel's, from the
//     opened descriptor) is named by an entry of its path, unless the path
//     fails closed with no declared entry able to cover it (changed during
//     the run, lost, unidentified);
//   - (b) a path whose entries are accepted as they are (none fails closed)
//     names after the run what every read of it reached;
//   - (c) a read whose path passed a location the agent changed later (a
//     link it passed, or a directory above what it reached) fails closed,
//     even if the agent undid the change.
func TestOutsideAgentChangesFailClosed(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const seed, runs = 2026_11_04, 16
	counts := map[string]int{}
	for run := range runs {
		f := newFixture(t)
		rng := rand.New(rand.NewPCG(seed, uint64(run)))
		steps := agentSteps(f, run, rng)
		data, err := json.Marshal(steps)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		stepsFile := filepath.Join(dir, "steps.json")
		if err := os.WriteFile(stepsFile, data, 0o644); err != nil {
			t.Fatal(err)
		}
		sock, stop := startAgent(t)
		stem := filepath.Join(dir, "internal_p")
		o, runErr := runObserved(f.root, "internal/p", f.pkg, stem, []string{agentVariable + "=" + sock, agentStepsVariable + "=" + stepsFile}, nil, self, "-test.run=^TestAgentTarget$", "-test.v")
		if err := stop(); err != nil {
			t.Fatalf("run %d: the agent: %v", run, err)
		}
		if runErr != nil {
			t.Fatalf("run %d: %v", run, runErr)
		}
		var results []agentRead
		if err := readResultsInto(stem+".out", &results); err != nil {
			t.Fatal(err)
		}
		if len(results) != len(steps) {
			t.Fatalf("run %d: %d results for %d steps", run, len(results), len(steps))
		}
		// The entries of the run's changes no access looked up, which every
		// one-line classification repeats.
		global, err := readLog([]byte("# test log\n"), f.pkg, []string{f.root}, os.TempDir(), o.id)
		if err != nil {
			t.Fatal(err)
		}
		lineEntries := func(op, p string) []entry {
			es, err := readLog([]byte("# test log\n"+op+" "+p+"\n"), f.pkg, []string{f.root}, os.TempDir(), o.id)
			if err != nil {
				t.Fatal(err)
			}
			return slices.DeleteFunc(es, func(e entry) bool { return slices.Contains(global, e) })
		}
		// The location each change of the agent touched, by the last step
		// before which the change may happen: its own, or for a flapping
		// link, the step that stops it.
		changedAt := map[int]string{}
		for j, s := range steps {
			switch {
			case s.Op == "flap":
				end := j + slices.IndexFunc(steps[j:], func(s agentStep) bool { return s.Op == "unflap" })
				changedAt[end] = s.Path
			case !s.reads() && s.Op != "unflap":
				changedAt[j] = s.Path
			}
		}
		for i, s := range steps {
			if !s.reads() {
				continue
			}
			r := results[i]
			at := fmt.Sprintf("seed %d run %d step %d %+v", seed, run, i, s)
			counts["reads"]++
			es := lineEntries(s.Op, s.Path)
			closed := slices.ContainsFunc(es, func(e entry) bool { return failsClosed(e.op) })
			uncoverable := slices.ContainsFunc(es, func(e entry) bool {
				return strings.HasPrefix(e.op, opRaced) || strings.HasPrefix(e.op, opLost) || strings.HasPrefix(e.op, opUnidentified)
			})
			want, inside := "", false
			if r.Reached != "" {
				want, inside = relative(r.Reached, []string{f.root})
			}
			// (a)
			if inside && !uncoverable && !slices.ContainsFunc(es, func(e entry) bool { return e.path == want }) {
				t.Errorf("%s: the kernel reached %s, and no entry names it: %v", at, want, es)
			}
			// (c)
			for j, p := range changedAt {
				if j < i {
					continue
				}
				passed := slices.Contains(r.Links, p) || r.Reached != "" && (r.Reached == p || strings.HasPrefix(r.Reached, p+"/"))
				if passed && !closed {
					t.Errorf("%s: the agent changed %s after the read (step %d), and the path is accepted: %v", at, p, j, es)
				}
				if passed {
					counts["reads whose location the agent changed later"]++
				}
			}
			if closed {
				counts["paths that fail closed"]++
				if slices.ContainsFunc(es, func(e entry) bool { return strings.HasPrefix(e.op, opRaced) }) {
					counts["paths changed during the run"]++
				}
				continue
			}
			// (b)
			post := classify(t, s.Op, s.Path, f.pkg, f.root)
			if inside {
				counts["accepted reads in the checkout"]++
				if len(post) == 0 || !strings.HasSuffix(post[len(post)-1], " "+want) {
					t.Errorf("%s: accepted %v, but after the run the path names %v, not %s, which the read reached", at, es, post, want)
				}
			}
		}
		for _, s := range steps {
			if s.Op == "flap" {
				counts["flapping runs"]++
			}
		}
	}
	for _, key := range []string{"reads", "reads whose location the agent changed later", "paths that fail closed", "paths changed during the run", "accepted reads in the checkout", "flapping runs"} {
		if counts[key] == 0 {
			t.Errorf("the runs have no %q: %v", key, counts)
		}
	}
	t.Logf("seed %d: %d runs: %v", seed, runs, counts)
}

// readResultsInto reads the line after resultsMarker in a run's output.
func readResultsInto(output string, into any) error {
	data, err := os.ReadFile(output)
	if err != nil {
		return err
	}
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), resultsMarker); ok {
			return json.Unmarshal([]byte(rest), into)
		}
	}
	return fmt.Errorf("%s has no results line", output)
}

// TestChangeAfterTheRunFailsClosed: a link the test read through is changed
// after the test exits and before the sweep classifies the run (A2): the
// read is named by its access-time object and fails closed. Without the
// comparison, the access-time object would pass as it is.
func TestChangeAfterTheRunFailsClosed(t *testing.T) {
	needSandbox(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newFixture(t)
	dir := t.TempDir()
	stepsFile := filepath.Join(dir, "steps.json")
	if err := os.WriteFile(stepsFile, []byte(`[{"Op":"open","Path":"testdata/local/a.txt"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	sock, stop := startAgent(t)
	o, err := runObserved(f.root, "internal/p", f.pkg, filepath.Join(dir, "internal_p"), []string{agentVariable + "=" + sock, agentStepsVariable + "=" + stepsFile}, nil, self, "-test.run=^TestAgentTarget$")
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if r := do(childOp{Op: "retarget", Path: filepath.Join(f.pkg, "testdata/local"), Target: f.root + "/docs"}); r.Err != "" {
		t.Fatal(r.Err)
	}
	es, err := o.entries([]string{f.root})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []entry{{opChanged + opOpen, "internal/p/testdata/in/a.txt"}, {opChanged + opLink, "internal/p/testdata/local"}} {
		if !slices.Contains(es, want) {
			t.Errorf("the link changed after the run, and the sweep has no %v: %v", want, es)
		}
	}
	// A declared entry that names the access-time object covers it; the
	// object after the run is not what was read.
	c := newClosure("internal/p", nil, nil)
	c.declared = []allowance{{"internal/p/testdata/in/a.txt", file, "the probe"}, {"internal/p/testdata/local", file, "the probe"}}
	if out := c.check(es, map[string]bool{}); slices.Contains(out, entry{opChanged + opOpen, "internal/p/testdata/in/a.txt"}) {
		t.Errorf("an exact declared entry does not cover the access-time object: %v", out)
	}
}
