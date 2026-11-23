package check

import (
	"go/constant"
	"math/big"
	"slices"
	"strings"

	"ergo.local/prototype/internal/sketch/stdlib"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Facts for lint (internal/sketch/lint). Each reads what the checker has
// already decided; none decides anything new, so a lint rule can match a
// resolved object or a fact and never an identifier's spelling.

// CallAddsFootprint reports whether the call x adds to its frame's
// footprint (rows.md, Footprints): a capability operation, a callee whose
// footprint is not empty or is unknown, a task start or a with. It follows
// the footprint rule the checker's own purity diagnostics name
// (impureWhy).
func CallAddsFootprint(info *Info, x *syntax.CallExpr) bool {
	return impureWhy(info, x) != ""
}

// GoTagKey reports a field whose wire key holds a double quote, the Go
// struct-tag habit (types.md, Codecs 2: the key is the wire name itself),
// and the name such a tag names, `json:"name,omitempty"` → name, else the
// key without its quotes and spaces. A colon alone is no sign
// (`"xml:lang"` is a real key), so a key with no quote is no habit.
func (f *Field) GoTagKey() (name string, habit bool) {
	key := f.Origin().WireKey
	if !strings.Contains(key, "\"") {
		return "", false
	}
	if i := strings.Index(key, ":\""); i > 0 {
		name = key[i+2:]
		if j := strings.IndexAny(name, ",\""); j >= 0 {
			name = name[:j]
		}
		return name, true
	}
	return strings.Trim(key, "\" "), true
}

// Exported reports whether f is visible to importers of its package
// (grammar.md, Visibility).
func (f *Func) Exported() bool { return isExported(f.Name) }

// WritesErrors reports whether f's error row is written in its
// declaration, not inferred from its body (rows.md, Error rows 6).
func (f *Func) WritesErrors() bool { return !f.inferErrors }

// ImplementationTypes are the types of the providers the with converts
// into the capabilities it discharges (Entries), its later operands
// included: a provider whose type is the capability itself (a capability
// value passed down) converts nothing and is not listed.
func (w *With) ImplementationTypes() []Type {
	var out []Type
	for ; w != nil; w = w.Next {
		for _, e := range w.Entries {
			for _, conv := range e.Conversions {
				if conv.Kind != 0 && conv.From != nil {
					out = append(out, conv.From)
				}
			}
		}
	}
	return out
}

// ProgramType reports a type declared in a program package: not std's,
// not a host binding's, not predeclared.
func ProgramType(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	if !ok || n.origin().Obj == nil || n.origin().Obj.Pkg == nil {
		return false
	}
	pkg := n.origin().Obj.Pkg
	return !pkg.Std() && !pkg.Host() && pkg != universeOnce.goPkg
}

// ProvidesOperation reports whether f is a method of its first
// parameter's type that provides an operation of an operation type
// the type declares it implements: a call of the operation reaches f with
// no call that names it.
func (f *Func) ProvidesOperation() bool {
	if f == nil || f.Sig == nil || len(f.Sig.Params) == 0 {
		return false
	}
	t := f.Sig.Params[0].Type
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	if !ok || n.method(f.Name) != f {
		return false
	}
	for _, impl := range n.origin().impls {
		if it, ok := impl.underlying().(*Interface); ok {
			for _, m := range it.Methods {
				if m.Name == f.Name {
					return true
				}
			}
		}
	}
	return false
}

// Entry is the package's entry function, main, or nil: a package with one
// is a program's root, whose code no other package imports.
func (p *Package) Entry() *Func {
	if p == nil || p.Scope == nil {
		return nil
	}
	f, _ := p.Scope.names["main"].(*Func)
	return f
}

// MentionsParam reports whether the type t names the type parameter tp
// anywhere in it. The checker resolves a type parameter's name to the
// type, not to a use (Info.Uses), so a reader finds it through types.
func MentionsParam(t Type, tp *TypeParam) bool {
	found := false
	eachTypeParam(t, func(p *TypeParam) { found = found || p == tp })
	return found
}

// Import is one import of a file: its spec, the name the file binds, and
// the package it resolved to (a std, program or host package).
type Import struct {
	Spec *syntax.ImportSpec
	Name string
	Pkg  *Package
}

// ImportsOf is the resolved imports of f, a file of p, in source order. A
// broken import is left out.
func (p *Package) ImportsOf(f *syntax.File) []Import {
	var out []Import
	for _, fi := range p.files {
		if fi.syntax != f {
			continue
		}
		for _, pn := range fi.imports {
			if !pn.broken && pn.Pkg != nil {
				out = append(out, Import{Spec: pn.spec, Name: pn.Name, Pkg: pn.Pkg})
			}
		}
	}
	return out
}

// Qualified is name, declared in q, as p's source spells it, for text a
// reader of p pastes back (a refuted case's arguments, a law's name): bare
// when q is p, the prelude or unknown, else qualified by the alias that the
// first of p's files to write one gives, else by q's name, its path's last
// element. It reads the resolved imports, never the path's spelling (an
// import path is unquoted, grammar.md Lexical 6), so the evaluator and the
// Go build, which both call it, print one text.
func (p *Package) Qualified(q *Package, name string) string {
	if q == nil || q == p || q.Prelude() {
		return name
	}
	return p.importName(q) + "." + name
}

// importName is the name p's files import q under (Qualified).
func (p *Package) importName(q *Package) string {
	if p == nil {
		return q.Name
	}
	for _, fi := range p.files {
		for _, pn := range fi.imports {
			if !pn.broken && pn.Pkg == q && pn.spec.Name != nil {
				return pn.Name
			}
		}
	}
	return q.Name
}

// FileBinds reports whether name is bound at the top of f, a file of p: an
// import name of the file, a declaration of the package, or a prelude
// name. A fix that adds an import of that name would collide with it.
func (p *Package) FileBinds(f *syntax.File, name string) bool {
	for _, fi := range p.files {
		if fi.syntax == f && fi.scope != nil {
			if o, _ := fi.scope.lookup(name); o != nil {
				return true
			}
		}
	}
	if p.Scope != nil {
		if o, _ := p.Scope.lookup(name); o != nil {
			return true
		}
	}
	return false
}

// DocOf is the doc comment of o's declaration and the package that
// declares it: a function, a type or a named context. Every other object
// (a local, a package let, a builtin) has none.
func DocOf(o Object) (*syntax.CommentGroup, *Package) {
	switch o := o.(type) {
	case *Func:
		if o.Decl != nil {
			return o.Decl.Doc, o.Pkg
		}
	case *TypeName:
		if o.Decl != nil {
			return o.Decl.Doc, o.Pkg
		}
	case *Context:
		if o.Decl != nil {
			return o.Decl.Doc, o.pkg
		}
	}
	return nil, nil
}

// QualifiedMember is the package member a qualified name pkg.Name names,
// which Info.Uses does not hold; nil when x is not one.
func QualifiedMember(info *Info, x *syntax.SelectorExpr) Object {
	id, ok := x.X.(*syntax.Ident)
	if !ok {
		return nil
	}
	pn, ok := info.Uses[id].(*PkgName)
	if !ok || pn.Pkg == nil {
		return nil
	}
	o, _ := pn.Pkg.member(x.Sel.Name)
	return o
}

// OpDoc is the doc comment of the operation an operation call x calls
// (CallOp or CallProviderOp) and the package of its operation type.
func OpDoc(info *Info, x *syntax.CallExpr) (*syntax.CommentGroup, *Package) {
	call := info.Calls[x]
	if call == nil || call.Op == nil {
		return nil, nil
	}
	recv := call.Recv
	if call.Kind == CallProviderOp {
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
			recv = sel.X
		}
	}
	if call.Kind != CallOp && call.Kind != CallProviderOp || recv == nil {
		return nil, nil
	}
	t := info.Types[recv]
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	if !ok || n.origin().Obj == nil || n.origin().Obj.Decl == nil {
		return nil, nil
	}
	obj := n.origin().Obj
	it, ok := obj.Decl.Spec.(*syntax.InterfaceType)
	if !ok {
		return nil, nil
	}
	for _, f := range it.Methods {
		for _, id := range f.Names {
			if id.Pos() == call.Op.Pos {
				return f.Doc, obj.Pkg
			}
		}
	}
	return nil, nil
}

// StdRole is a std declaration a lint rule reads by its identity: the
// package and the name std declares it with, never a call's spelling.
type StdRole int

const (
	RoleNone    StdRole = iota
	RoleRace            // tasks.Race
	RoleCollect         // slices.Collect, slices.Sorted: read a Seq to its end
	RoleAppend          // the prelude's Append
	RoleAfter           // timeout.After
)

// stdRoles names each role's declarations: the std package path and the
// declared name. TestStdRolesResolve (lint) checks that each resolves.
var stdRoles = []struct {
	path, name string
	role       StdRole
}{
	{"tasks", "Race", RoleRace},
	{"slices", "Collect", RoleCollect},
	{"slices", "Sorted", RoleCollect},
	{stdlib.PreludePath, "Append", RoleAppend},
	{"timeout", "After", RoleAfter},
}

// RoleOf is the std role of the function f, RoleNone for any function
// that is not one of std's role declarations.
func RoleOf(f *Func) StdRole {
	if f == nil || !f.Pkg.Std() {
		return RoleNone
	}
	for _, r := range stdRoles {
		if f.Pkg.Path == r.path && f.Name == r.name {
			return r.role
		}
	}
	return RoleNone
}

// IsSchedulerSleep reports whether x calls the Sleep operation of the
// prelude's Scheduler capability, on the provider a with binds.
func IsSchedulerSleep(info *Info, x *syntax.CallExpr) bool {
	call := info.Calls[x]
	if call == nil || call.Kind != CallProviderOp || call.Op == nil {
		return false
	}
	sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
	if !ok {
		return false
	}
	n, ok := info.Types[sel.X].(*Named)
	return ok && isSchedulerSleep(call.Op.Name, n)
}

// IsTimeoutExpired reports std timeout.Expired, the failure of a deadline.
func IsTimeoutExpired(t Type) bool {
	n, ok := t.(*Named)
	return ok && isStdObj(n.origin().Obj, "timeout", "Expired")
}

// IsTimeoutPackage reports std's timeout package.
func IsTimeoutPackage(p *Package) bool { return p.Std() && p.Path == "timeout" }

// IsBoolean reports whether t is bool or a type defined over it.
func IsBoolean(t Type) bool { return isBoolean(t) }

// IsString reports whether t is string or a type defined over it.
func IsString(t Type) bool { return isString(t) }

// SelectedField is the record field x selects, as its declaration has it
// (an instance's field is its origin's), and the package that declares
// the record; nil when x selects no field.
func SelectedField(info *Info, x *syntax.SelectorExpr) (*Field, *Package) {
	return recordField(info.Types[x.X], x.Sel)
}

// KeyedField is the record field a keyed element of the composite literal
// lit names, and the package that declares the record; nil when lit is
// no record or key no field of it.
func KeyedField(info *Info, lit *syntax.CompositeLit, key *syntax.Ident) (*Field, *Package) {
	return recordField(info.Types[lit], key)
}

func recordField(t Type, name *syntax.Ident) (*Field, *Package) {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	if !ok || n.origin().Obj == nil {
		return nil, nil
	}
	s, ok := n.origin().underlying().(*Struct)
	if !ok {
		return nil, nil
	}
	f, _ := s.field(name.Name)
	if f == nil {
		return nil, nil
	}
	return f, n.origin().Obj.Pkg
}

// OperationType reports whether tn declares an operation type.
func OperationType(tn *TypeName) bool {
	n, ok := tn.Type.(*Named)
	if !ok {
		return false
	}
	it, ok := n.underlying().(*Interface)
	return ok && it.obj == tn
}

// Implements are the operation types n's implements clause names.
func (n *Named) Implements() []*Named { return n.origin().impls }

func isStdObj(o *TypeName, path, name string) bool {
	return o != nil && o.Pkg.Std() && o.Pkg.Path == path && o.Name == name
}

// IsScheduler reports the prelude's Scheduler capability, a predeclared
// name (docs/spec/stdlib.md, Std is ordinary 2).
func IsScheduler(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	o := n.origin().Obj
	return o != nil && o.Name == "Scheduler" && o.Pkg != nil && o.Pkg.Path == stdlib.PreludePath
}

// isSchedulerSleep reports the Scheduler's operation Sleep, by its name in
// an operation type IsScheduler holds (docs/spec/stdlib.md, Std is
// ordinary 2).
func isSchedulerSleep(name string, scheduler *Named) bool {
	return IsScheduler(scheduler) && name == "Sleep"
}

// IsManualClock reports a clock a test drives: a type whose implements
// clause names the Scheduler, so a with can make it the tasks' scheduler
// (std time's Manual and testing's VirtualClock; LiveScheduler too, whose
// Sleep is a real one). Any module can declare one.
func IsManualClock(t Type) bool {
	if p, ok := t.(*Pointer); ok {
		t = p.Elem
	}
	n, ok := t.(*Named)
	return ok && slices.ContainsFunc(n.Implements(), func(i *Named) bool { return IsScheduler(i) })
}

// ClockCaps reports the capabilities a clock of the test serves: the
// Scheduler, and each operation type the implements clause of one of the
// clocks names (std time's Clock, for time.Manual).
func ClockCaps(clocks []*Named) func(*Named) bool {
	return func(n *Named) bool {
		if IsScheduler(n) {
			return true
		}
		for _, c := range clocks {
			for _, i := range c.Implements() {
				if i.origin() == n.origin() {
					return true
				}
			}
		}
		return false
	}
}

// TimeWait is how a call lets time pass: WaitNone, a manual clock's
// advance by more than zero, or a sleep (the Scheduler's Sleep through a
// with, or a clock's Sleep). A clock's method that no operation type of
// its implements clause declares and that gives no result is an advance
// (time.Manual's Advance): the test moves time. An advance by a constant
// zero passes no time: time.Manual's Advance returns once every task it
// woke waits again, which is a wait on the scheduler's idle (testing.Stage's
// Settle), not on time.
type TimeWait int

// Time waits.
const (
	WaitNone TimeWait = iota
	WaitAdvance
	WaitSleep
)

// TimeWaitOf is how the call x lets time pass.
func TimeWaitOf(info *Info, x *syntax.CallExpr) TimeWait {
	if IsSchedulerSleep(info, x) {
		return WaitSleep
	}
	f, clock := clockMethod(info, x)
	if f == nil {
		return WaitNone
	}
	if op := clockOp(clock, f); op != nil {
		if isSchedulerSleep(f.Name, op) {
			return WaitSleep
		}
		return WaitNone
	}
	if len(f.Sig.Results) > 0 {
		return WaitNone
	}
	if d := advanceBy(info, x); d != nil && constant.Sign(d) == 0 {
		return WaitNone
	}
	return WaitAdvance
}

// advanceBy is the constant duration an Advance call moves by, or nil.
func advanceBy(info *Info, x *syntax.CallExpr) constant.Value {
	if len(x.Args) == 0 {
		return nil
	}
	return info.Values[x.Args[len(x.Args)-1]]
}

// TimeDriven reports whether the call x reads or waits on time, caps
// holding the capabilities of time (ClockCaps): a function whose
// capability row holds one, an operation of one, or a method of a clock
// other than an advance: an operation (Now, Sleep) or one that gives a
// result (Elapsed, Sleepers).
func TimeDriven(info *Info, x *syntax.CallExpr, caps func(*Named) bool) bool {
	call := info.Calls[x]
	if call == nil {
		return false
	}
	if f, clock := clockMethod(info, x); f != nil {
		return clockOp(clock, f) != nil || len(f.Sig.Results) > 0
	}
	if call.Kind == CallProviderOp {
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
			if n, ok := info.Types[sel.X].(*Named); ok && caps(n) {
				return true
			}
		}
	}
	if call.Func != nil && call.Func.Sig != nil && call.Func.Sig.Uses != nil {
		for _, t := range call.Func.Sig.Uses.Elems() {
			if n, ok := t.(*Named); ok && caps(n) {
				return true
			}
		}
	}
	return false
}

// ManualClockCall reports whether x calls a method of a manual clock, and
// the clock it calls it on: a dot call's receiver or the first argument.
func ManualClockCall(info *Info, x *syntax.CallExpr) (syntax.Expr, bool) {
	if f, _ := clockMethod(info, x); f == nil {
		return nil, false
	}
	if c := info.Calls[x]; c.Recv != nil {
		return c.Recv, true
	}
	if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok && IsManualClock(info.Types[sel.X]) {
		return sel.X, true
	}
	if len(x.Args) == 0 {
		return nil, true
	}
	return x.Args[0], true
}

// clockMethod is the method of a manual clock that x calls, and the
// clock's type, or nil.
func clockMethod(info *Info, x *syntax.CallExpr) (*Func, *Named) {
	call := info.Calls[x]
	if call == nil || call.Kind != CallFunc || call.Func == nil || call.Func.Sig == nil || len(call.Func.Sig.Params) == 0 {
		return nil, nil
	}
	n, ok := call.Func.Sig.Params[0].Type.(*Named)
	if !ok || !IsManualClock(n) || n.method(call.Func.Name) != call.Func {
		return nil, nil
	}
	return call.Func, n
}

// clockOp is the operation type of the clock's implements clause that
// declares the method f, or nil: f is then the clock's own (an advance, a
// count of sleepers).
func clockOp(clock *Named, f *Func) *Named {
	for _, i := range clock.Implements() {
		if it, ok := i.underlying().(*Interface); ok && it.method(f.Name) != nil {
			return i
		}
	}
	return nil
}

// Mutable reports a `let mut` name, the only kind an assignment may
// target: a parameter or a plain let holds one value for its scope.
func (v *Var) Mutable() bool { return v.mut }

// PureValueFunc reports a function whose call depends only on its
// arguments and gives a plain value: it has a body whose footprint is
// empty (no capability, no task, no unknown code, no function parameter it
// runs), no pointer receiver, no pointer or function parameter, and one
// result of a basic type or a type defined over one. Two calls of it with
// the same arguments give the same value.
func PureValueFunc(f *Func) bool {
	if f == nil || f.Decl == nil || f.Ptr || f.Sig == nil || f.Pkg == nil || f.Pkg.Info == nil {
		return false
	}
	fp, ok := f.Pkg.Info.Footprints[f.Decl]
	if !ok || !fp.Empty() || len(fp.Params) > 0 {
		return false
	}
	for _, p := range f.Sig.Params {
		switch p.Type.(type) {
		case *Pointer, *Signature:
			return false
		}
	}
	return len(f.Sig.Results) == 1 && PlainValue(f.Sig.Results[0])
}

// PlainValue reports a basic type or a type defined over one: a value
// with no part another name can change.
func PlainValue(t Type) bool {
	if n, ok := t.(*Named); ok {
		t = n.Underlying()
	}
	_, ok := t.(*Basic)
	return ok
}

// RefinementDecides reports whether the refinement of an operand's type
// decides the comparison x: x compares a value of a refined type (or the
// length of one whose where bounds len(it)) with an integer constant, and
// every value the refinement admits gives the same answer, which is value.
// A refinement with a pattern bounds only the length.
func RefinementDecides(info *Info, x *syntax.BinaryExpr) (value, ok bool) {
	op := x.Op
	subject, c := x.X, x.Y
	if info.Values[subject] != nil {
		subject, c = c, subject
		op = mirror(op)
	}
	v := info.Values[c]
	if v == nil || info.Values[subject] != nil {
		return false, false
	}
	iv := constant.ToInt(v)
	if iv.Kind() != constant.Int {
		return false, false
	}
	var n *big.Int
	switch i := constant.Val(iv).(type) {
	case int64:
		n = big.NewInt(i)
	case *big.Int:
		n = i
	default:
		return false, false
	}
	r, ok := refinementOf(info, subject)
	if !ok {
		return false, false
	}
	seen := map[bool]bool{}
	for _, s := range r.Spans {
		lo, hi, known := compareSpan(op, s, n)
		if !known {
			return false, false
		}
		seen[lo] = true
		seen[hi] = true
	}
	if len(seen) != 1 {
		return false, false
	}
	for b := range seen {
		value = b
	}
	return value, true
}

// refinementOf is the refinement that bounds the subject: the refinement
// of its refined type, or, for len(y), of y's type when it bounds len(it).
func refinementOf(info *Info, subject syntax.Expr) (*Refinement, bool) {
	subject = unparen(subject)
	length := false
	if call, ok := subject.(*syntax.CallExpr); ok {
		c := info.Calls[call]
		if c == nil || c.Kind != CallLen || len(call.Args) != 1 {
			return nil, false
		}
		subject, length = call.Args[0], true
	}
	n, ok := info.Types[subject].(*Named)
	if !ok || n.origin().Obj == nil || n.origin().Obj.Refine == nil {
		return nil, false
	}
	r := n.origin().Obj.Refine
	if r.Len != length || len(r.Spans) == 0 {
		return nil, false
	}
	return r, true
}

// compareSpan is the answer of `v op n` at the ends of the span s, and
// whether the answer is the same over the whole span (each of < <= > >=
// == != changes its answer at most twice, at n; a span that holds n and
// more is not known).
func compareSpan(op syntax.Token, s Span, n *big.Int) (lo, hi, known bool) {
	at := func(v *big.Int) bool {
		c := v.Cmp(n)
		switch op {
		case syntax.LSS:
			return c < 0
		case syntax.LEQ:
			return c <= 0
		case syntax.GTR:
			return c > 0
		case syntax.GEQ:
			return c >= 0
		case syntax.EQL:
			return c == 0
		default:
			return c != 0
		}
	}
	switch op {
	case syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ, syntax.EQL, syntax.NEQ:
	default:
		return false, false, false
	}
	lo, hi = at(s.Lo), at(s.Hi)
	// == and != change inside the span when n is strictly inside it
	if (op == syntax.EQL || op == syntax.NEQ) && s.Lo.Cmp(n) <= 0 && s.Hi.Cmp(n) >= 0 && s.Lo.Cmp(s.Hi) != 0 {
		return false, false, false
	}
	return lo, hi, lo == hi
}

// mirror is the comparison with its operands swapped: c < x is x > c.
func mirror(op syntax.Token) syntax.Token {
	switch op {
	case syntax.LSS:
		return syntax.GTR
	case syntax.LEQ:
		return syntax.GEQ
	case syntax.GTR:
		return syntax.LSS
	case syntax.GEQ:
		return syntax.LEQ
	}
	return op
}

// IsConsole reports the prelude's Console capability, a predeclared name
// (docs/spec/stdlib.md, Std is ordinary 2).
func IsConsole(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	o := n.origin().Obj
	return o != nil && o.Name == "Console" && o.Pkg != nil && o.Pkg.Path == stdlib.PreludePath
}

// ProviderOpType is the capability whose operation the call x runs
// through a with or a row (`Store.Get(k)`), or nil.
func ProviderOpType(info *Info, x *syntax.CallExpr) *Named {
	call := info.Calls[x]
	if call == nil || call.Kind != CallProviderOp {
		return nil
	}
	sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
	if !ok {
		return nil
	}
	n, _ := info.Types[sel.X].(*Named)
	return n
}

// IsGoImport reports a package `import go` names: a Go package read
// through its bindings (interop.md), the Go boundary.
func IsGoImport(p *Package) bool { return p != nil && p.Host() }
