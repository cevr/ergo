package v1

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// The direct release boundary must protect its first instruction, restore
// every enclosing mask, and carry failures, defects and stops intact. The
// frame-law tests already cover the registration and LIFO list itself.
func TestReleaseRestoresMaskAndCause(t *testing.T) {
	for _, depth := range []int{0, 1, 2, 9} {
		for _, outcome := range []string{"success", "failure", "defect", "stop"} {
			t.Run(fmt.Sprintf("depth%d/%s", depth, outcome), func(t *testing.T) {
				task := rt.Root("release")
				task.Interrupt()
				masks := make([]rt.Mask, depth)
				for i := range masks {
					masks[i] = task.Mask()
				}
				calls := 0
				p := recovered(func() {
					Release(task, func() {
						task.Check() // the first capability checkpoint belongs inside the mask
						calls++
						if !task.Masked() || task.Stopped() {
							t.Error("release observed the pending stop")
						}
						switch outcome {
						case "failure":
							raise(bad{"release"}) // typed-exit transport remains intact
						case "defect":
							panic("release defect")
						case "stop":
							raise(rt.Interrupted)
						}
					})
				})
				if calls != 1 {
					t.Errorf("release body ran %d times, want once", calls)
				}
				if task.Masked() != (depth > 0) || task.Stopped() != (depth == 0) {
					t.Errorf("release did not restore outer depth %d", depth)
				}
				for i := len(masks) - 1; i >= 0; i-- {
					masks[i].Unmask()
					if task.Masked() != (i > 0) {
						t.Errorf("mask %d did not restore its enclosing region", i)
					}
				}
				if !task.Stopped() {
					t.Error("release discarded the stop or leaked a mask into its caller")
				}
				if outcome == "success" {
					if p != nil {
						t.Errorf("successful release raised %v", p)
					}
					return
				}
				if p == nil {
					t.Fatal("release discarded its exit")
				}
				rs := reasons(capture(task, p))
				if len(rs) != 1 {
					t.Fatalf("release reasons = %+v, want one", rs)
				}
				switch outcome {
				case "failure":
					if rs[0].Kind != rt.KindFailure || rs[0].Err != (bad{"release"}) {
						t.Errorf("lost typed exit: %+v", rs)
					}
				case "defect":
					value, ok := rs[0].Value.(*Panic)
					if rs[0].Kind != rt.KindDefect || !rs[0].InRelease || !ok || value.Value != "release defect" || !strings.Contains(string(value.Stack), "TestReleaseRestoresMaskAndCause") {
						t.Errorf("lost release defect or its source: %+v", rs)
					}
				case "stop":
					if rs[0].Kind != rt.KindInterruption {
						t.Errorf("lost interruption: %+v", rs)
					}
				}
			})
		}
	}
}

func TestReleaseKeepsEarlierExit(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "defect", "stop"} {
		t.Run(outcome, func(t *testing.T) {
			var order []string
			err := runMain(func(task *rt.Task) error {
				return frame(task, func(s *Scope) error {
					OnExit(s, func() { order = append(order, "first") })
					OnExit(s, func() {
						Release(task, func() {
							order = append(order, "broken")
							panic("release defect")
						})
					})
					OnExit(s, func() { order = append(order, "last") })
					switch outcome {
					case "failure":
						return bad{"body"}
					case "defect":
						panic("body defect")
					case "stop":
						task.Interrupt()
					}
					return nil
				})
			})
			if got := strings.Join(order, " "); got != "last broken first" {
				t.Errorf("releases = %q, want last broken first, once each", got)
			}
			rs := reasons(err)
			want := 2
			if outcome == "success" {
				want = 1
			}
			if len(rs) != want {
				t.Fatalf("exit = %+v, want %d reasons", rs, want)
			}
			original, release := 0, 0
			for _, reason := range rs {
				if p, ok := reason.Value.(*Panic); ok && p.Value == "release defect" && reason.InRelease {
					release++
					continue
				}
				switch outcome {
				case "failure":
					if reason.Kind == rt.KindFailure && reason.Err == (bad{"body"}) {
						original++
					}
				case "defect":
					if p, ok := reason.Value.(*Panic); ok && p.Value == "body defect" && !reason.InRelease {
						original++
					}
				case "stop":
					if reason.Kind == rt.KindInterruption {
						original++
					}
				}
			}
			if release != 1 || (outcome != "success" && original != 1) {
				t.Errorf("original reasons = %d, release defects = %d; exit %v", original, release, err)
			}
		})
	}
}

type releaseScheduler struct {
	clock *rt.Virtual
	armed chan struct{}
}

func (s releaseScheduler) NewTimer(d time.Duration) rt.Timer {
	timer := s.clock.NewTimer(d)
	close(s.armed)
	return timer
}

// The stop is acknowledged inside the release before its next checkpoint
// and timer. Virtual time advances only after that timer is armed. The
// identical unmasked body must interrupt and must arm no timer.
func TestReleaseKeepsStopPending(t *testing.T) {
	for _, before := range []bool{true, false} {
		for _, masked := range []bool{true, false} {
			t.Run(fmt.Sprintf("before%t/masked%t", before, masked), func(t *testing.T) {
				task := rt.Root("release")
				clock := rt.NewVirtual(time.Time{})
				entered, stopped, armed := make(chan struct{}), make(chan struct{}), make(chan struct{})
				done := make(chan error, 1)
				completed := false
				if before {
					task.Interrupt()
				}
				go func() {
					_, err := guarded(task, func(task *rt.Task) (int, error) {
						body := func() {
							task.Check()
							close(entered)
							<-stopped
							task.Check()
							if err := rt.Sleep(task, releaseScheduler{clock, armed}, time.Hour); err != nil {
								raise(err)
							}
							completed = true
						}
						if masked {
							Release(task, body)
						} else {
							body()
						}
						task.Check() // the release's mask must not escape to this checkpoint
						return 0, nil
					})
					done <- err
				}()
				finished, resumed := false, false
				defer func() {
					task.Interrupt()
					if !resumed {
						close(stopped)
					}
					// Drain a timer armed by a worker released during a failing
					// assertion, before checking that no goroutine outlives it.
					if !finished {
						select {
						case <-armed:
							clock.Advance(time.Hour)
						case <-done:
							finished = true
						case <-testwait.After(t):
							t.Error("release did not reach its timer or end during cleanup")
							return
						}
						if !finished {
							select {
							case <-done:
							case <-testwait.After(t):
								t.Error("release did not drain")
							}
						}
					}
				}()
				if masked || !before {
					select {
					case <-entered:
					case err := <-done:
						finished = true
						t.Fatalf("release ended before the entry rendezvous: %v", err)
					case <-testwait.After(t):
						t.Fatal("release did not enter")
					}
				}
				task.Interrupt()
				close(stopped)
				resumed = true
				if masked {
					select {
					case <-armed:
						clock.Advance(time.Hour)
					case err := <-done:
						finished = true
						t.Fatalf("pending stop interrupted release: %v", err)
					case <-testwait.After(t):
						t.Fatal("release did not reach its masked timer")
					}
				}
				select {
				case err := <-done:
					finished = true
					if err != rt.Interrupted || completed != masked {
						t.Errorf("exit=%v completed=%t, want interruption and completed=%t", err, completed, masked)
					}
				case <-testwait.After(t):
					t.Fatal("release did not finish")
				}
				if clock.Pending() != 0 {
					t.Error("release left a timer armed")
				}
			})
		}
	}
}
