//go:build ergoexplore

package rpc

import (
	"math/rand/v2"
	"sync/atomic"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
	v1 "ergo.local/prototype/runtime/v1"
)

// seeded is a strategy that picks among the enabled tasks by a seeded
// draw, so a seed replays its schedule.
type seeded struct{ r *rand.Rand }

func (s seeded) Pick(c rt.Choice) (int, error) { return s.r.IntN(len(c.Enabled)), nil }

// TestRPCCallEndWakesTheRequest is the explored form of rpc.md rule 6's
// request wait: under the controlled scheduler only enabled tasks run,
// and a task parked in the clock's domain is not enabled. So a call's end
// must unpark the request before the end is visible (rt.Wake in
// rpcRun.start); otherwise, in every order where the request parked
// before the call ended, no task is enabled and the run deadlocks. It
// catches rpc-end-unwoken, which no live-build test reaches: there the
// request's goroutine reads the end anyway and only the clock's count is
// wrong for a moment. Each seed is one schedule; a failing one names its
// seed, which replays it.
func TestRPCCallEndWakesTheRequest(t *testing.T) {
	envs, err := RPCParse(RPCRequest("0", "A", `{}`)+"\n"+RPCRequest("1", "B", `{}`)+"\n", true)
	if err != nil {
		t.Fatal(err)
	}
	handle := func(ct *rt.Task, tag, payload string) (string, error) { return payload, nil }
	for seed := uint64(0); seed < 64; seed++ {
		var out string
		res := rt.Explore(time.Unix(0, 0), seeded{rand.New(rand.NewPCG(seed, 1))}, func(root *rt.Task) error {
			out = RPCExits(v1.Site("rpc explore test"), root, envs, handle)
			return nil
		})
		if res.Deadlock != nil || res.Escape != "" || res.Exit != nil {
			t.Fatalf("seed %d: deadlock %v, escape %q, exit %v", seed, res.Deadlock, res.Escape, res.Exit)
		}
		if len(out) < 2 || out[0] != '[' {
			t.Fatalf("seed %d: RPCExits = %q", seed, out)
		}
	}
}

// TestRPCStopWinsOverAnEnd is the explored form of rpc.md rule 6's "a stop
// of the caller ends Call with the stop". A request has two calls: A ends
// at once, and B stops the request's task, then ends. In the schedules
// where both end before the request runs again, the request takes A's end
// and runs on, and at its next wait B's end and the stop are both ready.
// That is the order TestRPCCallsStopWithTheCaller reached about once in
// 4,000 runs under load (wf10 RPC-STOP). RPCExits must end with the stop:
// an answer would tell a stopped caller that its calls ran. It catches
// rpc-end-hides-stop, which takes an end while the stop is pending. Go's
// select picks one of the ready arms at random, so a seed with that order
// is red with even odds. The test counts the runs where both calls ended
// before the request answered anything (its emit saw both ends, or it
// emitted nothing), and wants at least 8 of them, so red is all but
// certain.
func TestRPCStopWinsOverAnEnd(t *testing.T) {
	envs, err := RPCParse(RPCRequest("0", "A", `{}`)+"\n"+RPCRequest("1", "B", `{}`)+"\n", true)
	if err != nil {
		t.Fatal(err)
	}
	both := 0
	for seed := uint64(0); seed < 64; seed++ {
		var got any
		var ended atomic.Int32
		var bothEnded, emitted bool
		res := rt.Explore(time.Unix(0, 0), seeded{rand.New(rand.NewPCG(seed, 1))}, func(root *rt.Task) error {
			holder := rt.NewScope(root)
			h := rt.Go(&holder, "rpc request", func(rq *rt.Task) (struct{}, error) {
				defer func() {
					got = recover()
					bothEnded = bothEnded || (!emitted && ended.Load() == 2)
				}()
				_ = RPCExitLines("rpc explore test", rq, envs, func(ct *rt.Task, tag, payload string) (string, error) {
					if tag == "B" {
						rq.Interrupt()
					}
					ended.Add(1)
					return payload, nil
				}, func(*rt.Task, string) error {
					bothEnded = bothEnded || ended.Load() == 2
					emitted = true
					return nil
				})
				return struct{}{}, nil
			})
			_, _ = h.Wait()
			return nil
		})
		if res.Deadlock != nil || res.Escape != "" {
			t.Fatalf("seed %d: deadlock %v, escape %q", seed, res.Deadlock, res.Escape)
		}
		if bothEnded {
			both++
		}
		if got != rt.Interrupted {
			t.Fatalf("seed %d: a stopped request's RPCExitLines ended with %v, want the stop (schedule %v)", seed, got, res.Schedule())
		}
	}
	if both < 8 {
		t.Fatalf("%d of 64 schedules ended both calls before the request ran on, want 8 or more", both)
	}
}
