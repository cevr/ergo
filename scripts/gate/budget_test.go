package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The memory budget, tested through the gate's run as a caller sees it:
// the environment sets the budgets, and steps run this test binary as an
// allocator (TestBudgetAllocator). This file names no gate internals, so it
// also runs against a gate without a budget, where it fails.

// TestBudgetAllocator is a step's process: run with two arguments, it
// allocates and touches that many MiB, prints "allocated", and holds the
// memory for the given duration. It prints "held" only after the full hold.
func TestBudgetAllocator(t *testing.T) {
	if flag.NArg() != 2 {
		t.Skip("a budget test runs it as a step")
	}
	mib, err := strconv.Atoi(flag.Arg(0))
	if err != nil {
		t.Fatal(err)
	}
	hold, err := time.ParseDuration(flag.Arg(1))
	if err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	for range mib / 16 {
		chunk := make([]byte, 16<<20)
		for i := 0; i < len(chunk); i += 4096 {
			chunk[i] = 1
		}
		chunks = append(chunks, chunk)
	}
	fmt.Printf("allocated %d MiB\n", mib)
	time.Sleep(hold)
	runtime.KeepAlive(chunks)
	fmt.Printf("held %d MiB\n", mib)
}

// allocatorStep allocates mib MiB and holds it. The shell around the
// allocator exits 0 even when the allocator is killed, so a verdict cannot
// come from the exit status alone. Its result is cacheable, so a test can
// see that a budget failure is never recorded.
func allocatorStep(name string, mib int, hold time.Duration) *Step {
	return &Step{
		Name:   name,
		Argv:   []string{"sh", "-c", `"$0" -test.run='^TestBudgetAllocator$' -test.count=1 "$1" "$2"; echo "allocator exited $?"`, os.Args[0], strconv.Itoa(mib), hold.String()},
		Inputs: &Inputs{Paths: []string{"src/" + name + ".txt"}},
	}
}

// The mechanisms a budget case runs under.
const (
	rssMechanism   = "rss"
	scopeMechanism = "scope"
)

// budgetCase is one gate run under a budget, checked on every mechanism
// unless scopeOnly.
type budgetCase struct {
	name, step, gate string
	scopeOnly        bool
	steps            func() []*Step
	check            func(t *testing.T, mechanism string, code int, out string, receipts []string)
}

// stepStopped checks that the gate stopped its one step, alloc, for the
// step budget, went on and recorded no pass.
func stepStopped(t *testing.T, code int, out string, receipts []string) {
	t.Helper()
	wantAll(t, out, "== alloc: FAILED", "gate: alloc: memory budget exceeded (512M)\n", "gate: FAILED in")
	if code != 1 || len(receipts) != 0 {
		t.Errorf("exit %d, receipts %v: want 1 and no receipt:\n%s", code, receipts, out)
	}
}

var budgetCases = []budgetCase{
	{
		// The acceptance case: a 2 GB allocation under a 512M step budget is
		// stopped during its hold, the gate goes on, reports it and records no pass.
		name: "over", step: "512M", gate: "2G",
		steps: func() []*Step {
			return []*Step{allocatorStep("alloc", 2048, 10*time.Second), {Name: "independent", Argv: []string{"sh", "-c", "echo independent ran"}}}
		},
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			stepStopped(t, code, out, receipts)
			wantAll(t, out, "== independent: ok", "independent ran")
			if strings.Contains(out, "held 2048 MiB") {
				t.Errorf("the allocator held its memory through the full interval:\n%s", out)
			}
		},
	},
	{
		// A step at one and a half times its budget is stopped too: the
		// bound is the budget, not a multiple of it.
		name: "boundary", step: "512M", gate: "2G",
		steps: func() []*Step { return []*Step{allocatorStep("alloc", 768, 5*time.Second)} },
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			stepStopped(t, code, out, receipts)
			if strings.Contains(out, "held 768 MiB") {
				t.Errorf("the allocator held its memory through the full interval:\n%s", out)
			}
		},
	},
	{
		// A process of the step that stays under the budget is stopped with
		// the one that passes it. FIFO handshakes confirm the survivor started
		// before allocation and let it finish only if allocation completes.
		name: "whole", step: "512M", gate: "2G",
		steps: func() []*Step {
			return []*Step{{
				Name: "alloc",
				Argv: []string{"sh", "-c", `mkfifo src/survivor-ready src/survivor-release
( echo "survivor started"; echo ready > src/survivor-ready; read -r _ < src/survivor-release; echo "survivor finished" ) &
read -r _ < src/survivor-ready
"$0" -test.run='^TestBudgetAllocator$' -test.count=1 2048 10s
allocatorStatus=$?
echo "allocator exited $allocatorStatus"
echo release > src/survivor-release
wait
echo "step exited"`, os.Args[0]},
				Inputs: &Inputs{Paths: []string{"src/alloc.txt"}},
			}}
		},
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			stepStopped(t, code, out, receipts)
			if !strings.Contains(out, "survivor started") {
				t.Errorf("the survivor did not signal that it started before allocation:\n%s", out)
			}
			if strings.Contains(out, "survivor finished") || strings.Contains(out, "step exited") {
				t.Errorf("part of the step ran on after the budget stopped it:\n%s", out)
			}
		},
	},
	{
		// A step under its budget passes, and the gate reports its peak.
		name: "under", step: "512M", gate: "2G",
		steps: func() []*Step { return []*Step{allocatorStep("alloc", 64, time.Second)} },
		check: func(t *testing.T, _ string, code int, out string, receipts []string) {
			wantAll(t, out, "allocated 64 MiB", "allocator exited 0")
			if code != 0 || !strings.Contains(out, "== alloc: ok (") || !strings.Contains(out, ", peak ") || len(receipts) != 1 {
				t.Errorf("exit %d, receipts %v: want a pass with its peak, recorded once:\n%s", code, receipts, out)
			}
		},
	},
	{
		// Two steps, each under the step budget, together pass the gate
		// budget: the gate stops the largest, so its steps never hold more
		// than the gate budget together. The RSS watch stops exactly one;
		// under the systemd scope the kernel may stop the other too, when
		// the first one's memory is not yet freed as the second one grows.
		name: "gate", step: "512M", gate: "768M",
		steps: func() []*Step {
			return []*Step{allocatorStep("a", 448, 3*time.Second), allocatorStep("b", 448, 3*time.Second)}
		},
		check: func(t *testing.T, mechanism string, code int, out string, receipts []string) {
			stopped := 0
			for _, name := range []string{"a", "b"} {
				if strings.Contains(out, "gate: "+name+": gate memory budget exceeded (768M)") {
					stopped++
				} else if !strings.Contains(out, "== "+name+": ok") {
					t.Errorf("step %s neither passed nor was stopped for the gate budget", name)
				}
			}
			if code != 1 || stopped == 0 || len(receipts) != 2-stopped || mechanism == rssMechanism && stopped != 1 {
				t.Errorf("exit %d, %d steps stopped for the gate budget under the %s mechanism, receipts %v: want 1, one step (either on the scope), and one receipt per passed step:\n%s", code, stopped, mechanism, receipts, out)
			}
		},
	},
	{
		// A gate started inside a step of this one opens no scope of its own:
		// it stays in the step's cgroup, whose caps hold.
		// TestBudgetNestedHelper is that gate's start.
		name: "nested", step: "512M", gate: "2G", scopeOnly: true,
		steps: func() []*Step {
			return []*Step{{Name: "nested", Argv: []string{"sh", "-c", `GATE_BUDGET_NESTED=1 "$0" -test.run='^TestBudgetNestedHelper$' -test.count=1 -test.v`, os.Args[0]}}}
		},
		check: func(t *testing.T, _ string, code int, out string, _ []string) {
			wantAll(t, out, "nested note: inside step cgroup ", "of an outer gate, whose caps hold", "nested cgroup: 0::/", "/step-")
			if code != 0 || strings.Contains(out, "re-executed in a scope") {
				t.Errorf("exit %d: want 0, and the nested gate in the outer step's cgroup:\n%s", code, out)
			}
		},
	},
}

func wantAll(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// receiptsIn lists the passes recorded in a gate cache directory.
func receiptsIn(t *testing.T, cache string) []string {
	t.Helper()
	receipts, err := filepath.Glob(filepath.Join(cache, "*", "*", "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	return receipts
}

// budgetFixture is a fixture with the inputs the allocator steps declare.
func budgetFixture(t *testing.T) string {
	root := fixture(t)
	for _, name := range []string{"alloc", "a", "b"} {
		write(t, root, "src/"+name+".txt", name+"\n")
	}
	return root
}

// TestBudgetRSSWatch runs the cases under the RSS watch, the mechanism of
// darwin and of hosts without a usable systemd scope, forced here so it
// runs on Linux too.
func TestBudgetRSSWatch(t *testing.T) {
	t.Setenv("ERGO_GATE_MEMORY_MECHANISM", "rss")
	for _, c := range budgetCases {
		if c.scopeOnly {
			continue
		}
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("ERGO_GATE_STEP_MEMORY", c.step)
			t.Setenv("ERGO_GATE_MEMORY", c.gate)
			cache := t.TempDir()
			code, out := runGate(t, budgetFixture(t), cache, c.steps())
			t.Logf("gate exit %d:\n%s", code, out)
			if !strings.Contains(out, "gate: memory budget: RSS watch "+c.step+" per step, "+c.gate+" per gate, no bound across gates (forced by ERGO_GATE_MEMORY_MECHANISM=rss)\n") {
				t.Errorf("the gate does not name the RSS watch and its budget:\n%s", out)
			}
			c.check(t, rssMechanism, code, out, receiptsIn(t, cache))
		})
	}
}
