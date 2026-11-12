package eval

import (
	"cmp"
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// A for over a Map (ORACLE O14; types.md, Loops and len 1). Go leaves the
// order unspecified, so the evaluator gives no verdict that rests on one:
// a run that ranged over a Map runs again with every Map visited in the
// opposite order, and only an outcome both runs share is the outcome
// (Run, RunMain). An outcome that differs between them depends on the
// order: a skip that says so, never a verdict.
//
// Within a run, the loop follows Go's rules with no snapshot: it visits
// each key present at the start at most once, in the run's order (keys
// ascending, or descending in the second run), reading the entry's value
// when it is reached; an entry deleted before it is reached is not
// visited. Go may or may not visit an entry inserted during the loop, so
// a loop whose Map grows is a skip.

// mapOrder is the order a run visits a Map's keys in.
type mapOrder int

const (
	mapAscending mapOrder = iota
	mapDescending
)

// rangeMap runs s, a for over the Map dom, whose place is root and path
// (root nil for a Map no binding holds).
func (in *interp) rangeMap(fr *frame, s *syntax.RangeStmt, dom mapV, root *slot, path []step) *abrupt {
	in.mapRanged = true
	keys := make([]Value, 0, len(dom.b.m))
	for k := range dom.b.m {
		keys = append(keys, k)
	}
	if ab := in.charge(fr, s, int64(len(keys))); ab != nil {
		return ab
	}
	slices.SortFunc(keys, compareKeys)
	if in.mapOrder == mapDescending {
		slices.Reverse(keys)
	}
	n := len(keys)
	for _, k := range keys {
		cur := dom
		if root != nil {
			v, ab := in.read(fr, root.v, path)
			if ab != nil {
				return ab
			}
			m, ok := v.(mapV)
			if !ok {
				ice("a ranged Map's place holds %T", v)
			}
			cur = m
		}
		if len(cur.b.m) > n {
			return fr.unsupported(s, "a for over a Map that the loop adds to, whose new entries the Go target may or may not visit")
		}
		v, present := cur.b.m[k]
		if !present {
			n-- // deleted before it was reached: not visited
			continue
		}
		if ab := in.charge(fr, s, 1); ab != nil {
			return ab
		}
		if s.Key != nil {
			if ab := in.loopBind(fr, s.Key, k); ab != nil {
				return ab
			}
		}
		if root != nil && !basicValue(v) {
			root.lend() // the binding holds its own value
		}
		if ab := in.loopBind(fr, s.Value, v); ab != nil {
			return ab
		}
		if done, ab := loopBody(in.iteration(fr, s.Body)); done {
			return ab
		}
		if ab := in.backEdge(fr, s); ab != nil {
			return ab
		}
	}
	return nil
}

// compareKeys is a total order of Map keys: numbers by value, strings by
// bytes, false before true, and keys of different kinds (or of kinds with
// no order of their own) by their text.
func compareKeys(a, b Value) int {
	switch x := a.(type) {
	case int64:
		if y, ok := b.(int64); ok {
			return cmp.Compare(x, y)
		}
	case uint64:
		if y, ok := b.(uint64); ok {
			return cmp.Compare(x, y)
		}
	case int32:
		if y, ok := b.(int32); ok {
			return cmp.Compare(x, y)
		}
	case float64:
		if y, ok := b.(float64); ok {
			return cmp.Compare(x, y)
		}
	case string:
		if y, ok := b.(string); ok {
			return cmp.Compare(x, y)
		}
	case bool:
		if y, ok := b.(bool); ok {
			switch {
			case x == y:
				return 0
			case !x:
				return -1
			}
			return 1
		}
	}
	return cmp.Or(cmp.Compare(fmt.Sprintf("%T", a), fmt.Sprintf("%T", b)), cmp.Compare(describe(a), describe(b)))
}

// orderNote is the skip of an outcome the Map order decides.
const orderNote = "its outcome depends on the order of a for over a Map, which the Go target leaves unspecified"

// sameResult reports whether two runs of one obligation reached one
// outcome: the work each used may differ.
func sameResult(a, b Result) bool {
	if a.Name != b.Name || a.Status != b.Status || a.Reason != b.Reason || a.Site != b.Site || a.Case != b.Case || a.Cases != b.Cases {
		return false
	}
	return slices.Equal(a.Arguments, b.Arguments)
}

// sameExit reports whether two runs of a main reached one outcome.
func sameExit(a, b Exit) bool {
	return a.Stdout == b.Stdout && a.Stderr == b.Stderr && a.Status == b.Status && a.Skipped == b.Skipped && a.Site == b.Site && a.Incomplete == b.Incomplete
}
