package closure

import (
	"os"
	"strings"
	"time"
)

// A swept run is stopped only when it hangs: when it has made no progress
// for its quiet window, or at the end this test binary's own deadline
// leaves it. Progress is any growth of the run's output or of its test log,
// where the observer writes every access of every process of the run, or
// any CPU a process of the run uses (treeCPU, on Linux): a test that
// computes for its window with no output and no file access is working. A
// fixed bound on the whole run stopped cmd/ergo's runs, which take 58 to
// 61 minutes under a merge tier's load, at their 27 to 30 minute deadline
// though they were working (AGENTS.md: a deadline only stops a hung test).

// watchOutcome is how a watched run ended.
type watchOutcome int

const (
	// watchExited: the run ended by itself.
	watchExited watchOutcome = iota
	// watchQuiet: the run made no progress for its quiet window.
	watchQuiet
	// watchEnd: the run reached the end this test binary's deadline
	// leaves it, still making progress.
	watchEnd
)

// progressSlice is the longest wait between two looks at a run's progress.
const progressSlice = 10 * time.Second

// watch waits for a run. now is the clock, wait waits at most d for the
// run to end and says whether it did, and progress is a measure of the
// run's work that grows while it works.
type watch struct {
	quiet    time.Duration
	end      time.Time // zero: no end
	now      func() time.Time
	wait     func(d time.Duration) (bool, error)
	progress func() int64
}

// run waits until the run ends, makes no progress for w.quiet, or reaches
// w.end, and says which.
func (w watch) run() (watchOutcome, error) {
	last, seen := w.now(), w.progress()
	for {
		now := w.now()
		stop, why := last.Add(w.quiet), watchQuiet
		if !w.end.IsZero() && w.end.Before(stop) {
			stop, why = w.end, watchEnd
		}
		if !now.Before(stop) {
			return why, nil
		}
		exited, err := w.wait(min(stop.Sub(now), progressSlice))
		if err != nil || exited {
			return watchExited, err
		}
		if p := w.progress(); p != seen {
			seen, last = p, w.now()
		}
	}
}

// fileProgress is the summed size of the files: a run's output and its
// test log. A file not yet made counts nothing.
func fileProgress(files ...string) func() int64 {
	return func() int64 {
		var n int64
		for _, f := range files {
			if info, err := os.Stat(f); err == nil {
				n += info.Size()
			}
		}
		return n
	}
}

// testLogOf is the test log a run's arguments name, or "".
func testLogOf(args []string) string {
	for _, a := range args {
		if log, ok := strings.CutPrefix(a, "-test.testlogfile="); ok {
			return log
		}
	}
	return ""
}

// runEnd is the end of a run that starts now: runReportMargin before this
// test binary's own deadline, at least a second from now, or zero when the
// binary has no deadline.
func runEnd() time.Time {
	end, ok := binaryDeadline()
	if !ok {
		return time.Time{}
	}
	return maxTime(end.Add(-runReportMargin), time.Now().Add(time.Second))
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
