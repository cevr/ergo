package check

import "strings"

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
			ways = append(ways, obj.Pkg.Name+"."+fn.Name)
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
	if it == nil || it.obj == nil || it.obj.Pkg == c.pkg || isExported(name) {
		return ""
	}
	return name + " is not an exported operation of " + qualifiedName(it.obj)
}

// sealedBy is why v does not implement the unexported operation m of it:
// only a type of it's package implements it, as in Go. implFn is the
// function that would implement it, when v's method is one.
func sealedBy(v Type, it *Interface, m *Method, implFn *Func) string {
	if it.obj == nil || isExported(m.Name) {
		return ""
	}
	var pkg *Package
	switch {
	case implFn != nil:
		pkg = implFn.Pkg
	default:
		n, ok := v.(*Named)
		if !ok {
			return "" // a type parameter's constraint: its own operations
		}
		pkg = n.origin().Obj.Pkg
	}
	if pkg == it.obj.Pkg {
		return ""
	}
	return m.Name + " is an unexported operation of " + qualifiedName(it.obj) + ", so only a type of package " + it.obj.Pkg.Name + " implements it"
}
