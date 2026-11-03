package bindc

// Layout of the four tier-1 targets (linux and darwin on amd64 and arm64).
// Their C ABIs (System V x86-64, AAPCS64 and Apple arm64) agree for the
// types of the subset: every scalar and pointer is naturally aligned (its
// alignment equals its size), an array has the alignment of its element,
// a struct has the largest alignment of its fields, every field starts at
// the next offset that is a multiple of its alignment, and the size of a
// struct is rounded up to a multiple of its alignment. Inferred from the
// ABI documents; checked by the cc oracle on the host and by the
// translate-c table of OpenTUI (TestCOracle, TestOpenTUIMatchesTranslateC).

// maxObject bounds the size of a struct of the subset. A field is at most
// 2^31-1 elements of a struct of at most maxObject bytes, so no sum below
// overflows int64.
const maxObject = 1<<31 - 1

// layout fills the offset, size and alignment of every field of a struct
// and the size and alignment of the struct. It reports false for a struct
// larger than maxObject.
func (p *parser) layout(d *TypeDecl) bool {
	off, align := int64(0), int64(1)
	for i := range d.Fields {
		f := &d.Fields[i]
		f.Size, f.Align = p.sizeOf(f.Type)
		off = roundUp(off, f.Align)
		f.Offset = off
		off += f.Size
		if off > maxObject {
			return false
		}
		if f.Align > align {
			align = f.Align
		}
	}
	d.Size, d.Align = roundUp(off, align), align
	return d.Size <= maxObject
}

// sizeOf is the size and alignment of a type that has a size.
func (p *parser) sizeOf(t Type) (size, align int64) {
	switch t.Kind {
	case TypeScalar:
		n := scalarSize[t.Scalar]
		return n, n
	case TypePointer:
		return pointerSize, pointerSize
	case TypeArray:
		s, a := p.sizeOf(*t.Elem)
		return s * t.Len, a
	case TypeNamed:
		d := p.decls[p.types[t.Name]]
		switch d.Kind {
		case DeclScalar:
			n := scalarSize[d.Scalar]
			return n, n
		case DeclCallback:
			return pointerSize, pointerSize
		case DeclStruct:
			return d.Size, d.Align
		}
	}
	panic("bindc: no size for " + t.Spell())
}

func roundUp(n, a int64) int64 { return (n + a - 1) / a * a }
