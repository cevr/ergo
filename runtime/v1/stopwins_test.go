package v1

import (
	"fmt"
	"testing"

	"ergo.local/prototype/internal/testwait"
	"ergo.local/prototype/runtime/rt"
)

// TestStopWinsReleaseModel checks Gap 47 at the task boundary (#197 L8
// B4): a value the body returned is released exactly once, by the Wait
// that delivered it or by the owner's release (Close, then Drain), also
// when rt's stop-wins dropped it because a stop was pending when the body
// returned. A body that failed or was interrupted gives nothing. Every
// pair of body outcome and owner is one case; the reference is "released
// once when the body returned a value, else never". It runs in process:
// the release is the lowering's glue as a counting Go function.
//
// Mutants unique to it: the boundary keeps no value past stop-wins; a
// Drain gives the zero value of a body that failed or was interrupted.
func TestStopWinsReleaseModel(t *testing.T) {
	type outcome int
	const (
		returnsFirst outcome = iota // the body returns before any stop
		returnsAfter                // the body returns after its stop is requested
		fails                       // the body fails
		interrupted                 // the body waits until its stop and is interrupted
	)
	type consumer int
	const (
		release     consumer = iota // the owner's end releases the task
		waitFirst                   // the owner waits, then hands the value on
		waitStopped                 // the owner is stopped while it waits
	)
	names := []string{"returns first", "returns after its stop", "fails", "interrupted"}
	owners := []string{"release", "wait", "wait stopped"}
	for o := returnsFirst; o <= interrupted; o++ {
		for c := release; c <= waitStopped; c++ {
			if c == waitFirst && (o == returnsAfter || o == interrupted) {
				continue // a Wait would wait for a stop nobody sends
			}
			t.Run(fmt.Sprintf("%s/%s", names[o], owners[c]), func(t *testing.T) {
				released, delivered := 0, 0
				waiting := make(chan struct{})
				body := func(ct *rt.Task) (int, error) {
					switch o {
					case returnsAfter:
						<-ct.Done()
					case fails:
						return 0, bad{"fails"}
					case interrupted:
						sleepUntilStopped(ct)
					}
					return 1, nil
				}
				owner := func(root *rt.Task) error {
					return frame(root, func(s *Scope) error {
						h := s.Owner()
						task := Owning(h, Go(s, "child", body), func(c *Task[int]) {
							c.Close(s.current().s.Task())
							for range c.Drain() {
								released++
							}
						})
						switch c {
						case release:
							if o == returnsFirst || o == fails {
								if !testwait.Until(t, func() bool { return task.h.Task().Parent() == nil }) {
									t.Fatal("the child did not finish")
								}
							}
						case waitFirst:
							if _, err := task.Wait(); err == nil {
								Hand(h, task)
								delivered++
							} else if _, ok := rt.Failure(err); !ok {
								t.Errorf("Wait: %v", err)
							}
						case waitStopped:
							close(waiting)
							_, _ = task.Wait() // raises the interruption
						}
						return nil
					})
				}
				_ = runMain(func(root *rt.Task) error {
					if c != waitStopped {
						return owner(root)
					}
					// The owner runs on a task of its own, stopped once it is
					// about to wait (or waits): Wait then stops the child.
					s := NewScope(root)
					parent := Go(&s, "owner", func(pt *rt.Task) (struct{}, error) {
						return struct{}{}, owner(pt)
					})
					<-waiting
					parent.Stop()
					return nil
				})
				want := 0
				if o == returnsFirst || o == returnsAfter {
					want = 1
				}
				if got := released + delivered; got != want {
					t.Errorf("released %d, delivered %d; want %d release in all", released, delivered, want)
				}
			})
		}
	}
}
