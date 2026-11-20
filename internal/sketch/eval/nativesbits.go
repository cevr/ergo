package eval

import (
	"math/bits"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// slices.Repeat (ORACLE O12), Go's slices.Repeat, whose panic is the
// defect; and Go's math/bits, which std bits binds through import go
// (goNatives). std sha256 calls Go's Sum256 through import go (NP4f-5;
// nativesgo.go).
func registerNativesbits(t nativeTables) {
	natives := t.std
	goNatives := t.goLib
	pureIn := func(into map[string]native, name string, f func(args []Value) Value) {
		into[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
			if ab := in.charge(fr, at, 1+argWork(args)); ab != nil {
				return nil, ab
			}
			out, why := guard(func() Value { return f(args) })
			if why != "" {
				return nil, fr.defect(at, "%s", why)
			}
			if ab := in.allocate(fr, at, valueSize(out)); ab != nil {
				return nil, ab
			}
			return out, nil
		}
	}
	goPure := func(name string, f func(args []Value) Value) { pureIn(goNatives, "math/bits."+name, f) }
	u := func(v Value) uint64 { return v.(uint64) }
	t.reads["math/bits.OnesCount"] = "uint64"
	goPure("OnesCount", func(a []Value) Value { return int64(bits.OnesCount(uint(u(a[0])))) })
	t.reads["math/bits.OnesCount64"] = "uint64"
	goPure("OnesCount64", func(a []Value) Value { return int64(bits.OnesCount64(u(a[0]))) })
	t.reads["math/bits.LeadingZeros"] = "uint64"
	goPure("LeadingZeros", func(a []Value) Value { return int64(bits.LeadingZeros(uint(u(a[0])))) })
	t.reads["math/bits.LeadingZeros64"] = "uint64"
	goPure("LeadingZeros64", func(a []Value) Value { return int64(bits.LeadingZeros64(u(a[0]))) })
	t.reads["math/bits.TrailingZeros"] = "uint64"
	goPure("TrailingZeros", func(a []Value) Value { return int64(bits.TrailingZeros(uint(u(a[0])))) })
	t.reads["math/bits.TrailingZeros64"] = "uint64"
	goPure("TrailingZeros64", func(a []Value) Value { return int64(bits.TrailingZeros64(u(a[0]))) })
	t.reads["math/bits.Len"] = "uint64"
	goPure("Len", func(a []Value) Value { return int64(bits.Len(uint(u(a[0])))) })
	t.reads["math/bits.Len64"] = "uint64"
	goPure("Len64", func(a []Value) Value { return int64(bits.Len64(u(a[0]))) })
	t.reads["math/bits.RotateLeft32"] = "uint32 int64"
	goPure("RotateLeft32", func(a []Value) Value { return bits.RotateLeft32(a[0].(uint32), int(a[1].(int64))) })
	t.reads["math/bits.RotateLeft64"] = "uint64 int64"
	goPure("RotateLeft64", func(a []Value) Value { return bits.RotateLeft64(u(a[0]), int(a[1].(int64))) })
	t.reads["slices.Repeat"] = "sliceV int64"
	natives["slices.Repeat"] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
		xs, n := args[0].(sliceV), args[1].(int64)
		// The work and the bytes are paid before the copy; a count Go
		// refuses is its panic, below.
		if n > 0 && int64(xs.n) > 0 {
			if n > (1<<40)/int64(xs.n) {
				return nil, fr.unsupported(at, "a slices.Repeat past the evaluator's budget")
			}
			if ab := in.build(fr, at, 1+n*int64(xs.n), n*int64(xs.n), 0); ab != nil {
				return nil, ab
			}
		}
		out, why := guard(func() Value { return newSlice(slices.Repeat(xs.elems(), int(n))) })
		if why != "" {
			return nil, fr.defect(at, "%s", why)
		}
		return out, nil
	}
}
