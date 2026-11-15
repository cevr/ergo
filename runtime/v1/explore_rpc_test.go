//go:build ergoexplore

package v1

import (
	"math/rand/v2"
	"testing"
	"time"

	"ergo.local/prototype/runtime/rt"
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
			out = RPCExits("rpc explore test", root, envs, handle)
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
