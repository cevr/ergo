package check

import (
	"go/constant"
	"sort"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The Info contract (check-v1 spec, "Interface to lower-v1"). The lowering
// reads only Check's result and the syntax tree: it never re-derives a
// type, a row or a resolution, and a package with any diagnostic is not
// lowered. In a package with no diagnostic:
//
//  1. Complete: every expression in a body or a package let has a Types
//     entry, every identifier a Defs or Uses entry, every call a Calls
//     entry, every pattern a Patterns entry, every match and catch a
//     Matches entry, every for-in a Loops entry and every with a Withs
//     entry, whose Entries hold its named wire's providers of what its
//     block needs, with their conversions; the wire itself holds the rest
//     (Wire.Provider, Wire.Overrides; TestInfoComplete). A package name before a dot and a
//     built-in callee are no values and have no Types entry. An inline
//     wire value's entries are stage E's (196x).
//  2. Final: no type in Info is invalid or holds a row variable; a type
//     parameter appears only inside its own generic declaration. The one
//     exception is go/types': the name in an explicit instantiation
//     Fn[int] keeps its generic type (round-4 M3).
//  3. Proved: a Match is total, so the lowering adds no default arm except
//     a catch's defect re-panic (rows.md, Catch 6).
//  4. Deterministic: every slice in Package and in Info's values is in
//     source order or by name, never in map order.
//
// Each stage of check-v1 populates the entries of the forms it checks;
// stage A populates every form the merge-round checker types.
//
// Tree names: the tree keeps parse-v1's desugared nodes (spec D-A8, Q8).
// A syntax.VarDecl is a `let`; a SwitchStmt is a `match` on literal
// patterns; a TypeSwitchStmt is any other `match`; a CallExpr whose Fun is
// the identifier `with` is a `with` block; a WireDecl is a package-level
// wire `let`; a RangeStmt is a `for … in`; a ForStmt is a `for` on a
// condition.
//
// Stage B forms (all additive):
//   - A bare record with no fields in value position, `return Pending` or
//     the receiver of `Runner.Each(f)`, is an Ident (or a pkg.T selector)
//     whose Uses entry is the record's *TypeName and whose Types entry is
//     the record type: the lowering writes Pending{}.
//   - An inline variant `Declined{ Reason string }` is a *TypeName in
//     Package.Types, after its union, whose Decl is a TypeDecl the checker
//     builds: its Name is the VariantSpec's (with a Defs entry), its Spec a
//     StructType over the VariantSpec's fields, and its TypeParams the
//     union's parameters the fields mention, as new identifiers that are
//     not in the tree (no Defs entry).
//   - A defined type `type N T` is a *Named whose Underlying is T's and
//     whose Defined is T; a record or union has a nil Defined.
//   - Every pattern node of a match, catch, let or for has a Patterns entry
//     (a ParenExpr shares its inner node's), and a binding's Var is its
//     Defs entry. A package-level `let P = e` defines each name of P as a
//     package *Var.
//     The frozen tree stores a local let's single bare name in VarSpec.Names
//     even when it tests a known type or constant (VarSpec.Pattern is nil).
//     In that case Info.Patterns holds its pattern, Uses names the referred
//     object, and Defs has no binder. Lowering reads that pattern first.
//   - A tuple value `(a, b)` and a tuple type have the *Tuple type; a
//     function's one result may be a tuple, and `let q, r = f()`
//     destructures it.

// Package is a checked package.
type Package struct {
	Path  string
	Name  string
	Scope *Scope
	// Wires is the wire namespace: wires are named only by with and by
	// wire embedding, so they do not collide with types (fixture with-name).
	Wires map[string]*Wire
	// Funcs is every function, extern included, in source order;
	// same-named functions (196m rule 3) keep separate *Func.
	Funcs []*Func
	// Types is every declared type in source order: records, operation
	// types, unions and defined types.
	Types []*TypeName
	// Tests is every test block with its parameters (stage E, 196l).
	Tests []*Test
	// Info is the checker's result for the lowering.
	Info *Info
	// ErrorTypes are the struct types this package names in an error row
	// (types.md, Error types 1); returning a value of one is a failure.
	ErrorTypes map[*Named]bool

	qualify bool // print its types with the package name
	files   []*fileInfo
	// collFuncs are the prelude's functions on slices and Maps by kind and
	// name (D-D6, D-D8), which x.f(…) reaches from a value of exactly an
	// unnamed slice or Map type; nil in every other package.
	collFuncs map[collKey]*Func
	// providerFresh is the freshness fact the wire ownership pass computed
	// during Check, kept so its tests read the fact that decided.
	providerFresh providerFreshness
}

// collKey names a prelude function on a slice (isMap false) or a Map.
type collKey struct {
	name  string
	isMap bool
}

// Info records the checker's results for the lowering (#197, #50).
type Info struct {
	// Types is every expression's final type; a literal carries its one
	// type (196u). A signed literal is one literal: `-128` where int8 is
	// expected has Types and Values entries (int8, -128) on the signed
	// expression, and the literal 128 under the sign, with any parentheses
	// around it, has none, so the lowering emits int8(-128) and never
	// int8(128).
	Types map[syntax.Expr]Type
	// Values is every constant expression's exact value, already checked
	// against its type.
	Values map[syntax.Expr]constant.Value
	// Defs is every declaring identifier: declarations, parameters, type
	// parameters, let, pattern, as, case err and loop bindings.
	Defs map[*syntax.Ident]Object
	// Uses is every referring identifier.
	Uses map[*syntax.Ident]Object
	// Calls is every call.
	Calls map[*syntax.CallExpr]*Call
	// Closes is a resolved R1 Close identity at a declaration, a function
	// reference, an operation selector or a call. An entry proves a method
	// with no operands or results and an empty failure row (R2). A field
	// called Close and an unrelated function or method have no entry.
	// Added by #199 stage 3a; general resource ownership remains #197's.
	Closes map[syntax.Node]*Close
	// Implicit is every implicit conversion at an assignment site, keyed
	// by the assigned expression. The checker's one assignability relation
	// produces it with the clause that admitted the value (types.md,
	// Assignability); the lowering never derives a conversion again.
	Implicit map[syntax.Expr]Conversion
	// ImplicitElems is, for a value of several elements that a multi-name
	// let (`let a, b Store = pair()`) or a return of several results takes
	// apart, each element's implicit conversion in order, a zero
	// Conversion where an element makes none. A value is listed only when
	// one of its elements makes a conversion. Added in check-v1 stage D3a
	// (additive).
	ImplicitElems map[syntax.Expr][]Conversion
	// Patterns is every pattern of a match, catch, let and for.
	Patterns map[syntax.Expr]*Pattern
	// Matches is every match (SwitchStmt, TypeSwitchStmt) and catch
	// (CatchExpr).
	Matches map[syntax.Node]*Match
	// Tails is every block-final expression statement whose value is the
	// block's value, with that type; an absent statement is discarded.
	Tails map[syntax.Stmt]Type
	// Literals is every function literal and trailing block, with its
	// final rows (196a).
	Literals map[syntax.Node]*Signature
	// Loops is every for-in.
	Loops map[*syntax.RangeStmt]*Loop
	// Withs is every with site.
	Withs map[*syntax.CallExpr]*With
	// InitOrder is every package-level let with an initializer, in Go's
	// initialization order (go/types Info.InitOrder): a let comes after
	// every let its initializer refers to, directly or through functions,
	// and otherwise in declaration order. The lowering runs them in this
	// order inside the program's root task (stage C round 2 review m1).
	InitOrder []*Initializer
	// TaskResults is every function of the package whose result type
	// holds a Task (check-v1 stage D3a, Task escape): a Task leaves its
	// function only by return, so the lowering can pass the caller's
	// scope to such a function as a hidden destination. Added in stage
	// D3a (additive).
	TaskResults map[*Func]bool
	// Footprints is every frame's footprint (rows.md, Footprints; #199
	// stage 1): each function with a body (*syntax.FuncDecl), function
	// literal (*syntax.FuncLit), trailing block of a call other than with
	// (*syntax.BlockStmt; a with block runs in its enclosing frame), named
	// wire (*syntax.WireDecl, the builder a with of it runs) and package
	// let (*syntax.VarSpec). The lowering places interruption points only
	// in a frame whose footprint is not Empty (runtime.md, I1) and never
	// derives a footprint again. Added for #199 stage 1 (additive).
	Footprints map[syntax.Node]Footprint
}

// Initializer is one package-level initializer: Vars are the names it
// binds (several for a let with a pattern or `let q, r = e`; a name `_`
// is a Var with no Defs entry) and Value its initializer expression.
type Initializer struct {
	Vars  []*Var
	Value syntax.Expr
}

func newInfo() *Info {
	return &Info{
		Types:         map[syntax.Expr]Type{},
		Values:        map[syntax.Expr]constant.Value{},
		Defs:          map[*syntax.Ident]Object{},
		Uses:          map[*syntax.Ident]Object{},
		Calls:         map[*syntax.CallExpr]*Call{},
		Closes:        map[syntax.Node]*Close{},
		Implicit:      map[syntax.Expr]Conversion{},
		ImplicitElems: map[syntax.Expr][]Conversion{},
		Patterns:      map[syntax.Expr]*Pattern{},
		Matches:       map[syntax.Node]*Match{},
		Tails:         map[syntax.Stmt]Type{},
		Literals:      map[syntax.Node]*Signature{},
		Loops:         map[*syntax.RangeStmt]*Loop{},
		Withs:         map[*syntax.CallExpr]*With{},
		TaskResults:   map[*Func]bool{},
		Footprints:    map[syntax.Node]Footprint{},
	}
}

// CallKind is what a call calls.
type CallKind int

const (
	// CallFunc calls a declared function, Func; a dot call x.f(…) (196m)
	// has Recv, lowered as the first argument.
	CallFunc CallKind = iota
	// CallOp calls an operation of an operation-type value: Op on Recv.
	CallOp
	// CallProviderOp calls an operation of a capability, Op, on the
	// provider the nearest with binds (types.md, Capabilities 2).
	CallProviderOp
	// CallFuncValue calls a function value.
	CallFuncValue
	// CallConversion converts its one argument to Type.
	CallConversion
	// CallLen is the len built-in.
	CallLen
	// CallWith is a with block; Info.Withs holds its site.
	CallWith
	// CallPrelude is not produced since check-v1 stage D3a: a prelude dot
	// call, xs.Append(x), m.Get(k) or m.Delete(k), is a CallFunc with Recv
	// and the full TypeArgs (D-D6, D-D8). The constant stays for compatibility;
	// TypeArgs changed meaning for generic dot calls in stage D3a.
	CallPrelude
	// CallKindCount bounds the call kinds. Keep it last: exhaustive
	// consumers must account for a newly admitted kind.
	CallKindCount
)

// Call is one call site.
type Call struct {
	Kind CallKind
	// Func is the called function (CallFunc).
	Func *Func
	// Op is the called operation of an operation type or capability
	// (CallOp, CallProviderOp).
	Op *Method
	// Name named a CallPrelude's library function; empty since stage D3a.
	Name string
	// Recv is the receiver of a dot call or an operation call; nil
	// otherwise.
	Recv syntax.Expr
	// TypeArgs are the type arguments of a generic function, written,
	// inferred or bound by a dot call's receiver (D-D8), one for each of
	// its type parameters, in its parameter order. Changed meaning in D3a:
	// formerly a generic dot call recorded only the remaining parameters.
	TypeArgs []Type
	// Type is a conversion's target type.
	Type Type
	// Fails and Needs are the callee's error and capability rows at this
	// site, after substitution. A with's Fails is the row its block's
	// tails pass through (rows.md, With 6).
	Fails, Needs *Row
	// TaskResult reports that the call's result holds a Task (CallFunc and
	// CallFuncValue, including a call of a function literal's value): the
	// caller needs a frame scope and passes it as the callee's hidden
	// destination (stdlib.md, Hidden arguments). Derived from the same
	// holdsTask as TaskResults. Added in check-v1 stage D3b (additive).
	TaskResult bool
}

// ConversionKind is an implicit conversion's kind.
type ConversionKind int

const (
	// ConvVariant puts a variant into its union.
	ConvVariant ConversionKind = iota + 1
	// ConvOperations puts a value into an operation type; Table maps each
	// operation name to the function that implements it.
	ConvOperations
	// ConvRow puts an error row's value into a union.
	ConvRow
)

// Conversion is an implicit conversion at an assignment site: From's value
// becomes To.
type Conversion struct {
	Kind     ConversionKind
	From, To Type
	Table    map[string]*Func
}

// PatternKind is a pattern's form.
type PatternKind int

const (
	PatWildcard PatternKind = iota + 1 // _
	PatBind                            // a name: Var
	PatTypeTest                        // a type: Type
	PatRecord                          // T{F: p, …}: Type and Fields
	PatConst                           // a literal: Value of Type
	PatTuple                           // (p, q): Elems
	PatAs                              // p as x: Elems[0] and Var
)

// Pattern is one pattern node.
type Pattern struct {
	Kind   PatternKind
	Var    *Var
	Type   Type
	Value  constant.Value
	Fields []FieldPattern
	Elems  []*Pattern
}

// FieldPattern is one field of a record pattern.
type FieldPattern struct {
	Name    string
	Pattern *Pattern
}

// Match is one match or catch.
type Match struct {
	// Subjects are the subject types: one, or a tuple's elements; a catch's
	// subjects are its error row's elements.
	Subjects []Type
	// Arms counts the arms, a case _ included.
	Arms int
	// Result is the match's value type, nil for a statement.
	Result Type
	// Partial marks try … catch: the failures no arm catches escape.
	Partial bool
}

// LoopDomain is what a for-in iterates (D-D4): a slice, a Map, a prelude
// Range[T] over an integer T, or a prelude Seq[T].
type LoopDomain int

const (
	LoopSlice LoopDomain = iota + 1
	LoopMap
	LoopRange
	LoopSeq
)

// Loop is one for-in.
type Loop struct {
	Domain LoopDomain
	// Elem is the element type, Pos the position type (an index or a Map
	// key); Pos is nil when there is none.
	Elem, Pos Type
	// Bindings are the loop's binding objects, in source order.
	Bindings []*Var
}

// With is one with site. Its size is bounded by its body, never by its
// wire (round-4 review M1): the wire's resolved bindings are shared, and a
// with points to them through Wires.
type With struct {
	// Wires are the named wires bound, in nesting order; an inline wire
	// value's entries are stage E's (196x). Wires[0] is the shared,
	// resolved wire: Wire.Provider and Wire.EachProvider read every
	// capability it binds, Wire.Overrides what its replaces overrode.
	Wires []*Wire
	// Discharged are the capabilities the block's row needs that this with
	// discharges, by name: each charge of the block that no inner with
	// discharged (rows.md, With 2 and 3). A capability the wire binds and
	// the block never needs is not listed.
	Discharged []Type
	// Entries holds, for a with of a named wire, one entry per discharged
	// capability: Entries[i] is the provider entry that binds
	// Discharged[i] once replace and embedding are resolved (rows.md,
	// Wire 7), Wires[0].Provider(Discharged[i]). An entry of several
	// capabilities appears at each of its capabilities; its conversion for
	// Discharged[i] is the one at that capability's index in its Caps.
	// Empty for an inline wire value (stage E, 196x).
	//
	// The release needs of an inner with's acquisitions are needs of the
	// block too: a release runs in the environment outside the inner with,
	// so this with discharges each one it binds (withOwnership).
	Entries []*WireEntry
	// Acquired are the provider entries this with builds and owns, in
	// their acquisition order (resources.md, Wire ownership; withown.go):
	// the entries of Entries, and each entry whose capability an acquired
	// provider uses while it is built. Each entry is one acquisition,
	// however many capabilities it binds. An overridden entry and an entry
	// that nothing the block needs reaches are not acquired. Empty for an
	// inline wire value (stage E, 196x).
	Acquired []*Acquisition
}

// Acquisition is one provider entry a with of a named wire builds: the
// entry, the indexes of the capabilities in Entry.Caps it still binds in
// the with's wire, and its provider's resolved Close (Info.Closes), nil
// when the provider is not a resource. The with releases each resource
// once, in reverse acquisition order, on every exit of its block.
type Acquisition struct {
	Entry *WireEntry
	Caps  []int
	Close *Close
}

// Override is a binding a replace overrode (Wire.Overrides): Entry no
// longer binds Cap, By does. The overridden entry is not built for that
// capability; an entry of several capabilities that still binds another
// one is built for it.
type Override struct {
	Cap       Type
	Entry, By *WireEntry
}

// WireEntry is one entry of a named wire, as written: a provider for
// capabilities, or an embedded wire.
type WireEntry struct {
	// Replace marks a `replace Cap: provider` entry.
	Replace bool
	// Caps are the capabilities the entry provides, in key order; none for
	// an embedding.
	Caps []Type
	// Provider is the provider expression; nil for an embedding.
	Provider syntax.Expr
	// Conversions put the provider into each capability, in Caps order: a
	// ConvOperations table, or the zero Conversion when the provider's type
	// is the capability itself. Implicit holds the same conversion at
	// Provider when the entry has one capability.
	Conversions []Conversion
	// Embedded is the embedded wire; nil for a provider entry.
	Embedded *Wire
	// Uses are the capabilities of its wire that the provider uses while
	// it is built (rows.md, Wire 4), as a row's elements: a with that
	// acquires the entry acquires their providers first.
	Uses []Type

	// seq orders a package's named wire entries: the wire pass meets the
	// wires by name, then their entries in source order (wirePass.own).
	seq int
}

// Test is a test block (stage E, 196l).
type Test struct {
	Name   string
	Decl   *syntax.TestDecl
	Params []*Var
}

// A type parameter is an Object, so Defs records its declaring name.
func (o *TypeParam) name() string    { return o.Name }
func (o *TypeParam) pos() syntax.Pos { return o.Pos }

// def records a declaring identifier (Info.Defs).
func (c *checker) def(id *syntax.Ident, o Object) {
	if c.silent == 0 && id != nil && id.Name != "_" && !c.synthetic[id] {
		c.pkg.Info.Defs[id] = o
	}
}

// callInfo is x's Calls entry, made on first use; under silent checking it
// is a scratch entry that Info never sees.
func (c *checker) callInfo(x *syntax.CallExpr) *Call {
	if c.silent > 0 {
		return &Call{}
	}
	call := c.pkg.Info.Calls[x]
	if call == nil {
		call = &Call{}
		c.pkg.Info.Calls[x] = call
	}
	return call
}

// recordCall fills x's Calls entry after its callee resolved: the kind,
// what it calls and the receiver of a dot call. The rows came from result.
func (c *checker) recordCall(x *syntax.CallExpr, fun operand) {
	call := c.callInfo(x)
	sel, _ := unparen(x.Fun).(*syntax.SelectorExpr)
	if ix, ok := unparen(x.Fun).(*syntax.IndexExpr); ok {
		sel, _ = unparen(ix.X).(*syntax.SelectorExpr)
	}
	recv := func() syntax.Expr {
		if sel == nil {
			return nil
		}
		if id, ok := sel.X.(*syntax.Ident); ok {
			if _, isPkg := c.pkg.Info.Uses[id].(*PkgName); isPkg {
				return nil // a qualified name pkg.f, not a dot call
			}
		}
		return sel.X
	}
	switch {
	case fun.mode == modeType:
		call.Kind, call.Type = CallConversion, fun.typ
	case fun.mode == modeBuiltin:
		call.Kind = CallLen
	case fun.mode == modeCapFn:
		call.Kind, call.Op = CallProviderOp, fun.method
	case fun.fn != nil:
		call.Kind, call.Func, call.Recv = CallFunc, fun.fn, recv()
		if fun.mode == modeValue && len(fun.self) > 0 {
			// x binds every type parameter of f (D-D8); a call that
			// infers the rest records them all in callGeneric.
			call.TypeArgs = fullTypeArgs(fun.fn, fun.self, nil)
		}
	case fun.method != nil:
		call.Kind, call.Op, call.Recv = CallOp, fun.method, recv()
	default:
		call.Kind = CallFuncValue
	}
}

// recordWith records a with site: the named wire, and the capabilities its
// block needed from it with their entries, which the block's charges found
// (withFrame), sorted by name. It reads nothing of the wire's summary, so
// its work and its size are the block's.
func (c *checker) recordWith(x *syntax.CallExpr, f *withFrame) {
	if c.silent > 0 {
		return
	}
	w := &With{}
	if len(x.Args) == 1 {
		if id, ok := unparen(x.Args[0]).(*syntax.Ident); ok && c.pkg.Wires[id.Name] != nil {
			w.Wires = []*Wire{c.pkg.Wires[id.Name]}
		} else if sel, ok := unparen(x.Args[0]).(*syntax.SelectorExpr); ok {
			if id, ok := sel.X.(*syntax.Ident); ok {
				if pn, ok := c.lookup(id.Name).(*PkgName); ok && !pn.broken && isExported(sel.Sel.Name) {
					if wire := pn.Pkg.Wires[sel.Sel.Name]; wire != nil {
						w.Wires = []*Wire{wire}
					}
				}
			}
		}
	}
	if !f.unknown && len(f.caps) > 0 {
		order := make([]int, len(f.caps))
		keys := make([]capKey, len(f.caps))
		for i, cap := range f.caps {
			order[i] = i
			keys[i], _ = keyOf(cap)
		}
		sort.Slice(order, func(a, b int) bool { return keys[order[a]].compare(keys[order[b]]) < 0 })
		for _, i := range order {
			w.Discharged = append(w.Discharged, f.caps[i])
			if w.Wires != nil {
				w.Entries = append(w.Entries, f.entries[i])
			}
		}
	}
	c.pkg.Info.Withs[x] = w
	c.callInfo(x).Kind = CallWith
	f.site = x
	c.withSites = append(c.withSites, withSite{
		x: x, file: c.cur, t: c.fn.t, outer: append([]*withFrame(nil), c.fn.t.withs...),
	})
}

// Provider returns the entry that binds cap in a with of w, once replace
// and embedding are resolved (rows.md, Wire 7), or nil when w binds no
// cap. It reads the wire's summary, shared with every wire that embeds w
// and every with of it (wiresum.go): a lookup reads at most the summary's
// height, O(log n) of the capabilities w reaches. Its conversion for cap is
// the one at cap's index in the entry's Caps.
func (w *Wire) Provider(cap Type) *WireEntry {
	k, ok := keyOf(cap)
	if !ok {
		return nil
	}
	if n := w.binds.node(k); n != nil {
		return n.entry
	}
	return nil
}

// EachProvider calls f on each capability w binds, by name, with the entry
// that binds it (Provider), until f returns false.
func (w *Wire) EachProvider(f func(cap Type, e *WireEntry) bool) {
	if w.binds == nil {
		return
	}
	var walk func(n *capNode) bool
	walk = func(n *capNode) bool {
		for n != nil {
			if !walk(n.left) || !f(n.t, n.entry) {
				return false
			}
			n = n.right
		}
		return true
	}
	walk(w.binds.root)
}

// finishInfo resolves the rows Info holds once every body is final, so no
// row variable remains (invariant 2), and without invalid, which is the
// check's mark of an unknown row and no failure a client can name
// (Row.unknown).
func (c *checker) finishInfo() {
	for x, call := range c.pkg.Info.Calls {
		if call.Kind == CallFunc || call.Kind == CallFuncValue {
			if t, ok := c.pkg.Info.Types[x]; ok && t != nil {
				call.TaskResult = c.holdsTask(t)
			}
		}
		if call.Fails != nil {
			call.Fails = c.types.resolve(call.Fails).known(c.types)
		}
		if call.Needs != nil {
			call.Needs = c.types.resolve(call.Needs).known(c.types)
		}
	}
	for x, sig := range c.pkg.Info.Literals {
		if sig.Errors.unknown(c.types) || sig.Uses.unknown(c.types) {
			s := *sig
			s.Errors, s.Uses = sig.Errors.known(c.types), sig.Uses.known(c.types)
			c.pkg.Info.Literals[x] = &s
		}
	}
	for p, name := range c.asNames {
		p.Var, _ = c.pkg.Info.Defs[name].(*Var)
	}
	for _, f := range c.funcs {
		if f.Sig != nil && c.resultsHoldTask(f.Sig) {
			c.pkg.Info.TaskResults[f] = true
		}
	}
	// A function's type keeps its row variables (inference writes the
	// solution beside them); Info holds the solved rows (invariant 2).
	memo := map[Type]Type{}
	for x, t := range c.pkg.Info.Types {
		if r := c.finalType(t, memo); r != t {
			c.pkg.Info.Types[x] = r
		}
	}
}

// finalType is t with every row variable in a function type's rows
// replaced by its solution; t itself when it holds none. Named types are
// not entered: their bodies are declared, with written rows.
func (c *checker) finalType(t Type, memo map[Type]Type) Type {
	if r, ok := memo[t]; ok {
		return r
	}
	out := t
	switch t := t.(type) {
	case *Signature:
		if len(t.TParams) > 0 {
			break // a generic function's own type (an explicit instantiation's name)
		}
		s := *t
		changed := false
		s.Params = make([]*Var, len(t.Params))
		for i, p := range t.Params {
			s.Params[i] = p
			if pt := c.finalType(p.Type, memo); pt != p.Type {
				q := *p
				q.Type = pt
				s.Params[i] = &q
				changed = true
			}
		}
		s.Results = make([]Type, len(t.Results))
		for i, r := range t.Results {
			s.Results[i] = c.finalType(r, memo)
			changed = changed || s.Results[i] != r
		}
		if e := c.types.resolve(t.Errors).known(c.types); e != t.Errors {
			s.Errors, changed = e, true
		}
		if u := c.types.resolve(t.Uses).known(c.types); u != t.Uses {
			s.Uses, changed = u, true
		}
		if changed {
			out = &s
		}
	case *Slice:
		if e := c.finalType(t.Elem, memo); e != t.Elem {
			out = &Slice{Elem: e}
		}
	case *Pointer:
		if e := c.finalType(t.Elem, memo); e != t.Elem {
			out = &Pointer{Elem: e}
		}
	case *Map:
		k, e := c.finalType(t.Key, memo), c.finalType(t.Elem, memo)
		if k != t.Key || e != t.Elem {
			out = &Map{Key: k, Elem: e}
		}
	case *Tuple:
		ts := make([]Type, len(t.Types))
		changed := false
		for i, e := range t.Types {
			ts[i] = c.finalType(e, memo)
			changed = changed || ts[i] != e
		}
		if changed {
			out = &Tuple{Types: ts}
		}
	}
	memo[t] = out
	return out
}

// tail records a block-final expression statement whose value is the
// block's value (Info.Tails).
func (c *checker) tail(s syntax.Stmt, t Type) {
	if c.silent == 0 && t != nil && !isInvalid(t) {
		c.pkg.Info.Tails[s] = t
	}
}

// pattern records a pattern node (Info.Patterns) and returns it.
func (c *checker) pattern(x syntax.Expr, p *Pattern) *Pattern {
	if c.silent == 0 && x != nil {
		c.pkg.Info.Patterns[x] = p
	}
	return p
}

// asPattern records `core as name`: its Var is the binding, filled in by
// finishInfo once the arm has declared it.
func (c *checker) asPattern(x syntax.Expr, core *Pattern, name *syntax.Ident) *Pattern {
	p := c.pattern(x, &Pattern{Kind: PatAs, Elems: []*Pattern{core}})
	if c.silent == 0 {
		if c.asNames == nil {
			c.asNames = map[*Pattern]*syntax.Ident{}
		}
		c.asNames[p] = name
	}
	return p
}

// match records a match or catch (Info.Matches).
func (c *checker) match(n syntax.Node, m *Match) {
	if c.silent == 0 {
		c.pkg.Info.Matches[n] = m
	}
}

// loop records a for-in (Info.Loops).
func (c *checker) loop(s *syntax.RangeStmt, l *Loop) {
	if c.silent == 0 {
		c.pkg.Info.Loops[s] = l
	}
}

// implicit records at x the implicit conversion that assignability chose
// for an assignment (Info.Implicit): a variant or a row value into its
// union, or a value into an operation type with its operations' functions.
// An assignment that makes none records nothing.
func (c *checker) implicit(x syntax.Expr, a assignment) {
	if c.silent > 0 || x == nil || a.conv.Kind == 0 {
		return
	}
	c.pkg.Info.Implicit[x] = a.conv
}

// implicitElems records at x, a value of several elements that a
// multi-name let or a return of several results takes apart, the implicit
// conversion of each element (Info.ImplicitElems), when one of them makes
// one.
func (c *checker) implicitElems(x syntax.Expr, as []assignment) {
	if c.silent > 0 || x == nil {
		return
	}
	converts := false
	for _, a := range as {
		converts = converts || a.conv.Kind != 0
	}
	if !converts {
		return
	}
	convs := make([]Conversion, len(as))
	for i, a := range as {
		convs[i] = a.conv
	}
	c.pkg.Info.ImplicitElems[x] = convs
}

func isUnion(t Type) bool {
	_, ok := under0(t).(*Union)
	return ok
}

// isRowValue reports the type of a `case err:` binding, an anonymous union
// of error types (D-C3).
func isRowValue(t Type) bool {
	_, ok := t.(*Union)
	return ok
}
