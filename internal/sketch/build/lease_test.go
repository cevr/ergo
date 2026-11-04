package build

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// leaseCompiler is the process-scoped identity of the lease helper.
var leaseCompiler = "process:" + strings.Repeat("7", 64)

// leaseHelper is helper mode "lease", a long-lived host that builds into
// one private root. For each line on standard input: "build" builds
// baseInput under leaseCompiler, takes the new module, then releases the one
// it held, so its builds overlap, and prints the module directory; "link"
// links the module it holds to a fresh program and prints "linked PATH";
// "release" releases the module it holds and prints "released". It exits at
// the end of its input, or when it is killed.
func leaseHelper() int {
	c := Cache{Root: os.Getenv(rootVariable)}
	in := baseInput()
	in.Compiler = leaseCompiler
	var held Module
	lines := bufio.NewScanner(os.Stdin)
	for lines.Scan() {
		switch lines.Text() {
		case "build":
			module, err := c.Module(in, emitFor(in, nil))
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				return 1
			}
			held.Release()
			held = module
			fmt.Println(module.Dir)
		case "link":
			output := filepath.Join(os.Getenv(rootVariable), "program")
			if out, err := hostToolchain().Command(held, output).CombinedOutput(); err != nil {
				fmt.Fprintf(os.Stderr, "%v: %s\n", err, out)
				return 1
			}
			fmt.Println("linked " + output)
		case "release":
			held.Release()
			held = Module{}
			fmt.Println("released")
		}
	}
	return 0
}

// privateRootOf is the private root of compiler under root.
func privateRootOf(root, compiler string) string {
	sum := sha256.Sum256([]byte(compiler))
	return filepath.Join(root, "private", hex.EncodeToString(sum[:16]))
}

// age sets root's modification time past privateLifetime.
func age(t *testing.T, root string) {
	t.Helper()
	old := time.Now().Add(-2 * privateLifetime)
	if err := os.Chtimes(root, old, old); err != nil {
		t.Fatal(err)
	}
}

// sweepUntil sweeps c in a loop, re-ageing root before each pass, until the
// returned stop is called. stop ends the loop, waits until the sweeping
// goroutine has returned, and reports how many passes ran; it is safe to
// call more than once, and the test's cleanup calls it on every path.
func sweepUntil(t *testing.T, c Cache, root string) (stop func() int64) {
	var n atomic.Int64
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		old := time.Now().Add(-2 * privateLifetime)
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = os.Chtimes(root, old, old)
			c.trim(time.Now(), 0)
			n.Add(1)
		}
	}()
	var once sync.Once
	stop = func() int64 {
		once.Do(func() { close(done); <-finished })
		return n.Load()
	}
	t.Cleanup(func() { stop() })
	return stop
}

// TestPrivateRootLease: a private root is removed only when no build holds
// it (round 1 review M1), and a build holds it only from Module until its
// Release (round 2 review O2). An owner process in the middle of a build,
// idle past privateLifetime, keeps its root; overlapping builds keep it
// while sweeps run; it stays while the owner links the module; once the
// owner releases its build, the live owner holds nothing and an aged root
// is removed, and its next build takes a fresh root; and once a building
// owner is killed, the next sweep removes the root.
func TestPrivateRootLease(t *testing.T) {
	if !leasesSupported {
		t.Skip("no flock here: private roots are never collected")
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c := Cache{Root: root}
	owner := exec.Command(binary, "-test.run=^$")
	owner.Env = append(os.Environ(), helperVariable+"=lease", rootVariable+"="+root)
	stdin, err := owner.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := owner.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	owner.Stderr = os.Stderr
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	killed := false
	defer func() {
		if !killed {
			_ = owner.Process.Kill()
			_ = owner.Wait()
		}
	}()
	replies := bufio.NewScanner(stdout)
	ask := func(line string) string {
		t.Helper()
		if _, err := io.WriteString(stdin, line+"\n"); err != nil {
			t.Fatal(err)
		}
		if !replies.Scan() {
			t.Fatalf("the owner did not answer %q: %v", line, replies.Err())
		}
		return replies.Text()
	}
	private := privateRootOf(root, leaseCompiler)
	exists := func() bool { _, err := os.Stat(filepath.Join(private, leaseName)); return err == nil }

	first := ask("build")
	if !strings.HasPrefix(first, private+string(filepath.Separator)) {
		t.Fatalf("the owner built %s, want a module under %s", first, private)
	}

	t.Run("a stale building owner keeps its root", func(t *testing.T) {
		for range 3 {
			age(t, private)
			c.trim(time.Now(), 0)
		}
		if !exists() {
			t.Fatal("a sweep removed the root of a live build")
		}
	})

	t.Run("overlapping builds keep the root while sweeps run", func(t *testing.T) {
		stop := sweepUntil(t, c, private)
		for range 10 {
			if dir := ask("build"); dir != first {
				t.Fatalf("the owner's build gave %s, want its module %s", dir, first)
			}
		}
		if sweeps := stop(); !exists() || sweeps == 0 {
			t.Fatalf("root present %v after %d concurrent sweeps", exists(), sweeps)
		}
	})

	t.Run("a root stays while its owner links", func(t *testing.T) {
		stop := sweepUntil(t, c, private)
		reply := ask("link")
		sweeps := stop()
		program, ok := strings.CutPrefix(reply, "linked ")
		if !ok {
			t.Fatalf("the owner answered %q", reply)
		}
		in := baseInput()
		in.Compiler = leaseCompiler
		if got := runProgram(t, program); got != expectedOutput(t, in, "r1", "unset") || !exists() {
			t.Fatalf("after %d sweeps: program printed %q, root present %v", sweeps, got, exists())
		}
	})

	t.Run("a live owner holds nothing between builds", func(t *testing.T) {
		if reply := ask("release"); reply != "released" {
			t.Fatalf("the owner answered %q", reply)
		}
		age(t, private)
		c.trim(time.Now(), 0)
		if _, err := os.Stat(private); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("the released root of a live owner is still there: %v", err)
		}
		if dir := ask("build"); dir != first || !exists() {
			t.Fatalf("the next build gave %s, root present %v; want a fresh %s", dir, exists(), first)
		}
	})

	t.Run("an abandoned root is removed", func(t *testing.T) {
		if err := owner.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		_ = owner.Wait()
		killed = true
		age(t, private)
		c.trim(time.Now(), 0)
		if _, err := os.Stat(private); !os.IsNotExist(err) {
			t.Fatalf("the abandoned root is still there: %v", err)
		}
		if names := entries(t, root, "private"); len(names) != 0 {
			t.Fatalf("the sweep left %v", names)
		}
	})
}

// TestLeaseRaceWithSweep: an owner taking a root while a sweep collects it
// never ends up holding a lease on a root that is gone. Each round ages an
// ownerless root and runs takeLease and collect at once; whatever the order,
// a lease that takeLease returns names the root's current lease file.
func TestLeaseRaceWithSweep(t *testing.T) {
	if !leasesSupported {
		t.Skip("no flock here")
	}
	root := filepath.Join(t.TempDir(), "private", "r")
	removed := 0
	for range 300 {
		f, err := takeLease(root)
		if err != nil {
			t.Fatal(err)
		}
		f.Close()
		age(t, root)
		var wg sync.WaitGroup
		var owned *os.File
		var takeErr error
		wg.Add(2)
		go func() { defer wg.Done(); owned, takeErr = takeLease(root) }()
		go func() {
			defer wg.Done()
			if collect(root) {
				removed++
			}
		}()
		wg.Wait()
		if takeErr != nil {
			t.Fatal(takeErr)
		}
		if !sameLease(owned, filepath.Join(root, leaseName)) {
			t.Fatal("the owner holds a lease on a root a sweep removed")
		}
		if collect(root) {
			t.Fatal("a sweep removed a root its owner holds")
		}
		owned.Close()
	}
	t.Logf("300 rounds: the sweep won %d", removed)
}

// TestCollectKeepsAnArrivingBuildsModule (round 2 review M1): a sweep
// collecting an ownerless root with real module files pauses after it has
// unlinked the root's lease; in the pause a build takes the root and its
// module; then the removal finishes. The build's module must still be
// whole: the sweep renamed the root aside first, so the build made a fresh
// root and emitted into it. Removing in place instead would let the build
// lock a new lease and reuse the old module, which the removal then deletes.
func TestCollectKeepsAnArrivingBuildsModule(t *testing.T) {
	if !leasesSupported {
		t.Skip("no flock here")
	}
	root := t.TempDir()
	c := Cache{Root: root}
	in := baseInput()
	in.Compiler = "process:" + strings.Repeat("8", 64)
	var calls atomic.Int32
	old, err := c.Module(in, emitFor(in, &calls))
	if err != nil {
		t.Fatal(err)
	}
	old.Release()
	private := privateRootOf(root, in.Compiler)
	age(t, private)

	var arrived Module
	var arriveErr error
	paused := false
	testHookCollecting = func(string) {
		paused = true
		arrived, arriveErr = c.Module(in, emitFor(in, &calls))
	}
	defer func() { testHookCollecting = nil }()
	if !collect(private) {
		t.Fatal("the sweep did not collect the ownerless, aged root")
	}
	defer arrived.Release()
	if !paused || arriveErr != nil {
		t.Fatalf("the build in the pause: ran %v, error %v", paused, arriveErr)
	}
	summary, err := in.Summarize()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := verify(arrived.Dir, arrived.Key, summary); err != nil {
		t.Fatalf("the module of the build that arrived during the collection is damaged: %v", err)
	}
	if arrived.Reused || calls.Load() != 2 {
		t.Fatalf("the build in the pause reused %v after %d emissions; want a fresh module", arrived.Reused, calls.Load())
	}
	program, err := link(t, hostToolchain(), arrived)
	if err != nil {
		t.Fatal(err)
	}
	if got := runProgram(t, program); got != expectedOutput(t, in, "r1", "unset") {
		t.Fatalf("the arrived module's program printed %q", got)
	}
}

// TestLeaseIsHeldPerBuild (round 2 review O2): a build holds its private
// root from Module until Release. Builds of one process share the lease and
// count their uses; the root is free only after the last release. A build
// whose emission fails, or panics, holds nothing when Module returns.
// Release is safe to repeat and never gives back another build's use.
func TestLeaseIsHeldPerBuild(t *testing.T) {
	if !leasesSupported {
		t.Skip("no flock here")
	}
	root := t.TempDir()
	c := Cache{Root: root}
	in := baseInput()
	in.Compiler = "process:" + strings.Repeat("9", 64)
	private := privateRootOf(root, in.Compiler)

	first, err := c.Module(in, emitFor(in, nil))
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Module(in, emitFor(in, nil))
	if err != nil {
		t.Fatal(err)
	}
	first.Release()
	first.Release() // a repeated release gives back nothing more
	if !heldLease(private) || collect(private) {
		t.Fatal("a root was freed while a build still holds it")
	}
	second.Release()
	if heldLease(private) {
		t.Fatal("the root is still held after every build released it")
	}

	failing := in
	failing.Sources = append(slices.Clone(in.Sources), Source{Path: "/src/app/c.ergo", Text: []byte("func failing() {}\n")})
	if _, err := c.Module(failing, func() ([]File, error) { return nil, errors.New("emission failed") }); err == nil {
		t.Fatal("a failing emission built a module")
	}
	if heldLease(private) {
		t.Fatal("a failed build still holds its root")
	}
	func() {
		defer func() { _ = recover() }()
		_, _ = c.Module(failing, func() ([]File, error) { panic("emission panicked") })
	}()
	if heldLease(private) {
		t.Fatal("a panicking build still holds its root")
	}
	age(t, private)
	if !collect(private) {
		t.Fatal("a root no build holds was not collected")
	}

	published := in
	published.Compiler = "sha256:compiler-1"
	m, err := c.Module(published, emitFor(published, nil))
	if err != nil {
		t.Fatal(err)
	}
	m.Release() // a published module holds its entry until Release
}
