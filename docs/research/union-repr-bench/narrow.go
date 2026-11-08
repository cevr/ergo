package unionreprbench

// Question 6: the cost of narrowing to a 64-byte variant, whose record a
// match arm binds and passes on.
//
//	type N64 = V64{A, B, C, D, E, F, G, H int64} | W64{P int64, Q int64}
//
// Form A copies the record out of its box. Form D builds it from slots
// that W64 shares (W64.P and W64.Q are V64.A's and V64.B's slots). Form Ds
// ("D, one slot per variant") keeps each variant's record whole in its own
// slot, so the union is the sum of the variants, and narrowing is a field
// read.

type V64 struct{ A, B, C, D, E, F, G, H int64 }
type W64 struct{ P, Q int64 }

type N64A interface{ isN64A() }

func (V64) isN64A() {}
func (W64) isN64A() {}

type N64D struct {
	tag                            uint8
	i0, i1, i2, i3, i4, i5, i6, i7 int64
}

type N64Ds struct {
	tag uint8
	v   V64
	w   W64
}

//go:noinline
func useV64(v V64) int64 { return v.A + v.B*2 + v.C*3 + v.D*4 + v.E*5 + v.F*6 + v.G*7 + v.H*8 }

//go:noinline
func useW64(w W64) int64 { return w.P - w.Q }

//go:noinline
func narrowA(u N64A) int64 {
	switch v := u.(type) {
	case V64:
		return useV64(v)
	case W64:
		return useW64(v)
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func narrowD(u N64D) int64 {
	switch u.tag {
	case 1:
		return useV64(V64{u.i0, u.i1, u.i2, u.i3, u.i4, u.i5, u.i6, u.i7})
	case 2:
		return useW64(W64{u.i0, u.i1})
	}
	panic("ergo: defect: no case of the match matched")
}

//go:noinline
func narrowDs(u N64Ds) int64 {
	switch u.tag {
	case 1:
		return useV64(u.v)
	case 2:
		return useW64(u.w)
	}
	panic("ergo: defect: no case of the match matched")
}

func n64Inputs(raw []rawValue) (a []N64A, d []N64D, ds []N64Ds) {
	for _, r := range raw {
		x := int64(r.x)
		if r.k == 0 {
			v := V64{x, x + 1, x + 2, x + 3, x + 4, x + 5, x + 6, x + 7}
			a = append(a, v)
			d = append(d, N64D{1, v.A, v.B, v.C, v.D, v.E, v.F, v.G, v.H})
			ds = append(ds, N64Ds{tag: 1, v: v})
		} else {
			w := W64{x, x / 2}
			a = append(a, w)
			d = append(d, N64D{tag: 2, i0: w.P, i1: w.Q})
			ds = append(ds, N64Ds{tag: 2, w: w})
		}
	}
	return
}
