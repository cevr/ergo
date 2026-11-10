package check

import "ergo.local/prototype/internal/sketch/syntax"

// Composite literals in the class H walk (round 17 M1). A keyed element's
// key is a field name in a struct literal and an expression in a map, slice
// or array literal, as in go/types (literals.go: the key of a map literal
// is checked as an expression, the key of a struct literal is looked up as
// a field). The walk decides which from the literal's written type, without
// typing a body: a type name resolves to its declaration, whose type was
// resolved before any body (decl.go run), and an elided type comes from the
// enclosing literal (the parser elides a type only there). Where the kind
// is not decided so (a type parameter, a name that is no type), every key
// is walked as an expression: an extra edge only merges components, and
// class G then refuses at worst; such keys are
// recorded for the oracle (refWalker.undecided).

// litKind is what the walk knows about a composite literal's type.
type litKind uint8

const (
	litUnknown litKind = iota // keys are walked as expressions, conservatively
	litStruct                 // keys are field names
	litKeyed                  // a map, slice or array: keys are expressions
	litInvalid                // an elided type where none may be elided (C1)
)

// A litType is a composite literal's type as the walk knows it: a checked
// type (typ), or a written structural type (expr: an array, map or struct
// type) and the names that resolve it (env).
type litType struct {
	kind litKind
	typ  Type
	expr syntax.Expr
	env  *typeEnv
}

// A typeEnv resolves the names of a written type: the file (imports), the
// type parameters in scope, which come first (decl.go lookupType), and in a
// body its local scopes, whose row bindings shadow declarations (a local
// value hides no type there; types.md, Scope 1).
// The walk reads its own scopes (bodyEnv), typing its own (typingEnv), so
// both read a literal's kind by the one rule (litTypeOf, elemOf).
type typeEnv struct {
	file   *fileInfo
	params map[string]bool
	local  func(name string) bool // nil outside a body
}

// fromType is the litType of a checked type.
func fromType(t Type) litType {
	if t == nil {
		return litType{}
	}
	switch under(t).(type) {
	case *Struct:
		return litType{kind: litStruct, typ: t}
	case *Slice, *Array, *Map, *MachineType:
		return litType{kind: litKeyed, typ: t}
	}
	return litType{}
}

// litTypeOf resolves a written type.
func (c *checker) litTypeOf(e syntax.Expr, env *typeEnv) litType {
	c.walkOps++
	switch e := e.(type) {
	case *syntax.ParenExpr:
		return c.litTypeOf(e.X, env)
	case *syntax.Ident:
		if env.params[e.Name] || env.local != nil && env.local(e.Name) {
			return litType{}
		}
		if tn, ok := c.typeObjectPast(e.Name, env.file).(*TypeName); ok {
			return fromType(tn.Type)
		}
	case *syntax.IndexExpr:
		// An instance: its kind is its generic type's; a field type that
		// names a type parameter is unknown (fromType).
		return c.litTypeOf(e.X, env)
	case *syntax.SelectorExpr:
		id, ok := e.X.(*syntax.Ident)
		if !ok || env.local != nil && env.local(id.Name) {
			return litType{}
		}
		if pn, ok := c.typeObjectPast(id.Name, env.file).(*PkgName); ok && !pn.broken {
			if tn, ok := pn.Pkg.Scope.names[e.Sel.Name].(*TypeName); ok {
				return fromType(tn.Type)
			}
		}
	case *syntax.MapType:
		if tn := c.declaredMap(); tn != nil {
			return fromType(tn.Type) // the package's own Map (L6)
		}
		return litType{kind: litKeyed, expr: e, env: env}
	case *syntax.ArrayType:
		return litType{kind: litKeyed, expr: e, env: env}
	}
	return litType{}
}

// typeObject looks a name up as lookup does above the locals: the package
// scope and the universe, then the file's imports.
func (c *checker) typeObject(name string, file *fileInfo) Object {
	return c.typeObjectWith(name, file, (*Scope).lookup)
}

// typeObjectPast is typeObject outside a value position, as lookupType
// looks a name up without type parameters: it sees through a refused
// package-level binder (P2, P5).
func (c *checker) typeObjectPast(name string, file *fileInfo) Object {
	return c.typeObjectWith(name, file, scopeLookupPast)
}

func (c *checker) typeObjectWith(name string, file *fileInfo, find func(*Scope, string) (Object, *Scope)) Object {
	if o, at := find(c.pkg.Scope, name); o != nil {
		if imp := c.shadowImport(name, at, file); imp != nil {
			return imp
		}
		return o
	}
	if file != nil {
		return file.scope.names[name]
	}
	return nil
}

// shadowImport gives a refused import its binding to prevent cascades:
// over a prelude name, or over its clashing package declaration. A local
// still wins. Typing and the lexical reference walk use the same rule.
func (c *checker) shadowImport(name string, at *Scope, file *fileInfo) Object {
	if file == nil {
		return nil
	}
	pn, ok := file.scope.names[name].(*PkgName)
	if ok && (c.abovePackage(at) || at == c.pkg.Scope && pn.broken) {
		return pn
	}
	return nil
}

// elemOf is the type of an elided literal that is a key (key) or an element
// of a literal of type lt. Only an array, slice or map literal elides its
// elements' type, and a map literal its keys' type, as in Go (round 17 C1):
// an elided literal anywhere else is an error (expr.go fieldValue), which
// the checker does not type further. It looks nothing up by field, so a
// literal's elements cost one step each (round 17 M2).
func (c *checker) elemOf(lt litType, key bool) litType {
	c.walkOps++
	if lt.kind == litStruct || lt.kind == litInvalid {
		return litType{kind: litInvalid}
	}
	if lt.typ != nil {
		switch u := under(lt.typ).(type) {
		case *Slice:
			if !key {
				return fromType(u.Elem)
			}
		case *Array:
			if !key {
				return fromType(u.Elem)
			}
		case *Map:
			if key {
				return fromType(u.Key)
			}
			return fromType(u.Elem)
		}
		return litType{}
	}
	switch e := lt.expr.(type) {
	case *syntax.ArrayType:
		if !key {
			return c.litTypeOf(e.Elt, lt.env)
		}
	case *syntax.MapType:
		if key {
			return c.litTypeOf(e.Key, lt.env)
		}
		return c.litTypeOf(e.Value, lt.env)
	}
	return litType{}
}

// bodyEnv is the type environment of the body being walked.
func (w *refWalker) bodyEnv() *typeEnv {
	return &typeEnv{file: w.c.cur, params: w.tparams, local: w.typeLocal}
}

// typingEnv is the type environment of the body being typed, as bodyEnv is
// the walk's: the body's type parameters (localTypeCtx) and the names its
// scopes bind below the package scope.
func (c *checker) typingEnv() *typeEnv {
	env := &typeEnv{file: c.cur, params: map[string]bool{}}
	for _, tp := range c.fn.typeParams() {
		env.params[tp.Name] = true
	}
	if c.fn != nil && c.fn.scope != nil {
		sc := c.fn.scope
		env.local = func(name string) bool {
			// A type position (P2), which sees through a local value to
			// a package type (types.md, Scope 1; lookupType).
			_, at := scopeLookupType(sc, name)
			return at != nil && at != c.pkg.Scope && !c.abovePackage(at)
		}
	}
	return env
}

// litTypeOfLit is the type of a composite literal as the walk knows it: its
// written type read by litTypeOf, or, for an elided type, the type elided
// reads from the enclosing literal (elemOf; nil where none gives one).
func (c *checker) litTypeOfLit(x *syntax.CompositeLit, elided func() litType) litType {
	if x.Type == nil {
		if elided == nil {
			return litType{}
		}
		return elided()
	}
	return c.litTypeOf(x.Type, c.typingEnv())
}

// element walks an element, key or value of a composite literal of type
// lt; an elided literal there takes its type from lt (elemOf), which is
// asked only then.
func (w *refWalker) element(e syntax.Expr, lt litType, key bool) {
	w.ops++
	if cl, ok := e.(*syntax.CompositeLit); ok && cl.Type == nil {
		w.compositeLit(cl, w.c.elemOf(lt, key))
		return
	}
	w.expr(e)
}

// compositeLit walks a composite literal; lt is its elided type, when its
// type is not written.
func (w *refWalker) compositeLit(x *syntax.CompositeLit, lt litType) {
	if x.Type == nil && lt.kind == litInvalid {
		// An elided type where none may be elided: the checker reports it
		// and does not type the literal, so nothing in it is a reference.
		syntax.Inspect(x, func(n syntax.Node) bool {
			w.visit(n)
			return true
		})
		return
	}
	w.visit(x)
	if x.Type != nil {
		w.typ(x.Type)
		lt = w.c.litTypeOf(x.Type, w.bodyEnv())
	}
	if x.Base != nil {
		w.expr(x.Base) // a record update's base is a value, read first
	}
	for _, e := range x.Elts {
		kv, ok := e.(*syntax.KeyValueExpr)
		if !ok {
			w.element(e, lt, false)
			continue
		}
		w.visit(kv)
		id, isIdent := kv.Key.(*syntax.Ident)
		switch {
		case lt.kind == litStruct && isIdent:
			w.visit(id) // a field name
		case lt.kind == litStruct:
			w.expr(kv.Key) // not a field name: an error, walked as a value
		default:
			if lt.kind == litUnknown && isIdent && w.undecided != nil {
				w.undecided[id] = true
			}
			w.element(kv.Key, lt, true)
		}
		w.element(kv.Value, lt, false)
	}
}
