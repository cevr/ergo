package v1

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"testing"
	"time"

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
	t.Run("boundaries", testBoundaryReleases)
}

// blockKind is what one generated block of a boundary does.
type blockKind int

const (
	valueNow  blockKind = iota // returns its value at once
	failsNow                   // fails at once
	valueLate                  // returns its value once its stop is requested
	waitsStop                  // waits until its stop and is interrupted
)

// testBoundaryReleases checks Gap 47 at the std boundaries that drop a
// successful value (#197 L8 B4): tasks.Race (its losers), tasks.Transform
// (the finished results when another call fails) and timeout.After (a
// value the deadline or a stop dropped). Seeded schedules of blocks; the
// reference is the conservation law: the values the boundary gives and the
// values its hidden release receives are the values the blocks returned,
// each exactly once. A failed or interrupted block returned none, so no
// zero value is released. The release counts in process.
//
// Mutants unique to it: a Race loser is not released; Transform's
// finished results are not released when another call fails; After drops
// a returned value; the zero value of a failed exit is released.
func testBoundaryReleases(t *testing.T) {
	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 47))
		n := 1 + r.IntN(4)
		kinds := make([]blockKind, n)
		for i := range kinds {
			kinds[i] = blockKind(r.IntN(4))
		}
		var mu sync.Mutex
		var returned, released []int
		rel := func(_ *rt.Task, v int) {
			mu.Lock()
			released = append(released, v)
			mu.Unlock()
		}
		// block runs block i (value i+1): it records a value it returns.
		block := func(ct *rt.Task, i int) (int, error) {
			switch kinds[i] {
			case failsNow:
				return 0, bad{"fails"}
			case valueLate:
				<-ct.Done()
			case waitsStop:
				sleepUntilStopped(ct)
			}
			mu.Lock()
			returned = append(returned, i+1)
			mu.Unlock()
			return i + 1, nil
		}
		ends := func(k blockKind) bool { return k == valueNow || k == failsNow }
		var given []int
		var what string
		switch seed % 3 {
		case 0:
			what = "Race"
			if !slices.ContainsFunc(kinds, ends) {
				kinds[0] = valueNow // a Race of blocks that never end waits for ever
			}
			v, err := raceIndexed(Site("race"), rt.Root("race"), rel, n, block)
			if err == nil {
				given = []int{v}
			}
		case 1:
			what = "Transform"
			if slices.ContainsFunc(kinds, func(k blockKind) bool { return !ends(k) }) {
				// A block that waits for its stop needs a failure that
				// starts while it holds a slot of the limit: the first.
				kinds[0] = failsNow
			}
			idx := make([]int, n)
			for i := range idx {
				idx[i] = i
			}
			out, err := TasksTransform(Site("transform"), rt.Root("transform"), func(ct *rt.Task, vs []int) {
				for _, v := range vs {
					rel(ct, v)
				}
			}, idx, 1+r.IntN(3), block)
			if err == nil {
				given = out
			}
		default:
			what = "After"
			s := newVirtualSleeper()
			if !ends(kinds[0]) {
				go func() {
					s.v.AwaitTimers(1)
					s.v.Advance(time.Hour)
				}()
			}
			_ = runMain(func(root *rt.Task) error {
				v, err := After(Site("after"), root, s, rel, time.Hour, func(ct *rt.Task) (int, error) { return block(ct, 0) })
				if err == nil {
					given = []int{v}
				}
				return nil
			})
			kinds = kinds[:1]
		}
		got := slices.Sorted(slices.Values(append(slices.Clone(given), released...)))
		want := slices.Sorted(slices.Values(returned))
		if !slices.Equal(got, want) {
			t.Fatalf("seed %d: %s of %v gave %v and released %v; the blocks returned %v", seed, what, kinds, given, released, returned)
		}
	}
}
