package lower

import (
	"fmt"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Flat signal unions (MU8b; runtime.md, Machines and actors 8). A union
// that a machine declaration takes as its signal lowers, where the rule
// below admits it, to a Go struct of a tag and a slot per variant that has
// fields, never a Go interface:
//
//	type Sig struct {
//		tag uint8 // the variant's index in the union's declaration
//		v1  Add   // the value of variant 1; zero unless tag is 1
//	}
//	func (Sig) of0(Increment) Sig { return Sig{tag: 0} }
//	func (Sig) of1(v Add) Sig     { return Sig{tag: 1, v1: v} }
//
// A []Sig is then a slice of small values: a step's match on a signal is a
// switch on a byte read from the slice, as a hand loop over a []byte is,
// where a sealed interface costs a load of its type word and a compare per
// case (union-repr.md, form B and form D, with Ds's layout: each variant's
// record whole in its own slot, so narrowing reads one field).
//
// Every value is built by its variant's of method, so a slot that the tag
// does not name is always zero, and Go's == on the struct is Ergo's ==
// (union-repr.md §7, the zero-slot invariant). A match tests and switches
// on the tag (tagSwitchOf, flatTest) and narrows by reading the slot
// (flatNarrow). A union has no text form of its own, so only an error
// type that holds a flat union, and a test property that takes one, need
// its text, its literal or a generator: those bail (below).
//
// The rule (flatUnionSet), decided once per program, is narrow on purpose:
// the union is a machine declaration's signal type in a package of the
// program's own source, it is not generic and not any machine's state, its
// variants are records that are neither generic nor error types and whose
// fields are all basic types, at most three variants have fields, and the
// struct fits the 32 bytes and 4 fields Go keeps in registers
// (union-repr.md §5). The prelude's Option is flat too (OPT2, the form C
// of union-repr.md §10 by the same struct): one generic Go struct for
// every instance,
//
//	type Option[T any] struct {
//		tag uint8   // 0 Some, 1 None
//		v0  Some[T] // Some's value; zero unless tag is 0
//	}
//
// so an Option that a function returns, a record holds or a slice keeps
// is a value, never a box: a Some of a value that does not fit an
// interface word allocates in form A when it leaves its function. Go
// spells one generic type for every instance, so the rule has no size
// bound for Option; the instances of a large T pay a copy where form A
// pays a box (union-repr.md §10, round 2). A union that a Go extern's
// signature names keeps its interface, the Go view a Go caller expects.
// Any other union keeps
// the sealed interface (form A). A lowering path that meets a flat union
// it does not lower calls flatBail: the whole program is lowered again
// with that union an interface, so a form this file does not cover costs
// speed, never correctness.
//
// The reference is form A for every union: unionLowering "interface", set
// by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.unionLowering=interface.
// TestMachineFlatRunAgrees runs each generated program with flat signals,
// with interface signals and through std's loop, and requires one output.
//
// Prior art: OCaml and ReScript lower a constant constructor to an
// integer and switch on it; Rust lays an enum out as a tag and its
// payloads; Borgo (compiles to Go) emits a struct with a tag and a field
// per variant field and switches on the tag; Gleam on Erlang keeps a
// variant a tagged tuple or a bare atom.
var unionLowering string

// unionMutant breaks one helper of the flat union, for
// TestMachineFlatRunMutants only: "sig-tag" (of writes the tag of the next
// variant: a wrong tag order), "sig-payload" (narrowing reads the zero
// value, not the slot: the payload of no variant), "sig-test" (a tag test
// holds for every variant: a signal another arm should take is accepted
// by the first), "sig-switch" (a switch on the tag takes the next
// variant's case) and "sig-bail" (an error type's text does not bail on a
// flat union it holds). Empty in every build.
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

// flatBail stops the lowering at n, which meets the flat union fu in a form
// this file does not lower: guard records fu, and program lowers the
// program again with fu an interface (lowerAgain). The refusal is an ice
// that names the union, so it is reported as one if a lowering ever kept
// it.
func flatBail(n syntax.Node, fu *flatUnion) {
	defer func() {
		r := recover()
		if ref, ok := r.(refusal); ok {
			ref.flat = fu.tn
			r = ref
		}
		panic(r)
	}()
	ice(n, "the flat union %s meets a form flatunion.go does not lower, so the program is lowered again with it an interface", fu.tn.Name)
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
		if opt := check.PreludeType("Option"); opt != nil && tn == opt.Obj {
			l.stats.OptionBails++
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
	var signals []*check.Named
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
				signals = append(signals, e)
			}
		}
	}
	for _, e := range signals {
		if states[e.Obj] || l.flatOff[e.Obj] {
			continue
		}
		if fu := l.flatShape(e); fu != nil {
			l.flatSet[e.Obj] = fu
		}
	}
	// The prelude's Option, unless a machine takes it as its state or its
	// signal, whose lowerings (machinerun.go) take a state or a signal
	// union apart by its interface.
	if opt := check.PreludeType("Option"); opt != nil && !l.flatOff[opt.Obj] && !states[opt.Obj] {
		signal := false
		for _, e := range signals {
			signal = signal || e.Obj == opt.Obj
		}
		if !signal && !l.optionCycle(opt.Obj) {
			l.flatSet[opt.Obj] = optionShape(opt)
		}
	}
	// A union a Go extern's signature names keeps its Go view.
	for _, p := range l.sourcedPkgs() {
		for _, f := range l.canon.funcs(p) {
			if f.Decl == nil || f.Decl.Extern == nil || f.Sig == nil {
				continue
			}
			for _, v := range f.Sig.Params {
				l.dropMentioned(v.Type, map[check.Type]bool{})
			}
			for _, t := range f.Sig.Results {
				l.dropMentioned(t, map[check.Type]bool{})
			}
		}
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
// union's box. A generic record met again with other type arguments
// counts too (`B[T] { x Option[B[[]T]] }` is Go's instantiation cycle), so
// a rare finite nesting keeps Option an interface: slower, never wrong.
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

// dropMentioned removes from the flat set every union that t names.
func (l *lowerer) dropMentioned(t check.Type, seen map[check.Type]bool) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true
	switch u := t.(type) {
	case *check.Named:
		// Option stays flat: no Go function takes or returns the
		// program's Option, since every extern that names one is an
		// adapter whose Option the lowering builds from Go's (v, ok), a
		// nil or an error (gocall.go: someWhen, goResult).
		obj := u.Obj
		if u.Orig != nil {
			obj = u.Orig.Obj
		}
		if fu := l.flatSet[obj]; fu == nil || !fu.generic {
			delete(l.flatSet, u.Obj)
			delete(l.flatSet, obj)
		}
		for _, a := range u.TArgs {
			l.dropMentioned(a, seen)
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				l.dropMentioned(f.Type, seen)
			}
		case *check.Union:
			for _, v := range under.Variants {
				l.dropMentioned(v, seen)
			}
		}
		if d := u.Defined(); d != nil {
			l.dropMentioned(d, seen)
		}
	case *check.Slice:
		l.dropMentioned(u.Elem, seen)
	case *check.Map:
		l.dropMentioned(u.Key, seen)
		l.dropMentioned(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			l.dropMentioned(e, seen)
		}
	case *check.Signature:
		for _, v := range u.Params {
			l.dropMentioned(v.Type, seen)
		}
		for _, r := range u.Results {
			l.dropMentioned(r, seen)
		}
	}
}

// flatShape is the flat form of the union n, or nil when the rule keeps
// it an interface.
func (l *lowerer) flatShape(n *check.Named) *flatUnion {
	if n.Orig != nil || len(n.TParams) > 0 || n.Obj.Decl == nil || !l.isSourced(n.Obj.Pkg) {
		return nil
	}
	u, ok := n.Underlying().(*check.Union)
	if !ok || len(u.Variants) == 0 || len(u.Variants) > 255 {
		return nil
	}
	if l.errorType(n, l.buildView()) {
		return nil
	}
	fu := &flatUnion{tn: n.Obj}
	// The struct's size as Go lays it out on a 64-bit target: the tag at
	// offset 0, then each slot at its alignment.
	size, align, fields := 1, 1, 1
	for _, v := range u.Variants {
		vn, ok := v.(*check.Named)
		if !ok || vn.Orig != nil || len(vn.TParams) > 0 || l.errorType(vn, l.buildView()) {
			return nil
		}
		st, ok := vn.Underlying().(*check.Struct)
		if !ok || len(st.Fields) > 4 {
			return nil
		}
		vsize, valign := 0, 1
		for _, f := range st.Fields {
			b, ok := f.Type.(*check.Basic)
			if !ok || b.Kind == check.Invalid {
				return nil
			}
			s, a := basicSize(b.Kind)
			vsize = roundUp(vsize, a) + s
			valign = max(valign, a)
		}
		vsize = roundUp(vsize, valign)
		fu.variants = append(fu.variants, vn)
		fu.slot = append(fu.slot, len(st.Fields) > 0)
		if len(st.Fields) > 0 {
			fields++
			size = roundUp(size, valign) + vsize
			align = max(align, valign)
		}
	}
	if roundUp(size, align) > 32 || fields > 4 {
		return nil
	}
	return fu
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
				return fu.instance(n)
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
	if fu.inst != nil {
		own = tc.goType(fu.inst)
		tc.l.stats.FlatOptions++
	} else {
		own = tc.goType(fu.tn.Type)
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
	return val + "." + slotName(k)
}

// flatTest is the Go test that val holds variant k.
func flatTest(val string, k int) string {
	if unionMutant == "sig-test" {
		return "(" + val + ".tag < 255)"
	}
	return "(" + val + ".tag == " + strconv.Itoa(k) + ")"
}

// flatHead is the head of a switch on val's tag.
func flatHead(val string, n int) string {
	if unionMutant == "sig-switch" && n > 1 {
		return "switch (" + val + ".tag + 1) % " + strconv.Itoa(n) + " {"
	}
	return "switch " + val + ".tag {"
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
	return &tagSwitch{tags: tags, head: flatHead(subject, len(fu.variants)), variant: func(k int) string { return fu.flatNarrow(tc, subject, k) }}
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
		return w + " := " + fu.flatNarrow(&fc.typeCtx, o, k) + "; " + flatTest(o, k)
	}
	return w + ", ok := " + o + ".(" + fc.goType(some) + "); ok"
}
