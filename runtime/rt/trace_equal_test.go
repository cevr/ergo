package rt_test

import (
	"testing"

	"ergo.local/prototype/runtime/rt"
	"ergo.local/prototype/runtime/rt/rttrace"
)

// keyed is a typed failure with a field, so each value is stored on its own
// when it becomes an error: two equal keyed values are two occurrences.
type keyed struct{ key string }

func (k keyed) Error() string { return "keyed " + k.key }

// key returns s built at run time. A keyed literal of constants is stored by
// the compiler in static data, one copy for every equal literal, so it would
// share its identity with every equal value; a run-time value is stored anew
// each time it becomes an error.
func key(s string) string { return string([]byte(s)) }

// equalFailures runs a traced root whose body produces two failures through
// make, accumulates both in one Cause in order, and returns the records, the
// exit, and the occurrence each reason came from (the call-exit or the
// child's body-exit), as the body observed it.
func equalFailures(t *testing.T, children bool, mk func() error) ([]rttrace.Record, error, [2]uint64) {
	t.Helper()
	rec := rttrace.New(64)
	root := rt.TracedRoot("app.main", rec)
	var from [2]uint64
	var tasks [2]uint64
	_, err := rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
		var c rt.Cause
		if !children {
			for i := range from {
				call := t0.TraceCall("app.Store.Get", "app.sketch:4:2")
				e := mk()
				t0.TraceCallExit(call, "app.Store.Get", "app.sketch:4:2", e)
				recs, _ := rec.Records()
				from[i] = callExits(recs, call)[0].ID
				c.Add(e)
			}
			return struct{}{}, c.Err()
		}
		_, err := rt.Scoped(t0, nil, func(s *rt.Scope) (struct{}, error) {
			var hs [2]*rt.Handle[struct{}]
			for i := range hs {
				hs[i] = rt.Go(s, "app.child", func(ct *rt.Task) (struct{}, error) {
					tasks[i] = ct.ID()
					return struct{}{}, mk()
				})
			}
			for _, h := range hs {
				_, e := h.Wait()
				c.Add(e)
			}
			return struct{}{}, c.Err()
		})
		return struct{}{}, err
	})
	recs := records(t, rec)
	if children {
		for i, task := range tasks {
			exit, _ := find(recs, rt.TraceBodyExit, task)
			from[i] = exit.ID
		}
	}
	return recs, err, from
}

// The second review's probes (round-3 M1): two failures enter one Cause in
// order. An origin names an occurrence, never an equal value. Equal values
// of a zero-size type are the same bits at the same address, so the runtime
// cannot tell which occurrence each reason came from and records none; a
// type whose values are stored on their own keeps each occurrence's
// identity, even when the two values are equal, and each reason names its
// own. No reason ever names another reason's occurrence.
func TestTraceEqualFailuresNameTheirOwnOccurrence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		children bool
		mk       func() error
		linked   bool // whether each reason can name its own occurrence
	}{
		{"two calls, zero-size", false, func() error { return missing{} }, false},
		{"two children, zero-size", true, func() error { return missing{} }, false},
		{"two calls, equal stored values", false, func() error { return keyed{key("same")} }, true},
		{"two children, equal stored values", true, func() error { return keyed{key("same")} }, true},
		{"two calls, equal constant literals", false, func() error { return keyed{"same"} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recs, err, from := equalFailures(t, tc.children, tc.mk)
			c, ok := err.(*rt.Cause)
			if !ok || len(c.Reasons) != 2 || c.Reasons[0].Kind != rt.KindFailure || c.Reasons[1].Kind != rt.KindFailure {
				t.Fatalf("exit %v; want two failures", err)
			}
			exit, _ := find(recs, rt.TraceBodyExit, recs[0].Task)
			if len(exit.Origins) != 2 {
				t.Fatalf("origins %v; want one per reason", exit.Origins)
			}
			for i, o := range exit.Origins {
				if o != 0 && o != from[i] {
					t.Fatalf("reason %d came from occurrence %d; recorded origin %d belongs to another reason (origins %v, sources %v)", i, from[i], o, exit.Origins, from)
				}
				if tc.linked && o != from[i] {
					t.Fatalf("reason %d: origin %d; want its own occurrence %d", i, o, from[i])
				}
				if !tc.linked && o != 0 {
					t.Fatalf("reason %d: origin %d; want none, since values with no storage of their own cannot be told apart", i, o)
				}
			}
		})
	}
}

// A lone zero-size failure still names its occurrence: one reason, one
// candidate. A failure the body returns without the occurrence it equals
// (a new value with its own storage) names none.
func TestTraceFailureOriginNeedsTheSameValue(t *testing.T) {
	for _, tc := range []struct {
		name   string
		raised error
		ret    func(raised error) error
		linked bool
	}{
		{"lone zero-size failure", missing{}, func(e error) error { return e }, true},
		{"the same stored value", keyed{key("a")}, func(e error) error { return e }, true},
		{"an equal value stored anew", keyed{key("a")}, func(error) error { return keyed{key("a")} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := rttrace.New(32)
			root := rt.TracedRoot("app.main", rec)
			var call uint64
			_, _ = rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
				call = t0.TraceCall("app.Store.Get", "app.sketch:4:2")
				t0.TraceCallExit(call, "app.Store.Get", "app.sketch:4:2", tc.raised)
				return struct{}{}, tc.ret(tc.raised)
			})
			recs := records(t, rec)
			ce := callExits(recs, call)[0]
			exit, _ := find(recs, rt.TraceBodyExit, root.ID())
			want := uint64(0)
			if tc.linked {
				want = ce.ID
			}
			if len(exit.Origins) != 1 || exit.Origins[0] != want {
				t.Fatalf("origins %v; want [%d]", exit.Origins, want)
			}
		})
	}
}

// Past the bound of unconsumed failures kept per task, the oldest is
// dropped, and no failure reason gets an origin: the dropped occurrence could
// have been its source.
func TestTraceDroppedFailureRecordsNoOrigin(t *testing.T) {
	rec := rttrace.New(256)
	root := rt.TracedRoot("app.main", rec)
	first := keyed{key("first")}
	_, _ = rt.Run(root, func(t0 *rt.Task) (struct{}, error) {
		var e error = first
		for i := 0; i < 20; i++ {
			call := t0.TraceCall("app.Store.Get", "app.sketch:4:2")
			t0.TraceCallExit(call, "app.Store.Get", "app.sketch:4:2", e)
			e = keyed{key("later")}
		}
		return struct{}{}, first
	})
	exit, _ := find(records(t, rec), rt.TraceBodyExit, root.ID())
	if len(exit.Origins) != 1 || exit.Origins[0] != 0 {
		t.Fatalf("origins %v; want [0] once an unconsumed failure was dropped", exit.Origins)
	}
}
