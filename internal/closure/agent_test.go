//go:build linux

package closure

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
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
	// The dial comes before the first step, so a run that ends with its
	// results connected to the agent (startAgent's stop relies on it). The
	// agent closes the connection when a step fails, so a wait for its
	// answer ends with the failure, and the run deadline bounds the rest.
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
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
//
// stop ends the agent after the run and returns the number of requests it
// answered. It waits on events, not on the clock. TestAgentTarget dials the
// agent before its first step, so a run that wrote its results (connected
// is true) left one connection: one the agent served already, or one still
// in the listener's backlog that the agent accepts and serves next. Either
// way the run's exit closed it, so the agent reads its end. Closing the
// listener then would refuse a connection the scheduler had not yet let the
// agent accept (a run with no request, under load). A run that did not
// write its results may not have dialed: stop closes the listener, and an
// agent that never accepted answered nothing.
//
// lateAccept makes the agent accept only once stop starts: the schedule in
// which the run ends before the agent runs at all.
func startAgent(t *testing.T, lateAccept bool) (sock string, stop func(connected bool) (int, error)) {
	t.Helper()
	sock = filepath.Join(t.TempDir(), "a")
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() }) // a test that fails before stop
	stopping := make(chan struct{})
	type result struct {
		served   bool
		answered int
		err      error
	}
	done := make(chan result, 1)
	go func() {
		if lateAccept {
			<-stopping
		}
		c, err := l.AcceptUnix()
		l.Close() // the agent serves one connection
		switch {
		case errors.Is(err, net.ErrClosed):
			done <- result{} // stop closed it: no connection came
		case err != nil:
			done <- result{err: err}
		default:
			answered, err := serveAgent(c)
			c.Close()
			done <- result{true, answered, err}
		}
	}()
	return sock, func(connected bool) (int, error) {
		close(stopping)
		if !connected {
			l.Close()
		}
		r := <-done
		if connected && !r.served && r.err == nil {
			r.err = errors.New("the target dialed the agent, and the agent served no connection")
		}
		return r.answered, r.err
	}
}

// serveAgent makes the steps a connection requests, each before its answer
// "ok", until the connection ends, and returns the number it answered. A
// step that fails ends the connection without an answer.
func serveAgent(c *net.UnixConn) (answered int, err error) {
	var flapping sync.WaitGroup
	var quit chan struct{}
	// A connection that ends while the link flaps (a run stopped at its
	// deadline) stops the flapping too.
	defer func() {
		if quit != nil {
			close(quit)
			flapping.Wait()
		}
	}()
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		var s agentStep
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			return answered, err
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
			quit = nil
		default:
			if r := do(childOp{Op: s.Op, Path: s.Path, Target: s.Target}); r.Err != "" {
				return answered, fmt.Errorf("the agent's %+v: %s", s, r.Err)
			}
		}
		if _, err := c.Write([]byte("ok\n")); err != nil {
			return answered, err
		}
		answered++
	}
	return answered, sc.Err()
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
		o, results, answered, err := runAgentTarget(t, f, dir, stepsFile, false, "-test.v")
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if requests := len(slices.DeleteFunc(slices.Clone(steps), agentStep.reads)); answered != requests {
			t.Fatalf("run %d: the agent answered %d requests of %d", run, answered, requests)
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

// runAgentTarget runs TestAgentTarget as the sweep runs a package, in the
// package directory of the fixture f, with the steps in stepsFile and an
// agent (startAgent). It returns the run, the target's results and the
// number of requests the agent answered. dir holds the run's files.
func runAgentTarget(t *testing.T, f fixture, dir, stepsFile string, lateAccept bool, args ...string) (observedRun, []agentRead, int, error) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		return observedRun{}, nil, 0, err
	}
	sock, stop := startAgent(t, lateAccept)
	stem := filepath.Join(dir, "internal_p")
	o, runErr := runObserved(f.root, "internal/p", f.pkg, stem, []string{agentVariable + "=" + sock, agentStepsVariable + "=" + stepsFile}, nil, self, append([]string{"-test.run=^TestAgentTarget$"}, args...)...)
	var results []agentRead
	if runErr == nil {
		runErr = readResultsInto(stem+".out", &results)
	}
	// Results mean the target dialed the agent before it exited.
	answered, agentErr := stop(runErr == nil)
	switch {
	case agentErr != nil && runErr != nil:
		return o, nil, answered, fmt.Errorf("the agent: %v; the run: %w", agentErr, runErr)
	case agentErr != nil:
		return o, nil, answered, fmt.Errorf("the agent: %w", agentErr)
	case runErr != nil:
		return o, nil, answered, runErr
	}
	return o, results, answered, nil
}

// TestAgentServesARunThatEndedFirst: a run with no request to the agent can
// end before the agent accepts its connection (the scheduler has not run
// the agent yet). lateAccept forces that schedule. The agent still serves
// the connection the run left, and the run's results count. Before, stop
// closed the listener at once, and the agent failed with "use of closed
// network connection" (TestOutsideAgentChangesFailClosed runs 4, 7 and 11
// under load). Only a run with no request can take this schedule: a
// request waits for the agent's answer, so the run cannot end first.
func TestAgentServesARunThatEndedFirst(t *testing.T) {
	needSandbox(t)
	f := newFixture(t)
	dir := t.TempDir()
	stepsFile := filepath.Join(dir, "steps.json")
	if err := os.WriteFile(stepsFile, []byte(`[{"Op":"open","Path":"testdata/local/a.txt"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, results, answered, err := runAgentTarget(t, f, dir, stepsFile, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || answered != 0 {
		t.Fatalf("want 1 result and no answered request, got %d and %d", len(results), answered)
	}
}

// TestChangeAfterTheRunFailsClosed: a link the test read through is changed
// after the test exits and before the sweep classifies the run (A2): the
// read is named by its access-time object and fails closed. Without the
// comparison, the access-time object would pass as it is.
func TestChangeAfterTheRunFailsClosed(t *testing.T) {
	needSandbox(t)
	f := newFixture(t)
	dir := t.TempDir()
	stepsFile := filepath.Join(dir, "steps.json")
	if err := os.WriteFile(stepsFile, []byte(`[{"Op":"open","Path":"testdata/local/a.txt"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	o, _, _, err := runAgentTarget(t, f, dir, stepsFile, false)
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
