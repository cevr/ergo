package lower

import (
	"ergo.local/prototype/internal/sketch/check"
)

// Generic unions (MSB1; types.md Unions 6). A union with type parameters
// of the program's own source or of the prelude (the prelude's Decision,
// a user's `type Result[T] = Ok[T] | Err`) lowers, as Option does (OPT2),
// to one generic Go struct for every instance: a tag and its shared slots
// (U1), where the fields of one type parameter share one slot:
//
//	type Decision[S any, O any] struct {
//		tag uint8
//		p0  S // Enter's, Stay's and Emit's Next
//		p1  O // Emit's and Done's Output
//	}
//	func (Decision[S, O]) of0(v Enter[S]) Decision[S, O] { return Decision[S, O]{tag: 0, p0: v.Next} }
//	func (u Decision[S, O]) as0() Enter[S]                { return Enter[S]{Next: u.p0} }
//
// So a step that returns Enter[Part]{Next: Minus} returns a struct of
// two bytes by value, never a box: before MSB1, Decision was a Go
// interface and each Enter and Stay a step returned was runtime.convT, one
// heap allocation per signal when Go did not inline the step.
//
// The rule (genericShape, genericCycles) is the flat rule's steps 1 and 2
// for the declaration, with no size bound: Go spells one type for every
// instance, as for Option, so an instance of a large type argument pays a
// copy where the interface paid a box. The union stays an interface when
// a variant is an error type (rows read failures by Go type), when two
// variants are instances of one generic record (`V[T] | V[int]`: one Go
// type at T = int), when a Go extern's signature names it, and when a
// record of the program holds itself by value through it (genericCycles,
// as optionCycle for Option). Each instance's helpers are the generic
// declaration's; flatUnionOf gives each instance its copy (instance).
//
// The reference is genericUnionLowering "interface", set by a test or by a
// build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.genericUnionLowering=interface.
// TestFlatUnionsAgree runs generated generic unions under both.
//
// Prior art: Rust and Swift lay a generic enum out per instance (a tag
// and the payloads); OCaml and ReScript keep a constant constructor an
// immediate integer and a non-constant one a block whatever the type
// argument; Go's own generics stencil by GC shape, so one struct type
// whose slots are type parameters costs no dictionary lookup to read.
// Gleam and Elixir keep a tagged tuple on the BEAM's heap.
var genericUnionLowering string

// genericShape is the flat form of the generic union n by the rule above,
// or nil when n stays an interface (n not a generic union included).
func (l *lowerer) genericShape(n *check.Named) *flatUnion {
	if genericUnionLowering == "interface" || n.Orig != nil || len(n.TParams) == 0 || n.Obj.Decl == nil {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) == 0 || len(u.Variants) > 255 || u.Row != nil {
		return nil
	}
	fu := &flatUnion{tn: n.Obj, generic: true, canShare: unionLowering != "slots"}
	origins := map[*check.TypeName]bool{}
	for _, v := range u.Variants {
		vn, ok := v.(*check.Named)
		if !ok || l.errorType(vn) {
			return nil
		}
		o := vn.Obj
		if vn.Orig != nil {
			o = vn.Orig.Obj
		}
		if o == nil || o.Decl == nil || origins[o] {
			return nil
		}
		origins[o] = true
		st, ok := vn.Underlying().(*check.Struct)
		if !ok {
			return nil
		}
		fu.variants = append(fu.variants, vn)
		fu.slot = append(fu.slot, len(st.Fields) > 0)
	}
	if fu.canShare {
		fu.shareSlots()
		fu.shared = true
	}
	return fu
}

// genericUnions adds to the flat set the generic unions of the program's
// packages and of the prelude that the rule admits (Option aside, which
// optionShape gives), skipping the unions skip names, and drops those a
// record holds itself through.
func (l *lowerer) genericUnions(skip func(*check.TypeName) bool) {
	pkgs := l.unionPkgs()
	opt := check.PreludeType("Option")
	if opt != nil {
		pkgs = append(pkgs, opt.Obj.Pkg)
	}
	seen := map[*check.Package]bool{}
	var gens []*check.TypeName
	for _, p := range pkgs {
		if p == nil || seen[p] {
			continue
		}
		seen[p] = true
		for _, tn := range l.canon.types(p) {
			n, ok := tn.Type.(*check.Named)
			if !ok || opt != nil && tn == opt.Obj || skip(tn) {
				continue
			}
			if fu := l.genericShape(n); fu != nil {
				l.flatSet[tn] = fu
				gens = append(gens, tn)
			}
		}
	}
	for changed := true; changed; {
		changed = false
		for _, g := range gens {
			if l.flatSet[g] != nil && l.genericCycle(g) {
				delete(l.flatSet, g)
				changed = true
			}
		}
	}
}

// genericCycle reports whether a record of the program holds itself by
// value through the generic union g (optionCycle's walk with g for
// Option): the interface breaks the cycle with its box, the struct would
// be an invalid recursive Go type. The walk goes through the other generic
// unions of the flat set as through records, so a cycle through two of
// them drops one, and the next round sees it boxed.
func (l *lowerer) genericCycle(g *check.TypeName) bool {
	for _, p := range l.unionPkgs() {
		for _, tn := range l.canon.types(p) {
			n, ok := tn.Type.(*check.Named)
			if !ok {
				continue
			}
			if _, ok := n.Underlying().(*check.Struct); !ok && n.Defined() == nil {
				continue
			}
			if l.genericLoop(n, g, map[*check.TypeName]int{}, 0, 0) {
				return true
			}
		}
	}
	return false
}

// genericLoop is optionLoop for the generic union g: t's Go value holds
// itself, a record or defined type on the path met again inside a g
// entered since. A variant of a generic flat union is no loop's record
// (Some[T] in Option[Option[T]]).
func (l *lowerer) genericLoop(t check.Type, g *check.TypeName, path map[*check.TypeName]int, opts, depth int) bool {
	if depth > 64 {
		return true // too deep to tell: keep g an interface
	}
	switch u := t.(type) {
	case *check.Named:
		obj := u.Obj
		if u.Orig != nil {
			obj = u.Orig.Obj
		}
		fu := l.flatSet[obj]
		gen := fu != nil && fu.generic
		switch {
		case obj == g:
			opts++
		case gen || l.genericVariant(obj):
		default:
			at, ok := path[obj]
			switch {
			case ok && opts > at:
				return true
			case !ok:
				path[obj] = opts
				defer delete(path, obj)
			}
		}
		if d := u.Defined(); d != nil {
			return l.genericLoop(d, g, path, opts, depth+1)
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				if l.genericLoop(f.Type, g, path, opts, depth+1) {
					return true
				}
			}
		case *check.Union:
			if !gen {
				return false
			}
			for _, v := range under.Variants {
				if l.genericLoop(v, g, path, opts, depth+1) {
					return true
				}
			}
		}
	case *check.Tuple:
		for _, e := range u.Types {
			if l.genericLoop(e, g, path, opts, depth+1) {
				return true
			}
		}
	case *check.Array:
		return l.genericLoop(u.Elem, g, path, opts, depth+1)
	}
	return false
}

// genericVariant reports whether obj is a variant's record of a generic
// flat union.
func (l *lowerer) genericVariant(obj *check.TypeName) bool {
	for _, fu := range l.flatSet {
		if !fu.generic {
			continue
		}
		for _, v := range fu.variants {
			if v.Obj == obj || v.Orig != nil && v.Orig.Obj == obj {
				return true
			}
		}
	}
	return false
}

// instanceSlotTypes are the checked types of the shared slots of the
// instance n of the generic flat union fu: each slot's type is that of the
// first variant field the declaration gave it, read from the instance.
func (fu *flatUnion) instanceSlotTypes(n *check.Named) []check.Type {
	vs := n.Underlying().(*check.Union).Variants
	out := make([]check.Type, len(fu.slots))
	done := make([]bool, len(fu.slots))
	for k, slots := range fu.fieldSlot {
		fields := vs[k].(*check.Named).Underlying().(*check.Struct).Fields
		for i, s := range slots {
			if !done[s] {
				out[s], done[s] = fields[i].Type, true
			}
		}
	}
	return out
}
