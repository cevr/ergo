package lower

// IR builder: a record literal stored into memory is written field by
// field (IR-STOREFIELDS). Go builds `*p = T{…}`, and `p.f = T{…}` for any
// place that is not a local variable's name, in a stack temporary and
// then copies the temporary to the place: the literal's fields are
// written twice, and the copy reads 16 bytes over two 8-byte stores, a
// store-forwarding stall. A hand-optimized Go program writes the fields
// of the place. lru's Cache.Put stores its new node into a reused arena
// slot through two such literals, the node and the slot's stored cell
// (std arena's reuse), once the inliner copied Put and reuse into it.
//
// storeFields rewrites, in a body the inliner changed, a statement
//
//	store P ; T{f1: e1, …, fn: en}
//
// whose literal names every field of an Ergo record (irExpr.FT), into
//
//	def t_i = e_i       (each operand that is not a local or a constant, in order)
//	store P.f1 ; t_1 …  (one store per field; a field that is itself such
//	                     a literal is written field by field the same way)
//
// Every operand is evaluated before any store, as the literal was, so an
// operand that reads the place (a swap) reads it before the write. The
// place is evaluated once per store; it is rewritten only when its path is
// locals, constants, fields and indexes by a local or a constant, so a
// store, which writes inside P, changes nothing the path reads.
//
// A literal reaches the store through a local too: `def L = T{…}` earlier
// in the same list, whose one read is the store's value or a field of the
// stored literal, L written by that def alone and read in no function
// literal nor through its address. The def becomes its operands'
// temporaries at its own place (a local operand written between the def
// and the store gets one too), and the store writes the fields from them:
// the inliner's binding of an `own v` parameter that reuse then stores.
//
// The reference is irStoreLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irStoreLowering=off).
//
// Prior art: GHC and OCaml's flambda unbox a constructor that is stored at
// once (no intermediate block); ReScript's and Gleam's JavaScript back
// ends write an object literal's properties in place; Go's own oaslit does
// this only for a local variable's name.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// irStoreLowering is "off" for the reference: a stored literal is Go's.
var irStoreLowering string

// IRStoreOn reports that stored record literals are written field by field
// (the property's reach check).
func IRStoreOn() bool { return irStoreLowering != "off" }

// recordFieldTypes are the Go types of a record literal's fields, in its
// element order, or nil.
func (b *irBuilder) recordFieldTypes(fts []check.Type, x syntax.Expr) []string {
	out := make([]string, len(fts))
	for i, t := range fts {
		if t == nil {
			return nil
		}
		out[i] = b.goType(t, x)
	}
	return out
}

// irRecordLit reports a literal that names every field of an Ergo record.
func irRecordLit(e *irExpr) bool {
	return e != nil && e.K == "lit" && e.FT != nil && len(e.FT) == len(e.Elems) && len(e.Keys) == len(e.Elems)
}

// storeFields applies the rewrite to body and every list nested in it,
// outside function literals.
func (in *irInliner) storeFields(body []*irStmt) []*irStmt {
	if irStoreLowering == "off" {
		return body
	}
	sf := &storeFielder{in: in, body: body, reads: map[int]int{}, pinned: map[int]bool{}, splice: map[*irStmt][]*irStmt{}}
	irWalk(body, false, func(*irStmt, bool) {}, func(e *irExpr, lit bool) {
		if e.K == "local" {
			sf.reads[e.I]++
			sf.pinned[e.I] = sf.pinned[e.I] || lit
		}
		if e.K == "addr" && e.X != nil && e.X.K == "local" {
			sf.pinned[e.X.I] = true
		}
	})
	return sf.list(body, nil)
}

type storeFielder struct {
	in     *irInliner
	body   []*irStmt
	reads  map[int]int
	pinned map[int]bool // read in a function literal, or its address taken
	// splice holds a forwarded def's temporaries: the def is replaced by
	// them in its list.
	splice map[*irStmt][]*irStmt
}

// irStoreFrame is a statement list and the index of the statement being
// rewritten in it, or of the one that holds it.
type irStoreFrame struct {
	list []*irStmt
	i    int
}

func (sf *storeFielder) list(list []*irStmt, up []irStoreFrame) []*irStmt {
	for i, s := range list {
		in := append(up[:len(up):len(up)], irStoreFrame{list, i})
		s.Then, s.Else, s.Body = sf.list(s.Then, in), sf.list(s.Else, in), sf.list(s.Body, in)
		for _, c := range s.Cases {
			c.Body = sf.list(c.Body, in)
		}
	}
	var out []*irStmt
	for i, s := range list {
		if s.K != "store" || s.Op != "" || s.X == nil || s.Y == nil || !irPlainPlace(s.X) {
			out = append(out, s)
			continue
		}
		at := append(up[:len(up):len(up)], irStoreFrame{list, i})
		var lit *irExpr
		switch {
		case irRecordLit(s.Y):
			lit = s.Y
		case s.Y.K == "local":
			lit = sf.forward(at, s.Y.I)
		}
		if lit == nil {
			out = append(out, s)
			continue
		}
		var pre []*irStmt
		lit = sf.operands(at, false, nil, lit, s.Pos, &pre)
		sf.in.st.report.StoreFields++
		out = append(out, pre...)
		sf.fields(s.X, lit, s.Pos, &out)
	}
	if len(sf.splice) == 0 {
		return out
	}
	var spliced []*irStmt
	for _, s := range out {
		if pre, ok := sf.splice[s]; ok {
			spliced = append(spliced, pre...)
			delete(sf.splice, s)
			continue
		}
		spliced = append(spliced, s)
	}
	return spliced
}

// forward is the literal of the def of local x before the store at the
// innermost frame of at, in its list or an enclosing one, when x is
// written by that def alone and pinned nowhere. The def's place gets the
// temporaries of the literal's operands (splice; the def too when x has
// other reads), and the literal returned names them.
func (sf *storeFielder) forward(at []irStoreFrame, x int) *irExpr {
	if sf.pinned[x] || irWrites(sf.body, x) != 1 {
		return nil
	}
	for f := len(at) - 1; f >= 0; f-- {
		fr := at[f]
		for k := fr.i - 1; k >= 0; k-- {
			d := fr.list[k]
			if d.K != "def" || d.Local != x {
				continue
			}
			if !irRecordLit(d.X) {
				return nil
			}
			// Between the def and the store: the rest of its list up to the
			// statement that holds the store, that statement whole when the
			// store is nested in it.
			end := fr.i
			if f < len(at)-1 {
				end++
			}
			between := fr.list[k+1 : end]
			if _, done := sf.splice[d]; done {
				// Forwarded already, to an earlier read: the def's value is
				// the literal of temporaries, whose locals must still hold.
				if irLitWritten(d.X, between) {
					return nil
				}
				return d.X
			}
			var pre []*irStmt
			lit := sf.operands(at, true, between, d.X, d.Pos, &pre)
			if sf.reads[x] > 1 {
				// Other reads keep the local: the def stays, built from the
				// same temporaries.
				pre = append(pre, &irStmt{K: "def", Pos: d.Pos, Local: x, X: lit})
			}
			sf.splice[d] = pre
			d.X = lit
			return lit
		}
	}
	return nil
}

// operands is lit with each operand bound to a new temporary in pre, in
// order, unless it is a constant, or a local: for a forwarded def's
// literal (between, the statements from the def to the store), a local
// none of them writes. A nested record literal's operands are bound the
// same way; at a store's own literal, a local that a forwardable def binds
// to a record literal is that literal.
func (sf *storeFielder) operands(at []irStoreFrame, fwd bool, between []*irStmt, lit *irExpr, p irPos, pre *[]*irStmt) *irExpr {
	n := *lit
	n.Elems = make([]*irExpr, len(lit.Elems))
	for k, e := range lit.Elems {
		switch {
		case e.K == "const" || e.K == "nil":
			n.Elems[k] = e
		case irRecordLit(e):
			n.Elems[k] = sf.operands(at, fwd, between, e, p, pre)
		case e.K == "local" && !fwd:
			if lit := sf.forward(at, e.I); lit != nil {
				n.Elems[k] = lit
			} else {
				n.Elems[k] = e
			}
		case e.K == "local" && irWrites(between, e.I) == 0:
			n.Elems[k] = e
		default:
			t := sf.in.newLocal("f"+strconv.Itoa(len(sf.in.extra))+"_st", lit.FT[k], p)
			*pre = append(*pre, &irStmt{K: "def", Pos: p, Local: t, X: e})
			n.Elems[k] = &irExpr{K: "local", Pos: e.Pos, I: t}
		}
	}
	return &n
}

// fields appends the stores of lit's fields into place, a nested record
// literal's field by field.
func (sf *storeFielder) fields(place, lit *irExpr, p irPos, out *[]*irStmt) {
	for k, e := range lit.Elems {
		dest := &irExpr{K: "field", Pos: place.Pos, Name: lit.Keys[k], X: irCloneTree(place)}
		if irRecordLit(e) {
			sf.fields(dest, e, p, out)
			continue
		}
		*out = append(*out, &irStmt{K: "store", Pos: p, X: dest, Y: e})
	}
}

// storedCall inlines a marked call whose value a statement stores,
// `store P ; f(…)`, as `def t = f(…)` (spliced as any def) then
// `store P ; t`; nil when the def is not inlined. P is a local or fields
// of one, whose address the call's arguments do not take (`&L`) and which
// no function literal among them names: the call cannot re-point the
// local, and a write through a field's address (`&L.f` of a record
// local) moves no field, so evaluating the place after the call writes
// where the store did (lru's `a.free = reuse(&a.slots[at], v)` in Put).
func (in *irInliner) storedCall(s *irStmt, p *irPath) []*irStmt {
	if irStoreLowering == "off" || s.K != "store" || s.Op != "" || s.X == nil || s.Y == nil {
		return nil
	}
	site := in.st.inl.sites[s.Y]
	if site == nil || site.result == "" || site.fallible {
		return nil
	}
	root := s.X
	for root.K == "field" && root.X != nil {
		root = root.X
	}
	if root.K != "local" || irReaches(s.Y, root.I) {
		return nil
	}
	t := in.newLocal("st"+strconv.Itoa(len(in.extra)), site.result, s.Pos)
	def := &irStmt{K: "def", Pos: s.Pos, Local: t, X: s.Y}
	spliced := in.splice(def, p)
	if spliced == nil {
		in.extra = in.extra[:len(in.extra)-1] // the temporary is unused
		return nil
	}
	return append(spliced, &irStmt{K: "store", Pos: s.Pos, X: s.X, Y: &irExpr{K: "local", Pos: s.Y.Pos, I: t}})
}

// irReaches reports an address of local x, or a function literal that
// names x, in e.
func irReaches(e *irExpr, x int) bool {
	found := false
	irExprWalkStmts(e, false, func(*irStmt, bool) {}, func(e *irExpr, lit bool) {
		if lit && e.K == "local" && e.I == x {
			found = true
		}
		if e.K == "addr" && e.X != nil && e.X.K == "local" && e.X.I == x {
			found = true
		}
	})
	return found
}

// irPlainPlace reports a place whose path is locals, constants, fields and
// indexes by a local or a constant.
func irPlainPlace(e *irExpr) bool {
	switch e.K {
	case "local":
		return true
	case "field":
		return e.X != nil && irPlainPlace(e.X)
	case "index":
		return e.X != nil && e.Y != nil && irPlainPlace(e.X) && (e.Y.K == "local" || e.Y.K == "const")
	}
	return false
}

// irLitWritten reports a local operand of lit, or of a record literal in
// it, that list writes.
func irLitWritten(lit *irExpr, list []*irStmt) bool {
	for _, e := range lit.Elems {
		if e.K == "local" && irWrites(list, e.I) > 0 || irRecordLit(e) && irLitWritten(e, list) {
			return true
		}
	}
	return false
}

// irWrites counts the statements of list that write local x.
func irWrites(list []*irStmt, x int) int {
	n := 0
	irWalk(list, false, func(s *irStmt, _ bool) {
		switch s.K {
		case "def", "set", "var", "def2", "mapget", "count":
			if s.Local == x {
				n++
			}
		}
		if s.Key != nil && *s.Key == x || s.Val != nil && *s.Val == x {
			n++
		}
		for _, l := range s.Ls {
			if l == x {
				n++
			}
		}
	}, func(*irExpr, bool) {})
	return n
}

// irCloneTree is a deep copy of a place expression.
func irCloneTree(e *irExpr) *irExpr {
	if e == nil {
		return nil
	}
	n := *e
	n.X, n.Y = irCloneTree(e.X), irCloneTree(e.Y)
	return &n
}
