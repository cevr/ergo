package examples_test

import (
	"slices"
	"sync/atomic"
	"testing"

	"ergo.local/prototype/prototypes/actorlib"
	"ergo.local/prototype/prototypes/actorlib/examples"
	"ergo.local/prototype/runtime/rt"
)

func within(t *testing.T, body func(task *rt.Task, s *rt.Scope) error) {
	t.Helper()
	_, err := rt.Run(rt.Root(t.Name()), func(task *rt.Task) (struct{}, error) {
		return rt.Scoped(task, nil, func(s *rt.Scope) (struct{}, error) {
			return struct{}{}, body(task, s)
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

func failure[E error](err error) (E, bool) {
	f, ok := rt.Failure(err)
	if !ok {
		var zero E
		return zero, false
	}
	e, ok := f.(E)
	return e, ok
}

func TestCounterKeepsMailboxOrder(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		c := examples.StartCounter(s, 4) // smaller than the burst: senders wait
		for range 1000 {
			if err := c.Send(task, examples.Inc{By: 1}); err != nil {
				return err
			}
		}
		n, err := examples.Count(task, c.Address())
		if err != nil {
			return err
		}
		if n != 1000 {
			t.Errorf("count: got %d, want 1000", n)
		}
		return c.Stop()
	})
}

func TestRosterRepliesOfSeveralTypes(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		room := examples.StartRoom(s, 2, 16).Address()
		for i, name := range []string{"ada", "grace"} {
			n, err := examples.JoinRoom(task, room, name)
			if err != nil || n != int64(i+1) {
				t.Errorf("join %s: got %d, %v", name, n, err)
			}
		}
		_, err := examples.JoinRoom(task, room, "edsger")
		if full, ok := failure[examples.RoomFull](err); !ok || full.Limit != 2 {
			t.Errorf("join over the limit: got %v, want RoomFull{2}", err)
		}
		members, err := examples.RoomMembers(task, room)
		if err != nil || !slices.Equal(members, []string{"ada", "grace"}) {
			t.Errorf("members: got %v, %v", members, err)
		}
		members[0] = "mallory" // a snapshot: the room keeps its own names
		if n, err := examples.LeaveRoom(task, room, "ada"); err != nil || n != 1 {
			t.Errorf("leave: got %d, %v", n, err)
		}
		if members, _ := examples.RoomMembers(task, room); !slices.Equal(members, []string{"grace"}) {
			t.Errorf("members after leave: got %v", members)
		}
		return nil
	})
}

func TestRosterAgentMatchesTheHandlerActor(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		room := examples.StartRoomAgent(s, 2, 16).Address()
		for i, name := range []string{"ada", "grace"} {
			n, err := examples.AgentJoin(task, room, name)
			if err != nil || n != int64(i+1) {
				t.Errorf("join %s: got %d, %v", name, n, err)
			}
		}
		if _, err := examples.AgentJoin(task, room, "edsger"); err == nil {
			t.Errorf("join over the limit succeeded")
		} else if full, ok := failure[examples.RoomFull](err); !ok || full.Limit != 2 {
			t.Errorf("join over the limit: got %v, want RoomFull{2}", err)
		}
		members, err := examples.AgentMembers(task, room)
		if err != nil || !slices.Equal(members, []string{"ada", "grace"}) {
			t.Errorf("members: got %v, %v", members, err)
		}
		members[0] = "mallory"
		if n, err := examples.AgentLeave(task, room, "ada"); err != nil || n != 1 {
			t.Errorf("leave: got %d, %v", n, err)
		}
		if members, _ := examples.AgentMembers(task, room); !slices.Equal(members, []string{"grace"}) {
			t.Errorf("members after leave: got %v", members)
		}
		return nil
	})
}

// fakeProbe is a Probe provider whose endpoint is down until up is set.
type fakeProbe struct {
	up     atomic.Bool
	checks atomic.Int32
}

func (p *fakeProbe) Check(_ *rt.Task, endpoint string) error {
	p.checks.Add(1)
	if !p.up.Load() {
		return examples.ProbeDown{Endpoint: endpoint}
	}
	return nil
}

func TestBreakerMachineActor(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		probe := &fakeProbe{}
		b := examples.StartBreaker(s, probe, 16)
		addr := b.Address()
		for range examples.Trip {
			if err := b.Send(task, examples.Failure{Endpoint: "pay"}); err != nil {
				return err
			}
		}
		if st, err := b.State(task); err != nil || st != (examples.Open{Endpoint: "pay"}) {
			t.Errorf("state after %d failures: got %v, %v", examples.Trip, st, err)
		}
		// A step failure goes to the asker and leaves the state unchanged.
		if _, err := b.Ask(task, examples.Retry{}); err == nil {
			t.Errorf("retry with the probe down succeeded")
		} else if _, ok := failure[examples.ProbeDown](err); !ok {
			t.Errorf("retry with the probe down: got %v, want ProbeDown", err)
		}
		d, err := examples.RetryOrReject(task, addr)
		if _, ok := d.(actorlib.Reject[examples.BreakerState, string]); err != nil || !ok {
			t.Errorf("caught retry: got %#v, %v; want Reject", d, err)
		}
		if st, _ := b.State(task); st != (examples.Open{Endpoint: "pay"}) {
			t.Errorf("state after a failed probe: got %v", st)
		}
		probe.up.Store(true)
		d, err = b.Ask(task, examples.Retry{})
		if e, ok := d.(actorlib.Enter[examples.BreakerState, string]); err != nil || !ok || e.Next != (examples.HalfOpen{Endpoint: "pay"}) {
			t.Errorf("retry with the probe up: got %#v, %v", d, err)
		}
		if _, err := b.Ask(task, examples.Success{}); err != nil {
			return err
		}
		if st, _ := b.State(task); st != (examples.Closed{}) {
			t.Errorf("state after success: got %v", st)
		}
		d, err = b.Ask(task, examples.Retire{})
		if done, ok := d.(actorlib.Done[examples.BreakerState, string]); err != nil || !ok || done.Output != "retired" {
			t.Errorf("retire: got %#v, %v", d, err)
		}
		if err := b.Send(task, examples.Success{}); err == nil {
			t.Errorf("Send after Done succeeded")
		} else if _, ok := failure[actorlib.Stopped](err); !ok {
			t.Errorf("Send after Done: got %v, want Stopped", err)
		}
		if err := b.Stop(); err != nil {
			t.Errorf("Stop after Done: %v", err)
		}
		if n := probe.checks.Load(); n != 3 {
			t.Errorf("probe checks: got %d, want 3", n)
		}
		return nil
	})
}

func TestBreakerSendFailureEndsTheActor(t *testing.T) {
	within(t, func(task *rt.Task, s *rt.Scope) error {
		b := examples.StartBreaker(s, &fakeProbe{}, 16)
		for range examples.Trip {
			if err := b.Send(task, examples.Failure{Endpoint: "pay"}); err != nil {
				return err
			}
		}
		// No asker observes this step failure, so it ends the actor.
		if err := b.Send(task, examples.Retry{}); err != nil {
			return err
		}
		_, err := b.State(task)
		if _, ok := failure[actorlib.Stopped](err); !ok {
			t.Errorf("State after a terminal failure: got %v, want Stopped", err)
		}
		if down, ok := failure[examples.ProbeDown](b.Stop()); !ok || down.Endpoint != "pay" {
			t.Errorf("Stop: want the ProbeDown that ended the actor")
		}
		return nil
	})
}
