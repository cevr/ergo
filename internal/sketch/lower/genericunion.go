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
// instance, so an instance of a large type argument pays a copy where the
// interface paid a box. The prelude's Option is one such union, with no
// layout of its own (ADT-NOPRIV OPTION-SHAPE): its Some's one field is
// its shared slot, `struct{tag uint8; p0 T}`, the bytes of its old
// `struct{tag uint8; v0 Some[T]}`. The union stays an interface when
// every variant is an error type (it may name a row, and rows read
// failures by Go type: G6, as flatShape's MSB3 rule; the reference
// msb3Lowering "off" keeps one with any error variant an interface), when
// two variants are instances of one generic record (`V[T] | V[int]`: one
// Go type at T = int), when a Go extern's signature names it (Option
// aside, genericUnions), and when a record of the program holds itself by
// value through it (genericCycle). Each instance's helpers are the generic
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
	errs := 0 // the variants that are error types
	for _, v := range u.Variants {
		vn, ok := v.(*check.Named)
		if !ok {
			return nil
		}
		if l.errorType(vn) {
			if msb3Lowering == "off" {
				return nil
			}
			errs++
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
	// G6, as flatShape's rule for a union that is not generic (MSB3): a
	// union whose every variant is an error type may name a row, which
	// catch and ConvRow test by Go type, so it stays an interface; one of
	// which only some variants are error types names no row, and its error
	// variants are records in its slots like any other.
	if errs == len(fu.variants) {
		return nil
	}
	if fu.canShare {
		fu.shareSlots()
		fu.shared = true
	}
	return fu
}

// genericUnions adds to the flat set the generic unions of the program's
// packages and of the prelude that the rule admits, the prelude's Option
// among them (ADT-NOPRIV OPTION-SHAPE: Option has no layout of its own),
// skipping the unions skip names and those a Go extern's signature names
// (extern), and drops those a record holds itself through.
//
// The one site of the rule that names Option is here, until SHAPE-BOUNDARY
// (prelude-unions.txt, its row): an extern that names an Option is an
// adapter whose Option the lowering builds from Go's (v, ok), a nil or an
// error (gocall.go: someWhen, goResult), so Option keeps its flat form
// where an extern names it, as no other union of its shape does yet. And
// under the reference optionLowering "own" Option takes its own form of
// before, a slot per variant (optionShape).
func (l *lowerer) genericUnions(skip func(*check.TypeName) bool, extern map[*check.TypeName]bool) {
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
			option := opt != nil && tn == opt.Obj
			if !ok || skip(tn) || extern[tn] && !option {
				continue
			}
			if option && optionLowering == "own" {
				l.flatSet[tn] = optionShape(n)
				gens = append(gens, tn)
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
// value through the generic union g: the interface breaks the cycle with its box, the struct would
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
			if l.genericLoop(n, g, map[*check.TypeName]int{}, map[string]int{}, 0, 0) {
				return true
			}
		}
	}
	return false
}

// genericLoop reports whether, with the generic union g flat, t's Go
// value holds itself: a record or defined type on the path met again inside a g
// entered since. A variant of a generic flat union is tracked by its full
// instantiated type key (so Option[Option[int]] is finite while a self-recursive
// generic union like Formula[S] -> NotFormula[S] -> Formula[S] terminates cleanly
// without blaming unrelated generic unions).
func (l *lowerer) genericLoop(t check.Type, g *check.TypeName, path map[*check.TypeName]int, seen map[string]int, opts, depth int) bool {
	if depth > 64 {
		return opts > 0 // too deep to tell: only blame g if g was actually entered
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
			key := l.typeKey(u)
			if at, ok := seen[key]; ok {
				return opts > at
			}
			seen[key] = opts
			defer delete(seen, key)
			opts++
		case gen || l.genericVariant(obj):
			key := l.typeKey(u)
			if at, ok := seen[key]; ok {
				return opts > at
			}
			seen[key] = opts
			defer delete(seen, key)
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
			return l.genericLoop(d, g, path, seen, opts, depth+1)
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				if l.genericLoop(f.Type, g, path, seen, opts, depth+1) {
					return true
				}
			}
		case *check.Union:
			if !gen {
				return false
			}
			for _, v := range under.Variants {
				if l.genericLoop(v, g, path, seen, opts, depth+1) {
					return true
				}
			}
		}
	case *check.Tuple:
		for _, e := range u.Types {
			if l.genericLoop(e, g, path, seen, opts, depth+1) {
				return true
			}
		}
	case *check.Array:
		return l.genericLoop(u.Elem, g, path, seen, opts, depth+1)
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
