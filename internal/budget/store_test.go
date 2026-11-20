//go:build unix

package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

func testBudget(t *testing.T, cores, share int) *Budget {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "budget.conf")
	if err := os.WriteFile(cfg, []byte("cores "+strconv.Itoa(cores)+"\nshare "+strconv.Itoa(share)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestTakersShareTheBudget: takers on separate open files (each one's
// locks are its own, as separate processes' are) never hold more than B
// cores together, and every one of them gets its cores: a release wakes
// the waiters (no lost hand-on, no deadlock). Mutant:
// budget-watch-never-wakes.
func TestTakersShareTheBudget(t *testing.T) {
	b := testBudget(t, 3, 1)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	var used, peak atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := Request{Width: 1 + i%2, Crit: i%3 == 0, Lane: "t" + strconv.Itoa(i)}
			g, err := b.Take(ctx, req)
			if err != nil {
				errs <- err
				return
			}
			n := used.Add(int64(req.Width))
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			used.Add(-int64(req.Width))
			g.Release()
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a taker did not get its cores: %v", err)
	}
	if p := peak.Load(); p > 3 {
		t.Fatalf("%d cores held at once, budget 3", p)
	}
}

// TestFullBudgetQueues: with the budget full (widths 2 and 1 of 3), a third
// take waits in the queue, and takes the cores when one holder releases.
// Mutant: budget-store-admit-by-count.
func TestFullBudgetQueues(t *testing.T) {
	b := testBudget(t, 3, 0)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	wide, err := b.Take(ctx, Request{Width: 2, Lane: "wide"})
	if err != nil {
		t.Fatal(err)
	}
	narrow, err := b.Take(ctx, Request{Width: 1, Lane: "narrow"})
	if err != nil {
		t.Fatal(err)
	}
	defer narrow.Release()
	got := make(chan *Grant, 1)
	go func() {
		g, err := b.Take(ctx, Request{Width: 1, Lane: "third"})
		if err != nil {
			t.Error(err)
		}
		got <- g
	}()
	if !testwait.Until(t, func() bool { return waiters(t, b) == 1 }) {
		t.Fatal("the third take never queued")
	}
	select {
	case <-got:
		t.Fatal("a take was admitted past a full budget")
	default:
	}
	wide.Release()
	if g := <-got; g != nil {
		g.Release()
	}
}

// waiters is the count of waiters in b's saved state.
func waiters(t *testing.T, b *Budget) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(b.cfg.State, "state.json"))
	if err != nil {
		return 0
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return len(s.Waiters)
}

// TestCrashFreesTheCores: a holder that dies without Release (its entry's
// lock dropped by the kernel, here by closing its file) frees its cores,
// and the waiter it blocked gets them. Mutant: budget-dead-entry-alive.
func TestCrashFreesTheCores(t *testing.T) {
	b := testBudget(t, 2, 0)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	g, err := b.Take(ctx, Request{Width: 2, Lane: "dies"})
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan error, 1)
	go func() {
		w, err := b.Take(ctx, Request{Width: 2, Lane: "waits"})
		if err == nil {
			w.Release()
		}
		got <- err
	}()
	g.file.Close() // the crash: the kernel drops the lock; no Release runs
	if err := <-got; err != nil {
		t.Fatalf("the waiter behind a crashed holder: %v", err)
	}
}

// TestLentGrantTakesNothing: a child that inherits a grant (GrantEnv)
// runs in it, even when the budget is full, so a holder never waits on a
// child queued for the budget (the model's NoHolderWaitsOnQueue). A child
// wider than the grant is not lent. Mutant: budget-lend-ignored.
func TestLentGrantTakesNothing(t *testing.T) {
	b := testBudget(t, 2, 0)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	g, err := b.Take(ctx, Request{Width: 2, Lane: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Release()
	name, value, _ := strings.Cut(g.Env(), "=")
	t.Setenv(name, value)
	c, err := b.Take(ctx, Request{Width: 2, Lane: "child"})
	if err != nil {
		t.Fatalf("a child in its parent's grant: %v", err)
	}
	if !c.Lent() {
		t.Fatal("the child's grant is not lent")
	}
	c.Release()
}

// TestRunKillsTheGroup: `ergo budget -- <cmd>` gives the command
// GOMAXPROCS as its width and its exit code back, and kills what the
// command left running in its group (a leaked process group). Mutant:
// budget-run-leaves-group.
func TestRunKillsTheGroup(t *testing.T) {
	b := testBudget(t, 2, 0)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	var out bytes.Buffer
	code, err := Run(ctx, b, Request{Width: 2, Lane: "run"},
		[]string{"sh", "-c", "sleep 1000 & echo $! > " + pidFile + "; echo $GOMAXPROCS; exit 3"}, nil, &out, os.Stderr)
	if err != nil || code != 3 {
		t.Fatalf("Run: code %d, err %v; want 3", code, err)
	}
	if got := strings.TrimSpace(out.String()); got != "2" {
		t.Errorf("GOMAXPROCS in the command: %q, want 2", got)
	}
	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	// The background child is this test's own; it dies even when Run left it.
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	if !testwait.Until(t, func() bool { return gone(pid) }) {
		t.Errorf("the command's background child %d still runs after Run", pid)
	}
}

// gone reports a process that is not running: no such process, or a
// zombie waiting for its new parent.
func gone(pid int) bool {
	if syscall.Kill(pid, 0) != nil {
		return true
	}
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return true
	}
	_, rest, _ := strings.Cut(string(data), ") ")
	return strings.HasPrefix(rest, "Z")
}
