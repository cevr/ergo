package rt

import (
	"testing"
	"testing/synctest"
)

// TestRestartOnReusesAFreeHandle holds RestartOn's guard
// (FU-handle-reuse): a child restarts in its last handle once that
// handle's job has returned on the carrier's goroutine, with no
// allocation, and in a new handle while the job still runs, since the
// end of a task touches its handle until its job returns. Mutants
// restart-never-reuses and restart-ignores-running are killed only here.
func TestRestartOnReusesAFreeHandle(t *testing.T) {
	if Exploring {
		t.Skip("under the explored scheduler RestartOn is Start")
	}
	synctest.Test(t, func(t *testing.T) {
		root := Root("restart")
		var c Carrier
		defer c.Close()
		body := funcBody[int](func(*Task) (int, error) { return 1, nil })
		h := RestartOn(nil, &c, root, "child", body)
		if _, err := h.Wait(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait() // the job has returned: the carrier waits
		again := RestartOn(h, &c, root, "child", body)
		if again != h {
			t.Errorf("a child after a returned job restarted in a new handle; want its last one")
		}
		if _, err := again.Wait(); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if n := testing.AllocsPerRun(20, func() {
			h = RestartOn(h, &c, root, "child", body)
			_, _ = h.Wait()
			synctest.Wait()
		}); n != 0 {
			t.Errorf("a restart in a free handle allocates %v times; want 0", n)
		}

		// A job that still runs: its handle is consumed (Stop), but the
		// task's end has not returned yet.
		release := make(chan struct{})
		held := RestartOn(nil, &c, root, "held", funcBody[int](func(*Task) (int, error) {
			<-release
			return 0, nil
		}))
		synctest.Wait()
		next := RestartOn(held, &c, root, "child", body)
		close(release)
		if next == held {
			t.Errorf("a child restarted in the handle of a job that still runs; want a new handle")
			return
		}
		if _, err := held.Wait(); err != nil {
			t.Fatal(err)
		}
		if _, err := next.Wait(); err != nil {
			t.Fatal(err)
		}
	})
}
