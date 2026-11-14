package eval

import (
	"crypto/sha256"
	"math/bits"
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The pure std functions of bits, sha256 and slices.Repeat (ORACLE O12),
// from Go's own packages as each std doc comment names them: bits.X is
// Go's math/bits.X, sha256.Sum the bytes of Go's sha256.Sum256 as a
// string, slices.Repeat Go's slices.Repeat, whose panic is the defect.
func init() {
	pure := func(name string, f func(args []Value) Value) {
		natives[name] = func(in *interp, fr *frame, at syntax.Node, fn *check.Func, args []Value) (Value, *abrupt) {
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
	u := func(v Value) uint64 { return v.(uint64) }
	pure("bits.OnesCount", func(a []Value) Value { return int64(bits.OnesCount64(u(a[0]))) })
	pure("bits.OnesCount64", func(a []Value) Value { return int64(bits.OnesCount64(u(a[0]))) })
	pure("bits.LeadingZeros", func(a []Value) Value { return int64(bits.LeadingZeros(uint(u(a[0])))) })
	pure("bits.LeadingZeros64", func(a []Value) Value { return int64(bits.LeadingZeros64(u(a[0]))) })
	pure("bits.TrailingZeros", func(a []Value) Value { return int64(bits.TrailingZeros(uint(u(a[0])))) })
	pure("bits.TrailingZeros64", func(a []Value) Value { return int64(bits.TrailingZeros64(u(a[0]))) })
	pure("bits.Len", func(a []Value) Value { return int64(bits.Len(uint(u(a[0])))) })
	pure("bits.Len64", func(a []Value) Value { return int64(bits.Len64(u(a[0]))) })
	pure("bits.RotateLeft32", func(a []Value) Value { return bits.RotateLeft32(a[0].(uint32), int(a[1].(int64))) })
	pure("bits.RotateLeft64", func(a []Value) Value { return bits.RotateLeft64(u(a[0]), int(a[1].(int64))) })
	pure("sha256.Sum", func(a []Value) Value {
		d := sha256.Sum256([]byte(a[0].(string)))
		return string(d[:])
	})
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
