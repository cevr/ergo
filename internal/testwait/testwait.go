// Package testwait bounds a test's wait by the test's own deadline, never
// by a fixed time. A loaded gate host stretches an event that comes within
// milliseconds on a quiet host into seconds, so a fixed bound is a verdict
// of the host's load, not of the code (Test rule, wf10: "No fixed time
// limits (wait for events)"). A wait here ends at the event, or at End: a
// little before go test's -timeout panics, so the test still fails with its
// own message and the other tests of the binary still run.
//
// It is the one place of the repository where a test's wait may hold a
// duration constant (the poll interval and the report share); the residue
// wall-clock lint refuses one anywhere else.
package testwait

import (
	"context"
	"os/exec"
	"time"
)

// T is what a bound needs from a test: its deadline. *testing.T has it.
type T interface {
	Helper()
	Deadline() (time.Time, bool)
}

// reportShare is the most of the time left that End keeps for the report:
// a tenth of it, at most a minute.
const reportShare = time.Minute

// firstPoll and lastPoll bound the interval of Until: an event with no
// channel is sampled, first often, then each interval twice the last, up
// to a millisecond.
const (
	firstPoll = 10 * time.Microsecond
	lastPoll  = time.Millisecond
)

// End returns the time a wait of t must end by: the test's deadline less a
// tenth of the time left (at most a minute), which is left for the report
// before go test's own timeout panics. ok is false when the test has no
// deadline (-timeout=0): then only the event ends the wait.
func End(t T) (time.Time, bool) {
	t.Helper()
	deadline, ok := t.Deadline()
	if !ok {
		return time.Time{}, false
	}
	return deadline.Add(-min(time.Until(deadline)/10, reportShare)), true
}

// After returns a channel that fires at End(t). Without a deadline it is
// nil and never fires: the wait ends at its event only.
func After(t T) <-chan time.Time {
	t.Helper()
	end, ok := End(t)
	if !ok {
		return nil
	}
	return time.After(time.Until(end))
}

// AfterFunc calls f in its own goroutine at End(t), as a watchdog that
// ends a wait that would otherwise hang, unless stop is called first.
// Without a deadline f is never called. stop reports whether it stopped
// the call.
func AfterFunc(t T, f func()) (stop func() bool) {
	t.Helper()
	end, ok := End(t)
	if !ok {
		return func() bool { return true }
	}
	return time.AfterFunc(time.Until(end), f).Stop
}

// Left returns the time from now to End(t), at least a second, for a bound
// that a child process takes as a flag (go test's -timeout, a runner's
// -timeout). It is 0 when the test has no deadline: -timeout=0 asks for no
// bound, as the test was asked.
func Left(t T) time.Duration {
	t.Helper()
	end, ok := End(t)
	if !ok {
		return 0
	}
	return max(time.Until(end), time.Second)
}

// Context returns a child of parent that ends at End(t); without a
// deadline it ends only when cancelled.
func Context(t T, parent context.Context) (context.Context, context.CancelFunc) {
	t.Helper()
	end, ok := End(t)
	if !ok {
		return context.WithCancel(parent)
	}
	return context.WithDeadline(parent, end)
}

// Until samples cond until it holds, and reports whether it did before
// End(t). It is for an event with no channel to wait on (a file written by
// another process, a /proc entry gone).
func Until(t T, cond func() bool) bool {
	t.Helper()
	end, ok := End(t)
	for interval := firstPoll; !cond(); interval = min(2*interval, lastPoll) {
		if ok && !time.Now().Before(end) {
			return cond()
		}
		time.Sleep(interval)
	}
	return true
}

// WaitDelay sets cmd.WaitDelay to the time left to End(t): once the
// process has exited, Wait waits that long for its pipes to close. Without
// a deadline it waits for them as long as it takes (0).
func WaitDelay(t T, cmd *exec.Cmd) {
	t.Helper()
	cmd.WaitDelay = Left(t)
}
