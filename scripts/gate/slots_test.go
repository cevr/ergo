package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestSlotPoolServesWritersFirst: with every slot taken, a freed slot goes
// to the writers' queue first, though its step came last, and each queue
// is served in the order its steps came.
func TestSlotPoolServesWritersFirst(t *testing.T) {
	pool := newSlotPool(1)
	pool.acquire(false)
	order := make(chan string, 8)
	wait := func(first, rest int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if f, r := pool.waiting(); f == first && r == rest {
				return
			}
			if time.Now().After(deadline) {
				f, r := pool.waiting()
				t.Fatalf("waiting %d, %d; want %d, %d", f, r, first, rest)
			}
			time.Sleep(time.Millisecond)
		}
	}
	start := func(name string, first bool) {
		go func() {
			pool.acquire(first)
			order <- name
			pool.release()
		}()
	}
	start("a", false)
	wait(0, 1)
	start("b", false)
	wait(0, 2)
	start("writer", true)
	wait(1, 2)
	start("build", true)
	wait(2, 2)
	pool.release()
	var got []string
	for range 4 {
		got = append(got, <-order)
	}
	if want := []string{"writer", "build", "a", "b"}; !slices.Equal(got, want) {
		t.Fatalf("slots went to %v, want %v", got, want)
	}
	// Every slot is free again: two acquires of a pool of one would block.
	pool.acquire(false)
	if f, r := pool.waiting(); f != 0 || r != 0 || pool.free != 0 {
		t.Fatalf("after the run: waiting %d, %d, free %d", f, r, pool.free)
	}
}

// TestWriterPathMarksWritersAndTheirNeeds: in every tier the writers' queue
// holds build bin/ergo and every step it needs (in the full tier, go test
// build), and no closure sweep.
func TestWriterPathMarksWritersAndTheirNeeds(t *testing.T) {
	host := sweepOS
	t.Cleanup(func() { sweepOS = host })
	sweepOS = "linux"
	root := checkoutRoot(t)
	full, err := gateSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	fast, err := fastSteps(root)
	if err != nil {
		t.Fatal(err)
	}
	for tier, steps := range map[string][]*Step{fastTier: fast, fullTier: full} {
		index := map[string]int{}
		for i, step := range steps {
			index[step.Name] = i
		}
		marked := writerPath(steps, index)
		var names []string
		for i, step := range steps {
			if marked[i] {
				names = append(names, step.Name)
			}
			if marked[i] && step.Sweep != "" {
				t.Errorf("%s tier: the sweep %s is in the writers' queue", tier, step.Name)
			}
		}
		if !slices.Contains(names, buildCLIStep) {
			t.Errorf("%s tier: writers' queue %v lacks %s", tier, names, buildCLIStep)
		}
		for i, step := range steps {
			for _, need := range step.Needs {
				if marked[i] && !marked[index[need]] {
					t.Errorf("%s tier: %s is in the writers' queue and %s, which it needs, is not", tier, step.Name, need)
				}
			}
		}
	}
	steps := []*Step{{Name: "a"}, {Name: "b", Needs: []string{"a"}}, {Name: "w", Needs: []string{"b"}, Writes: []string{"bin/"}}, {Name: "c", Needs: []string{"a"}}}
	if got := writerPath(steps, map[string]int{"a": 0, "b": 1, "w": 2, "c": 3}); !slices.Equal(got, []bool{true, true, true, false}) {
		t.Errorf("writerPath = %v, want a, b and w", got)
	}
}

// TestWriterTakesTheNextFreedSlot: in a gate of one slot, six steps queue
// at once, and the writer queues only when the build it needs ends, 0.3 s
// later. The writer still runs before at least three of the six: it takes
// the next freed slot after one step at most, where a queue in arrival
// order runs it after all six.
func TestWriterTakesTheNextFreedSlot(t *testing.T) {
	root := fixture(t)
	t.Setenv(cpuVariable, "1")
	order := filepath.Join(t.TempDir(), "order")
	record := func(name, wait string) []string {
		return []string{"sh", "-c", wait + `echo ` + name + ` >> '` + order + `'`}
	}
	steps := []*Step{{Name: "build", Argv: record("build", "sleep 0.3; ")}}
	for _, name := range []string{"s1", "s2", "s3", "s4", "s5", "s6"} {
		steps = append(steps, &Step{Name: name, Argv: record(name, "sleep 0.05; ")})
	}
	steps = append(steps, &Step{Name: "writer", Needs: []string{"build"}, Writes: []string{"gen/"}, Argv: record("writer", "")})
	code, out := runGate(t, root, t.TempDir(), steps)
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	content, err := os.ReadFile(order)
	if err != nil {
		t.Fatal(err)
	}
	ran := strings.Fields(string(content))
	at := slices.Index(ran, "writer")
	if at < 0 || len(ran) != 8 || len(ran)-1-at < 3 {
		t.Fatalf("the steps ran in the order %v; want the writer before at least three of s1-s6", ran)
	}
}
