package closure

import (
	"testing"
	"time"
)

// fakeRun is a run on a clock the watch drives: it works (its progress
// grows each time it is read) until workUntil, and ends by itself at
// exitAt (never when zero).
type fakeRun struct {
	t                 time.Duration
	workUntil, exitAt time.Duration
	reads             int64
}

func (f *fakeRun) watch(quiet, end time.Duration) watch {
	base := time.Unix(0, 0)
	w := watch{
		quiet: quiet,
		now:   func() time.Time { return base.Add(f.t) },
		wait: func(d time.Duration) (bool, error) {
			if f.exitAt != 0 && f.t+d >= f.exitAt {
				f.t = f.exitAt
				return true, nil
			}
			f.t += d
			return false, nil
		},
		progress: func() int64 {
			if f.t < f.workUntil {
				f.reads++
			}
			return f.reads
		},
	}
	if end != 0 {
		w.end = base.Add(end)
	}
	return w
}

// TestWatchStopsOnlyAQuietRun: the sweep stops a run that has made no
// progress for its quiet window, and never one that keeps working, however
// long it takes, until the end its test binary's deadline leaves. Mutants:
// progress does not move the window (closure-hang-progress-ignored, the
// fixed deadline this replaced: a run working past one window is
// stopped), the end is ignored (closure-hang-end-ignored), the quiet
// window is ignored (closure-hang-quiet-ignored).
func TestWatchStopsOnlyAQuietRun(t *testing.T) {
	const q = 30 * time.Minute
	cases := []struct {
		name    string
		run     fakeRun
		end     time.Duration
		want    watchOutcome
		atLeast time.Duration
		atMost  time.Duration
	}{
		// cmd/ergo under load: an hour of work, twice its window.
		{"works past its window, then ends", fakeRun{workUntil: 61 * time.Minute, exitAt: 61 * time.Minute}, 0, watchExited, 61 * time.Minute, 61 * time.Minute},
		{"works, then hangs", fakeRun{workUntil: 40 * time.Minute}, 0, watchQuiet, 40*time.Minute + q - progressSlice, 40*time.Minute + q},
		{"hangs at once", fakeRun{}, 0, watchQuiet, q, q},
		{"works to its end", fakeRun{workUntil: 10 * time.Hour}, 3 * q, watchEnd, 3 * q, 3 * q},
		{"quiet, then ends by itself", fakeRun{exitAt: q / 2}, 0, watchExited, q / 2, q / 2},
	}
	for _, c := range cases {
		run := c.run
		got, err := run.watch(q, c.end).run()
		if err != nil || got != c.want || run.t < c.atLeast || run.t > c.atMost {
			t.Errorf("%s: outcome %d at %v (%v); want %d in [%v, %v]", c.name, got, run.t, err, c.want, c.atLeast, c.atMost)
		}
	}
}
