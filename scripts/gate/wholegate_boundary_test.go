package main

import (
	"bytes"
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"ergo.local/prototype/internal/testinputs/testenv"
	"ergo.local/prototype/scripts/gate/replay"
)

// TestReceivedVariablesAreKeyed is a property over every variable of
// testenv.Names, each consumer of the step environment (a step without
// ToolEnv, a step with ToolEnv, a tool query) and each change of the
// caller's value (unset to set, one value to another, set to unset): the
// whole-gate key changes, or the consumer receives the same environment.
// So every variable a step or a tool query receives is a key input or
// derived from key inputs the same way on every run. (Round-1 finding B1:
// the caller's ERGO_TOOL_VERSIONS reached every step and was not keyed.)
func TestReceivedVariablesAreKeyed(t *testing.T) {
	root := fixture(t)
	type consumer struct {
		name string
		env  func() []string
	}
	consumers := []consumer{
		{"a step without ToolEnv", func() []string {
			g := &gate{root: root}
			return g.commandEnvironment(&Step{Name: "plain", Argv: []string{"sh", "-c", "true"}})
		}},
		{"a step with ToolEnv", func() []string {
			g := &gate{root: root}
			return g.commandEnvironment(&Step{Name: "tools", Argv: []string{"go", "test"}, ToolEnv: []string{"sh"}})
		}},
		{"a tool query", toolQueryEnvironment},
	}
	key := func() string { return (&replay.Facts{Environment: declaredEnvironment()}).Key() }
	received := func(c consumer) string {
		environment := slices.Clone(c.env())
		slices.Sort(environment)
		return strings.Join(environment, "\n")
	}
	type state struct {
		set   bool
		value string
	}
	transitions := [][2]state{
		{{false, ""}, {true, "one"}},
		{{true, "one"}, {true, "two"}},
		{{true, "two"}, {false, ""}},
		{{true, ""}, {false, ""}},
	}
	apply := func(name string, s state) {
		t.Setenv(name, s.value)
		if !s.set {
			os.Unsetenv(name)
		}
	}
	checked, derived := 0, 0
	for _, name := range testenv.Names {
		for _, transition := range transitions {
			apply(name, transition[0])
			beforeKey := key()
			apply(name, transition[1])
			afterKey := key()
			for _, c := range consumers {
				checked++
				if afterKey != beforeKey {
					continue
				}
				derived++
				apply(name, transition[0])
				before := received(c)
				apply(name, transition[1])
				if after := received(c); after != before {
					t.Errorf("%s %v -> %v: the key stayed the same and %s receives another environment:\n%s",
						name, transition[0], transition[1], c.name, lineDiff(before, after))
				}
			}
		}
	}
	// The tool digest is the one listed variable that is not a key input,
	// so the property is not vacuous: its changes reach the check above.
	if derived == 0 {
		t.Fatal("no change left the key the same; the received side was never checked")
	}
	t.Logf("%d changes, %d with an equal key", checked, derived)
}

// lineDiff names the lines one environment has that the other has not.
func lineDiff(before, after string) string {
	a, b := strings.Split(before, "\n"), strings.Split(after, "\n")
	var out []string
	for _, line := range a {
		if !slices.Contains(b, line) {
			out = append(out, "- "+line)
		}
	}
	for _, line := range b {
		if !slices.Contains(a, line) {
			out = append(out, "+ "+line)
		}
	}
	return strings.Join(out, "\n")
}

// TestCallerToolDigestNeverReachesAStep is the round-1 verifier's
// reproduction of B1 (TestVerifyRawToolDigest), kept as a regression
// test. A real one-step gate whose step passes only while the caller's
// ERGO_TOOL_VERSIONS is absent or "good" records; then only the caller's
// value changes. The inner gate must not replay a pass that the uncached
// run fails.
func TestCallerToolDigestNeverReachesAStep(t *testing.T) {
	root := fixture(t)
	t.Setenv(testenv.ToolDigest, "good")
	steps := []*Step{{Name: "reads declared environment", Argv: []string{"sh", "-c", `test "${ERGO_TOOL_VERSIONS+set}" != set || test "$ERGO_TOOL_VERSIONS" = good`}}}
	g := wholeFixture(t, root, steps)
	before, err := g.collect()
	if err != nil {
		t.Fatal(err)
	}
	code, out := runWhole(t, g)
	if code != 0 || !strings.Contains(out, "recorded whole-gate key") {
		t.Fatalf("prime %d: %s", code, out)
	}
	t.Setenv(testenv.ToolDigest, "bad")
	after, err := g.collect()
	if err != nil {
		t.Fatal(err)
	}
	code, out = runWhole(t, g)
	raw := g.derive(steps, true, g.whole)
	var uncached bytes.Buffer
	rawCode := raw.run(&uncached)
	t.Logf("key equal=%t inner exit=%d uncached exit=%d\n%s\n%s", before.Key() == after.Key(), code, rawCode, out, uncached.String())
	if code == 0 && rawCode == 1 {
		t.Fatal("STALE PASS: declared variable changed, whole gate replayed, uncached step failed")
	}
}

// TestFIFOInputDoesNotReplay is the round-1 verifier's reproduction of B2
// (TestVerifyFIFOInput), kept as a regression test. A FIFO in the checkout
// holds "good\n" since more than RecentWindow; a real step reads it and
// passes. The FIFO keeps its inode, so no stat field changes when the step
// drains it. The next inner gate must not replay a pass that the uncached
// run fails. The verifier slept past RecentWindow; here the collections
// happen an hour ahead instead, which is the same for the recent rule.
func TestFIFOInputDoesNotReplay(t *testing.T) {
	prior := collectionTime
	collectionTime = func() time.Time { return time.Now().Add(time.Hour) }
	t.Cleanup(func() { collectionTime = prior })
	root := fixture(t)
	name := root + "/stream"
	if err := syscall.Mkfifo(name, 0o600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(name, os.O_RDWR|syscall.O_NONBLOCK, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	if _, err := pipe.Write([]byte("good\n")); err != nil {
		t.Fatal(err)
	}
	steps := []*Step{{Name: "reads FIFO", Argv: []string{"python3", "-c", `import os; fd=os.open('stream',os.O_RDONLY|os.O_NONBLOCK); assert os.read(fd,16)==b'good\n'`}}}
	g := wholeFixture(t, root, steps)
	g.programs = []string{"sh", "python3"}
	code, out := runWhole(t, g)
	if code != 0 || !strings.Contains(out, "gate: no whole-gate record: stream is a FIFO") {
		t.Fatalf("prime exit %d, want a pass that does not record because of the FIFO:\n%s", code, out)
	}
	before, err := g.collect()
	if err != nil {
		t.Fatal(err)
	}
	code, out = runWhole(t, g)
	raw := g.derive(steps, true, g.whole)
	var uncached bytes.Buffer
	rawCode := raw.run(&uncached)
	after, err := g.collect()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("key equal=%t replay exit=%d uncached exit=%d\n%s\n%s", before.Key() == after.Key(), code, rawCode, out, uncached.String())
	if code == 0 && rawCode == 1 {
		t.Fatal("STALE FIFO PASS: different stream bytes, same whole-gate key")
	}
	if strings.Contains(out, "gate: replayed") {
		t.Fatalf("the gate replayed with a FIFO in the checkout:\n%s", out)
	}
}

// TestHostMemoryIsObserved checks the host fingerprint's physical memory
// at the collector boundary: only a successful positive observation is a
// fact that can match. An unknown size (the reader's 0, or a negative
// value) makes the facts unreplayable, so a gate neither records nor
// replays; a size that changes between the start and the end of a run
// keeps it from recording, and a later gate at another size does not
// replay. A stable size records and replays (the control). (Round-1
// finding M2: an unknown size was the matching value "0".)
func TestHostMemoryIsObserved(t *testing.T) {
	// Explicit budgets: the budget does not depend on the size, so only
	// the memory fact is under test.
	t.Setenv(stepMemoryVariable, "8G")
	t.Setenv(gateMemoryVariable, "8G")
	t.Setenv(hostMemoryVariable, "8G")
	prior := readPhysicalMemory
	t.Cleanup(func() { readPhysicalMemory = prior })
	gateAt := func(t *testing.T) *gate {
		g := wholeFixture(t, fixture(t), []*Step{{Name: "pass", Argv: []string{"true"}}})
		g.programs = []string{"sh", "true"}
		return g
	}
	const size = int64(64 << 30)
	for _, unknown := range []int64{0, -1} {
		t.Run(fmt.Sprintf("unknown %d", unknown), func(t *testing.T) {
			readPhysicalMemory = func() int64 { return unknown }
			g := gateAt(t)
			facts, err := g.collect()
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(facts.Problem(), "the physical memory cannot be read") {
				t.Fatalf("problem %q, want the physical memory named", facts.Problem())
			}
			for run := 1; run <= 2; run++ {
				code, out := runWhole(t, g)
				if code != 0 || strings.Contains(out, "recorded whole-gate key") || strings.Contains(out, "gate: replayed") {
					t.Fatalf("run %d exit %d; want a pass that neither records nor replays:\n%s", run, code, out)
				}
			}
		})
	}
	t.Run("changing", func(t *testing.T) {
		observed := size
		readPhysicalMemory = func() int64 { observed += 1 << 20; return observed }
		g := gateAt(t)
		code, out := runWhole(t, g)
		if code != 0 || !strings.Contains(out, "gate: no whole-gate record: the checkout or the host changed during the run: host fact physical memory") {
			t.Fatalf("exit %d; want a pass that does not record, naming the memory:\n%s", code, out)
		}
		code, out = runWhole(t, g)
		if strings.Contains(out, "gate: replayed") {
			t.Fatalf("exit %d; replayed at a changed size:\n%s", code, out)
		}
	})
	t.Run("stable", func(t *testing.T) {
		readPhysicalMemory = func() int64 { return size }
		g := gateAt(t)
		if code, out := runWhole(t, g); code != 0 || !strings.Contains(out, "recorded whole-gate key") {
			t.Fatalf("exit %d; want a record:\n%s", code, out)
		}
		if code, out := runWhole(t, g); code != 0 || !strings.Contains(out, "gate: replayed 1 steps") {
			t.Fatalf("exit %d; want a replay:\n%s", code, out)
		}
		readPhysicalMemory = func() int64 { return size * 2 }
		if code, out := runWhole(t, g); code != 0 || strings.Contains(out, "gate: replayed") {
			t.Fatalf("exit %d; replayed at another size:\n%s", code, out)
		}
	})
}

// TestUnknownPhysicalMemoryDoesNotReplay is the round-1 verifier's
// reproduction of M2 (TestVerifyUnknownPhysicalMemory), kept as a
// regression test, with the fault injected at the collector's reader.
func TestUnknownPhysicalMemoryDoesNotReplay(t *testing.T) {
	prior := readPhysicalMemory
	readPhysicalMemory = func() int64 { return 0 }
	t.Cleanup(func() { readPhysicalMemory = prior })
	t.Setenv(stepMemoryVariable, "8G")
	t.Setenv(gateMemoryVariable, "8G")
	t.Setenv(hostMemoryVariable, "8G")
	root := fixture(t)
	g := wholeFixture(t, root, []*Step{{Name: "pass", Argv: []string{"true"}}})
	g.programs = []string{"sh", "true"}
	facts, err := g.collect()
	if err != nil {
		t.Fatal(err)
	}
	code, out := runWhole(t, g)
	code2, out2 := runWhole(t, g)
	t.Logf("memory accessor returned unknown (0); problem=%q first=%d second=%d\n%s\n%s", facts.Problem(), code, code2, out, out2)
	if code == 0 && code2 == 0 && strings.Contains(out2, "replayed") {
		t.Fatal("UNKNOWN HOST FACT REPLAYS: unreadable physical memory became a matching constant")
	}
}
