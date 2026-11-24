//go:build unix

package v1

import (
	"bufio"
	"errors"
	"fmt"
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
// nothing in a normal run. It watches SIGINT and SIGTERM, says "ready",
// prints the code of the first stop it takes, unwatches, says
// "unwatched", and then waits on a queue nothing pushes to, so only the
// default action of a second stop ends it.
func TestSignalHelper(t *testing.T) {
	if os.Getenv(signalVariable) == "" {
		t.Skip("the child of TestSignalWatchTakesTheStop")
	}
	Main(func(root *rt.Task) error {
		q := NewOpen[int](8)
		n := SignalWatch(q, []int{1, 2})
		fmt.Println("ready")
		code := q.Pop(root)
		fmt.Println("got", code)
		SignalUnwatch(n)
		SignalUnwatch(n)
		fmt.Println("unwatched")
		_, err := New[int](0).Pop(root)
		return err
	})
}

// TestSignalWatchTakesTheStop is the host side of std signal: a watched
// SIGINT or SIGTERM reaches the queue as its code instead of ending the
// process, and after Unwatch the same signal ends the process again by
// its default action. It runs the test binary as a child because the
// subject is the process's own signal disposition, which a test must not
// change in its own process. The child runs in its own process group, and
// the test signals only that child.
func TestSignalWatchTakesTheStop(t *testing.T) {
	for _, c := range []struct {
		sig  syscall.Signal
		code string
	}{{syscall.SIGINT, "1"}, {syscall.SIGTERM, "2"}} {
		cmd := exec.Command(os.Args[0], "-test.run=^TestSignalHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), signalVariable+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
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
		t.Cleanup(func() { stop() })
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
	}
}
