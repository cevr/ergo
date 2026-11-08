package check

import (
	"strings"

	"ergo.local/prototype/internal/sketch/std"
)

// Construction authority and member visibility across packages
// (types.md, Zero values 6; grammar.md, Visibility 2 and 3; 196b plan Q6,
// Q7). A record with an unexported field is built only by its package:
// outside it, a literal of the record, a record update of it and every
// implicit zero value that holds it are S114, and its values come from the
// functions the package exports. An unexported field or operation is not
// named outside its package (S104), and an operation type with an
// unexported operation is implemented only by its package's types.
//
// hasAuthority is the one owner of the rule; literals, record updates,
// zero values, property generators and the exported fact Constructible
// all read it.
//
// Prior art: Rust refuses a struct literal and a functional update with a
// private field outside its module (E0451); Gleam's opaque types hide their
// constructors; Go leaves Q{} open across packages (types.md, Zero values 6
// receipt).

// hasAuthority reports whether s is the body of a record with an
// unexported field declared in a package other than from, so from may not
// build it field by field.
func hasAuthority(s *Struct, from *Package) bool {
	if s.obj == nil || s.obj.Pkg == from {
		return false
	}
	for _, f := range s.Fields {
		if !isExported(f.Name) {
			return true
		}
	}
	return false
}

// literalAuthority is the record whose authority a literal or a record
// update of t needs in package from: t's own body (a defined type over a
// record has the record's body, so its package), or nil.
func literalAuthority(t Type, from *Package) *TypeName {
	if s, ok := under(t).(*Struct); ok && hasAuthority(s, from) {
		return s.obj
	}
	return nil
}

// zeroAuthority is the record whose authority the zero value of t needs in
// package from: t's own body, or a record t holds by value through fields
// and tuple elements at any depth, or nil. A slice or Map is empty at zero,
// so it builds no element.
func zeroAuthority(t Type, from *Package) *TypeName {
	seen := map[*Struct]bool{}
	var walk func(Type) *TypeName
	walk = func(t Type) *TypeName {
		switch u := under(t).(type) {
		case *Tuple:
			for _, e := range u.Types {
				if a := walk(e); a != nil {
					return a
				}
			}
		case *Struct:
			if seen[u] {
				return nil
			}
			seen[u] = true
			if hasAuthority(u, from) {
				return u.obj
			}
			for _, f := range u.Fields {
				if a := walk(f.Type); a != nil {
					return a
				}
			}
		}
		return nil
	}
	return walk(t)
}

// Constructible reports whether package from may build a value of t from
// its parts: false when t, or a type a value of t holds (a field, a tuple,
// slice or Map element or key, a union variant, at any depth), is a record
// with an unexported field declared in another package. Generators (#162),
// codecs (#49) and refinements read it, and build such a type only through
// its package's exported functions (docs/decisions.md, 2026-11-03 (a)).
func Constructible(t Type, from *Package) bool {
	seen := map[Type]bool{}
	var ok func(Type) bool
	ok = func(t Type) bool {
		if seen[t] {
			return true
		}
		seen[t] = true
		switch u := under(t).(type) {
		case *Struct:
			if hasAuthority(u, from) {
				return false
			}
			for _, f := range u.Fields {
				if !ok(f.Type) {
					return false
				}
			}
		case *Tuple:
			for _, e := range u.Types {
				if !ok(e) {
					return false
				}
			}
		case *Slice:
			return ok(u.Elem)
		case *Map:
			return ok(u.Key) && ok(u.Elem)
		case *Union:
			for _, v := range u.Variants {
				if !ok(v) {
					return false
				}
			}
		}
		return true
	}
	return ok(t)
}

// Buildable reports whether package from can make a value of t with no
// authority: from literals, empty literals, zero values, conversions and
// function values of its own, never with a value that a function of
// another package returns (fu-constructible). It is the least fixed point
// of these rules over t's structure:
//   - a record whose body has authority in from (hasAuthority) is never
//     built; any other record is built when each of its fields is (an
//     omitted field is a zero value, and a zero value is buildable only
//     where a literal is);
//   - a tuple is built when each element is, a union when one variant is
//     (a variant value is a value of the union), a borrow when its element is;
//   - a basic type is built from a literal, a slice or a Map from an empty
//     literal whatever its element (it holds none), and a function value
//     from a literal of from;
//   - an open operation type is built: from declares a type that
//     implements it; a sealed one (an unexported operation of another
//     package, sealedBy) is built when one of its implementers is, and
//     only its owner's types implement it (implementers; fu-hostguard2);
//   - a type parameter and every other type count as built.
//
// Constructible is a different fact: whether a generator may build t from
// its parts with no hidden field anywhere, so an empty slice of an
// authority record is not Constructible but is Buildable. The host guard
// (stdlib.md, Host packages) reads Buildable.
func Buildable(t Type, from *Package) bool {
	// The nodes reachable from t, each once, and their children.
	idx := map[Type]int{}
	var nodes []Type
	var kids [][]int
	sealed := map[int]bool{}
	var visit func(Type) int
	visit = func(t Type) int {
		if i, ok := idx[t]; ok {
			return i
		}
		i := len(nodes)
		idx[t] = i
		nodes = append(nodes, t)
		kids = append(kids, nil)
		var cs []Type
		switch u := under(t).(type) {
		case *Struct:
			if !hasAuthority(u, from) {
				for _, f := range u.Fields {
					cs = append(cs, f.Type)
				}
			}
		case *Tuple:
			cs = u.Types
		case *Union:
			cs = u.Variants
		case *Pointer:
			cs = []Type{u.Elem}
		case *Interface:
			if owner := sealedOwner(u, from); owner != nil {
				sealed[i] = true
				cs = implementers(u, owner)
			}
		}
		for _, c := range cs {
			j := visit(c)
			kids[i] = append(kids[i], j)
		}
		return i
	}
	visit(t)
	// Kleene iteration from all false: each step is monotone, so it stops
	// at the least fixed point (a value is built in finitely many steps).
	val := make([]bool, len(nodes))
	for changed := true; changed; {
		changed = false
		for i, n := range nodes {
			if val[i] {
				continue
			}
			v := true
			switch u := under(n).(type) {
			case *Struct:
				if hasAuthority(u, from) {
					v = false
					break
				}
				for _, j := range kids[i] {
					v = v && val[j]
				}
			case *Tuple, *Pointer:
				for _, j := range kids[i] {
					v = v && val[j]
				}
			case *Union:
				v = false
				for _, j := range kids[i] {
					v = v || val[j]
				}
			case *Interface:
				if sealed[i] {
					v = false
					for _, j := range kids[i] {
						v = v || val[j]
					}
				}
			}
			if v {
				val[i] = true
				changed = true
			}
		}
	}
	return val[0]
}

// Authorities are the records with authority in from on Buildable's graph
// of t (fu-ambient-ctor): the records a value of t holds through fields of
// records without authority, tuple elements, union variants, borrows and a
// sealed operation type's implementers, where the walk stops. A slice, a
// Map, a function type and an open operation type are built empty or by
// from, so their parts are not on the graph. Another package builds t
// exactly when it holds a value of each record on some path, so a value of
// one of them, handed out with no authority, can make t buildable.
func Authorities(t Type, from *Package) []*TypeName {
	var out []*TypeName
	seen := map[Type]bool{}
	var walk func(Type)
	walk = func(t Type) {
		if seen[t] {
			return
		}
		seen[t] = true
		switch u := under(t).(type) {
		case *Struct:
			if hasAuthority(u, from) {
				out = appendAuthority(out, u.obj)
				return
			}
			for _, f := range u.Fields {
				walk(f.Type)
			}
		case *Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *Union:
			for _, v := range u.Variants {
				walk(v)
			}
		case *Pointer:
			walk(u.Elem)
		case *Interface:
			if owner := sealedOwner(u, from); owner != nil {
				for _, c := range implementers(u, owner) {
					walk(c)
				}
			}
		}
	}
	walk(t)
	return out
}

// Exposes are the records with authority in from whose values package from
// gets out of a value of t (fu-ambient-ctor): t itself, a field it may
// read (every field of a record without authority, the exported fields of
// one with authority), a tuple element, a slice or Map element or key, a
// union variant, a borrow's element, the result of a function value or of
// an operation of an operation-type value, at any depth.
func Exposes(t Type, from *Package) []*TypeName {
	var out []*TypeName
	seen := map[Type]bool{}
	var walk func(Type)
	walk = func(t Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		switch u := under(t).(type) {
		case *Struct:
			auth := hasAuthority(u, from)
			if auth {
				out = appendAuthority(out, u.obj)
			}
			for _, f := range u.Fields {
				if !auth || isExported(f.Name) {
					walk(f.Type)
				}
			}
		case *Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *Union:
			for _, v := range u.Variants {
				walk(v)
			}
		case *Pointer:
			walk(u.Elem)
		case *Slice:
			walk(u.Elem)
		case *Map:
			walk(u.Key)
			walk(u.Elem)
		case *Signature:
			for _, r := range u.Results {
				walk(r)
			}
		case *Interface:
			for _, m := range u.Methods {
				if m.Sig != nil {
					for _, r := range m.Sig.Results {
						walk(r)
					}
				}
			}
		}
	}
	walk(t)
	return out
}

func appendAuthority(out []*TypeName, obj *TypeName) []*TypeName {
	for _, o := range out {
		if o == obj {
			return out
		}
	}
	return append(out, obj)
}

// sealedOwner is the package whose types alone implement it, seen from
// from: the package of an unexported operation of it declared in another
// package (sealedBy), or nil when from may implement it.
func sealedOwner(it *Interface, from *Package) *Package {
	for _, m := range it.Methods {
		if m.pkg != nil && m.pkg != from {
			return m.pkg
		}
	}
	return nil
}

// implementers are the types of owner that may implement it: each a
// declared type of owner, not an operation type, with a function of owner
// for every operation of it whose first parameter is the type or a borrow
// of it. It may hold more types than implement it (signatures are not
// compared), never fewer: a value of a sealed operation type is a value of
// one of its owner's types (sealedBy), and a method is a function of the
// type's package (types.md, Methods 1). So Buildable over it errs only
// towards built.
func implementers(it *Interface, owner *Package) []Type {
	var out []Type
	for _, tn := range owner.Types {
		n, ok := tn.Type.(*Named)
		if !ok || asInterface(n) != nil {
			continue
		}
		all := true
		for _, m := range it.Methods {
			found := false
			for _, f := range owner.Funcs {
				if f.Name != m.Name || f.Sig == nil || len(f.Sig.Params) == 0 {
					continue
				}
				p := f.Sig.Params[0].Type
				if b, ok := p.(*Pointer); ok {
					p = b.Elem
				}
				if pn, ok := p.(*Named); ok && pn.origin().Obj == tn {
					found = true
					break
				}
			}
			if !found {
				all = false
				break
			}
		}
		if all {
			out = append(out, n)
		}
	}
	return out
}

// qualifiedName is a declaration's name as another package writes it.
func qualifiedName(obj *TypeName) string {
	if obj.Pkg == nil {
		return obj.Name
	}
	return obj.Pkg.Name + "." + obj.Name
}

// authorityText is the reason of an S114 for the record obj: who builds
// it, and the exported functions of its package whose result is it, up to
// three, so the message names the way in.
func authorityText(obj *TypeName) string {
	text := qualifiedName(obj) + " has an unexported field, so only package " + obj.Pkg.Name + " builds it"
	var ways []string
	for _, fn := range obj.Pkg.Funcs {
		if len(ways) == 3 {
			break
		}
		if !isExported(fn.Name) || fn.Sig == nil || len(fn.Sig.Results) != 1 {
			continue
		}
		if n, ok := fn.Sig.Results[0].(*Named); ok && n.origin().Obj == obj {
			// A prelude function is in scope with no package name.
			if obj.Pkg.Path == std.PreludePath {
				ways = append(ways, fn.Name)
			} else {
				ways = append(ways, obj.Pkg.Name+"."+fn.Name)
			}
		}
	}
	switch len(ways) {
	case 0:
		return text + "; no exported function of package " + obj.Pkg.Name + " returns it"
	case 1:
		return text + "; use " + ways[0]
	}
	return text + "; use " + strings.Join(ways[:len(ways)-1], ", ") + " or " + ways[len(ways)-1]
}

// literalAuthorityText is the S114 of a literal or record update of t,
// which needs obj's authority.
func (c *checker) literalAuthorityText(t Type, obj *TypeName, update bool) string {
	what := "a literal of " + c.localTypeText(t)
	if update {
		what = "a record update of " + c.localTypeText(t)
	}
	if n, ok := t.(*Named); !ok || n.origin().Obj != obj {
		what += ", defined over " + qualifiedName(obj) + ","
	}
	return what + " builds a new value: " + authorityText(obj)
}

// hiddenField reports whether the field name of s is unexported and s's
// record is declared in another package (grammar.md, Visibility 2).
func (c *checker) hiddenField(s *Struct, name string) bool {
	return s.obj != nil && s.obj.Pkg != c.pkg && !isExported(name)
}

func hiddenFieldText(s *Struct, name string) string {
	return name + " is not an exported field of " + qualifiedName(s.obj)
}

// hiddenOperation is the S104 text of a call of the operation name of it
// from this package, or "" when the call is allowed.
func (c *checker) hiddenOperation(it *Interface, name string) string {
	if it == nil {
		return ""
	}
	m := it.method(name)
	if m == nil || m.pkg == nil || m.pkg == c.pkg {
		return ""
	}
	return name + " is not an exported operation of " + operationOwner(it, m)
}

// operationOwner names the operation type that declares m, for a message.
func operationOwner(it *Interface, m *Method) string {
	if it.obj != nil {
		return qualifiedName(it.obj)
	}
	return "an operation type of package " + m.pkg.Name
}

// operation is the operation name of t's operation set when t is an
// operation type, a type defined over one, a type parameter (its
// constraint's) or a pointer to one of them: the method methodOf finds
// when it returns no function.
func operation(t Type, name string) *Method {
	switch t := t.(type) {
	case *Pointer:
		return operation(t.Elem, name)
	case *TypeParam:
		if it := asInterface(t.Constraint); it != nil {
			return it.method(name)
		}
		return nil
	}
	if it := asInterface(t); it != nil {
		return it.method(name)
	}
	return nil
}

// sealedBy is why v does not implement the unexported operation m of it.
// An unexported operation's identity is its name and its package (Method
// pkg), as in Go: v's operation of that name is the same one only when it
// comes from the same package, whether v's is a function (implFn), an
// operation of an operation type or of a type parameter's constraint
// (196b Q6). So a type of another package does not implement it, directly
// or through a constraint of its own.
func sealedBy(v Type, it *Interface, m *Method, implFn *Func) string {
	if m.pkg == nil {
		return ""
	}
	var pkg *Package
	if implFn != nil {
		pkg = implFn.Pkg
	} else if src := operation(v, m.Name); src != nil {
		pkg = src.pkg
	}
	if pkg == m.pkg {
		return ""
	}
	return m.Name + " is an unexported operation of " + operationOwner(it, m) + ", so only a type of package " + m.pkg.Name + " implements it"
}
