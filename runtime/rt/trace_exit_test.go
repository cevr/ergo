package rt_test

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
	"ergo.local/prototype/runtime/rt/rttrace"
)

// callExits returns the call-exit records of call.
func callExits(recs []rttrace.Record, call uint64) []rttrace.Record {
	var out []rttrace.Record
	for _, r := range recs {
		if r.Kind == rt.TraceCallExit && r.Link == call {
			out = append(out, r)
		}
	}
	return out
}

// Every unwind boundary closes the calls it unwinds, with the outcome the
// recovery gives the panic: body or release, crossed with defect or
// interruption (round-2 M2, the reviewer's unwind probe). The exit keeps its
// classification.
func TestTraceUnwindClosesCalls(t *testing.T) {
	for _, mode := range []string{"body-defect", "body-interrupted", "release-defect", "release-interrupted"} {
		t.Run(mode, func(t *testing.T) {
			defect := mode == "body-defect" || mode == "release-defect"
			inRelease := mode == "release-defect" || mode == "release-interrupted"
			rec := rttrace.New(32)
			root := rt.TracedRoot("app.main", rec)
			var call uint64
			body := func(t0 *rt.Task) (struct{}, error) {
				call = t0.TraceCall("app.Cap.Op", "app.sketch:4:1")
				if defect {
					panic("provider defect")
				}
				t0.Interrupt()
				if inRelease {
					panic(rt.Interrupted) // a release is masked: Check does not fire
				}
				t0.Check()
				return struct{}{}, nil
			}
			_, err := rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
				if !inRelease {
					return body(t0)
				}
				return rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
					s.Defer(func() { _, _ = body(t0) })
					return struct{}{}, nil
				})
			})
			recs := records(t, rec)
			exits := callExits(recs, call)
			if len(exits) != 1 {
				t.Fatalf("%d call-exits of call %d; want 1: %+v", len(exits), call, recs)
			}
			ce := exits[0]
			if defect && (ce.Defect == nil || ce.Cause != nil) {
				t.Fatalf("call-exit %+v; want the defect", ce)
			}
			if !defect && (ce.Cause != rt.Interrupted || ce.Defect != nil) {
				t.Fatalf("call-exit %+v; want interrupted", ce)
			}
			if defect {
				if p, _ := find(recs, rt.TracePanic, root.ID()); p.Link != ce.ID || p.InRelease != inRelease {
					t.Fatalf("panic %+v; want a link to call-exit %d, inRelease %v", p, ce.ID, inRelease)
				}
			}
			c, isCause := err.(*rt.Cause)
			switch {
			case defect && (!isCause || len(c.Reasons) != 1 || c.Reasons[0].Kind != rt.KindDefect || c.Reasons[0].InRelease != inRelease):
				t.Fatalf("exit %v; want one defect, inRelease %v", err, inRelease)
			case !defect && err != rt.Interrupted:
				t.Fatalf("exit %v; want interrupted", err)
			}
		})
	}
}

// A release closes only the calls it opened: a defect in a release that runs
// inside an outer capability call leaves the outer call to its own exit.
func TestTraceReleaseUnwindKeepsOuterCall(t *testing.T) {
	rec := rttrace.New(32)
	root := rt.TracedRoot("app.main", rec)
	var outer, inner uint64
	_, _ = rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
		outer = t0.TraceCall("app.Outer.Op", "app.sketch:2:1")
		_, err := rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
			s.Defer(func() {
				inner = t0.TraceCall("app.Cap.Close", "app.sketch:3:1")
				panic("cleanup defect")
			})
			return struct{}{}, nil
		})
		t0.TraceCallExit(outer, "app.Outer.Op", "app.sketch:2:1", err)
		return struct{}{}, err
	})
	recs := records(t, rec)
	if ex := callExits(recs, inner); len(ex) != 1 || ex[0].Defect == nil {
		t.Fatalf("inner call-exits %+v; want one with the defect", ex)
	}
	ex := callExits(recs, outer)
	if len(ex) != 1 || ex[0].Defect != nil {
		t.Fatalf("outer call-exits %+v; want one, from the call's own exit", ex)
	}
	if _, ok := ex[0].Cause.(*rt.Cause); !ok {
		t.Fatalf("outer call-exit cause %v; want the scope's defect exit", ex[0].Cause)
	}
}

// A scope nested in a capability call recovers a defect of its body without
// closing the call that holds it.
func TestTraceScopeUnwindKeepsOuterCall(t *testing.T) {
	rec := rttrace.New(32)
	root := rt.TracedRoot("app.main", rec)
	var outer, inner uint64
	_, _ = rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
		outer = t0.TraceCall("app.Outer.Op", "app.sketch:2:1")
		_, err := rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
			inner = t0.TraceCall("app.Cap.Op", "app.sketch:3:1")
			panic("provider defect")
		})
		t0.TraceCallExit(outer, "app.Outer.Op", "app.sketch:2:1", err)
		return struct{}{}, err
	})
	recs := records(t, rec)
	if ex := callExits(recs, inner); len(ex) != 1 || ex[0].Defect == nil {
		t.Fatalf("inner call-exits %+v; want one with the defect", ex)
	}
	if ex := callExits(recs, outer); len(ex) != 1 || ex[0].Defect != nil {
		t.Fatalf("outer call-exits %+v; want one, from the call's own exit", ex)
	}
}

// failedCall records an ok call, then a call that fails with missing, and
// returns the failure and its call-exit.
func failedCall(t0 *rt.Task, recs func() []rttrace.Record) (error, func() uint64) {
	ok := t0.TraceCall("app.Store.Get", "app.sketch:3:2")
	t0.TraceCallExit(ok, "app.Store.Get", "app.sketch:3:2", nil)
	bad := t0.TraceCall("app.Store.Get", "app.sketch:4:2")
	t0.TraceCallExit(bad, "app.Store.Get", "app.sketch:4:2", missing{})
	return missing{}, func() uint64 { return callExits(recs(), bad)[0].ID }
}

// Each reason of a compound exit links its own origin (round-2 M1): a typed
// failure its failed call-exit, each cleanup defect its own panic, an
// interruption its cancel-request. A cleanup defect is never the origin of
// the failure.
func TestTraceCompoundExitLinksEachReason(t *testing.T) {
	type want struct {
		kind  rt.Kind
		event rt.EventKind // the kind the origin names
	}
	cases := []struct {
		name string
		body func(t0 *rt.Task, recs func() []rttrace.Record) (error, func() uint64)
		want []want
	}{
		{
			// The reviewer's native compound probe, through Scope.
			name: "failure-and-cleanup-defect",
			body: func(t0 *rt.Task, recs func() []rttrace.Record) (error, func() uint64) {
				var bad func() uint64
				_, err := rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
					s.Defer(func() { panic("cleanup defect") })
					var e error
					e, bad = failedCall(t0, recs)
					return struct{}{}, e
				})
				return err, bad
			},
			want: []want{{rt.KindFailure, rt.TraceCallExit}, {rt.KindDefect, rt.TracePanic}},
		},
		{
			name: "two-cleanup-defects",
			body: func(t0 *rt.Task, recs func() []rttrace.Record) (error, func() uint64) {
				_, err := rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
					s.Defer(func() { panic("first registered") })
					s.Defer(func() { panic(42) })
					return struct{}{}, nil
				})
				return err, nil
			},
			want: []want{{rt.KindDefect, rt.TracePanic}, {rt.KindDefect, rt.TracePanic}},
		},
		{
			name: "failure-two-cleanup-defects",
			body: func(t0 *rt.Task, recs func() []rttrace.Record) (error, func() uint64) {
				var bad func() uint64
				_, err := rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
					s.Defer(func() { panic("first registered") })
					s.Defer(func() { panic("second registered") })
					var e error
					e, bad = failedCall(t0, recs)
					return struct{}{}, e
				})
				return err, bad
			},
			want: []want{{rt.KindFailure, rt.TraceCallExit}, {rt.KindDefect, rt.TracePanic}, {rt.KindDefect, rt.TracePanic}},
		},
		{
			name: "failure-and-interruption",
			body: func(t0 *rt.Task, recs func() []rttrace.Record) (error, func() uint64) {
				var bad func() uint64
				_, err := rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
					t0.Interrupt()
					var e error
					e, bad = failedCall(t0, recs)
					return struct{}{}, e
				})
				return err, bad
			},
			want: []want{{rt.KindFailure, rt.TraceCallExit}, {rt.KindInterruption, rt.TraceCancelRequest}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := rttrace.New(64)
			root := rt.TracedRoot("app.main", rec)
			var bad func() uint64
			_, err := rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
				e, b := tc.body(t0, func() []rttrace.Record { rs, _ := rec.Records(); return rs })
				bad = b
				return struct{}{}, e
			})
			c, ok := err.(*rt.Cause)
			if !ok || len(c.Reasons) != len(tc.want) {
				t.Fatalf("exit %v; want %d reasons", err, len(tc.want))
			}
			recs := records(t, rec)
			ids := byID(t, recs)
			exit, _ := find(recs, rt.TraceBodyExit, root.ID())
			if len(exit.Origins) != len(c.Reasons) || exit.Link != 0 {
				t.Fatalf("body-exit origins %v, link %d; want one origin per reason of %v", exit.Origins, exit.Link, err)
			}
			seen := map[uint64]bool{}
			for i, w := range tc.want {
				r, o := c.Reasons[i], ids[exit.Origins[i]]
				if r.Kind != w.kind || o.Kind != w.event || seen[o.ID] {
					t.Fatalf("reason %d (%v) has origin %+v; want a distinct %v", i, r.Kind, o, w.event)
				}
				seen[o.ID] = true
				switch w.event {
				case rt.TraceCallExit:
					if o.ID != bad() {
						t.Fatalf("failure origin %d; want the failed call-exit %d, not the ok call", o.ID, bad())
					}
				case rt.TracePanic:
					if o.Defect != r.Value || !o.InRelease {
						t.Fatalf("defect %v has origin %+v; want its own release panic", r.Value, o)
					}
				}
			}
		})
	}
}
