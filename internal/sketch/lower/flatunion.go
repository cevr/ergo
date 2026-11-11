package lower

import (
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Flat unions (MU8b, OPT2, OPT3; types.md Unions 6, runtime.md "Flat
// unions"). A union of the program lowers, where the rule below admits
// it, to a Go struct of a tag and a slot per variant that has fields,
// never a Go interface:
//
//	type Sig struct {
//		tag uint8 // the variant's index in the union's declaration
//		v1  Add   // the value of variant 1; zero unless tag is 1
//	}
//	func (Sig) of0(Increment) Sig { return Sig{tag: 0} }
//	func (Sig) of1(v Add) Sig     { return Sig{tag: 1, v1: v} }
//
// A []Sig is then a slice of small values: a match is a switch on a byte,
// as a hand loop over a []byte is, where a sealed interface costs a load
// of its type word and a compare per case, and a value that leaves its
// function costs no box (union-repr.md, form B and form D, with Ds's
// layout: each variant's record whole in its own slot, so narrowing reads
// one field).
//
// Every value is built by its variant's of method, so a slot that the tag
// does not name is always zero, and Go's == on the struct is Ergo's ==
// (union-repr.md §7, the zero-slot invariant). A match tests and switches
// on the tag (tagSwitchOf, test) and narrows by reading the slot
// (flatNarrow). An error's text, a law's literal and generator, wire's
// codec and a machine law's state key switch on the tag too.
//
// The rule (flatUnionSet), decided once per program (union-repr.md, rule
// G, steps 1, 2 and 6):
//
//  1. The union is declared in a package of the program's own source, it
//     is not generic, no machine takes it as its state (machinerun.go has
//     a flat state of its own), and no Go extern's signature names it.
//  2. Its variants are records that are neither generic nor error types,
//     and the union is no error type: a row value is a Go error, which
//     ConvRow and catch test by type.
//  3. Its Go value does not hold itself: a variant field reaches the
//     union again by value, through records, tuples, arrays, Options or
//     other flat unions (Go refuses a recursive struct; the interface box
//     breaks the cycle, as Swift's indirect case does). A slice, a Map, a
//     function or a boxed union breaks a cycle.
//  4. The struct fits Go's registers (ssa.CanSSA): at most 32 bytes and at
//     most 4 fields at each struct level (the tag counts), and no array of
//     more than one element. Above it, a value carried through a call goes
//     through memory and costs 10x a box's pass (union-repr.md §5). A
//     field whose Go form the rule does not know (a Go type, a std
//     type) keeps the union an interface.
//
// Steps 3 and 4 repeat until no union changes: a union that becomes an
// interface changes the size of the unions that hold it.
//
// The prelude's Option is flat too (OPT2, the form C of union-repr.md §10
// by the same struct): one generic Go struct for every instance,
//
//	type Option[T any] struct {
//		tag uint8   // 0 Some, 1 None
//		v0  Some[T] // Some's value; zero unless tag is 0
//	}
//
// so an Option that a function returns, a record holds or a slice keeps
// is a value, never a box. Go spells one generic type for every instance,
// so the rule has no size bound for Option; the instances of a large T pay
// a copy where form A pays a box (union-repr.md §10, round 2). Option
// stays an interface where a machine takes it as its state or its signal,
// or a record holds itself through it. An Option of a go.Ref[S], which no
// Ergo value makes nil (interop.md), is the Go pointer *S itself: nil is
// None (the pointer niche, as Rust's Option<&T> and ReScript's option),
// wherever no generic declaration but Option and Some takes a go.Ref
// through its type arguments (nicheAdmitted).
//
// A lowering path that meets a flat union it does not lower calls
// flatBail: the whole program is lowered again with that union an
// interface, so a form this file does not cover costs speed, never
// correctness.
//
// The reference is form A for every union: unionLowering "interface", set
// by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.unionLowering=interface.
// TestMachineFlatRunAgrees runs each generated machine with flat signals,
// with interface signals and through std's loop; TestFlatUnionsAgree runs
// generated programs of Options and of generated user unions under both
// lowerings; each requires one output.
//
// Prior art: OCaml and ReScript lower a constant constructor to an
// integer and switch on it; Rust lays an enum out as a tag and its
// payloads, with a niche for Option<&T>; Borgo (compiles to Go) emits a
// struct with a tag and a field per variant field and switches on the
// tag; Gleam on Erlang keeps a variant a tagged tuple or a bare atom.
var unionLowering string

// unionMutant breaks one helper of the flat union, for the mutant tests
// only: "sig-tag" (of writes the tag of the next variant: a wrong tag
// order), "sig-payload" (narrowing reads the zero value, not the slot: the
// payload of no variant), "sig-test" (a tag test holds for every variant:
// a value another arm should take is accepted by the first) and
// "sig-switch" (a switch on the tag takes the next variant's case). Empty
// in every build.
var unionMutant string

// flatUnion is the flat form of one union. For a generic union (Option),
// the set holds the form of the declaration, and flatUnionOf gives each
// instance its own copy: inst is the instance and variants are its
// variants, so every Go type the helpers spell is the instance's.
type flatUnion struct {
	tn       *check.TypeName
	variants []*check.Named // by tag
	slot     []bool         // whether variant k has fields, and so a slot
	generic  bool           // the declaration takes type parameters
	inst     *check.Named   // the instance, for a generic union's copy
	// niche reports an Option instance of a go.Ref[S] whose Go value is
	// the pointer itself: nil is None, any other pointer Some.
	niche bool
}

// instance is fu's copy for n, an instance of its generic declaration.
func (fu *flatUnion) instance(n *check.Named) *flatUnion {
	c := *fu
	c.inst = n
	c.variants = make([]*check.Named, len(fu.variants))
	for k, v := range n.Underlying().(*check.Union).Variants {
		c.variants[k] = v.(*check.Named)
	}
	return &c
}

// nicheOff stands for the pointer niche in flatOff and flatBails: a path
// that meets a niche Option it does not lower bails on it, and the program
// is lowered again with every Option of a go.Ref in Option's struct.
var nicheOff = &check.TypeName{Name: "Option's pointer niche"}

// flatBail stops the lowering at n, which meets the flat union fu in a form
// this file does not lower: guard records fu, and program lowers the
// program again with fu an interface (lowerAgain). The refusal is an ice
// that names the union, so it is reported as one if a lowering ever kept
// it.
func flatBail(n syntax.Node, fu *flatUnion) {
	tn := fu.tn
	if fu.niche {
		tn = nicheOff
	}
	defer func() {
		r := recover()
		if ref, ok := r.(refusal); ok {
			ref.flat = tn
			r = ref
		}
		panic(r)
	}()
	ice(n, "the flat union %s meets a form flatunion.go does not lower, so the program is lowered again with it an interface", tn.Name)
}

// lowerAgain lowers the program again, from the start, with every union
// this lowering bailed on an interface. Each round turns at least one union
// into an interface, so the rounds end.
func (l *lowerer) lowerAgain() ([]byte, error) {
	var tests *testLowering
	if l.tests != nil {
		tests = &testLowering{gens: map[pkgKey]bool{}, lits: map[pkgKey]bool{}}
	}
	next := lowererOf(l.canon, l.pkg, l.goModule, tests)
	next.noEntry, next.matchTest = l.noEntry, l.matchTest
	next.flatOff = map[*check.TypeName]bool{}
	for tn := range l.flatOff {
		next.flatOff[tn] = true
	}
	for _, tn := range l.flatBails {
		if l.flatOff[tn] {
			return nil, fmt.Errorf("lower: the union %s bailed while it was an interface (a compiler defect)", tn.Name)
		}
		next.flatOff[tn] = true
	}
	out, err := next.program()
	// The counts are the lowering that wrote the program, and the bails
	// before it.
	l.stats = next.stats
	for _, tn := range l.flatBails {
		if opt := check.PreludeType("Option"); opt != nil && (tn == opt.Obj || tn == nicheOff) {
			l.stats.OptionBails++
		} else {
			l.stats.UnionBails++
		}
	}
	return out, err
}

// flatUnionSet decides, once, which unions of the program are flat.
func (l *lowerer) flatUnionSet() map[*check.TypeName]*flatUnion {
	if l.flatSet != nil {
		return l.flatSet
	}
	l.flatSet = map[*check.TypeName]*flatUnion{}
	if unionLowering == "interface" {
		return l.flatSet
	}
	states := map[*check.TypeName]bool{}
	signals := map[*check.TypeName]bool{}
	for _, p := range l.sourcedPkgs() {
		for _, tn := range l.canon.types(p) {
			if tn.Decl == nil {
				continue
			}
			if _, ok := tn.Decl.Spec.(*syntax.MachineType); !ok {
				continue
			}
			named, ok := tn.Type.(*check.Named)
			if !ok {
				continue
			}
			m, ok := named.Underlying().(*check.MachineType)
			if !ok {
				continue
			}
			if s, ok := m.State.(*check.Named); ok {
				states[s.Obj] = true
			}
			if e, ok := m.Signal.(*check.Named); ok {
				signals[e.Obj] = true
			}
		}
	}
	// The prelude's Option, unless a machine takes it as its state or its
	// signal, whose lowerings (machinerun.go) take a state or a signal
	// union apart by its interface.
	opt := check.PreludeType("Option")
	if opt != nil && !l.flatOff[opt.Obj] && !states[opt.Obj] && !signals[opt.Obj] && !l.optionCycle(opt.Obj) {
		l.flatSet[opt.Obj] = optionShape(opt)
	}
	// A union a Go extern's signature names keeps its Go view.
	extern := map[*check.TypeName]bool{}
	for _, p := range l.sourcedPkgs() {
		for _, f := range l.canon.funcs(p) {
			if f.Decl == nil || f.Decl.Extern == nil || f.Sig == nil {
				continue
			}
			for _, v := range f.Sig.Params {
				mentioned(v.Type, extern, map[check.Type]bool{})
			}
			for _, t := range f.Sig.Results {
				mentioned(t, extern, map[check.Type]bool{})
			}
		}
	}
	// Steps 1 and 2: the candidates.
	var cands []*check.TypeName
	for _, p := range l.sourcedPkgs() {
		for _, tn := range l.canon.types(p) {
			n, ok := tn.Type.(*check.Named)
			if !ok || states[tn] || extern[tn] || l.flatOff[tn] {
				continue
			}
			if fu := l.flatShape(n); fu != nil {
				l.flatSet[tn] = fu
				cands = append(cands, tn)
			}
		}
	}
	// Steps 3 and 4, until no union changes.
	for changed := true; changed; {
		changed = false
		for _, tn := range cands {
			if l.flatSet[tn] == nil {
				continue
			}
			lay, cycle, known := l.layoutOf(tn.Type, map[*check.TypeName]bool{}, 0)
			switch {
			case cycle != nil:
				delete(l.flatSet, cycle)
				changed = true
			case !known || !lay.ssa:
				delete(l.flatSet, tn)
				changed = true
			}
		}
	}
	if opt != nil && l.flatSet[opt.Obj] != nil && !l.flatOff[nicheOff] {
		l.niche = l.nicheAdmitted(opt.Obj)
	}
	return l.flatSet
}

// optionCycle reports whether a record of the program holds itself by
// value through an Option (`type Node { Next Option[Node] }`). The
// interface Option breaks the cycle with its box; the flat one would make
// the record an invalid recursive Go type, so the program keeps Option an
// interface.
func (l *lowerer) optionCycle(opt *check.TypeName) bool {
	for _, p := range l.sourcedPkgs() {
		for _, tn := range l.canon.types(p) {
			n, ok := tn.Type.(*check.Named)
			if !ok {
				continue
			}
			if _, ok := n.Underlying().(*check.Struct); !ok && n.Defined() == nil {
				continue
			}
			if optionLoop(n, opt, map[*check.TypeName]int{}, 0) {
				return true
			}
		}
	}
	return false
}

// optionLoop reports whether t's Go value, with Option flat, holds itself:
// a record or a defined type of the program on the path (with the number
// of Options entered when it was entered) met again inside an Option
// entered since. It walks records, defined types, tuples, arrays and
// Options by value, never a slice, a Map, a borrow, a function or another
// union (a cycle through a flat union of the program is that union's, rule
// step 3). A generic record met again with other type arguments counts
// too (`B[T] { x Option[B[[]T]] }` is Go's instantiation cycle), so a rare
// finite nesting keeps Option an interface: slower, never wrong.
func optionLoop(t check.Type, opt *check.TypeName, path map[*check.TypeName]int, opts int) bool {
	switch u := t.(type) {
	case *check.Named:
		obj := u.Obj
		if u.Orig != nil {
			obj = u.Orig.Obj
		}
		if obj == opt {
			opts++
		} else if !isOptionVariant(obj, opt) { // Some and None are no loop's record
			at, ok := path[obj]
			switch {
			case ok && opts > at:
				return true
			case !ok:
				// A record met again with no Option between is a
				// nesting of a generic one (Pair[Pair[int]]): an
				// infinite one is the checker's error.
				path[obj] = opts
				defer delete(path, obj)
			}
		}
		if d := u.Defined(); d != nil {
			return optionLoop(d, opt, path, opts)
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				if optionLoop(f.Type, opt, path, opts) {
					return true
				}
			}
		case *check.Union:
			if obj != opt {
				return false
			}
			for _, v := range under.Variants {
				if optionLoop(v, opt, path, opts) {
					return true
				}
			}
		}
	case *check.Tuple:
		for _, e := range u.Types {
			if optionLoop(e, opt, path, opts) {
				return true
			}
		}
	case *check.Array:
		return optionLoop(u.Elem, opt, path, opts)
	}
	return false
}

// isOptionVariant reports whether obj is Some or None, a variant of the
// Option opt.
func isOptionVariant(obj, opt *check.TypeName) bool {
	for _, v := range opt.Type.(*check.Named).Underlying().(*check.Union).Variants {
		n := v.(*check.Named)
		if n.Obj == obj || n.Orig != nil && n.Orig.Obj == obj {
			return true
		}
	}
	return false
}

// mentioned adds to out every non-generic union that t names. Option is
// left out: no Go function takes or returns the program's Option, since
// every extern that names one is an adapter whose Option the lowering
// builds from Go's (v, ok), a nil or an error (gocall.go: someWhen,
// goResult).
func mentioned(t check.Type, out map[*check.TypeName]bool, seen map[check.Type]bool) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true
	switch u := t.(type) {
	case *check.Named:
		if u.Orig == nil && len(u.TParams) == 0 {
			out[u.Obj] = true
		}
		for _, a := range u.TArgs {
			mentioned(a, out, seen)
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				mentioned(f.Type, out, seen)
			}
		case *check.Union:
			for _, v := range under.Variants {
				mentioned(v, out, seen)
			}
		}
		if d := u.Defined(); d != nil {
			mentioned(d, out, seen)
		}
	case *check.Slice:
		mentioned(u.Elem, out, seen)
	case *check.Map:
		mentioned(u.Key, out, seen)
		mentioned(u.Elem, out, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			mentioned(e, out, seen)
		}
	case *check.Signature:
		for _, v := range u.Params {
			mentioned(v.Type, out, seen)
		}
		for _, r := range u.Results {
			mentioned(r, out, seen)
		}
	}
}

// flatShape is the flat form of the union n by the rule's steps 1 and 2,
// or nil when they keep it an interface (n not a union included).
func (l *lowerer) flatShape(n *check.Named) *flatUnion {
	if n.Orig != nil || len(n.TParams) > 0 || n.Obj.Decl == nil || !l.isSourced(n.Obj.Pkg) {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) == 0 || len(u.Variants) > 255 || u.Row != nil {
		return nil
	}
	if l.errorType(n, l.buildView()) {
		return nil
	}
	fu := &flatUnion{tn: n.Obj}
	for _, v := range u.Variants {
		vn, ok := v.(*check.Named)
		if !ok || vn.Orig != nil || len(vn.TParams) > 0 || vn.Obj.Decl == nil || l.errorType(vn, l.buildView()) {
			return nil
		}
		st, ok := vn.Underlying().(*check.Struct)
		if !ok {
			return nil
		}
		fu.variants = append(fu.variants, vn)
		fu.slot = append(fu.slot, len(st.Fields) > 0)
	}
	return fu
}

// goLayout is the size and alignment of a Go value on a 64-bit target,
// and whether Go keeps it in registers (ssa.CanSSA).
type goLayout struct {
	size, align int
	ssa         bool
}

// ssaBytes and ssaFields are Go's register limits (cmd/compile ssa:
// CanSSA, MaxStruct): a struct of more bytes or more fields at one level
// goes through memory.
const (
	ssaBytes  = 32
	ssaFields = 4
)

// structLayout lays fields out in order, as Go does.
func structLayout(fields []goLayout) goLayout {
	off, align, ssa := 0, 1, len(fields) <= ssaFields
	for _, f := range fields {
		off = roundUp(off, f.align) + f.size
		align = max(align, f.align)
		ssa = ssa && f.ssa
	}
	size := roundUp(off, align)
	return goLayout{size: size, align: align, ssa: ssa && size <= ssaBytes}
}

// layoutOf is the Go layout of t's value, with the unions of the flat set
// flat. cycle is a flat union that t reaches again inside its own value
// (path holds the flat unions entered); known is false when the rule does
// not know t's Go form (a Go type, a std type, a type parameter).
func (l *lowerer) layoutOf(t check.Type, path map[*check.TypeName]bool, depth int) (lay goLayout, cycle *check.TypeName, known bool) {
	word := goLayout{size: 8, align: 8, ssa: true}
	if depth > 64 {
		return goLayout{}, nil, false
	}
	fields := func(types []check.Type) (goLayout, *check.TypeName, bool) {
		var fs []goLayout
		for _, ft := range types {
			f, c, ok := l.layoutOf(ft, path, depth+1)
			if c != nil || !ok {
				return goLayout{}, c, ok
			}
			fs = append(fs, f)
		}
		return structLayout(fs), nil, true
	}
	switch u := t.(type) {
	case *check.Basic:
		if u.Kind == check.Invalid {
			return goLayout{}, nil, false
		}
		s, a := basicSize(u.Kind)
		return goLayout{size: s, align: a, ssa: true}, nil, true
	case *check.Slice:
		return goLayout{size: 24, align: 8, ssa: true}, nil, true
	case *check.Map, *check.Pointer, *check.Signature:
		return word, nil, true
	case *check.Tuple:
		return fields(u.Types)
	case *check.Array:
		e, c, ok := l.layoutOf(u.Elem, path, depth+1)
		if c != nil || !ok {
			return goLayout{}, c, ok
		}
		return goLayout{size: e.size * int(u.Len), align: e.align, ssa: u.Len == 0 || u.Len == 1 && e.ssa}, nil, true
	case *check.Named:
		if _, ok := check.GoRefElem(u); ok || check.IsContext(u) {
			return word, nil, true
		}
		obj := u.Obj
		if u.Orig != nil {
			obj = u.Orig.Obj
		}
		opt := check.PreludeType("Option")
		switch {
		case obj.Pkg == nil || obj.Pkg.Host():
			return goLayout{}, nil, false
		case opt != nil && obj == opt.Obj:
			if l.flatSet[obj] == nil {
				return goLayout{size: 16, align: 8, ssa: true}, nil, true
			}
			some := u.Underlying().(*check.Union).Variants[0]
			s, c, ok := l.layoutOf(some, path, depth+1)
			if c != nil || !ok {
				return goLayout{}, c, ok
			}
			return structLayout([]goLayout{{size: 1, align: 1, ssa: true}, s}), nil, true
		case opt != nil && isOptionVariant(obj, opt.Obj):
			// Some's record; None's is empty.
		case !l.isSourced(obj.Pkg):
			return goLayout{}, nil, false
		}
		if d := u.Defined(); d != nil {
			return l.layoutOf(d, path, depth+1)
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			types := make([]check.Type, len(under.Fields))
			for i, f := range under.Fields {
				types[i] = f.Type
			}
			return fields(types)
		case *check.Union:
			fu := l.flatSet[obj]
			if fu == nil || fu.generic {
				return goLayout{size: 16, align: 8, ssa: true}, nil, true // an interface
			}
			if path[obj] {
				return goLayout{}, obj, true
			}
			path[obj] = true
			defer delete(path, obj)
			lays := []goLayout{{size: 1, align: 1, ssa: true}}
			for k, v := range fu.variants {
				if !fu.slot[k] {
					continue
				}
				s, c, ok := l.layoutOf(v, path, depth+1)
				if c != nil || !ok {
					return goLayout{}, c, ok
				}
				lays = append(lays, s)
			}
			return structLayout(lays), nil, true
		case *check.Interface:
			return goLayout{size: 16, align: 8, ssa: true}, nil, true // an operation type
		}
	}
	return goLayout{}, nil, false
}

// optionShape is the flat form of the prelude's Option, opt: Some, whose
// slot is its record Some[T], then None, with no slot.
func optionShape(opt *check.Named) *flatUnion {
	fu := &flatUnion{tn: opt.Obj, generic: true}
	for _, v := range opt.Underlying().(*check.Union).Variants {
		vn := v.(*check.Named)
		fu.variants = append(fu.variants, vn)
		fu.slot = append(fu.slot, len(vn.Underlying().(*check.Struct).Fields) > 0)
	}
	return fu
}

// nicheAdmitted reports whether every Option of a go.Ref[S] in the program
// can be the Go pointer *S. Go spells one generic Option[T] struct for
// every T, so a go.Ref that a generic declaration other than Option and
// Some takes through a type argument (a generic function, record or
// union, a std function) would meet that struct inside it; and an Option
// or a Some whose type argument holds a go.Ref deeper than at its top
// (Option[Option[go.Ref[S]]]) would share its Go type with another. The
// walk reads every type the program names, as instancesOf does.
func (l *lowerer) nicheAdmitted(opt *check.TypeName) bool {
	some := opt.Type.(*check.Named).Underlying().(*check.Union).Variants[0].(*check.Named)
	someObj := some.Obj
	if some.Orig != nil {
		someObj = some.Orig.Obj
	}
	ok := true
	visited := map[check.Type]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		if !ok || t == nil || visited[t] {
			return
		}
		visited[t] = true
		switch u := t.(type) {
		case *check.Named:
			if _, ref := check.GoRefElem(u); ref {
				return
			}
			obj := u.Obj
			if u.Orig != nil {
				obj = u.Orig.Obj
			}
			for _, a := range u.TArgs {
				switch {
				case !mentionsGoRef(a, map[check.Type]bool{}):
				case obj != opt && obj != someObj:
					ok = false
				default:
					if _, ref := check.GoRefElem(a); !ref {
						ok = false
					}
				}
				walk(a)
			}
			switch s := u.Underlying().(type) {
			case *check.Struct:
				for _, f := range s.Fields {
					walk(f.Type)
				}
			case *check.Union:
				for _, v := range s.Variants {
					walk(v)
				}
			}
			walk(u.Defined())
		case *check.Slice:
			walk(u.Elem)
		case *check.Array:
			walk(u.Elem)
		case *check.Map:
			walk(u.Key)
			walk(u.Elem)
		case *check.Pointer:
			walk(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *check.Union:
			for _, v := range u.Variants {
				walk(v)
			}
		case *check.Signature:
			for _, p := range u.Params {
				walk(p.Type)
			}
			for _, r := range u.Results {
				walk(r)
			}
		}
	}
	for _, p := range append([]*check.Package{l.pkg}, l.libraryPackages()...) {
		info := p.Info
		if info == nil {
			continue
		}
		for _, t := range info.Types {
			walk(t)
		}
		for _, c := range info.Calls {
			walk(c.Type)
		}
		for _, in := range info.Instances {
			if preludeAppend(in.Func) || preludeMapWrite(in.Func) {
				// Their Go bodies hold a T only as an element (runtime/v1
				// Append, Put, Delete), never in an Option.
				continue
			}
			for _, a := range in.TypeArgs {
				if mentionsGoRef(a, map[check.Type]bool{}) {
					return false
				}
			}
		}
		for _, o := range info.Defs {
			switch o := o.(type) {
			case *check.Var:
				walk(o.Type)
			case *check.Func:
				if o.Sig != nil {
					walk(o.Sig)
				}
			case *check.Field:
				walk(o.Type)
			}
		}
		for _, pt := range info.Patterns {
			walk(pt.Type)
		}
		for _, s := range info.Literals {
			walk(s)
		}
	}
	return ok
}

// mentionsGoRef reports whether t names a go.Ref.
func mentionsGoRef(t check.Type, seen map[check.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	switch u := t.(type) {
	case *check.Named:
		if _, ok := check.GoRefElem(u); ok {
			return true
		}
		for _, a := range u.TArgs {
			if mentionsGoRef(a, seen) {
				return true
			}
		}
		if u.Orig == nil {
			if s, ok := u.Underlying().(*check.Struct); ok {
				for _, f := range s.Fields {
					if mentionsGoRef(f.Type, seen) {
						return true
					}
				}
			}
		}
		return mentionsGoRef(u.Defined(), seen)
	case *check.Slice:
		return mentionsGoRef(u.Elem, seen)
	case *check.Array:
		return mentionsGoRef(u.Elem, seen)
	case *check.Map:
		return mentionsGoRef(u.Key, seen) || mentionsGoRef(u.Elem, seen)
	case *check.Pointer:
		return mentionsGoRef(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if mentionsGoRef(e, seen) {
				return true
			}
		}
	case *check.Signature:
		for _, p := range u.Params {
			if mentionsGoRef(p.Type, seen) {
				return true
			}
		}
		for _, r := range u.Results {
			if mentionsGoRef(r, seen) {
				return true
			}
		}
	}
	return false
}

// nicheElem is S's go.Ref for t, an Option of a go.Ref[S] that the niche
// makes the Go pointer itself; ok false for any other type.
func (l *lowerer) nicheElem(t *check.Named) (check.Type, bool) {
	if t.Orig == nil || len(t.TArgs) != 1 {
		return nil, false
	}
	opt := check.PreludeType("Option")
	if opt == nil || t.Orig.Obj != opt.Obj {
		return nil, false
	}
	if _, ok := check.GoRefElem(t.TArgs[0]); !ok {
		return nil, false
	}
	if l.flatUnionSet()[opt.Obj] == nil || !l.niche {
		return nil, false
	}
	return t.TArgs[0], true
}

// basicSize is the size and alignment of a basic type's Go form on a
// 64-bit target.
func basicSize(k check.BasicKind) (size, align int) {
	switch k {
	case check.Bool, check.Int8, check.Uint8:
		return 1, 1
	case check.Int16, check.Uint16:
		return 2, 2
	case check.Int32, check.Uint32, check.Float32:
		return 4, 4
	case check.String, check.Complex128:
		return 16, 8
	case check.Complex64:
		return 8, 4
	}
	return 8, 8
}

// roundUp is n rounded up to a multiple of a.
func roundUp(n, a int) int { return (n + a - 1) / a * a }

// flatUnionOf is the flat form of t, a union or a type defined over one;
// nil when t lowers otherwise.
func (l *lowerer) flatUnionOf(t check.Type) *flatUnion {
	for i := 0; i < 64; i++ {
		n, ok := t.(*check.Named)
		if !ok {
			return nil
		}
		if fu := l.flatUnionSet()[n.Obj]; fu != nil {
			switch {
			case n.Orig == nil:
				return fu
			case fu.generic:
				c := fu.instance(n)
				_, c.niche = l.nicheElem(n)
				return c
			}
		}
		d := n.Defined()
		if d == nil {
			return nil
		}
		t = d
	}
	return nil
}

// isSum reports a type whose values a match tells apart by variant: a Go
// interface or a flat union.
func (fc *funcCtx) isSum(t check.Type) bool {
	return fc.isInterface(t) || fc.l.flatUnionOf(t) != nil
}

// tagOfGo is the tag of the variant whose Go type is goType, or -1.
func (fu *flatUnion) tagOfGo(tc *typeCtx, goType string) int {
	for k, v := range fu.variants {
		if tc.goType(v) == goType {
			return k
		}
	}
	return -1
}

// tagOf is the tag of the variant t, or -1.
func (fu *flatUnion) tagOf(t check.Type) int {
	n, ok := t.(*check.Named)
	if !ok {
		return -1
	}
	for k, v := range fu.variants {
		if v.Obj == n.Obj {
			return k
		}
	}
	return -1
}

// slotName is the Go field of variant k's slot.
func slotName(k int) string { return "v" + strconv.Itoa(k) }

// ofName is the Go method that puts variant k into the union.
func ofName(k int) string { return "of" + strconv.Itoa(k) }

// flatDecl writes the union's Go type and its of methods; tparams is the
// Go type parameter list of a generic union, "" otherwise.
func (l *lowerer) flatDecl(b interface{ WriteString(string) (int, error) }, td *typeDecl, fu *flatUnion, tc *typeCtx, tparams string) {
	b.WriteString("type " + td.name + tparams + " struct {\n\ttag uint8\n")
	self := td.name
	if len(td.tparams) > 0 {
		names := make([]string, len(td.tparams))
		for i, tp := range td.tparams {
			names[i] = tc.params[tp]
		}
		self += "[" + strings.Join(names, ", ") + "]"
	}
	for k, v := range fu.variants {
		if fu.slot[k] {
			b.WriteString("\t" + slotName(k) + " " + tc.goType(v) + "\n")
		}
	}
	b.WriteString("}\n\n")
	for k, v := range fu.variants {
		tag := k
		if unionMutant == "sig-tag" {
			tag = (k + 1) % len(fu.variants)
		}
		param, slot := "_", ""
		if fu.slot[k] {
			param, slot = "v", ", "+slotName(k)+": v"
		}
		b.WriteString("func (" + self + ") " + ofName(k) + "(" + param + " " + tc.goType(v) + ") " + self + " { return " + self + "{tag: " + strconv.Itoa(tag) + slot + "} }\n")
	}
	b.WriteString("\n")
}

// flatValue is val, a value of variant k, put into the flat union fu whose
// Go type, or a type defined over it, is goType.
func (fu *flatUnion) flatValue(tc *typeCtx, goType string, k int, val string) string {
	var own string
	switch {
	case fu.niche:
		tc.l.stats.NicheOptions++
		own = tc.goType(fu.inst)
		// Some's pointer, or nil for None.
		v := "(" + own + ")(nil)"
		if fu.slot[k] {
			v = "(" + val + ")." + memberName("Value")
		}
		if goType != own {
			v = goType + "(" + v + ")"
		}
		return v
	case fu.inst != nil:
		own = tc.goType(fu.inst)
		tc.l.stats.FlatOptions++
	default:
		own = tc.goType(fu.tn.Type)
		tc.l.stats.FlatUnions++
	}
	// The literal is parenthesized: the value may stand in an if or a
	// switch header, where Go parses a bare T{} as the block.
	v := "(" + own + "{})." + ofName(k) + "(" + val + ")"
	if goType != own {
		v = goType + "(" + v + ")"
	}
	return v
}

// flatNarrow is the variant k of val, a flat value that holds it.
func (fu *flatUnion) flatNarrow(tc *typeCtx, val string, k int) string {
	if !fu.slot[k] || unionMutant == "sig-payload" {
		return tc.goType(fu.variants[k]) + "{}"
	}
	if fu.niche {
		return tc.goType(fu.variants[k]) + "{" + memberName("Value") + ": " + val + "}"
	}
	return val + "." + slotName(k)
}

// tagExpr is the Go expression of val's tag: its tag field, or for a
// niche Option the tag its pointer stands for (0 Some, 1 None).
func (fu *flatUnion) tagExpr(tc *typeCtx, val string) string {
	if fu.niche {
		return tc.l.nilTagHelper() + "(" + val + ")"
	}
	return val + ".tag"
}

// test is the Go test that val holds variant k.
func (fu *flatUnion) test(val string, k int) string {
	if unionMutant == "sig-test" {
		return "(" + val + ".tag < 255)"
	}
	if fu.niche {
		if fu.slot[k] {
			return "(" + val + " != nil)"
		}
		return "(" + val + " == nil)"
	}
	return "(" + val + ".tag == " + strconv.Itoa(k) + ")"
}

// head is the head of a switch on val's tag.
func (fu *flatUnion) head(tc *typeCtx, val string) string {
	n := len(fu.variants)
	if unionMutant == "sig-switch" && n > 1 && !fu.niche {
		return "switch (" + val + ".tag + 1) % " + strconv.Itoa(n) + " {"
	}
	return "switch " + fu.tagExpr(tc, val) + " {"
}

// nilTagHelper is the generic function that gives a niche Option's tag:
// 1 (None) for the nil pointer, 0 (Some) for any other. Go inlines it.
func (l *lowerer) nilTagHelper() string {
	key := pkgKey{"niche tag", nil}
	name := l.pkgName(key, "ergoNilTag")
	if !l.texts[key] {
		l.texts[key] = true
		l.gen.WriteString("func " + name + "[T comparable](v T) uint8 {\n\tvar zero T\n\tif v == zero {\n\t\treturn 1\n\t}\n\treturn 0\n}\n\n")
	}
	return name
}

// tagSwitch is how a switch on a subject over Go types reads its tags: the
// tag of each type, the switch's head and the variant each case binds.
type tagSwitch struct {
	tags    []int
	head    string
	variant func(k int) string
}

// tagSwitchOf is the tag switch on subject, of the checked type t, over the
// Go types types: the flat state of a machine's flat step (machinerun.go)
// or a flat union; nil for a Go type switch. subject must be a Go name or
// a selector of one: the cases read it again.
func (fc *funcCtx) tagSwitchOf(subject string, t check.Type, types []string, site syntax.Node) *tagSwitch {
	if tags, ok := fc.flatTags(subject, types); ok {
		return &tagSwitch{tags: tags, head: fc.flatSwitch(), variant: fc.flatVariant}
	}
	fu := fc.l.flatUnionOf(t)
	if fu == nil {
		return nil
	}
	tc := &fc.typeCtx
	tags := make([]int, len(types))
	for i, g := range types {
		k := fu.tagOfGo(tc, g)
		if k < 0 {
			flatBail(site, fu)
		}
		tags[i] = k
	}
	return &tagSwitch{tags: tags, head: fu.head(tc, subject), variant: func(k int) string { return fu.flatNarrow(tc, subject, k) }}
}

// flatVariantValue is val, of the variant type from, put into the flat
// union to, when to is one and from is its variant; ok false when to is
// not flat. A from that is no variant of to bails.
func (fc *funcCtx) flatVariantValue(from, to check.Type, val string, site syntax.Node) (string, bool) {
	fu := fc.l.flatUnionOf(to)
	if fu == nil {
		return val, false
	}
	tc := &fc.typeCtx
	if tc.goType(from) == tc.goType(to) {
		return val, true
	}
	k := fu.tagOf(from)
	if k < 0 {
		flatBail(site, fu)
	}
	return fu.flatValue(tc, tc.goType(to), k, val), true
}

// The Options the lowering builds from Go values (a Map read, a std or Go
// comma-ok, a nil Go result, a machine's After and Work) are spelled by
// these three helpers, in either form of Option.

// optionSome is the Go value of the Option instance t that holds val, a
// value of its T.
func (fc *funcCtx) optionSome(t check.Type, val string, site syntax.Node) string {
	some, _ := optionVariants(t, site)
	if fu := fc.l.flatUnionOf(t); fu != nil && fu.niche {
		// The pointer is the Option (val is a go.Ref, never nil).
		fc.l.stats.NicheOptions++
		if g := fc.goType(t); g != fc.goType(fu.inst) {
			return g + "(" + val + ")"
		}
		return val
	}
	v := fc.goType(some) + "{" + memberName("Value") + ": " + val + "}"
	if fu := fc.l.flatUnionOf(t); fu != nil {
		return fu.flatValue(&fc.typeCtx, fc.goType(t), fu.tagOf(some), v)
	}
	return v
}

// optionNone is the Go value None of the Option instance t.
func (fc *funcCtx) optionNone(t check.Type, site syntax.Node) string {
	_, none := optionVariants(t, site)
	v := fc.goType(none) + "{}"
	if fu := fc.l.flatUnionOf(t); fu != nil {
		return fu.flatValue(&fc.typeCtx, fc.goType(t), fu.tagOf(none), v)
	}
	return v
}

// optionSomeIf is the init and condition of a Go if that holds when o, of
// the Option instance t, is a Some, and binds w to that Some record.
func (fc *funcCtx) optionSomeIf(t check.Type, o, w string, site syntax.Node) string {
	some, _ := optionVariants(t, site)
	if fu := fc.l.flatUnionOf(t); fu != nil {
		k := fu.tagOf(some)
		return w + " := " + fu.flatNarrow(&fc.typeCtx, o, k) + "; " + fu.test(o, k)
	}
	return w + ", ok := " + o + ".(" + fc.goType(some) + "); ok"
}
