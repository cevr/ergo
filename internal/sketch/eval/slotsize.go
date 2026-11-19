package eval

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goSize is what one value of r takes in the Go target (64-bit), the
// bytes the byte budget charges for one slot of a collection of r: the
// budget measures what the built program allocates, so a [65536]int32
// costs 256 KiB, not 65536 interface slots (MX-EVSLOT). A type whose
// layout the evaluator does not read (an interface, a union, a type
// parameter, a function) is two words, valueBytes. At least 1, and never
// more than 1<<56, so a product of counts still fits.
func goSize(r rtype) int64 {
	size, _ := goLayout(r)
	return max(size, 1)
}

// goLayout is r's Go size and alignment.
func goLayout(r rtype) (size, align int64) {
	const most = 1 << 56
	r = r.env.resolve(r.t)
	if b := r.basic(); b != nil {
		switch b.Kind {
		case check.Bool, check.Int8, check.Uint8:
			return 1, 1
		case check.Int16, check.Uint16:
			return 2, 2
		case check.Int32, check.Uint32, check.Float32:
			return 4, 4
		case check.String, check.Complex128:
			return 16, 8
		}
		return 8, 8
	}
	u := r.under()
	switch t := u.t.(type) {
	case *check.Slice:
		return 24, 8
	case *check.Map, *check.Pointer:
		return 8, 8
	case *check.Array:
		each, a := goLayout(rtype{t.Elem, u.env})
		if t.Len > 0 && each > most/t.Len {
			return most, a
		}
		return t.Len * each, a
	case *check.Struct:
		var fields []rtype
		for _, f := range t.Fields {
			fields = append(fields, rtype{f.Type, u.env})
		}
		return goFields(fields)
	case *check.Tuple:
		var fields []rtype
		for _, e := range t.Types {
			fields = append(fields, rtype{e, u.env})
		}
		return goFields(fields)
	}
	return valueBytes, 8
}

// goFields is the Go layout of a struct of fields in order: each field at
// its alignment, the whole rounded up to the largest.
func goFields(fields []rtype) (size, align int64) {
	const most = 1 << 56
	align = 1
	for _, f := range fields {
		s, a := goLayout(f)
		size = (size + a - 1) / a * a
		size += s
		align = max(align, a)
		if size >= most {
			return most, align
		}
	}
	return (size + align - 1) / align * align, align
}

// slotBytes is what one slot of b's store costs of the byte budget: its
// element type's Go size when the store was made from a checked type,
// else valueBytes.
func (b *backing) slotBytes() int64 {
	if b.elem > 0 {
		return b.elem
	}
	return valueBytes
}

// buildSlots charges both budgets of a store of n slots of elem bytes
// each (build's form for a typed store).
func (in *interp) buildSlots(fr *frame, at syntax.Node, steps, n, elem int64) *abrupt {
	if n > 0 && elem > (1<<62)/n {
		if ab := in.charge(fr, at, steps); ab != nil {
			return ab
		}
		return in.allocate(fr, at, -1)
	}
	return in.build(fr, at, steps, 0, n*elem)
}
