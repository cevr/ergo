// Package gobind writes the Ergo binding of a Go package: the declarations
// that `import go P` makes usable, each with its Ergo signature and rows, and
// a reason for each Go declaration that does not map (docs/spec/interop.md).
// It is the only code that reads Go signatures: it runs go list and
// go/types, and the checker reads only the bindings it writes.
//
// Prior art: Borgo's importer writes committed declaration files from Go
// source (borgo-lang/borgo importer/importer.go); Kotlin/Native's cinterop
// and Swift's Clang importer turn host headers into a library the compiler
// reads. Unlike Borgo, gobind keeps (T, bool) a tuple and keeps a pointer a
// handle, and it states a row on every call.
package gobind

import (
	"fmt"
	"go/constant"
	"go/types"
	"math"
	"slices"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/gofacts"
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
	"ergo.local/prototype/runtime/v1/hostfs"
)

// Kind is the kind of a Go declaration.
type Kind int

const (
	Func   Kind = iota // a package-level function
	Method             // a method of a named non-interface type
	Type               // a named type
	Const              // a constant
	Var                // a variable
)

// Decl is one exported Go declaration of a package and its Ergo binding.
// Refused is the reason it does not map, and then Ergo is empty. Go is its
// Go text, the comment line `// ergo:go …` of the binding.
type Decl struct {
	Kind     Kind
	Name     string // Go's name; Type.Method for a method
	Go       string
	Ergo     string // the declaration after `extern go <target> `
	Variadic bool   // the last parameter is Go's ...T, spread at the call
	// Context: the first parameter is Go's context.Context, hidden in the
	// Ergo signature; the lowering passes the calling task's context (IG10).
	Context bool
	// Reads are the Ergo binders of the parameters (a method's receiver
	// included) that Go's escape analysis says the function only reads,
	// on every platform: the binding's `// ergo:reads` line (Derive).
	Reads []string
	// Vouched are the parameters the reviewed table (internal/gofacts)
	// vouches the function only reads where Go's analysis cannot see it:
	// one `// ergo:reads p (vouched: reason)` line each. A vouched fact
	// acts only on a current validated line of the ledger (LAWFFI LF2).
	Vouched []Vouch
	// Effect is the reason of the function's memory-only row
	// (gofacts.Effects; np4f-plan GBX6), acting on its guard line: the
	// binding writes no needs row and an `// ergo:effect none (vouched:
	// <reason>)` line.
	Effect string
	// EffectFrom is the target of that row when a method Go promotes from
	// an embedded field takes the declaring type's row (GB): the binding
	// writes `// ergo:effect none from <EffectFrom> (vouched: <reason>)`.
	EffectFrom string
	// Host is the reason of the function's host row (gofacts.Hosts;
	// HOST-ROW), acting on its guard line: the binding writes needs
	// Ambient, not go.Foreign, and an `// ergo:effect ambient (vouched:
	// <reason>)` line.
	Host string
	// Via is the Go function the binding calls in Go's function's place
	// (ValueResults; HOST-ROW-3): one that returns values where Go's
	// returns an interface, so reading the result needs no row. Empty
	// for a declaration bound as Go writes it.
	Via string
	// Total: every platform proves the function total, no call of it
	// panics (Derive's totalFact, total.go): the binding's
	// `// ergo:total` line (GOBIND-TOTAL).
	Total   bool
	Refused string
	// Writes is the binder of the parameter that Go writes in place
	// (v1.GoWrites(); GBX2), or "". The Ergo signature returns that
	// parameter's type: the call's value is the slice Go wrote, and the
	// binding's `// ergo:writes` line names it.
	Writes string
	// Members are the refusals of a struct's fields and an interface's
	// methods, by member name, for a type that maps.
	Members []Member
	// Refs are the named types the head of the Ergo text spells (a
	// signature, a receiver, a defined type's underlying type); Lines
	// are a struct's or an interface's members with their own refs.
	// Close reads them; Render does not.
	Refs     []string
	Lines    []Line
	refPkgs  []*types.Package
	readable []readable
}

// Vouch is a vouched `reads` fact: the parameter's Ergo binder and the
// reviewer's reason.
type Vouch struct{ Param, Reason string }

// Line is one field or method of a mapped struct or interface.
type Line struct {
	Name, Go, Text string
	Refs           []string
}

// Member is a field or interface method that a mapped type leaves out.
type Member struct {
	Name, Go, Refused string
}

// Target is the extern target of d in package path: `net/url.Parse`,
// `net/url.URL.Hostname`, or d's Via.
func (d Decl) Target(path string) string {
	if d.Via != "" {
		return d.Via
	}
	return path + "." + d.Name
}

// HostFS is the import path of the toolchain's Go package that gives a
// file's metadata as values (runtime/v1/hostfs).
const HostFS = hostfs.Path

// ValueResults are the Go functions whose binding calls another in their
// place (HOST-ROW-3): Go's returns fs.FileInfo or []fs.DirEntry, whose
// methods any Go type may implement, so a row on them would vouch for
// every implementation; the function it names returns structs of the
// same values, read once on the implementation os returns. The named
// function is bound and reviewed as any Go function is (a host row, its
// guard), and every binding of Go's, std's or a user's, gets the same
// one. Keyed by Go's target, valued by the one the binding calls.
//
// A method's entry (S2-FILE-B) names a function that takes the receiver
// first: the binding is still the method, `func (f *File) Stat()
// hostfs.Info`, and its target is that function, which the call runs as
// hostfs.FileStat(f).
var ValueResults = map[string]string{
	"os.Stat":         HostFS + ".Stat",
	"os.Lstat":        HostFS + ".Lstat",
	"os.ReadDir":      HostFS + ".ReadDir",
	"os.File.Stat":    HostFS + ".FileStat",
	"os.File.ReadDir": HostFS + ".FileReadDir",
	"os.File.Readdir": HostFS + ".FileReaddir",
}

// Qualifier spells a package other than the bound one in Ergo source: the
// name the binding's import gives it.
type Qualifier func(*types.Package) string

// Map maps one exported Go object of pkg by the table of
// docs/spec/interop.md (Type mapping). For a method, obj is the method and
// recv the named type whose method set holds it. q spells other packages.
// facts are the derived facts (Derive): a (*T, error) function binds its
// pointer as go.Ref[T] only with their nonNilResult, else as
// Option[go.Ref[T]].
func Map(obj types.Object, recv *types.Named, q Qualifier, facts Facts) Decl {
	return mapIn(obj.Pkg(), obj, recv, q, facts)
}

// mapIn is Map with obj's types spelled from the bound package home: a
// type of obj's own package is qualified when home is another
// (ValueResults). The target, and every row it reads, stay obj's.
func mapIn(home *types.Package, obj types.Object, recv *types.Named, q Qualifier, facts Facts) Decl {
	m := &mapper{pkg: obj.Pkg(), home: home, q: q, facts: facts}
	if recv != nil {
		m.pkg, m.home = recv.Obj().Pkg(), recv.Obj().Pkg()
		m.refs = []string{m.pkg.Path() + "." + recv.Obj().Name()}
	}
	d := m.decl(obj, recv)
	if d.Refused == "" {
		d.Refs = m.refs
		d.readable = m.readable
	}
	d.refPkgs = m.refPkgs
	return d
}

type mapper struct {
	pkg *types.Package
	// home is the package whose binding the declaration is written in:
	// pkg, or os for a ValueResults function of hostfs.
	home  *types.Package
	q     Qualifier
	facts Facts
	// refs are the named types the Ergo text spells, "path.Name", and
	// ".Map" for a prelude type (Close); refPkgs their packages.
	refs    []string
	refPkgs []*types.Package
	// readable are the parameters of a function that may carry a derived
	// `reads` fact (readsShape), set by funcDecl.
	readable []readable
	// writes is the binder of the parameter Go writes in place, set by
	// funcDecl (Decl.Writes).
	writes string
	// effect is the reason of the memory-only row the last funcDecl
	// bound by (GBX6), or "".
	effect string
	// effectFrom is the target of that row when it is not the bound
	// method's own: the declaring type's, for a method Go promotes from an
	// embedded field (GB); else "".
	effectFrom string
	// host is the reason of the host row the last funcDecl wrote
	// (HOST-ROW), or "".
	host string
}

// readable is a parameter, or a method's receiver, that may carry a
// derived `reads` fact: its Go name, which Go's escape analysis prints,
// and its binder in the Ergo signature, which the binding's
// `// ergo:reads` line names.
type readable struct{ goName, ergoName string }

// readsShape reports a Go parameter type whose `reads` fact Go's escape
// analysis can establish: a slice (or a type over one) whose elements
// reach no map, interface, function or channel. Go's analysis does not
// count a write into a map as a mutation (`m[k] = v`, `delete(m, k)` and
// `m[k][0] = v` all print "does not escape, mutate, or call" at go1.27;
// LF0's fixture), and it cannot follow an interface, a function value or a
// channel, so a type that reaches one gets no fact.
func readsShape(t types.Type) bool {
	s, ok := types.Unalias(t).Underlying().(*types.Slice)
	return ok && plainElem(s.Elem(), map[types.Type]bool{})
}

// plainElem reports a type that reaches no map, interface, function,
// channel or unsafe pointer through its elements, fields and pointers.
func plainElem(t types.Type, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	if seen[t] {
		return true
	}
	seen[t] = true
	switch u := t.Underlying().(type) {
	case *types.Basic:
		return u.Kind() != types.UnsafePointer
	case *types.Slice:
		return plainElem(u.Elem(), seen)
	case *types.Array:
		return plainElem(u.Elem(), seen)
	case *types.Pointer:
		return plainElem(u.Elem(), seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if !plainElem(u.Field(i).Type(), seen) {
				return false
			}
		}
		return true
	}
	return false
}

// prelude records a use of the prelude type name, which a type of the bound
// package of that name would hide (Close).
func (m *mapper) prelude(name string) { m.refs = append(m.refs, "."+name) }

// std qualifies a name from Ergo std package pkg (cmp, core, option): as a
// Render placeholder when m.q produces placeholders, or as pkg directly.
func (m *mapper) std(pkg string) string {
	if strings.HasPrefix(m.q(types.NewPackage("x", "x")), "⟦") {
		return "⟦ergo:" + pkg + " " + pkg + "⟧"
	}
	return pkg
}

type pos int

const (
	atParam     pos = iota // a parameter
	atResult               // a result without a last error, or a tuple member
	atErrResult            // the one result beside a last error
	atField                // a struct field
	atInner                // a slice or Map element, a callback's part
)

// refusal is a type that does not map, with its reason.
type refusal struct{ reason string }

func (r refusal) Error() string { return r.reason }

func refuse(format string, args ...any) error { return refusal{fmt.Sprintf(format, args...)} }

func (m *mapper) goText(obj types.Object) string {
	qual := func(p *types.Package) string {
		if p == m.pkg {
			return ""
		}
		return p.Name()
	}
	s := types.ObjectString(obj, qual)
	if tn, ok := obj.(*types.TypeName); ok && !tn.IsAlias() {
		// ObjectString writes the whole underlying type; the line names
		// its kind only, so a large struct stays one short line.
		s = "type " + tn.Name() + " " + kindOf(tn.Type().Underlying())
		if n, ok := tn.Type().(*types.Named); ok && n.TypeParams().Len() > 0 {
			s = "type " + tn.Name() + "[…] " + kindOf(tn.Type().Underlying())
		}
	}
	if c, ok := obj.(*types.Const); ok {
		s = "const " + c.Name() + " " + types.TypeString(c.Type(), qual)
	}
	return strings.ReplaceAll(s, "\n", " ")
}

func kindOf(u types.Type) string {
	switch u := u.(type) {
	case *types.Struct:
		return "struct"
	case *types.Interface:
		return "interface"
	case *types.Signature:
		return "func"
	case *types.Basic:
		return u.Name()
	default:
		return types.TypeString(u, func(p *types.Package) string { return p.Name() })
	}
}

func (m *mapper) decl(obj types.Object, recv *types.Named) Decl {
	d := Decl{Name: obj.Name(), Go: m.goText(obj)}
	switch o := obj.(type) {
	case *types.Func:
		d.Kind = Func
		if recv != nil {
			d.Kind = Method
			d.Name = recv.Obj().Name() + "." + o.Name()
		}
		text, variadic, ctx, err := m.funcDecl(o, recv)
		if err != nil {
			d.Refused = err.Error()
			return d
		}
		d.Ergo, d.Variadic, d.Context, d.Writes, d.Effect, d.EffectFrom, d.Host = text, variadic, ctx, m.writes, m.effect, m.effectFrom, m.host
	case *types.TypeName:
		d.Kind = Type
		text, members, lines, err := m.typeDecl(o)
		if err != nil {
			d.Refused = err.Error()
			return d
		}
		d.Ergo, d.Members, d.Lines = text, members, lines
	case *types.Const:
		d.Kind = Const
		t, err := m.constType(o)
		if err != nil {
			d.Refused = err.Error()
			return d
		}
		d.Ergo = "let " + o.Name() + " " + t
	case *types.Var:
		d.Kind = Var
		if v1.GoReadOnlyVars()[m.pkg.Path()+"."+o.Name()] != "" && !resourceType(o.Type()) && !isError(o.Type()) {
			// A reviewed variable no Go code of its package writes after
			// init (GBX3): a read-only let, a pointer never nil, so a
			// go.Ref. The table holds no interface variable.
			t, err := (&sigMapper{mapper: m, nonNil: true}).typ(o.Type(), atErrResult, false)
			if err != nil {
				d.Refused = err.Error()
				return d
			}
			d.Ergo = "let " + o.Name() + " " + t
			return d
		}
		if !isError(o.Type()) && !resourceType(o.Type()) {
			d.Refused = "a Go variable is host state; call a function"
			return d
		}
		// Package variables are borrowed, never acquired (owner q62).
		// A pointer has no successful-call non-nil promise, so keep the
		// normal field/result Option mapping, not atErrResult's go.Ref.
		t, err := (&sigMapper{mapper: m}).typ(o.Type(), atField, false)
		if err != nil {
			d.Refused = err.Error()
			return d
		}
		d.Ergo = "let " + o.Name() + " " + t
	default:
		d.Refused = "not a function, type, constant or variable"
	}
	return d
}

// resourceType is the Go-side form of interop.md Resources 1. Only the
// actual value's method set counts: a pointer-only Close does not make a
// copy of T implement Close. The checker reads this same signature from
// the binding, without running a Go tool (check/hostres.go).
func resourceType(t types.Type) bool {
	m := types.NewMethodSet(t).Lookup(nil, "Close")
	if m == nil {
		return false
	}
	sig, ok := m.Obj().Type().(*types.Signature)
	if !ok || sig.Params().Len() != 0 || sig.Variadic() {
		return false
	}
	n := sig.Results().Len()
	return n == 0 || n == 1 && isError(sig.Results().At(0).Type())
}

// pure reports whether a member of the bound package is in the reviewed
// pure tier: a package of v1.PureImports whose guard line acts
// (check.PureActs; LAWFFI LF2d), minus v1.PureImportsExcept().
func (m *mapper) pure(name string) bool {
	path := m.pkg.Path()
	return check.PureActs(path) && !slices.Contains(v1.PureImportsExcept()[path], name)
}

func (m *mapper) needs(name string) string {
	if m.pure(name) {
		return ""
	}
	return " needs go.Foreign"
}

// ---------------------------------------------------------------- functions

func (m *mapper) funcDecl(f *types.Func, recv *types.Named) (string, bool, bool, error) {
	sig := f.Type().(*types.Signature)
	subst := map[*types.TypeParam]types.Type{}
	var tparams []string
	unconstrainedBefore := false
	for i := 0; i < sig.TypeParams().Len(); i++ {
		tp := sig.TypeParams().At(i)
		c, core, err := m.constraint(tp)
		if err != nil {
			return "", false, false, err
		}
		if core != nil {
			subst[tp] = core
			continue
		}
		if c == "" {
			unconstrainedBefore = true
			tparams = append(tparams, tp.Obj().Name())
			continue
		}
		if unconstrainedBefore {
			return "", false, false, refuse("type parameters in an order Ergo cannot write: an unconstrained one before %s %s", tp.Obj().Name(), c)
		}
		tparams = append(tparams, tp.Obj().Name()+" "+c)
	}
	target := m.pkg.Path() + "." + f.Name()
	if recv != nil {
		target = m.pkg.Path() + "." + recv.Obj().Name() + "." + f.Name()
	}
	n := sig.Results().Len()
	// A first context.Context parameter is hidden (IG10), except in the
	// context package itself, whose functions take a context as their
	// subject (context.Cause(ctx)), not as the call's cancellation.
	sm := &sigMapper{mapper: m, subst: subst, sync: syncActs(target), method: recv != nil,
		outerFails: n > 0 && isError(sig.Results().At(n-1).Type()),
		ctx:        m.pkg.Path() != "context" && sig.Params().Len() > 0 && isContext(sig.Params().At(0).Type()),
		nonNil:     m.facts[declTarget(f)][nonNilResult]}
	params, results, fails, variadic, err := sm.signature(sig, false)
	if err != nil {
		return "", false, false, err
	}
	// A synchronous callback's rows are the call's (IG6): row parameters
	// CE (its failures, when Go's callback returns an error) and CR (what
	// it needs), named apart from Go's own type parameters.
	for _, row := range []struct {
		name string
		used bool
	}{{rowFails, sm.usedFails}, {rowNeeds, sm.usedNeeds}} {
		if !row.used {
			continue
		}
		for i := 0; i < sig.TypeParams().Len(); i++ {
			if sig.TypeParams().At(i).Obj().Name() == row.name {
				return "", false, false, refuse("a Go type parameter named %s, the binding's row parameter", row.name)
			}
		}
		tparams = append(tparams, row.name)
	}
	// A Go type supplies no lifetime proof. Resource-capable parameters
	// are kept (own) unless a per-function source review establishes a
	// lend.
	off, first := 0, 0
	if recv != nil {
		off = 1
	}
	if sm.ctx {
		first = 1
	}
	for i := range params {
		params[i].own = goResourceParam(sig.Params().At(i+first).Type()) && !v1.GoArgLent(target, i+off)
	}
	// A Go function that sorts or reverses its slice in place and returns
	// nothing (the reviewed v1.GoWrites(); GBX2) returns the slice in Ergo:
	// `let s = slices.Sort(s)`. The lowering passes Go the value the
	// value rule gives it (a copy, or the dead local itself) and takes it
	// back as the call's value.
	// A function or method with results of its own (`(*os.File).Read`;
	// HOST-ROW-3 slice 2) returns the written slice first, then Go's
	// results: `func (f *File) Read(b []byte) ([]byte, int) fails
	// go.Error`.
	if w, ok := v1.GoWrites()[target]; ok && w.Reason != "" {
		if i := w.Index; i >= 0 && i < len(params) && !(variadic && i == len(params)-1) && goSlice(sig.Params().At(i+first).Type()) {
			switch {
			case results == "":
				results = params[i].typ
			case strings.HasPrefix(results, "("):
				results = "(" + params[i].typ + ", " + results[1:]
			default:
				results = "(" + params[i].typ + ", " + results + ")"
			}
			m.writes = params[i].name
		}
	}
	var b strings.Builder
	b.WriteString("func ")
	if recv != nil {
		r := sig.Recv()
		rt := recv.Obj().Name()
		if !inValueSet(recv, f.Name()) {
			rt = "*" + rt // a pointer receiver: the method needs a handle or a borrow
		}
		used := map[string]bool{}
		for _, p := range params {
			used[p.name] = true
		}
		name := r.Name()
		if !binder(name) || used[name] {
			name = "r"
			for i := 0; used[name]; i++ {
				name = fmt.Sprintf("r%d", i)
			}
		}
		// A method lends its receiver for the call (GB1): only the release
		// (Close) and a method whose result may be the receiver itself,
		// which would give the caller a second owner of one Go value,
		// keep it. Any other result that holds a handle keeps the
		// receiver alive instead (the checker's keeps rule, lend.go).
		mode := ""
		if goResourceParam(sig.Recv().Type()) && !v1.GoArgLent(target, 0) && keepsReceiver(f, sig, recv) {
			mode = "own "
		}
		fmt.Fprintf(&b, "(%s %s%s) ", name, mode, rt)
		if readsShape(r.Type()) && recv.TypeArgs().Len() == 0 && recv.TypeParams().Len() == 0 {
			m.readable = append(m.readable, readable{r.Name(), name})
		}
	}
	// A generic function's escape verdict is printed per instance shape,
	// not per declaration, so it gets no fact (LAWFFI risk 3).
	if sig.TypeParams().Len() == 0 && (recv == nil || recv.TypeParams().Len() == 0) {
		for i, p := range params {
			if v := sig.Params().At(i + first); readsShape(v.Type()) {
				m.readable = append(m.readable, readable{v.Name(), p.name})
			}
		}
	}
	b.WriteString(f.Name())
	if len(tparams) > 0 {
		b.WriteString("[" + strings.Join(tparams, ", ") + "]")
	}
	b.WriteString(paramList(params))
	if results != "" {
		b.WriteString(" " + results)
	}
	switch {
	case fails && sm.usedFails:
		b.WriteString(" fails " + rowFails + " | go.Error")
	case fails:
		b.WriteString(" fails go.Error")
	}
	needs := m.needs(f.Name())
	if sm.ctx {
		// A call that takes the task's context is a host wait: foreign,
		// whatever its package (it needs the task, which a pure frame
		// does not have).
		needs = " needs go.Foreign"
	}
	m.effect, m.effectFrom, m.host = "", "", ""
	if e, ok := gofacts.HostOf(target); ok && needs == " needs go.Foreign" && !sm.ctx && hostActs(target) {
		// A host call charges Ambient, the authority to choose the host,
		// not go.Foreign (HOST-ROW); the binding says why.
		needs, m.host = " needs Ambient", e.Reason
	}
	// A method Go promotes from an embedded field is the field's method:
	// its memory-only row is the declaring type's (GB), and the binding
	// names that row with `from`.
	row := declaredTarget(f, target)
	if e, ok := gofacts.EffectOf(row); ok && needs != "" && !sm.ctx && effectActs(row) {
		// A memory-only call needs no capability (GBX6): its row is
		// what is left, and the binding says why.
		needs, m.effect = "", e.Reason
		if row != target {
			m.effectFrom = row
		}
	}
	if sm.usedNeeds {
		if needs == "" {
			needs = " needs " + rowNeeds
		} else {
			needs = " needs " + rowNeeds + " |" + strings.TrimPrefix(needs, " needs")
		}
	}
	b.WriteString(needs)
	return b.String(), variadic, sm.ctx, nil
}

// goResourceParam is the boundary's resource-capable shape. A non-empty
// interface can receive a resource even when it has no Close (io.Reader).
// A concrete host type is a resource only by its Close, not its fields.
func goResourceParam(t types.Type) bool {
	t = types.Unalias(t)
	if isError(t) {
		return false // the error adapter produces go.Error, which crosses
	}
	if _, ok := t.(*types.Pointer); ok {
		return true // a Go pointer may keep an Ergo borrow
	}
	if _, ok := t.(*types.TypeParam); ok {
		return true
	}
	if it, ok := t.Underlying().(*types.Interface); ok {
		return !it.Empty()
	}
	methodType := t
	if n, ok := t.(*types.Named); ok {
		methodType = types.NewPointer(n)
	}
	ms := types.NewMethodSet(methodType)
	if m := ms.Lookup(nil, "Close"); m != nil {
		s := m.Obj().Type().(*types.Signature)
		if s.Params().Len() == 0 && (s.Results().Len() == 0 || s.Results().Len() == 1 && isError(s.Results().At(0).Type())) {
			return true
		}
	}
	switch t := t.(type) {
	case *types.Slice:
		return goResourceParam(t.Elem())
	case *types.Array:
		return goResourceParam(t.Elem())
	case *types.Map:
		return goResourceParam(t.Elem())
	}
	return false
}

// keepsReceiver reports a method of the named type recv that keeps its
// receiver (GB1; interop.md, Resources 5): the release, Close() or Close()
// error, and, on a resource, a method whose result may hold the receiver
// itself (mayHold), since the caller would then own one Go value twice and
// release it twice. Every other method lends its receiver for the call.
func keepsReceiver(f *types.Func, sig *types.Signature, recv *types.Named) bool {
	if f.Name() == "Close" && releaseShape(f) {
		return true
	}
	if !resourceType(types.NewPointer(recv)) {
		return false
	}
	return resultsMayHold(sig, recv)
}

// resultsMayHold reports a result of sig, other than a last error, that may
// hold a value of recv (mayHold).
func resultsMayHold(sig *types.Signature, recv *types.Named) bool {
	for i := 0; i < sig.Results().Len(); i++ {
		if t := sig.Results().At(i).Type(); !isError(t) && mayHold(t, recv, map[types.Type]bool{}) {
			return true
		}
	}
	return false
}

// mayHold reports a Go type whose value may hold a value of recv: recv
// itself or a pointer to it, an interface whose methods *recv has by name,
// a type parameter, or a struct field, element or key that may. The
// checker's alias rule reads the same shape from the binding
// (check/hostclauses.go, hostMayHold).
func mayHold(t types.Type, recv *types.Named, seen map[types.Type]bool) bool {
	t = types.Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	if n, ok := t.(*types.Named); ok && n.Obj() == recv.Obj() {
		return true
	}
	if _, ok := t.(*types.TypeParam); ok {
		return true
	}
	if n, ok := t.(*types.Named); ok {
		// A generic type's arguments (iter.Seq[Value] yields Values): the
		// checker reads a binding's Seq[Value] by its arguments too.
		for i := 0; i < n.TypeArgs().Len(); i++ {
			if mayHold(n.TypeArgs().At(i), recv, seen) {
				return true
			}
		}
	}
	switch u := t.Underlying().(type) {
	case *types.Pointer:
		return mayHold(u.Elem(), recv, seen)
	case *types.Interface:
		// recv may stand in an interface whose methods its method set has
		// by name (the checker reads names only: hostMayHold); an empty
		// interface holds anything.
		ms := types.NewMethodSet(types.NewPointer(recv))
		if types.IsInterface(recv) {
			ms = types.NewMethodSet(recv)
		}
		for i := 0; i < u.NumMethods(); i++ {
			if ms.Lookup(u.Method(i).Pkg(), u.Method(i).Name()) == nil {
				return false
			}
		}
		return true
	case *types.Slice:
		return mayHold(u.Elem(), recv, seen)
	case *types.Array:
		return mayHold(u.Elem(), recv, seen)
	case *types.Map:
		return mayHold(u.Key(), recv, seen) || mayHold(u.Elem(), recv, seen)
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			if u.Field(i).Exported() && mayHold(u.Field(i).Type(), recv, seen) {
				return true
			}
		}
	}
	return false
}

// The row parameters of a function whose callbacks are synchronous.
const (
	rowFails = "CE"
	rowNeeds = "CR"
)

// inValueSet reports whether method name is in the method set of the value
// type n, so a value of n calls it (Go's value receiver or a promotion
// through a value).
func inValueSet(n *types.Named, name string) bool {
	return types.NewMethodSet(n).Lookup(n.Obj().Pkg(), name) != nil
}

// constraint maps a type parameter's constraint: "" for any, "comparable",
// "cmp.Ordered" for cmp.Ordered, or a core type ~[]E or ~map[K]V that replaces
// the parameter (core is set).
func (m *mapper) constraint(tp *types.TypeParam) (string, types.Type, error) {
	c := tp.Constraint()
	if n, ok := c.(*types.Named); ok {
		obj := n.Obj()
		if obj.Pkg() == nil && obj.Name() == "comparable" {
			return "comparable", nil, nil
		}
		if obj.Pkg() != nil && obj.Pkg().Path() == "cmp" && obj.Name() == "Ordered" {
			return m.std("cmp") + ".Ordered", nil, nil
		}
	}
	iface, _ := c.Underlying().(*types.Interface)
	if iface != nil && iface.Empty() {
		return "", nil, nil
	}
	if iface != nil && iface.NumMethods() == 0 && iface.NumEmbeddeds() == 1 {
		if u, ok := iface.EmbeddedType(0).(*types.Union); ok && u.Len() == 1 && u.Term(0).Tilde() {
			switch u.Term(0).Type().(type) {
			case *types.Slice, *types.Map:
				return "", u.Term(0).Type(), nil
			}
		}
	}
	return "", nil, refuse("type parameter %s has the constraint %s, which has no Ergo form (v1 maps any, comparable, cmp.Ordered, ~[]E and ~map[K]V)", tp.Obj().Name(), types.TypeString(c, func(p *types.Package) string { return p.Name() }))
}

// param is a bound parameter: its name, its Ergo type, and whether the
// callee keeps it (the mode word own, q168).
type param struct {
	name, typ string
	own       bool
}

func paramList(ps []param) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.name + " " + p.typ
		if p.own {
			parts[i] = p.name + " own " + p.typ
		}
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

// binder reports whether a Go parameter name is an Ergo binder: a name, not
// `_`, not a keyword and not predeclared (grammar.md Declarations 2; D-D3).
func binder(name string) bool {
	if name == "" || name == "_" || syntax.IsKeyword(name) || check.IsPredeclared(name) {
		return false
	}
	for i, r := range name {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}

type sigMapper struct {
	*mapper
	subst map[*types.TypeParam]types.Type
	// sync: the function is in v1.GoSyncCallbacks(); method: it is a
	// method (row-free callbacks); outerFails: its last result is error.
	// usedFails and usedNeeds record a callback that took the row
	// parameters.
	sync, method, outerFails bool
	usedFails, usedNeeds     bool
	// ctx: the first parameter is context.Context, which the Ergo
	// signature hides (IG10); the callers pass the task's context.
	ctx bool
	// nonNil: the pointer beside the last error is proven non-nil when
	// the error is nil (Derive's nonNilResult, or a reviewed read-only
	// variable), so it binds as go.Ref, not Option[go.Ref].
	nonNil bool
}

// declTarget is the extern target of the Go declaration of f, by the type
// that declares a method (a promoted method's facts are its declaring
// type's), "" for a method of no named type.
func declTarget(f *types.Func) string {
	sig := f.Type().(*types.Signature)
	if sig.Recv() == nil {
		return f.Pkg().Path() + "." + f.Name()
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := types.Unalias(t).(*types.Named)
	if !ok {
		return ""
	}
	return n.Obj().Pkg().Path() + "." + n.Obj().Name() + "." + f.Name()
}

// isContext reports Go's context.Context.
func isContext(t types.Type) bool {
	n, ok := types.Unalias(t).(*types.Named)
	return ok && n.Obj().Pkg() != nil && n.Obj().Pkg().Path() == "context" && n.Obj().Name() == "Context"
}

// signature maps a Go signature: named parameters, the result type, whether
// the last result is error (the row fails go.Error), and whether the last
// parameter is variadic. A callback (cb) writes no parameter names.
func (m *sigMapper) signature(sig *types.Signature, cb bool) ([]param, string, bool, bool, error) {
	var params []param
	used := map[string]bool{}
	first := 0
	if m.ctx && !cb {
		first = 1
	}
	for i := first; i < sig.Params().Len(); i++ {
		v := sig.Params().At(i)
		t := v.Type()
		var text string
		var err error
		if sig.Variadic() && i == sig.Params().Len()-1 {
			if cb {
				return nil, "", false, false, refuse("a variadic callback")
			}
			text, err = m.typ(t.(*types.Slice).Elem(), atInner, cb)
			text = "[]" + text
		} else {
			text, err = m.typ(t, atParam, cb)
		}
		if err != nil {
			return nil, "", false, false, refuse("parameter %d: %s", i+1, err)
		}
		name := v.Name()
		if !binder(name) || used[name] {
			name = fmt.Sprintf("p%d", i)
		}
		used[name] = true
		params = append(params, param{name: name, typ: text})
	}
	n := sig.Results().Len()
	fails := n > 0 && isError(sig.Results().At(n-1).Type())
	if fails {
		n--
	}
	var parts []string
	for i := 0; i < n; i++ {
		at := atResult
		if fails && n == 1 {
			at = atErrResult
		}
		text, err := m.typ(sig.Results().At(i).Type(), at, cb)
		if err != nil {
			return nil, "", false, false, refuse("result %d: %s", i+1, err)
		}
		parts = append(parts, text)
	}
	result := ""
	switch {
	case len(parts) == 1:
		result = parts[0]
	case len(parts) > 1:
		result = "(" + strings.Join(parts, ", ") + ")"
	}
	return params, result, fails, sig.Variadic(), nil
}

// ---------------------------------------------------------------- types

var errorType = types.Universe.Lookup("error").Type()

func isError(t types.Type) bool { return types.Identical(t, errorType) }

// typ maps a Go type at a position (interop.md, Type mapping). cb is set
// inside a callback's signature.
func (m *sigMapper) typ(t types.Type, at pos, cb bool) (string, error) {
	t = types.Unalias(t)
	if isError(t) {
		return "go.Error", nil
	}
	switch t := t.(type) {
	case *types.Basic:
		return basic(t)
	case *types.TypeParam:
		if core, ok := m.subst[t]; ok {
			return m.typ(core, at, cb)
		}
		return t.Obj().Name(), nil
	case *types.Named:
		return m.named(t, at, cb)
	case *types.Pointer:
		return m.pointer(t, at, cb)
	case *types.Slice:
		e, err := m.typ(t.Elem(), atInner, cb)
		if err != nil {
			return "", err
		}
		return "[]" + e, nil
	case *types.Map:
		k, err := m.typ(t.Key(), atInner, cb)
		if err != nil {
			return "", err
		}
		if k == "go.Any" {
			return "", refuse("Ergo compares no go.Any, so a go.Any keys no Map")
		}
		v, err := m.typ(t.Elem(), atInner, cb)
		if err != nil {
			return "", err
		}
		m.prelude("Map")
		return "Map[" + k + ", " + v + "]", nil
	case *types.Array:
		// [N]T is Ergo's [N]T (types.md, Arrays; L1). Go copies an array
		// whole, so it shares nothing with Go unless an element holds a
		// slice or a Map, which v1 does not copy at the boundary.
		if goHoldsShared(t.Elem()) {
			return "", refuse("an array whose elements hold a slice or a Map shares them with Go; v1 binds an array that Go copies whole")
		}
		e, err := m.typ(t.Elem(), atInner, cb)
		if err != nil {
			return "", err
		}
		return "[" + strconv.FormatInt(t.Len(), 10) + "]" + e, nil
	case *types.Chan:
		return "", refuse("Ergo has no channels (196c); wrap the Go channel in a Go function of your module, or use std queue")
	case *types.Interface:
		if t.Empty() {
			// GB2: Go's empty interface is go.Any; a value with a Go form
			// goes in, and go.As[T] takes one out (interop.md, Go any).
			return "go.Any", nil
		}
		return "", refuse("an unnamed interface type")
	case *types.Struct:
		return "", refuse("an unnamed struct type")
	case *types.Signature:
		switch {
		case cb:
			return "", refuse("a function inside a callback")
		case at != atParam:
			return "", refuse("a Go function as a result, a field or an element")
		}
		return m.callback(t)
	}
	return "", refuse("the Go type %s maps to no Ergo type", t)
}

func (m *sigMapper) callback(sig *types.Signature) (string, error) {
	if sig.TypeParams().Len() > 0 {
		return "", refuse("a generic callback")
	}
	params, result, fails, _, err := m.signature(sig, true)
	if err != nil {
		return "", refuse("callback %s", err)
	}
	types := make([]string, len(params))
	for i, p := range params {
		types[i] = p.typ
	}
	s := "func(" + strings.Join(types, ", ") + ")"
	if result != "" {
		s += " " + result
	}
	// Callbacks (IG6; interop.md, Callbacks). A synchronous callback of a
	// function takes the call's rows: it fails CE when Go's callback
	// returns an error that the call returns, and needs CR. One of a
	// method is row-free (a method takes no row parameters in v1), and
	// fails go.Error, Go's own error, when both return one. An escaping
	// callback may run after the call: `escaping`, no capability, and only
	// go.Error, which crosses as Go's error. A callback whose Go type
	// returns an error always has a fails row, so the lowering reads Go's
	// callback type from the Ergo one (lower.goCallback).
	if strings.HasPrefix(result, "(") {
		return "", refuse("a callback with more than one result")
	}
	switch {
	case m.sync && !m.method:
		switch {
		case fails && m.outerFails:
			s += " fails " + rowFails
			m.usedFails = true
		case fails:
			s += " fails go.Error"
		}
		s += " needs " + rowNeeds
		m.usedNeeds = true
	case m.sync:
		if fails {
			s += " fails go.Error"
		}
	default:
		s = "own " + s // the parameter's mode: the callee may keep it
		if fails {
			s += " fails go.Error"
		}
	}
	return s, nil
}

// goSlice reports a Go slice type, or a type over one.
func goSlice(t types.Type) bool {
	if tp, ok := types.Unalias(t).(*types.TypeParam); ok {
		t = tp.Constraint() // S ~[]E: its core type is the slice
		if iface, ok := t.Underlying().(*types.Interface); ok && iface.NumEmbeddeds() == 1 {
			if u, ok := iface.EmbeddedType(0).(*types.Union); ok && u.Len() == 1 {
				t = u.Term(0).Type()
			}
		}
	}
	_, ok := types.Unalias(t).Underlying().(*types.Slice)
	return ok
}

// goHoldsShared reports a Go type whose value shares memory when copied: a
// slice or a Map, a type defined over one, or an array of such. A struct
// is a host type, which crosses as it is.
func goHoldsShared(t types.Type) bool {
	switch u := types.Unalias(t).Underlying().(type) {
	case *types.Slice, *types.Map:
		return true
	case *types.Array:
		return goHoldsShared(u.Elem())
	}
	return false
}

func basic(t *types.Basic) (string, error) {
	switch t.Kind() {
	case types.Bool, types.String,
		types.Int, types.Int8, types.Int16, types.Int32, types.Int64,
		types.Uint, types.Uint8, types.Uint16, types.Uint32, types.Uint64,
		types.Float32, types.Float64, types.Complex64, types.Complex128:
		return t.Name(), nil // byte and rune keep Go's alias name
	case types.Uintptr:
		return "", refuse("Ergo has no uintptr")
	case types.UnsafePointer:
		return "", refuse("Ergo has no unsafe; unsafe stays at the FFI edge")
	}
	return "", refuse("the Go type %s maps to no Ergo type", t.Name())
}

// named maps a named type: time.Duration is duration, iter.Seq[T] is the
// prelude Seq[T], and any other is the host type, when its declaration maps.
func (m *sigMapper) named(n *types.Named, at pos, cb bool) (string, error) {
	obj := n.Obj()
	path := ""
	if obj.Pkg() != nil {
		path = obj.Pkg().Path()
	}
	switch {
	case path == "time" && obj.Name() == "Duration":
		return "duration", nil
	case path == "iter" && obj.Name() == "Seq":
		e, err := m.typ(n.TypeArgs().At(0), atInner, cb)
		if err != nil {
			return "", err
		}
		return m.std("core") + ".Seq[" + e + "]", nil
	case n.TypeArgs().Len() > 0:
		return "", refuse("the generic Go type %s maps to no Ergo type in v1", obj.Name())
	}
	if u, ok := n.Underlying().(*types.Signature); ok && at == atParam && !cb && obj.Exported() {
		// A parameter of a named Go function type (fs.WalkDirFunc) takes
		// a callback of its underlying signature: Go assigns a function
		// literal of that signature to it (IG6).
		return m.callback(u)
	}
	if reason := typeRefusal(obj); reason != "" {
		return "", refuse("type %s is refused: %s", obj.Name(), reason)
	}
	return m.spell(obj), nil
}

func (m *sigMapper) spell(obj *types.TypeName) string {
	m.refs = append(m.refs, obj.Pkg().Path()+"."+obj.Name())
	m.refPkgs = append(m.refPkgs, obj.Pkg())
	if obj.Pkg() == m.home {
		return obj.Name()
	}
	return m.q(obj.Pkg()) + "." + obj.Name()
}

// pointer maps *S, S a named non-interface type: a borrow *S as a
// parameter, go.Ref[S] as the one result beside an error where the
// binder proves it non-nil when the error is (nonNil; GOBIND-NIL),
// Option[go.Ref[S]] as any other result or a field (q32, q38).
func (m *sigMapper) pointer(p *types.Pointer, at pos, cb bool) (string, error) {
	n, ok := types.Unalias(p.Elem()).(*types.Named)
	if !ok {
		return "", refuse("a pointer to an unnamed type %s", types.TypeString(p.Elem(), nil))
	}
	if _, iface := n.Underlying().(*types.Interface); iface {
		return "", refuse("a pointer to an interface")
	}
	if at == atInner || (cb && at != atParam) {
		return "", refuse("a Go pointer in an element or a callback result may be nil; v1 maps a pointer only as a parameter, a result or a field")
	}
	s, err := m.named(n, at, cb)
	if err != nil {
		return "", err
	}
	if s == "duration" || strings.HasPrefix(s, m.std("core")+".Seq[") {
		return "", refuse("a pointer to %s", s)
	}
	switch at {
	case atParam:
		return "*" + s, nil
	case atErrResult:
		if m.nonNil {
			return "go.Ref[" + s + "]", nil
		}
	}
	return m.std("option") + ".Option[go.Ref[" + s + "]]", nil
}

// typeRefusal is why a named type's declaration does not map, or "".
func typeRefusal(obj *types.TypeName) string {
	if !obj.Exported() {
		return "unexported"
	}
	if obj.Pkg() != nil && internalPath(obj.Pkg().Path()) {
		return "it is declared in an internal or vendored package"
	}
	if obj.IsAlias() {
		return "a Go alias; the binding names the type it stands for"
	}
	n, ok := obj.Type().(*types.Named)
	if !ok {
		return "not a named type"
	}
	if n.TypeParams().Len() > 0 {
		return "a generic Go type maps to no Ergo type in v1"
	}
	switch u := n.Underlying().(type) {
	case *types.Basic:
		if _, err := basic(u); err != nil {
			return err.Error()
		}
	case *types.Struct:
	case *types.Interface:
		if u.Empty() {
			return "Ergo has no any; an empty interface hides the host type"
		}
		if !u.IsMethodSet() {
			return "a constraint interface is no type of a value"
		}
		exported := false
		for i := 0; i < u.NumMethods(); i++ {
			exported = exported || u.Method(i).Exported()
		}
		if !exported {
			return "an interface with no exported method"
		}
	case *types.Slice, *types.Map, *types.Array:
		m := &sigMapper{mapper: &mapper{pkg: obj.Pkg(), home: obj.Pkg(), q: func(p *types.Package) string { return p.Name() }}}
		if _, err := m.typ(u, atInner, false); err != nil {
			return err.Error()
		}
	case *types.Signature:
		return "a Go function type maps to no Ergo host type in v1"
	case *types.Chan:
		return "Ergo has no channels (196c)"
	case *types.Pointer:
		return "a defined pointer type"
	default:
		return "its Go type maps to no Ergo type"
	}
	return ""
}

func internalPath(path string) bool {
	for _, e := range strings.Split(path, "/") {
		if e == "internal" || e == "vendor" {
			return true
		}
	}
	return false
}

// typeDecl maps a named type's declaration: `type T basic`, `type T []E`,
// `type T { Field T, … }` for a struct (read-only fields, q34) or `type T {
// Method(sig), … }` for an interface.
func (m *mapper) typeDecl(obj *types.TypeName) (string, []Member, []Line, error) {
	if path := obj.Pkg().Path(); path == "time" && obj.Name() == "Duration" {
		return "", nil, nil, refuse("time.Duration is Ergo's duration")
	}
	if path := obj.Pkg().Path(); path == "iter" && obj.Name() == "Seq" {
		return "", nil, nil, refuse("iter.Seq is the prelude Seq")
	}
	if reason := typeRefusal(obj); reason != "" {
		return "", nil, nil, refuse("%s", reason)
	}
	sm := &sigMapper{mapper: m}
	n := obj.Type().(*types.Named)
	var members []Member
	var lines []Line
	switch u := n.Underlying().(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if !f.Exported() {
				continue
			}
			goText := f.Name() + " " + types.TypeString(f.Type(), func(p *types.Package) string { return p.Name() })
			m.refs = nil
			t, err := sm.typ(f.Type(), atField, false)
			if err != nil {
				members = append(members, Member{f.Name(), goText, err.Error()})
				continue
			}
			lines = append(lines, Line{f.Name(), goText, f.Name() + " " + t, m.refs})
		}
	case *types.Interface:
		for i := 0; i < u.NumMethods(); i++ {
			f := u.Method(i)
			if !f.Exported() {
				continue
			}
			goText := types.ObjectString(f, func(p *types.Package) string { return p.Name() })
			m.refs = nil
			params, results, fails, variadic, err := sm.signature(f.Type().(*types.Signature), false)
			if err == nil && variadic {
				err = refuse("a variadic interface method")
			}
			if err != nil {
				members = append(members, Member{f.Name(), goText, err.Error()})
				continue
			}
			if f.Name() == "Close" && !isGoCloser(obj) && releaseShape(f) && fails {
				// The release: Go's io.Closer, included as Go embeds it
				// (resources R1; interop.md, Resources 1). io's own
				// Closer is the contract's Go form.
				m.refs = []string{"io.Closer"}
				include := ".." + m.q(types.NewPackage("io", "io")) + ".Closer"
				if m.pkg.Path() == "io" {
					include = "..Closer"
				}
				lines = append(lines, Line{f.Name(), goText, include, m.refs})
				continue
			}
			target := m.pkg.Path() + "." + obj.Name() + "." + f.Name()
			fsig := f.Type().(*types.Signature)
			for j := range params {
				params[j].own = goResourceParam(fsig.Params().At(j).Type()) && !v1.GoArgLent(target, j)
			}
			line := f.Name() + paramList(params)
			// An operation lends its receiver (GB1), as a method does: only
			// the release and, on a resource, one whose result may hold
			// the receiver keep it.
			if !v1.GoArgLent(target, -1) && (f.Name() == "Close" && releaseShape(f) || resourceType(n) && resultsMayHold(fsig, n)) {
				line = "own " + line // the operation keeps its receiver
			}
			if results != "" {
				line += " " + results
			}
			if fails {
				line += " fails go.Error"
			}
			line += m.needs("")

			lines = append(lines, Line{f.Name(), goText, line, m.refs})
		}
		m.refs = nil
		if len(lines) == 0 {
			return "", members, nil, refuse("no method of the interface maps")
		}
	default:
		t, err := sm.typ(u, atInner, false)
		if err != nil {
			return "", nil, nil, err
		}
		return "type " + obj.Name() + " " + t + m.releaseClause(n), nil, nil, nil
	}
	m.refs = nil
	if _, iface := n.Underlying().(*types.Interface); iface {
		return typeText(obj.Name(), lines), members, lines, nil
	}
	return typeText(obj.Name(), lines) + m.releaseClause(n), members, lines, nil
}

// releaseClause is ` implements Closer` for a Go type whose method set
// has Close() or Close() error, the release (resources R1; interop.md,
// Resources 1): the binder reads the name once, and the binding states
// the contract. In package io, Closer is io.Closer, the contract's Go
// form; a package that declares another Closer of its own names
// io.Closer, which stands for the contract in a binding.
func (m *mapper) releaseClause(n *types.Named) string {
	if !resourceType(types.NewPointer(n)) {
		return ""
	}
	if m.pkg.Path() == "io" {
		return " implements Closer"
	}
	return " implements " + m.std("core") + ".Closer"
}

// isGoCloser reports Go's io.Closer, whose own Close stays listed.
func isGoCloser(obj *types.TypeName) bool {
	return obj.Pkg() != nil && obj.Pkg().Path() == "io" && obj.Name() == "Closer"
}

// releaseShape reports Close() or Close() error.
func releaseShape(f *types.Func) bool {
	sig := f.Type().(*types.Signature)
	n := sig.Results().Len()
	return sig.Params().Len() == 0 && !sig.Variadic() && (n == 0 || n == 1 && isError(sig.Results().At(0).Type()))
}

// typeText is the declaration of a struct or interface type with lines.
func typeText(name string, lines []Line) string {
	if len(lines) == 0 {
		return "type " + name + " {}"
	}
	var texts []string
	for _, l := range lines {
		texts = append(texts, l.Text)
	}
	return "type " + name + " {\n\t" + strings.Join(texts, ",\n\t") + ",\n}"
}

// constType maps a constant's type: a typed constant keeps its type, an
// untyped one takes Go's default type when its value fits (196u).
func (m *mapper) constType(c *types.Const) (string, error) {
	b, ok := c.Type().(*types.Basic)
	if !ok || b.Info()&types.IsUntyped == 0 {
		return (&sigMapper{mapper: m}).typ(c.Type(), atField, false)
	}
	v := c.Val()
	switch b.Kind() {
	case types.UntypedBool:
		return "bool", nil
	case types.UntypedString:
		return "string", nil
	case types.UntypedRune:
		return "rune", nil
	case types.UntypedInt:
		if _, exact := constant.Int64Val(v); !exact {
			return "", refuse("the untyped constant does not fit Go's default type int; Ergo has no untyped constants (196u)")
		}
		return "int", nil
	case types.UntypedFloat:
		if f, _ := constant.Float64Val(v); f == 0 && constant.Sign(v) != 0 || math.IsInf(f, 0) {
			return "", refuse("the untyped constant does not fit Go's default type float64; Ergo has no untyped constants (196u)")
		}
		return "float64", nil
	case types.UntypedComplex:
		return "complex128", nil
	}
	return "", refuse("an untyped %s constant", b.Name())
}

// syncActs reports a function whose callbacks the reviewed table
// v1.GoSyncCallbacks() lists as synchronous, with its guard line acting
// (check.SyncActs; LAWFFI LF2d).
func syncActs(target string) bool {
	ok, _, _ := check.SyncActs(target)
	return ok
}

// hostActs reports a function whose host row acts (check.HostActs;
// HOST-ROW).
func hostActs(target string) bool {
	ok, _, _ := check.HostActs(target)
	return ok
}

// declaredTarget is the target of f where Go declares it: for a method,
// its own receiver type's (`sync/atomic.Int64.Add`), which differs from
// target, the bound type's, when Go promotes f from an embedded field
// (GB). A function, or a method whose receiver is not a named type, is
// target itself.
func declaredTarget(f *types.Func, target string) string {
	sig, ok := f.Origin().Type().(*types.Signature)
	if !ok || sig.Recv() == nil || f.Pkg() == nil {
		return target
	}
	t := sig.Recv().Type()
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	n, ok := t.(*types.Named)
	if !ok {
		return target
	}
	return f.Pkg().Path() + "." + n.Obj().Name() + "." + f.Name()
}

// effectActs reports a function whose memory-only row acts
// (check.EffectActs; np4f-plan GBX6).
func effectActs(target string) bool {
	ok, _, _ := check.EffectActs(target)
	return ok
}
