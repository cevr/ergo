package eval

import (
	"cmp"
	"slices"
	"time"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// More std natives (ORACLE O16), each from its std doc comment and Go's
// own function: slices' WithCapacity (Go's make), SortStableFunc (Go's
// stable sort of a copy), largest (Go's slices.Max), arena's
// grow (Go's append, as the prelude's Append) and takeValue (the stored
// value, its cell cleared), and machines' notPositive (the defect of a
// timer that is not positive, in the runtime's text).
func registerNativesmore(t nativeTables) {
	natives := t.std
	t.reads["slices.WithCapacity"] = "int64"
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
	t.reads["slices.SortStableFunc"] = "sliceV func/2"
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
	t.reads["arena.grow"] = "sliceV T"
	natives["arena.grow"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		return in.appendTo(fr, at, args[0].(sliceV), args[1], nil)
	}
	t.reads["arena.takeValue"] = "mut:host:arena.stored"
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
	t.reads["machines.notPositive"] = "int64 int64"
	natives["machines.notPositive"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		now, wait := time.Duration(args[0].(int64)), time.Duration(args[1].(int64))
		return nil, fr.defect(at, "%s", "a Machine built from parts: the timer armed at "+now.String()+" has In "+wait.String()+", which is not positive; a timer waits a positive duration")
	}
	// std machines' firing mark (AfterFiring): set and cleared on the
	// running task, read by a machine's After (machinevalue.go,
	// everyTimeout).
	t.reads["machines.firingOn"] = ""
	natives["machines.firingOn"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		in.firing = true
		return unit{}, in.charge(fr, at, 1)
	}
	t.reads["machines.firingOff"] = ""
	natives["machines.firingOff"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		in.firing = false
		return unit{}, in.charge(fr, at, 1)
	}
}

// clearedCell is the value of an arena cell takeValue emptied: no live
// Ref reads it.
type clearedCell struct{}
