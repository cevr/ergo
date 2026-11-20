//go:build unix

package budget

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
)

func testBudget(t *testing.T, cores, share int, more ...string) *Budget {
	t.Helper()
	cfg := filepath.Join(t.TempDir(), "budget.conf")
	text := "cores " + strconv.Itoa(cores) + "\nshare " + strconv.Itoa(share) + "\n" + strings.Join(more, "\n") + "\n"
	if err := os.WriteFile(cfg, []byte(text), 0o644); err != nil {
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
	data, err := os.ReadFile(filepath.Join(b.state, "state.json"))
	if err != nil {
		return 0
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return len(s.Waiters)
}

// TestConfigEditWakesAWaiter: an edit of the budget's file (replaced by a
// rename, as the owner edits it) takes effect at once: a waiter that did
// not fit in 2 cores takes when the file says 3, with no entry ending to
// wake it. Mutants: budget-conf-not-reread, budget-conf-edit-no-wake.
func TestConfigEditWakesAWaiter(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the file watch is Linux's inotify")
	}
	b := testBudget(t, 2, 0)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	holder, err := b.Take(ctx, Request{Width: 2, Lane: "holder"})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	got := make(chan error, 1)
	go func() {
		g, err := b.Take(ctx, Request{Width: 1, Lane: "waiter"})
		if err == nil {
			g.Release()
		}
		got <- err
	}()
	if !testwait.Until(t, func() bool { return waiters(t, b) == 1 }) {
		t.Fatal("the waiter never queued")
	}
	tmp := b.cfg.Path + ".new"
	if err := os.WriteFile(tmp, []byte("cores 3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, b.cfg.Path); err != nil {
		t.Fatal(err)
	}
	if err := <-got; err != nil {
		t.Fatalf("the waiter after the budget grew to 3 cores: %v", err)
	}
}

// TestMemoryQueues: a take that fits in cores but not in memory waits
// (WithinMemory): with 8 of 10 reserved, a command never measured
// (reserved the default, 8) queues; its context is already done, so a
// take that queued returns its error. Mutant: budget-admit-by-cores.
func TestMemoryQueues(t *testing.T) {
	b := testBudget(t, 4, 0, "memory 10", "default-memory 8")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	g, err := b.Take(ctx, Request{Width: 1, Lane: "holder"})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Release()
	if g.Memory() != 8 {
		t.Fatalf("an unmeasured command reserves %d, want the default 8", g.Memory())
	}
	done, stop := context.WithCancel(ctx)
	stop()
	if w, err := b.Take(done, Request{Width: 1, Lane: "over"}); err == nil {
		w.Release()
		t.Fatal("a take of 8 was admitted with 8 of 10 reserved")
	}
}

// TestPeakRaisesTheReservation: a command's next take reserves the largest
// peak it reached, and a later smaller peak does not shrink it (mem.ergo's
// LastPeak). Mutant: budget-last-peak.
func TestPeakRaisesTheReservation(t *testing.T) {
	b := testBudget(t, 4, 0, "memory 100", "default-memory 8")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	// Reserved: the default, then the largest peak and a quarter.
	for i, c := range []struct{ peak, want int64 }{{40, 8}, {10, 50}, {0, 50}} {
		g, err := b.Take(ctx, Request{Width: 1, Lane: "run", Key: "go test ./x"})
		if err != nil {
			t.Fatal(err)
		}
		if g.Memory() != c.want {
			t.Fatalf("run %d reserves %d, want %d", i+1, g.Memory(), c.want)
		}
		g.Finish(0, c.peak)
		g.Release()
	}
}

// TestRunCapsAtTheReservation: with memory budgeted, a command runs capped
// at its reservation in a scope of its own: one that allocates past it is
// killed (SIGKILL), and its next take reserves twice the cap. Mutant:
// budget-no-cap.
func TestRunCapsAtTheReservation(t *testing.T) {
	if !scopes() {
		t.Skip("needs the user's systemd for a scope")
	}
	b := testBudget(t, 2, 0, "memory 1G", "default-memory 1G")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	// dd's buffer is its block size: 200M allocated, against a 64M cap.
	argv := []string{"dd", "if=/dev/zero", "of=/dev/null", "bs=200M", "count=1"}
	code, err := Run(ctx, b, Request{Width: 1, Lane: "over", Memory: 64 << 20, Key: "dd"}, argv, nil, io.Discard, io.Discard)
	if err != nil || code != 137 {
		t.Fatalf("a command over its 64M cap: code %d, err %v; want 137 (killed)", code, err)
	}
	if got := b.peak("dd"); got != 128<<20 {
		t.Fatalf("the peak kept after a kill at 64M: %d, want %d (twice the cap)", got, 128<<20)
	}
	// Killed inside a shell, which exits 1: the scope's kill count, not
	// the code, says the cap killed (2026-11-20). Mutant:
	// budget-kill-by-code.
	sh := []string{"sh", "-c", "dd if=/dev/zero of=/dev/null bs=200M count=1 || exit 1"}
	code, err = Run(ctx, b, Request{Width: 1, Lane: "over", Memory: 64 << 20, Key: "dd-sh"}, sh, nil, io.Discard, io.Discard)
	if err != nil || code != 1 {
		t.Fatalf("a shell whose dd is over its 64M cap: code %d, err %v; want 1", code, err)
	}
	if got := b.peak("dd-sh"); got != 128<<20 {
		t.Fatalf("the peak kept after a kill at 64M inside a shell: %d, want %d (twice the cap)", got, 128<<20)
	}
}

// TestUnknownCommandIsNotKilled: a command never measured is charged the
// default (64M here) but not capped at it: one that needs 200M runs to
// its end, and its peak is recorded, so its next take reserves it (the
// orchestrator's ruling of 2026-11-20; mem.ergo UnknownNotCapped). Mutant:
// budget-cap-unknown.
func TestUnknownCommandIsNotKilled(t *testing.T) {
	if !scopes() {
		t.Skip("needs the user's systemd for a scope")
	}
	b := testBudget(t, 2, 0, "memory 1G", "default-memory 64M")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	argv := []string{"dd", "if=/dev/zero", "of=/dev/null", "bs=200M", "count=1"}
	code, err := Run(ctx, b, Request{Width: 1, Lane: "unknown", Key: "dd-unknown"}, argv, nil, io.Discard, io.Discard)
	if err != nil || code != 0 {
		t.Fatalf("a never-measured command that needs 200M, charged 64M: code %d, err %v; want 0", code, err)
	}
	if got := b.peak("dd-unknown"); got < 200<<20 {
		t.Fatalf("the peak recorded: %d, want at least 200M", got)
	}
}

// TestSliceCapsTheBudget: the budget's commands together are capped at
// the budget's memory, so a never-measured command that needs more than
// the whole budget (200M of 128M) is killed by the slice, not the host,
// and its real peak so far is recorded (mem.ergo RealWithinMemory).
// Mutant: budget-no-slice-cap.
func TestSliceCapsTheBudget(t *testing.T) {
	if !scopes() {
		t.Skip("needs the user's systemd for a scope")
	}
	b := testBudget(t, 2, 0, "memory 128M", "default-memory 64M")
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	argv := []string{"dd", "if=/dev/zero", "of=/dev/null", "bs=200M", "count=1"}
	code, err := Run(ctx, b, Request{Width: 1, Lane: "over-budget", Key: "dd-over"}, argv, nil, io.Discard, io.Discard)
	if err != nil || code != 137 {
		t.Fatalf("a command past the 128M budget: code %d, err %v; want 137 (killed by the slice)", code, err)
	}
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
// child queued for the budget (the model's NoHolderWaitsOnQueue). Mutant:
// budget-lend-ignored.
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

// TestWiderChildRunsInTheGrant: a child wider than its parent's grant
// (a heavy run of 2 cores calling light.sh's width 4) runs in the grant at
// the grant's width; it never joins the queue, where its parent would wait
// on it. The child's context is already done, so a child that queued
// returns its error at once. Mutant: budget-lend-wider-queues.
func TestWiderChildRunsInTheGrant(t *testing.T) {
	b := testBudget(t, 3, 0)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	g, err := b.Take(ctx, Request{Width: 1, Lane: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	defer g.Release()
	name, value, _ := strings.Cut(g.Env(), "=")
	t.Setenv(name, value)
	done, stop := context.WithCancel(ctx)
	stop()
	c, err := b.Take(done, Request{Width: 3, Lane: "child"})
	if err != nil {
		t.Fatalf("a child wider than its parent's grant: %v", err)
	}
	if !c.Lent() || c.Width() != 1 {
		t.Fatalf("the child's grant: lent %v, width %d; want lent, width 1", c.Lent(), c.Width())
	}
	c.Release()
}

// TestRunKillsTheGroup: `ergo budget -- <cmd>` gives the command
// GOMAXPROCS as its width and its exit code back, and kills what the
// command left running in its group (a leaked process group): when Run
// returns, the background child is gone or has SIGKILL pending. Mutant:
// budget-run-leaves-group. The child does not hold the command's output:
// one that did would keep Run waiting under the mutant, a hang no test
// can call a kill.
func TestRunKillsTheGroup(t *testing.T) {
	if _, err := os.Stat("/proc/self/status"); err != nil {
		t.Skip("reads a process's state from /proc")
	}
	b := testBudget(t, 2, 0)
	ctx, cancel := testwait.Context(t, context.Background())
	defer cancel()
	pidFile := filepath.Join(t.TempDir(), "pid")
	var out bytes.Buffer
	code, err := Run(ctx, b, Request{Width: 2, Lane: "run"},
		[]string{"sh", "-c", "sleep 1000 </dev/null >/dev/null 2>&1 & echo $! > " + pidFile + "; echo $GOMAXPROCS; exit 3"}, nil, &out, os.Stderr)
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
	if !killed(pid) {
		// The background child is this test's own; it dies even when Run left it.
		syscall.Kill(pid, syscall.SIGKILL)
		t.Errorf("the command's background child %d still runs after Run, with no SIGKILL pending", pid)
	}
}

// killed reports a process that is gone (no such process, or a zombie) or
// has SIGKILL pending: kill(2) has returned, so the signal is queued even
// when the process has not yet run to its exit.
func killed(pid int) bool {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return true
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch key {
		case "State":
			if strings.HasPrefix(value, "Z") || strings.HasPrefix(value, "X") {
				return true
			}
		case "SigPnd", "ShdPnd":
			if mask, err := strconv.ParseUint(value, 16, 64); err == nil && mask&(1<<(uint(syscall.SIGKILL)-1)) != 0 {
				return true
			}
		}
	}
	return false
}
