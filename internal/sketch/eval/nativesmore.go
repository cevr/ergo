package eval

import (
	"cmp"
	"math"
	"slices"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// More std natives (ORACLE O16), each from its std doc comment and Go's
// own function: slices' WithCapacity (Go's make), SortStableFunc (Go's
// stable sort of a copy), largest (Go's slices.Max), math.Trunc, arena's
// grow (Go's append, as the prelude's Append) and takeValue (the stored
// value, its cell cleared), and machines' notPositive (the defect of a
// timer that is not positive, in the runtime's text).
func init() {
	natives["slices.WithCapacity"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		n := args[0].(int64)
		if n < 0 {
			return nil, fr.defect(at, "runtime error: makeslice: cap out of range")
		}
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		return newSlice(nil), nil
	}
	natives["slices.SortStableFunc"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs := args[0].(sliceV).elems()
		if ab := in.build(fr, at, int64(len(xs)), int64(len(xs)), 0); ab != nil {
			return nil, ab
		}
		out := slices.Clone(xs)
		var stop *abrupt
		slices.SortStableFunc(out, func(a, b Value) int {
			if stop != nil {
				return 0
			}
			v, ab := in.callValue(fr, at, args[1], []Value{a, b})
			if ab != nil {
				stop = ab
				return 0
			}
			return cmp.Compare(v.(int64), 0)
		})
		if stop != nil {
			return nil, stop
		}
		return newSlice(out), nil
	}
	natives["slices.largest"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs := args[0].(sliceV).elems()
		if ab := in.charge(fr, at, 1+int64(len(xs))); ab != nil {
			return nil, ab
		}
		if len(xs) == 0 {
			return nil, fr.defect(at, "slices.Max: empty list")
		}
		// Go's slices.Max folds Go's max over the list: a float's NaN
		// anywhere gives NaN, and +0 beats -0.
		best := xs[0]
		for _, x := range xs[1:] {
			switch b := best.(type) {
			case float64:
				best = max(b, x.(float64))
			case float32:
				best = max(b, x.(float32))
			default:
				if less, _, _ := arith(syntax.LSS, best, x); less.(bool) {
					best = x
				}
			}
		}
		return best, nil
	}
	natives["math.Trunc"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		if ab := in.charge(fr, at, 1); ab != nil {
			return nil, ab
		}
		return math.Trunc(args[0].(float64)), nil
	}
	natives["arena.grow"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return in.appendTo(fr, at, args[0].(sliceV), args[1], nil)
	}
	natives["arena.takeValue"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		p, ok := args[0].(*ptrV)
		if !ok {
			ice("arena.takeValue of %T", args[0])
		}
		cur, ab := in.read(fr, p.root.v, p.path)
		if ab != nil {
			return nil, ab
		}
		cell, ok := cur.(*record)
		if !ok {
			ice("an arena cell is %T", cur)
		}
		i := fieldIndex(cell.t, "Value")
		v := cell.fields[i]
		cleared := &record{t: cell.t, fields: slices.Clone(cell.fields)}
		cleared.fields[i] = clearedCell{}
		nv, ab := in.storePath(fr, p.root, p.root.v, p.path, 0, cleared)
		if ab != nil {
			return nil, ab
		}
		p.root.v = nv
		return v, nil
	}
	natives["machines.notPositive"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		now, wait := time.Duration(args[0].(int64)), time.Duration(args[1].(int64))
		return nil, fr.defect(at, "%s", "a Machine built from parts: the timer armed at "+now.String()+" has In "+wait.String()+", which is not positive; a timer waits a positive duration")
	}
}

// clearedCell is the value of an arena cell takeValue emptied: no live
// Ref reads it.
type clearedCell struct{}
