package check

import (
	"fmt"
	"go/constant"
	"sort"
	"strings"

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
//     entry per operand (With.Next), whose Entries hold its context's
//     providers of what its block needs, with their conversions; the context
//     itself holds the rest (Context.Provider, Context.Overrides,
//     Context.Entries, for a named, local or inline context alike;
//     TestInfoComplete). A package name before a dot and a built-in callee
//     are no values and have no Types entry. A local context's let name has
//     its *Context as its Defs entry, and its uses theirs as Uses entries;
//     its literal has no Types entry, as a with's inline literal has none.
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
// the identifier `with` is a `with` block; a ContextDecl is a package-level
// context `let`; a RangeStmt is a `for … in`; a ForStmt is a `for` on a
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

// Package is a checked package. Std reports its std identity.
type Package struct {
	Path  string
	Name  string
	Scope *Scope
	// Contexts is the context namespace: contexts are named only by with and by
	// context embedding, so they do not collide with types (fixture with-name).
	Contexts map[string]*Context
	// Funcs is every function, extern included, in source order;
	// same-named functions (196m rule 3) keep separate *Func.
	Funcs []*Func
	// Types is every declared type in source order: records, operation
	// types, unions and defined types.
	Types []*TypeName
	// Tests is every test block with its parameters (stage E, 196l).
	Tests []*Test
	// Laws is every law of the package in source order, top-level and
	// member, a duplicate name included (stage E3b, 196l; law-top). The
	// run build never runs one; the test build runs them through LawRuns.
	Laws []*Law
	// LawRuns are the laws the package's test build runs or reports
	// (laws.go, rule 7): its own laws and the laws of its imports that it
	// is the least package to see bound to a provider.
	LawRuns []LawRun
	// Info is the checker's result for the lowering.
	Info *Info
	// ErrorTypes are, by the owner of the body whose error row names them,
	// the struct types this package names in an error row (types.md, Error
	// types 1; owners.go); returning a value of one is a failure in a body
	// that sees the owner. IsErrorType reads them.
	ErrorTypes map[FactOwner]map[*Named]bool

	qualify bool // print its types with the package name
	// std marks the prelude and each package the std importer serves (C30,
	// check-v1 stage E4). Only prelude and stdImporter.Import set it, before
	// the package is checked; Std reads it.
	std bool
	// host is the binding a Go package is read from (gobinding.go); nil
	// for an Ergo package.
	host *hostPkg
	// owners holds the owner of every node of a body the program does not
	// own (owners.go); OwnerOf reads it.
	owners map[syntax.Node]FactOwner
	files  []*fileInfo
	// collFuncs are the prelude's functions on slices and Maps by kind and
	// name (D-D6, D-D8), which x.f(…) reaches from a value of exactly an
	// unnamed slice or Map type; nil in every other package.
	collFuncs map[collKey]*Func
	// testBuilds are, in a test package (fstest), the builders it declares
	// for each operation type of the package it tests (builders.go rule
	// 2). Only the package's own check writes it.
	testBuilds map[*TypeName][]*Builder
}

// Std reports a std package: the prelude or a package of the embedded
// standard library, which only the std importer checks (C30, added in
// check-v1 stage E4). A program package never has it, whatever its path,
// so the lowering keys std's namespaces on it, not on a path string.
func (p *Package) Std() bool { return p != nil && p.std }

// Host reports whether p is a Go package read from its binding (import go;
// docs/spec/interop.md).
func (p *Package) Host() bool { return p != nil && p.host != nil }

// Files is the package's source files in the order the checker met them.
// A std package's files carry their embedded file name (syntax.File.Name),
// so a reader can name the file that holds any node of the package: the
// lowering locates a std body's nodes through them (lower/sources.go).
func (p *Package) Files() []*syntax.File {
	out := make([]*syntax.File, len(p.files))
	for i, f := range p.files {
		out[i] = f.syntax
	}
	return out
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
	// WirePairs are the by-name wire forms (types.md, Codecs 1) that this
	// package's encode and decode calls reach, by type (L11).
	WirePairs map[*TypeName]*WirePair
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
	// Spreads is, for every spread `T{..s, …}` whose base s has a record
	// type other than T, the fields of T it copies from s, in T's order
	// (types.md, Structs 3; owner q43). A field T names is not listed. An
	// update with a base of exactly T has no entry: it copies the base.
	Spreads map[*syntax.CompositeLit][]Spread
	// Works is every Work literal of the work rule's forms (Run or Listen
	// with their maps; work.go), which the lowering builds Start from. Nil
	// when the package has none. Added by MU4 (additive).
	Works map[*syntax.CompositeLit]*WorkLit
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
	// Footprints is every frame's footprint (rows.md, Footprints; #199
	// stage 1): each function with a body (*syntax.FuncDecl), function
	// literal (*syntax.FuncLit), trailing block of a call other than with
	// (*syntax.BlockStmt; a with block runs in its enclosing frame), named
	// context (*syntax.ContextDecl, the builder a with of it runs), package
	// let (*syntax.VarSpec), test (*syntax.TestDecl) and law
	// (*syntax.LawDecl, OwnerLaw's, so no build reads it; stage E3b). The
	// lowering places interruption points only in a frame whose footprint
	// is not Empty (runtime.md, I1) and never derives a footprint again.
	// Added for #199 stage 1 (additive).
	Footprints map[syntax.Node]Footprint
	// Instances is every instantiation of a generic function (D-E1; stage
	// B round 2 m6): a call of one, keyed by the *syntax.CallExpr, whether
	// its type arguments are written, inferred or bound by a dot call's
	// receiver; and an instantiation used as a value, Max[int], keyed by
	// the *syntax.IndexExpr. Added in check-v1 stage E1 (additive).
	Instances map[syntax.Expr]*Instance
	// ReturnedValues is every expression a return or a function body's
	// tail gives back whose type is an error type, true when the
	// function's declared result makes it the success value and false
	// when it is a failure (K1, fs1; rows.md, Error rows 4). The checker
	// decides it by type identity; the lowering reads it and never
	// compares types again (batch-12 M1: two packages named errs).
	ReturnedValues map[syntax.Expr]bool
	// Releases is the release plan of the ownership pass (#197 L8 A2;
	// resources R5, R10, R21, R41, R43; ownership.go): what is still owned,
	// in release order (last acquired first), where control leaves a place.
	// Keys: a scope (*syntax.BlockStmt, *syntax.CaseClause,
	// *syntax.CatchArm) at its normal end; a statement, or the header
	// expression of an if, match or for, for the temporaries it owns; an
	// exit edge (*syntax.ReturnStmt, *syntax.BranchStmt, the failure edge
	// of a *syntax.TryExpr that leaves the function) for everything it
	// leaves; the assigned *syntax.Ident of an assignment for the old value
	// (R41). A missing key releases nothing. Added in L8 A2 (additive).
	Releases map[syntax.Node][]Release
	// OwnedVars and OwnedTemps are the values the ownership pass gives an
	// owner (#197 L8 B1): each owned local and taken parameter, and each
	// temporary a statement owns (R43), with the release the lowering
	// registers for it: the call of its type's checked Close, or the
	// generated release of its parts (Drop, L8 B2). The lowering registers
	// each in the step that receives the value (R8). Added in L8 B1
	// (additive).
	OwnedVars  map[*Var]*Drop
	OwnedTemps map[syntax.Expr]*Drop
	// TaskCaptures records the resource captures handed to the root of a
	// started task (R12). A repeated callback only lends them: the root
	// owns and releases them once, outside all runs of the callback.
	TaskCaptures map[syntax.Node]*TaskCapture
	// ResultDrops is the release of the result of a std call whose runtime
	// body takes the hidden ResultRelease (v1.Releaser): a boundary that
	// drops a successful result releases it (Gap 47, L8 B4). A call whose
	// result holds no resource has none; the lowering passes nil.
	ResultDrops map[*syntax.CallExpr]*Drop
	// FieldTakes is each field taken in place (`b.r.Close()`, R39, Gap
	// 38): the owned record's local and the field's index. The lowering
	// marks the field on the record's entry there, and the generated
	// release skips it. Added in L8 B2 (additive).
	FieldTakes map[*syntax.SelectorExpr]FieldTake
	// Handoffs is each use of an owned local or taken parameter that
	// hands it off (a move or a take), with the owned local it hands off:
	// the ident's own, or the root of the borrow a Close is called through
	// (`let p = &h; p.Close()`). The lowering removes that local's entry
	// there. Added in L8 B1 (additive).
	Handoffs map[*syntax.Ident]*Var
	// Takes is the inferred takes clause of each unexported function with
	// a body and no written clause (#197 L8 A3; takes.go): the indexes into
	// its signature's Params that it takes, ascending. A written clause is
	// in Signature.Takes; a function that takes nothing has no key. Added
	// in L8 A3 (additive).
	Takes map[*Func][]int
	// Copies is each generic function's copy sites (#197 L8 A3; plan 1.3;
	// takes.go): per type parameter of its signature, the first place its
	// body copies a value of that type (a second use after a hand-off, or a
	// call of a function that copies it). A call that binds such a type
	// parameter to a resource is S108. Added in L8 A3 (additive).
	Copies map[*Func][]CopySite
}

// CopySite is one place a generic body copies a value of its type
// parameter TParam (an index into the signature's TParams).
type CopySite struct {
	TParam int
	At     syntax.Node
	// Where is At as file:line:col; empty for a declaration with no body.
	Where string
	// What says how it copies, for the diagnostic: "x is used twice".
	What string
	// Key marks a map key (resources R40: S112 at the instantiation).
	Key bool
}

// Release is one value a place releases: an owned local or taken
// parameter (Var), or a temporary the statement owns (Temp). Flag marks a
// value owned on some paths only: the release runs when the value was not
// handed off (a drop flag, resources R21).
type Release struct {
	Var  *Var
	Temp syntax.Expr
	Flag bool
	// Fields are the record's fields taken in place on every path (a bit
	// per field index; R39, Gap 38): the generated Close skips them.
	// MaybeFields are those taken on some paths only: each has a drop
	// flag. Added in L8 A4 (additive).
	Fields, MaybeFields uint64
}

// Instance is one instantiation of a generic function.
type Instance struct {
	// Func is the generic function.
	Func *Func
	// TypeArgs are its type arguments, one for each of Func's type
	// parameters in their order: a type, or a *RowArg for a row parameter.
	TypeArgs []Type
	// Witnesses are, for each type parameter whose constraint is an
	// operation type (a method set, 196m), in parameter order, the
	// operations its type argument supplies. The lowering passes each as
	// one extra leading value (a struct of function fields), not as Go
	// methods (stage E1 orchestrator answer 2).
	Witnesses []Witness
}

// Witness is the operations a type argument supplies for one type
// parameter whose constraint is an operation type.
type Witness struct {
	// Param is the generic function's constrained type parameter; Arg is
	// its type argument.
	Param *TypeParam
	Arg   Type
	// Ops are the constraint's operations in its declared order.
	Ops []WitnessOp
}

// WitnessOp is one operation of a witness.
type WitnessOp struct {
	Name string
	// Sig is the operation as a function of Arg: Arg first, then the
	// operation's parameters, with the instance's type arguments
	// substituted. The witness holds a function value of this type.
	Sig *Signature
	// Func is the package function (196m) that implements the operation
	// for Arg. It is nil when Arg is itself a type parameter of the
	// enclosing function, whose own witness supplies the operation by
	// Name, or an operation type, whose value carries it.
	Func *Func
	// Inst is Func's instance when Func is generic: its type arguments
	// bound by Arg (a 196m function on Box[T] for Box[int]) and its own
	// witnesses.
	Inst *Instance
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
		Spreads:       map[*syntax.CompositeLit][]Spread{},
		ImplicitElems: map[syntax.Expr][]Conversion{},
		Patterns:      map[syntax.Expr]*Pattern{},
		Matches:       map[syntax.Node]*Match{},
		Tails:         map[syntax.Stmt]Type{},
		Literals:      map[syntax.Node]*Signature{},
		Loops:         map[*syntax.RangeStmt]*Loop{},
		Withs:         map[*syntax.CallExpr]*With{},
		Footprints:    map[syntax.Node]Footprint{},
		Instances:     map[syntax.Expr]*Instance{},

		ReturnedValues: map[syntax.Expr]bool{},
		Releases:       map[syntax.Node][]Release{},
		OwnedVars:      map[*Var]*Drop{},
		OwnedTemps:     map[syntax.Expr]*Drop{},
		TaskCaptures:   map[syntax.Node]*TaskCapture{},
		ResultDrops:    map[*syntax.CallExpr]*Drop{},
		FieldTakes:     map[*syntax.SelectorExpr]FieldTake{},
		Handoffs:       map[*syntax.Ident]*Var{},
		Takes:          map[*Func][]int{},
		Copies:         map[*Func][]CopySite{},
	}
}

// recordInstance records the instantiation of the generic function fn at
// x with targs, one for each of fn's type parameters, and the witness of
// each parameter an operation type constrains.
func (c *checker) recordInstance(x syntax.Expr, fn *Func, targs []Type) {
	if c.silent > 0 {
		return
	}
	if inst := c.instanceOf(fn, targs, x); inst != nil {
		c.pkg.Info.Instances[x] = inst
	}
}

// instanceOf is the instance of the generic function fn at targs, with its
// witnesses, or nil when fn is not generic or a type argument is missing.
// A witness operation implemented by a generic function carries that
// function's complete instance in turn. Reuse completed instances and
// diagnose a dependency on an active instance; never truncate a chain.
// Growing polymorphic recursion is rejected by checkInstantiationCycles.
func (c *checker) instanceOf(fn *Func, targs []Type, at syntax.Node) *Instance {
	inst, why := c.witnessInstance(fn, targs)
	if why != "" {
		c.errorf(at, CodeType, "%s", why)
	}
	return inst
}

// witnessInstance constructs the same complete fact without publishing a
// diagnostic. Assignability returns its reason to the conversion's caller,
// which owns the source expression and file; a generic declaration is not
// a conversion site.
func (c *checker) witnessInstance(fn *Func, targs []Type) (*Instance, string) {
	type entry struct {
		inst *Instance
		done bool
	}
	seen := map[*Func][]*entry{}
	var path []string
	var build func(*Func, []Type) (*Instance, string)
	build = func(fn *Func, targs []Type) (*Instance, string) {
		if fn == nil || fn.Sig == nil || len(fn.Sig.TParams) == 0 {
			return nil, ""
		}
		if len(targs) != len(fn.Sig.TParams) {
			return nil, fmt.Sprintf("cannot complete the witness instance of %s: missing type arguments", fn.Name)
		}
		for _, t := range targs {
			if t == nil || isInvalid(t) {
				return nil, fmt.Sprintf("cannot complete the witness instance of %s: missing type arguments", fn.Name)
			}
		}
		var args []string
		for _, t := range targs {
			args = append(args, t.String())
		}
		name := fn.Name + "[" + strings.Join(args, ", ") + "]"
		for _, prev := range seen[fn] {
			same := true
			for i, arg := range targs {
				same = same && c.identical(arg, prev.inst.TypeArgs[i])
			}
			if !same {
				continue
			}
			if prev.done {
				return prev.inst, ""
			}
			return nil, fmt.Sprintf("instantiation cycle in witnesses: %s", strings.Join(append(path, name), " -> "))
		}
		inst := &Instance{Func: fn, TypeArgs: targs}
		e := &entry{inst: inst}
		seen[fn] = append(seen[fn], e)
		path = append(path, name)
		defer func() { path = path[:len(path)-1] }()
		if why := c.instanceWitnesses(inst, build); why != "" {
			return nil, why
		}
		e.done = true
		return inst, ""
	}
	return build(fn, targs)
}

// instanceWitnesses fills every operation's signature and implementing
// instance. A failed dependency invalidates the whole instance.
func (c *checker) instanceWitnesses(inst *Instance, build func(*Func, []Type) (*Instance, string)) string {
	fn, targs := inst.Func, inst.TypeArgs
	m := paramMap(fn.Sig.TParams, targs)
	for i, tp := range fn.Sig.TParams {
		if tp.Kind != KindType || tp.Constraint == nil {
			continue
		}
		constraint := c.subst(tp.Constraint, m)
		if why := c.satisfies(targs[i], constraint); why != "" {
			return fmt.Sprintf("cannot complete the witness of %s: %s", fn.Name, why)
		}
		it := asInterface(constraint)
		if it == nil || len(it.Methods) == 0 {
			continue
		}
		w := Witness{Param: tp, Arg: targs[i]}
		for _, op := range it.Methods {
			method, f := c.methodOf(targs[i], op.Name)
			if method == nil {
				return fmt.Sprintf("cannot complete the witness of %s: %s has no operation %s", fn.Name, targs[i], op.Name)
			}
			sig := *op.Sig
			sig.Params = append([]*Var{{Name: "self", Type: targs[i]}}, op.Sig.Params...)
			wo := WitnessOp{Name: op.Name, Sig: &sig, Func: f}
			if f != nil && f.Sig != nil && len(f.Sig.TParams) > 0 {
				var why string
				wo.Inst, why = build(f, receiverTypeArgs(targs[i], f))
				if why != "" {
					return why
				}
			}
			w.Ops = append(w.Ops, wo)
		}
		inst.Witnesses = append(inst.Witnesses, w)
	}
	return ""
}

// receiverTypeArgs is a 196m function's instance bound by its receiver.
func receiverTypeArgs(self Type, f *Func) []Type {
	if p, ok := self.(*Pointer); ok {
		self = p.Elem
	}
	bound := selfBinding(self, f)
	args := make([]Type, len(f.Sig.TParams))
	for i, tp := range f.Sig.TParams {
		args[i] = bound[tp]
	}
	return args
}

// CallKind is what a call calls.
type CallKind int

const (
	// CallFunc calls a declared function, Func; a dot call x.f(…) (196m)
	// has Recv, lowered as the first argument.
	CallFunc CallKind = iota
	// CallOp calls an operation of an operation-type value: Op on Recv.
	// Recv may be a value of a type parameter (or a pointer to one) whose
	// method-set constraint declares Op (D-E1; check-v1 stage E1).
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
	// CallLawOp calls Op, an operation of the operation type that declares
	// the law around it, by its bare name (196l, spec D-E4): no receiver, no
	// charge (Needs is empty), and Fails is the operation's failure row,
	// which joins the law's. It stands only in a law body, which neither
	// build lowers: laws are checked, not run (#162). Added in check-v1
	// stage E3b (additive).
	CallLawOp
	// CallEncode is the encode built-in, encode(v, w): Type is v's type,
	// a wire one (types.md, Codecs). Added in L11.
	CallEncode
	// CallDecode is the decode built-in, decode[T](r): Type is T and Fails
	// its derived row (wireRow). Added in L11.
	CallDecode
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
	// StartsTask reports a call whose result holds a running task the
	// caller then owns, a Task or a tasks.Set (running.go; #197 L8 B3t):
	// task.Go, tasks.NewSet, or a function, function value or operation
	// that returns one. The caller needs a frame scope (frames.go), and
	// its footprint starts a task.
	StartsTask bool
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
	// ConvMachine puts a machine declaration's value into the prelude's
	// Machine[S, E, O, F, R, W] (README Gap 46): Table["Step"] is the
	// machine's step function.
	ConvMachine
)

// Conversion is an implicit conversion at an assignment site: From's value
// becomes To.
type Conversion struct {
	Kind     ConversionKind
	From, To Type
	Table    map[string]*Func
	// Instances carries each generic Table function's complete instance,
	// including the witnesses required by its constraints. The lowering
	// consumes this fact rather than resolving the receiver again.
	Instances map[string]*Instance
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
// Range[T] over an integer T, a prelude Seq[T], or a string's runes (L4).
type LoopDomain int

const (
	LoopSlice LoopDomain = iota + 1
	LoopMap
	LoopRange
	LoopSeq
	// LoopString iterates a string's runes as Go's range does: the
	// position is the byte offset of the rune's first byte, an invalid
	// byte is U+FFFD of width one.
	LoopString
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
// context (round-4 review M1): the context's resolved bindings are shared, and a
// with points to them through Contexts.
type With struct {
	// Contexts are the resolved named or inline contexts bound. Contexts[0] is the shared,
	// resolved context: Context.Provider and Context.EachProvider read every
	// capability it binds, Context.Overrides what its replaces overrode.
	Contexts []*Context
	// Discharged are the capabilities the block's row needs that this with
	// discharges, by name: each charge of the block that no inner with
	// discharged (rows.md, With 2 and 3). A capability the context binds and
	// the block never needs is not listed.
	Discharged []Type
	// Entries holds one entry per discharged
	// capability: Entries[i] is the provider entry that binds
	// Discharged[i] once replace and embedding are resolved (rows.md,
	// Context 7), Contexts[0].Provider(Discharged[i]). An entry of several
	// capabilities appears at each of its capabilities; its conversion for
	// Discharged[i] is the one at that capability's index in its Caps.
	//
	// The release needs of an inner with's acquisitions are needs of the
	// block too: a release runs in the environment outside the inner with,
	// so this with discharges each one it binds (withOwnership).
	Entries []*ContextEntry
	// Acquired are the provider entries this with builds and owns, in
	// their acquisition order (resources.md, Context ownership; withown.go):
	// the entries of Entries, and each entry whose capability an acquired
	// provider uses while it is built. Each entry is one acquisition,
	// however many capabilities it binds. An overridden entry and an entry
	// that nothing the block needs reaches are not acquired. Named, local and
	// inline contexts use the same fact: a local context named by two withs
	// is acquired, and released, once per with.
	Acquired []*Acquisition
	// Context is the context binding a with of a context value opens
	// (runtime.md, G1), nil for a with of a context. Such a with has no Contexts
	// and discharges nothing.
	Value *ContextBinding
	// Next is the fact of the with's next operand, nil for the last:
	// Info.Withs holds the first operand's, and `with A, B { b }` has the
	// facts `with A { with B { b } }` would have, A's Next being B's
	// (rows.md, With 7).
	Next *With
}

// ContextBinding is the operand of `with v { … }` when v's type satisfies
// the context-binding protocol (runtime.md, G1): two functions on the type,
// `contextKey(v T) string` and `contextText(v T) string`, each with empty
// error and capability rows. The names are unexported, so the protocol adds
// nothing to the surface of the package that declares T. The protocol is structural: the checker never
// reads the package or the name of the function that made v. The lowering
// evaluates the operand once, calls Key and Text on it, and binds Text
// under Key for the block and for every task started inside it.
type ContextBinding struct {
	// Type is the operand's type.
	Type Type
	// Key and Text are the protocol's functions on Type.
	Key, Text *Func
	// KeyInst and TextInst are the instances of Key and Text at Type when
	// they are generic: their type arguments bound by Type's (as a dot
	// call's receiver binds them, D-D8), with a witness for each type
	// parameter an operation type constrains; nil when the function is
	// not generic. Added in check-v1 stage E6b (additive).
	KeyInst, TextInst *Instance
}

// Acquisition is one provider entry a with builds: the
// entry, the indexes of the capabilities in Entry.Caps it still binds in
// the with's context, and its provider's resolved Close (Info.Closes), nil
// when the provider is not a resource. The with releases each resource
// once, in reverse acquisition order, on every exit of its block.
type Acquisition struct {
	Entry *ContextEntry
	Caps  []int
	Close *Close
	// Drop is the generated release of a provider whose type holds a
	// resource in its parts (L8 B2; resources R3, R11): the with releases
	// it through Drop, which runs Close first; nil when Close alone is the
	// release, or for a plain provider. Added in L8 B2 (additive).
	Drop *Drop
}

// Releases reports an acquisition the with releases.
func (a *Acquisition) Releases() bool { return a.Close != nil || a.Drop != nil }

// Override is a binding a replace overrode (Context.Overrides): Entry no
// longer binds Cap, By does. The overridden entry is not built for that
// capability; an entry of several capabilities that still binds another
// one is built for it.
type Override struct {
	Cap       Type
	Entry, By *ContextEntry
}

// ContextEntry is one entry of a named context, as written: a provider for
// capabilities, or an embedded context.
type ContextEntry struct {
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
	// Embedded is the embedded context; nil for a provider entry.
	Embedded *Context
	// Uses are the capabilities of its context that the provider uses while
	// it is built (rows.md, Context 4), as a row's elements: a with that
	// acquires the entry acquires their providers first.
	Uses []Type

	// seq orders a package's named context entries: the context pass meets the
	// contexts by name, then their entries in source order (contextPass.own).
	seq int
}

// Test is a test block (stage E, 196l).
type Test struct {
	Name   string
	Decl   *syntax.TestDecl
	Params []*Var

	file *fileInfo // the declaring file, selected while the test is checked
}

// Law is a law (196l, spec D-E4): a top-level declaration or a member of an
// operation type. The test build of the least package that sees it and a
// binding of its slots runs it (laws.go, rule 7). Its name has no
// value: Info.Defs maps the name to the Law, and no scope holds it. Added
// in check-v1 stage E3b (additive). Since law-top, Owner is nil for a
// top-level law (a contract change: the field was always set before).
type Law struct {
	Name  string
	Decl  *syntax.LawDecl
	Owner *TypeName // the operation type that declares it; nil at top level
	Pkg   *Package  // the package that declares it
	// Machine is the machine type that declares it (MU6), with Owner nil:
	// a machine law's body has the scope of a top-level law's.
	Machine *TypeName
	// Targets are the variants of the machine's state that a reachability
	// law (Decl.Reach) names, in order.
	Targets []*Named
	// Params are its parameters, in source order; a field with no name
	// holds one "_".
	Params []*Var
	// Fails is the inferred failure row of its body: a failure that
	// escapes it refutes the law when ergo test runs it.
	Fails *Row
	// Slots are the operation types the law quantifies over (laws.go,
	// rule 7): its owner first, for a member law, then each parameter of an
	// operation type, in order. A run binds each slot to a provider
	// (Package.LawRuns).
	Slots []LawSlot
	// Unrun is why ergo test does not run the law, "" when it does: a
	// parameter with no generator or a generic operation type.
	Unrun string
	// Where holds the refinement of each parameter that takes a where
	// (refine.go), at its index in Params; nil without any.
	Where []*Refinement

	file *fileInfo // the declaring file, selected while the law is checked
}

// LawSlot is an operation type a law quantifies over (Law.Slots): its
// owner (Param -1) or its parameter Param.
type LawSlot struct {
	Param int
	Type  *Named
}

// LawRun is a law a package's test build reports (Package.LawRuns): the
// bindings of its slots that this package runs (laws.go, rule 7). A law
// of the package with no binding here, or one that does not run
// (Law.Unrun), has a LawRun with no bindings.
type LawRun struct {
	Law *Law
	// Bindings each hold one provider per slot of Law.Slots, in order. A
	// law with no slots that runs has one empty binding.
	Bindings [][]LawProvider
}

// LawProvider is a provider bound to a law's slot (LawRun.Bindings). Unrun
// is why it does not run, "" when it does: its values have no generator.
//
// Ctor is the provider's constructor when its values have no generator
// and its package declares New<P> (laws.go, lawConstructor; fs4): each
// case calls it, with generated arguments, instead of generating a value.
type LawProvider struct {
	Type  *Named
	Conv  Conversion
	Ctor  *Func
	Unrun string
}

// A law is an Object, so Defs records its declaring name.
func (o *Law) name() string    { return o.Name }
func (o *Law) pos() syntax.Pos { return o.Decl.Name.Pos() }

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
		call.Kind = builtinKind(fun.obj.(*Builtin))
	case fun.mode == modeCapFn:
		call.Kind, call.Op = CallProviderOp, fun.method
	case fun.fn != nil:
		call.Kind, call.Func, call.Recv = CallFunc, fun.fn, recv()
		if fun.mode == modeValue && len(fun.self) > 0 {
			// x binds every type parameter of f (D-D8); a call that
			// infers the rest records them all in callGeneric.
			call.TypeArgs = fullTypeArgs(fun.fn, fun.self, nil)
			c.recordInstance(x, fun.fn, call.TypeArgs)
		} else if inst := c.pkg.Info.Instances[unparen(x.Fun)]; fun.mode == modeValue && inst != nil {
			// (Max[int])(1): the parenthesised instance is a value whose
			// instantiation was recorded on its index expression; the call
			// calls that instance.
			call.TypeArgs = inst.TypeArgs
			c.pkg.Info.Instances[x] = inst
		}
	case fun.method != nil:
		call.Kind, call.Op, call.Recv = CallOp, fun.method, recv()
	default:
		call.Kind = CallFuncValue
	}
}

// recordWith records a with site, one With per operand frame, chained by
// Next in operand order: the operand's context, and the capabilities the
// block needed from it with their entries, which the block's charges found
// (withFrame), sorted by name. It reads nothing of the context's summary,
// so its work and its size are the block's. The sites are listed innermost
// operand first, each with the frames around it (withOwnership).
func (c *checker) recordWith(x *syntax.CallExpr, frames []*withFrame) {
	if c.silent > 0 {
		return
	}
	withs := make([]*With, len(frames))
	for i, f := range frames {
		w := &With{Value: f.value}
		if f.context != nil {
			w.Contexts = []*Context{f.context}
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
				if w.Contexts != nil {
					w.Entries = append(w.Entries, f.entries[i])
				}
			}
		}
		if i > 0 {
			withs[i-1].Next = w
		}
		withs[i] = w
		f.with = w
	}
	c.pkg.Info.Withs[x] = withs[0]
	c.callInfo(x).Kind = CallWith
	// Operand i's environment is the scope around its frame, a persistent
	// version shared, never copied (chargeRelease; #229).
	for i := len(frames) - 1; i >= 0; i-- {
		c.withSites = append(c.withSites, withSite{x: x, operand: i, w: withs[i], file: c.cur, t: c.fn.t, outer: frames[i].outer})
	}
}

// Provider returns the entry that binds cap in a with of w, once replace
// and embedding are resolved (rows.md, Context 7), or nil when w binds no
// cap. It reads the context's summary, shared with every context that embeds w
// and every with of it (contextsum.go): a lookup reads at most the summary's
// height, O(log n) of the capabilities w reaches. Its conversion for cap is
// the one at cap's index in the entry's Caps.
func (w *Context) Provider(cap Type) *ContextEntry {
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
func (w *Context) EachProvider(f func(cap Type, e *ContextEntry) bool) {
	if w.binds == nil {
		return
	}
	if len(w.binds.extra) > 0 {
		// Only inline contexts have an overlay. Merge it with embedded
		// bindings in qualified-name order, suppressing overridden entries.
		var nodes []*capNode
		for _, n := range w.binds.extra {
			nodes = append(nodes, n)
		}
		var collect func(*capNode)
		collect = func(n *capNode) {
			if n == nil {
				return
			}
			collect(n.left)
			if w.binds.extra[n.key] == nil {
				nodes = append(nodes, n)
			}
			collect(n.right)
		}
		collect(w.binds.root)
		sort.Slice(nodes, func(i, j int) bool { return nodes[i].key.compare(nodes[j].key) < 0 })
		for _, n := range nodes {
			if !f(n.t, n.entry) {
				return
			}
		}
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
		switch call.Kind {
		case CallFunc, CallFuncValue, CallOp, CallProviderOp, CallLawOp:
			call.StartsTask = c.running(c.pkg.Info.Types[x]) != nil
		}
		if call.Kind == CallFunc && releasesResult(call.Func) {
			if d, bad := c.dropOf(c.pkg.Info.Types[x]); d != nil && bad == "" {
				c.pkg.Info.ResultDrops[x] = d
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
	u, ok := t.(*Union)
	return ok && u.Row != nil
}
