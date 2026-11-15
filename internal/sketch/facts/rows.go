package facts

import (
	"slices"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The rows relations (SEMTOOLS SEM2b): each function's derived needs row,
// and what each call site charges its declaration. A call charges the
// capabilities of its callee's row at the site (check.Call.Needs); those
// a with around the call discharges (its block holds the call, and the
// with's Discharged names the capability) are Discharges, the rest
// Brings. A call in a function literal is charged to the declaration
// that holds the literal, and a with around the literal discharges it.

// charge is what the call whose name is at (file, off) charges, by
// capability id.
type charge struct {
	file uint16
	off  uint32
	caps []uint32
}

// withBlock is a with's block, by file and offsets, with the capability
// ids it discharges (every operand's).
type withBlock struct {
	file     uint16
	from, to uint32
	caps     []uint32
}

// callName is the name a call's site is at: the called name of x.Fun.
func callName(x syntax.Expr) *syntax.Ident {
	for {
		switch e := x.(type) {
		case *syntax.Ident:
			return e
		case *syntax.SelectorExpr:
			return e.Sel
		case *syntax.IndexExpr:
			x = e.X
		case *syntax.ParenExpr:
			x = e.X
		default:
			return nil
		}
	}
}

// capability is the symbol of an element of a needs row: an operation
// type, by its type's symbol. A row variable has none.
func (b *builder) capability(t check.Type) (uint32, bool) {
	n := named(t)
	if n != nil && n.Orig != nil {
		n = n.Orig
	}
	if n == nil || n.Obj == nil {
		return 0, false
	}
	key, sym, ok := b.symbol(n.Obj, nil, false)
	if !ok {
		return 0, false
	}
	return b.id(key, sym), true
}

// capabilities is the symbols of r's elements, in r's order.
func (b *builder) capabilities(r *check.Row) []uint32 {
	var out []uint32
	for _, t := range r.Elems() {
		if id, ok := b.capability(t); ok && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// noteCall records what the call x charges and, for a with, its block.
func (b *builder) noteCall(x *syntax.CallExpr) {
	if c := b.info.Calls[x]; c != nil && !c.Needs.Empty() {
		if name := callName(x.Fun); name != nil {
			b.charges = append(b.charges, charge{file: b.file, off: uint32(name.NamePos), caps: b.capabilities(c.Needs)})
		}
	}
	if w := b.info.Withs[x]; w != nil && x.Trailing != nil {
		wb := withBlock{file: b.file, from: uint32(x.Trailing.Lbrace), to: uint32(x.Trailing.Rbrace)}
		for ; w != nil; w = w.Next {
			for _, t := range w.Discharged {
				if id, ok := b.capability(t); ok && !slices.Contains(wb.caps, id) {
					wb.caps = append(wb.caps, id)
				}
			}
		}
		if len(wb.caps) > 0 {
			b.withs = append(b.withs, wb)
		}
	}
}

// needs writes an edge from each function of the package to each
// capability of its needs row, in the package's function order.
func (b *builder) needs() {
	for _, f := range b.pkg.Funcs {
		if f.Sig == nil || f.Sig.Uses.Empty() {
			continue
		}
		key, _, ok := b.funcSymbol(f)
		if !ok {
			continue
		}
		id, ok := b.ids[key]
		if !ok || !b.t.Symbols[id].Declared {
			continue
		}
		for _, c := range b.capabilities(f.Sig.Uses) {
			b.t.Needs = append(b.t.Needs, Edge{From: id, To: c, Site: NoSym})
		}
	}
}

// chargeEdges writes, for each call edge in its order, an edge from the
// caller to each capability the call charges: Brings, or Discharges when
// a with around the call discharges it.
func (b *builder) chargeEdges() {
	type at struct {
		file uint16
		off  uint32
	}
	byName := map[at][]uint32{}
	for _, c := range b.charges {
		byName[at{c.file, c.off}] = c.caps
	}
	t := b.t
	for _, e := range t.Calls {
		s := t.Sites[e.Site]
		for _, c := range byName[at{s.File, s.Off}] {
			discharged := false
			for _, w := range b.withs {
				if w.file == s.File && w.from < s.Off && s.Off < w.to && slices.Contains(w.caps, c) {
					discharged = true
					break
				}
			}
			if discharged {
				t.Discharges = append(t.Discharges, Edge{From: e.From, To: c, Site: e.Site})
			} else {
				t.Brings = append(t.Brings, Edge{From: e.From, To: c, Site: e.Site})
			}
		}
	}
}
