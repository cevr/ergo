//go:build ergoexplore

package v1

import (
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"ergo.local/prototype/runtime/rt"
)

// finishFirstChild runs the first child through its exit, then lets each
// other task keep the baton until it has a reason to give it up.
type finishFirstChild struct{}

func (finishFirstChild) Pick(c rt.Choice) (int, error) {
	for i, x := range c.Enabled {
		if x.Task == "0.0" {
			return i, nil
		}
	}
	return (rt.Cooperative{}).Pick(c)
}

// TestMailboxWaitsForItsOwnWake: an unrelated child's completion leaves
// a permit on its parent's task word. The parent next waits to receive or
// send on a mailbox. Only the peer's matching operation may end that
// wait. The peer waits for a quiescent bubble before doing its operation,
// so the parent's first physical park cannot race the legitimate wake.
func TestMailboxWaitsForItsOwnWake(t *testing.T) {
	take := func(m *mailbox[int], t *rt.Task) error {
		var got int
		if err := m.popInto(t, &got); err != nil {
			return err
		}
		if got != 1 {
			return fmt.Errorf("mailbox took %d, want 1", got)
		}
		return nil
	}
	put := func(m *mailbox[int], t *rt.Task) error { return m.Push(t, 1) }
	for _, tc := range []struct {
		name       string
		root, peer func(*mailbox[int], *rt.Task) error
	}{
		{"pop", take, put},
		{"push", put, take},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				res := rt.Explore(time.Unix(0, 0), finishFirstChild{}, func(t *rt.Task) (err error) {
					m := newMailbox[int](0, false)
					s := rt.NewScope(t)
					defer s.End(&err)
					rt.Go(&s, "unrelated child", func(*rt.Task) (struct{}, error) { return struct{}{}, nil })
					peer := rt.Go(&s, "mailbox peer", func(t *rt.Task) (struct{}, error) {
						synctest.Wait()
						return struct{}{}, tc.peer(m, t)
					})
					if err := tc.root(m, t); err != nil {
						return err
					}
					_, err = peer.Wait()
					return err
				})
				if res.Escape != "" || res.Exit != nil || res.Stopped != nil || res.Deadlock != nil {
					t.Fatalf("escape %q, exit %v, stopped %v, deadlock %v, schedule %v",
						res.Escape, res.Exit, res.Stopped, res.Deadlock, res.Schedule())
				}
			})
		})
	}
}
