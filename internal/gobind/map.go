// Package gobind writes the Ergo binding of a Go package: the declarations
// that `import go P` makes usable, each with its Ergo signature and rows, and
// a reason for each Go declaration that does not map (docs/spec/interop.md;
// #202). It is the only code that reads Go signatures: it runs go list and
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
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
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
	Refused  string
	// Members are the refusals of a struct's fields and an interface's
	// methods, by member name, for a type that maps.
	Members []Member
}

// Member is a field or interface method that a mapped type leaves out.
type Member struct {
	Name, Go, Refused string
}

// Target is the extern target of d in package path: `net/url.Parse`,
// `net/url.URL.Hostname`.
func (d Decl) Target(path string) string { return path + "." + d.Name }

// Qualifier spells a package other than the bound one in Ergo source: the
// name the binding's import gives it.
type Qualifier func(*types.Package) string

// Map maps one exported Go object of pkg by the table of
// docs/spec/interop.md (Type mapping). For a method, obj is the method and
// recv the named type whose method set holds it. q spells other packages.
func Map(obj types.Object, recv *types.Named, q Qualifier) Decl {
	m := &mapper{pkg: obj.Pkg(), q: q}
	if recv != nil {
		m.pkg = recv.Obj().Pkg()
	}
	return m.decl(obj, recv)
}

type mapper struct {
	pkg *types.Package
	q   Qualifier
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
		text, variadic, err := m.funcDecl(o, recv)
		if err != nil {
			d.Refused = err.Error()
			return d
		}
		d.Ergo, d.Variadic = text, variadic
	case *types.TypeName:
		d.Kind = Type
		text, members, err := m.typeDecl(o)
		if err != nil {
			d.Refused = err.Error()
			return d
		}
		d.Ergo, d.Members = text, members
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
		if !isError(o.Type()) {
			d.Refused = "a Go variable is host state; call a function"
			return d
		}
		d.Ergo = "let " + o.Name() + " go.Error"
	default:
		d.Refused = "not a function, type, constant or variable"
	}
	return d
}

// pure reports whether a member of the bound package is in the reviewed
// pure tier: a package of v1.PureImports, minus v1.PureImportsExcept.
func (m *mapper) pure(name string) bool {
	path := m.pkg.Path()
	return slices.Contains(v1.PureImports, path) && !slices.Contains(v1.PureImportsExcept[path], name)
}

func (m *mapper) needs(name string) string {
	if m.pure(name) {
		return ""
	}
	return " needs go.Foreign"
}

// ---------------------------------------------------------------- functions

func (m *mapper) funcDecl(f *types.Func, recv *types.Named) (string, bool, error) {
	sig := f.Type().(*types.Signature)
	subst := map[*types.TypeParam]types.Type{}
	var tparams []string
	unconstrainedBefore := false
	for i := 0; i < sig.TypeParams().Len(); i++ {
		tp := sig.TypeParams().At(i)
		c, core, err := constraint(tp)
		if err != nil {
			return "", false, err
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
			return "", false, refuse("type parameters in an order Ergo cannot write: an unconstrained one before %s %s", tp.Obj().Name(), c)
		}
		tparams = append(tparams, tp.Obj().Name()+" "+c)
	}
	sm := &sigMapper{mapper: m, subst: subst}
	params, results, fails, variadic, err := sm.signature(sig, false)
	if err != nil {
		return "", false, err
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
		fmt.Fprintf(&b, "(%s %s) ", name, rt)
	}
	b.WriteString(f.Name())
	if len(tparams) > 0 {
		b.WriteString("[" + strings.Join(tparams, ", ") + "]")
	}
	b.WriteString(paramList(params))
	if results != "" {
		b.WriteString(" " + results)
	}
	if fails {
		b.WriteString(" fails go.Error")
	}
	b.WriteString(m.needs(f.Name()))
	return b.String(), variadic, nil
}

// inValueSet reports whether method name is in the method set of the value
// type n, so a value of n calls it (Go's value receiver or a promotion
// through a value).
func inValueSet(n *types.Named, name string) bool {
	return types.NewMethodSet(n).Lookup(n.Obj().Pkg(), name) != nil
}

// constraint maps a type parameter's constraint: "" for any, "comparable",
// "ordered" for cmp.Ordered, or a core type ~[]E or ~map[K]V that replaces
// the parameter (core is set).
func constraint(tp *types.TypeParam) (string, types.Type, error) {
	c := tp.Constraint()
	if n, ok := c.(*types.Named); ok {
		obj := n.Obj()
		if obj.Pkg() == nil && obj.Name() == "comparable" {
			return "comparable", nil, nil
		}
		if obj.Pkg() != nil && obj.Pkg().Path() == "cmp" && obj.Name() == "Ordered" {
			return "ordered", nil, nil
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

type param struct{ name, typ string }

func paramList(ps []param) string {
	parts := make([]string, len(ps))
	for i, p := range ps {
		parts[i] = p.name + " " + p.typ
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
}

// signature maps a Go signature: named parameters, the result type, whether
// the last result is error (the row fails go.Error), and whether the last
// parameter is variadic. A callback (cb) writes no parameter names.
func (m *sigMapper) signature(sig *types.Signature, cb bool) ([]param, string, bool, bool, error) {
	var params []param
	used := map[string]bool{}
	for i := 0; i < sig.Params().Len(); i++ {
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
		params = append(params, param{name, text})
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
		v, err := m.typ(t.Elem(), atInner, cb)
		if err != nil {
			return "", err
		}
		return "Map[" + k + ", " + v + "]", nil
	case *types.Array:
		return "", refuse("Ergo has no fixed arrays; a Go array %s maps to no Ergo type", types.TypeString(t, nil))
	case *types.Chan:
		return "", refuse("Ergo has no channels (196c); wrap the Go channel in a Go function of your module, or use std queue")
	case *types.Interface:
		if t.Empty() {
			return "", refuse("Ergo has no any; an interface{} parameter or result hides the host type")
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
	if fails {
		s += " fails go.Error"
	}
	return s, nil
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
		return "Seq[" + e + "]", nil
	case n.TypeArgs().Len() > 0:
		return "", refuse("the generic Go type %s maps to no Ergo type in v1", obj.Name())
	}
	if reason := typeRefusal(obj); reason != "" {
		return "", refuse("type %s is refused: %s", obj.Name(), reason)
	}
	return m.spell(obj), nil
}

func (m *sigMapper) spell(obj *types.TypeName) string {
	if obj.Pkg() == m.pkg {
		return obj.Name()
	}
	return m.q(obj.Pkg()) + "." + obj.Name()
}

// pointer maps *S, S a named non-interface type: a borrow *S as a
// parameter, go.Ref[S] as the one result beside an error (nil there is a
// defect), Option[go.Ref[S]] as another result or a field (q32, q38).
func (m *sigMapper) pointer(p *types.Pointer, at pos, cb bool) (string, error) {
	n, ok := types.Unalias(p.Elem()).(*types.Named)
	if !ok {
		return "", refuse("a pointer to an unnamed type %s", types.TypeString(p.Elem(), nil))
	}
	if _, iface := n.Underlying().(*types.Interface); iface {
		return "", refuse("a pointer to an interface")
	}
	if at == atInner {
		return "", refuse("a Go pointer in an element or a callback result may be nil; v1 maps a pointer only as a parameter, a result or a field")
	}
	s, err := m.named(n, at, cb)
	if err != nil {
		return "", err
	}
	if s == "duration" || strings.HasPrefix(s, "Seq[") {
		return "", refuse("a pointer to %s", s)
	}
	switch at {
	case atParam:
		return "*" + s, nil
	case atErrResult:
		return "go.Ref[" + s + "]", nil
	}
	return "Option[go.Ref[" + s + "]]", nil
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
	case *types.Slice, *types.Map:
		m := &sigMapper{mapper: &mapper{pkg: obj.Pkg(), q: func(p *types.Package) string { return p.Name() }}}
		if _, err := m.typ(u, atInner, false); err != nil {
			return err.Error()
		}
	case *types.Signature:
		return "a Go function type maps to no Ergo host type in v1"
	case *types.Chan:
		return "Ergo has no channels (196c)"
	case *types.Array:
		return "Ergo has no fixed arrays"
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
func (m *mapper) typeDecl(obj *types.TypeName) (string, []Member, error) {
	if path := obj.Pkg().Path(); path == "time" && obj.Name() == "Duration" {
		return "", nil, refuse("time.Duration is Ergo's duration")
	}
	if path := obj.Pkg().Path(); path == "iter" && obj.Name() == "Seq" {
		return "", nil, refuse("iter.Seq is the prelude Seq")
	}
	if reason := typeRefusal(obj); reason != "" {
		return "", nil, refuse("%s", reason)
	}
	sm := &sigMapper{mapper: m}
	n := obj.Type().(*types.Named)
	var members []Member
	var lines []string
	switch u := n.Underlying().(type) {
	case *types.Struct:
		for i := 0; i < u.NumFields(); i++ {
			f := u.Field(i)
			if !f.Exported() {
				continue
			}
			goText := f.Name() + " " + types.TypeString(f.Type(), func(p *types.Package) string { return p.Name() })
			t, err := sm.typ(f.Type(), atField, false)
			if err != nil {
				members = append(members, Member{f.Name(), goText, err.Error()})
				continue
			}
			lines = append(lines, f.Name()+" "+t)
		}
	case *types.Interface:
		for i := 0; i < u.NumMethods(); i++ {
			f := u.Method(i)
			if !f.Exported() {
				continue
			}
			goText := types.ObjectString(f, func(p *types.Package) string { return p.Name() })
			params, results, fails, variadic, err := sm.signature(f.Type().(*types.Signature), false)
			if err == nil && variadic {
				err = refuse("a variadic interface method")
			}
			if err != nil {
				members = append(members, Member{f.Name(), goText, err.Error()})
				continue
			}
			line := f.Name() + paramList(params)
			if results != "" {
				line += " " + results
			}
			if fails {
				line += " fails go.Error"
			}
			line += m.needs("")
			lines = append(lines, line)
		}
		if len(lines) == 0 {
			return "", members, refuse("no method of the interface maps")
		}
	default:
		t, err := sm.typ(u, atInner, false)
		if err != nil {
			return "", nil, err
		}
		return "type " + obj.Name() + " " + t, nil, nil
	}
	if len(lines) == 0 {
		return "type " + obj.Name() + " {}", members, nil
	}
	return "type " + obj.Name() + " {\n\t" + strings.Join(lines, ",\n\t") + ",\n}", members, nil
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
