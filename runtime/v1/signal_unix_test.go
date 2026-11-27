//go:build unix

package v1

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

const signalVariable = "ERGO_RUNTIME_V1_SIGNAL"

// TestSignalHelper is the child of TestSignalWatchTakesTheStop; it does
// nothing in a normal run. It watches SIGINT, SIGTERM, SIGUSR1, and SIGUSR2,
// says "ready", prints the code of the first stop it takes, and unwatches
// (twice to check idempotency). In "terminate" mode it then prints "unwatched"
// and waits on a queue nothing pushes to so a second SIGINT or SIGTERM ends
// it by default action. In "user" mode it transfers to a fresh watch on
// SIGUSR1/SIGUSR2 to verify Unwatch detached the first watch before unwatched
// no-op delivery, prints "rewatched <code>", unwatches that too, and exits
// cleanly after EOF on stdin once a third signal leaves queues empty ("quiet").
func TestSignalHelper(t *testing.T) {
	mode := os.Getenv(signalVariable)
	if mode == "" {
		t.Skip("the child of TestSignalWatchTakesTheStop")
	}
	Main(func(root *rt.Task) error {
		q := NewOpen[int](8)
		n := SignalWatch(q, []int{1, 2, 4, 5})
		fmt.Println("ready")
		code := q.Pop(root)
		fmt.Println("got", code)
		SignalUnwatch(n)
		SignalUnwatch(n)
		if mode != "user" {
			fmt.Println("unwatched")
			_, err := New[int](0).Pop(root)
			return err
		}
		q2 := NewOpen[int](8)
		n2 := SignalWatch(q2, []int{4, 5})
		fmt.Println("unwatched")
		code2 := q2.Pop(root)
		if stale, ok := q.Poll(); ok {
			return fmt.Errorf("unwatched queue still received signal %d", stale)
		}
		SignalUnwatch(n2)
		SignalUnwatch(n2)
		fmt.Println("rewatched", code2)
		var buf [1]byte
		for {
			if _, err := os.Stdin.Read(buf[:]); err != nil {
				break
			}
		}
		if stale, ok := q.Poll(); ok {
			return fmt.Errorf("first queue received signal %d after unwatch", stale)
		}
		if stale, ok := q2.Poll(); ok {
			return fmt.Errorf("second queue received signal %d after unwatch", stale)
		}
		fmt.Println("quiet")
		return nil
	})
}

// TestSignalNumberAndCodeMapping verifies SignalNumber, signalOfCode, and
// codeOfSignal round-trip across all five Stop signal codes (SIGINT, SIGTERM,
// SIGHUP, SIGUSR1, SIGUSR2).
func TestSignalNumberAndCodeMapping(t *testing.T) {
	cases := []struct {
		code int
		sig  syscall.Signal
	}{
		{1, syscall.SIGINT},
		{2, syscall.SIGTERM},
		{3, syscall.SIGHUP},
		{4, syscall.SIGUSR1},
		{5, syscall.SIGUSR2},
	}
	for _, c := range cases {
		if got := signalOfCode(c.code); got != c.sig {
			t.Errorf("signalOfCode(%d) = %v; want %v", c.code, got, c.sig)
		}
		if got := codeOfSignal(c.sig); got != c.code {
			t.Errorf("codeOfSignal(%v) = %d; want %d", c.sig, got, c.code)
		}
		if got := SignalNumber(c.code); got != int(c.sig) {
			t.Errorf("SignalNumber(%d) = %d; want %d", c.code, got, int(c.sig))
		}
	}
}

// TestSignalWatchTakesTheStop is the host side of std signal: a watched
// SIGINT, SIGTERM, SIGUSR1, or SIGUSR2 reaches the queue as its code instead
// of ending or bypassing the process, and after Unwatch the signal returns to
// Go's default disposition (ending the process for SIGINT/SIGTERM; no-op and
// detached from the old watch for SIGUSR1/SIGUSR2). It runs the test binary as
// a child because the subject is the process's own signal disposition, which a
// test must not change in its own process. The child runs in its own process
// group, and the test signals only that child.
func TestSignalWatchTakesTheStop(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		code string
		user bool
	}{
		{syscall.SIGINT, "1", false},
		{syscall.SIGTERM, "2", false},
		{syscall.SIGUSR1, "4", true},
		{syscall.SIGUSR2, "5", true},
	} {
		t.Run(c.sig.String(), func(t *testing.T) {
			mode := "terminate"
			if c.user {
				mode = "user"
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestSignalHelper$", "-test.count=1")
			cmd.Env = append(os.Environ(), signalVariable+"="+mode)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			var in io.Closer
			if c.user {
				pipe, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				in = pipe
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			group := cmd.Process.Pid
			// Only a hung child: the test's own deadline ends it and names it.
			stop := testwait.AfterFunc(t, func() {
				t.Errorf("%v: the child hung; its group is killed", c.sig)
				_ = syscall.Kill(-group, syscall.SIGKILL)
			})
			defer stop()
			lines := bufio.NewScanner(out)
			next := func() string {
				if !lines.Scan() {
					return "<end>"
				}
				return lines.Text()
			}
			if got := next(); got != "ready" {
				t.Fatalf("%v: first line %q; want ready", c.sig, got)
			}
			if err := syscall.Kill(group, c.sig); err != nil {
				t.Fatal(err)
			}
			if got := next(); got != "got "+c.code {
				t.Fatalf("%v: %q; want %q", c.sig, got, "got "+c.code)
			}
			if got := next(); got != "unwatched" {
				t.Fatalf("%v: %q; want unwatched", c.sig, got)
			}
			if err := syscall.Kill(group, c.sig); err != nil {
				t.Fatal(err)
			}
			if !c.user {
				for lines.Scan() {
				}
				err = cmd.Wait()
				var exit *exec.ExitError
				if !errors.As(err, &exit) {
					t.Fatalf("%v: after Unwatch the child ended with %v; want the signal's default action", c.sig, err)
				}
				status, _ := exit.Sys().(syscall.WaitStatus)
				if !status.Signaled() || status.Signal() != c.sig {
					t.Errorf("%v: after Unwatch: %s; want killed by %v", c.sig, strings.TrimSpace(exit.String()), c.sig)
				}
				return
			}
			if got := next(); got != "rewatched "+c.code {
				t.Fatalf("%v: %q; want %q", c.sig, got, "rewatched "+c.code)
			}
			if err := syscall.Kill(group, c.sig); err != nil {
				t.Fatal(err)
			}
			if err := in.Close(); err != nil {
				t.Fatal(err)
			}
			if got := next(); got != "quiet" {
				t.Fatalf("%v: %q; want quiet", c.sig, got)
			}
			for lines.Scan() {
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("%v: after final Unwatch the child ended with %v; want clean exit", c.sig, err)
			}
		})
	}
}
