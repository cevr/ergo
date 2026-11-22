package check

import "ergo.local/prototype/internal/sketch/syntax"

// Failure parameters of a generic type (ROW-PARAM, option A; types.md,
// Generics 8). A type parameter of a generic type that a function of the
// type's package takes as a Slot row, F of `type Async[T, F]` in
// `func Await[T, F](a Async[T, F]) T fails F`, holds a failure: every
// argument written for it is a row of error types. It stays a type
// parameter (a Slot row keeps its Go type, rows.md Lowering), so the rule
// adds two things, both read from written text before any body:
//
//  1. Naming. A type written there is an error type of the writing body's
//     owner, as in a `fails` row (collectRowArgs): `ui.Async[int, Oops]`
//     makes Oops one, and only the tests' when only a test writes it.
//  2. Refusal. A type written there that is no error type is S104 at the
//     argument (checkSlotArgs), where the inference would otherwise refuse
//     the first Await with an S108 that names neither.
//
// The functions are those of the type's own package, so a type whose only
// Slot use is another package's function keeps a plain type parameter
// there: kinds are a declaration's, never inferred across packages.
//
// Prior art: Koka infers effect-row kinds of a type's parameters from their
// uses within the declaring module; Haskell's kind inference also stops at
// the module boundary, where the kinds are fixed in the interface.

// slotBound returns, for the generic type a head names, the name of a
// function of its package that takes each of its type parameters as a Slot
// row ("" for none), or nil when the head names no generic type.
func (c *checker) slotBound(head syntax.Expr) []string {
	_, n := c.namedHead(head)
	if n == nil {
		return nil
	}
	n = n.origin()
	if len(n.TParams) == 0 || n.Obj == nil {
		return nil
	}
	if b, ok := c.slotBounds[n]; ok {
		return b
	}
	var funcs []*Func
	switch p := n.Obj.Pkg; {
	case p == c.pkg:
		funcs = c.funcs
	case p != nil:
		funcs = p.Funcs
	}
	out := make([]string, len(n.TParams))
	for _, f := range funcs {
		if f.Sig == nil {
			continue
		}
		eachNamed(f.Sig, map[Type]bool{}, func(u *Named) {
			if u.origin() != n {
				return
			}
			for i, a := range u.TArgs {
				if i < len(out) && out[i] == "" && n.TParams[i].Kind == KindType {
					if tp := SlotRow(a); tp != nil && tp.Slot {
						out[i] = f.Name
					}
				}
			}
		})
	}
	if c.slotBounds == nil {
		c.slotBounds = map[*Named][]string{}
	}
	c.slotBounds[n] = out
	return out
}

// eachNamed calls f on every named type of t's parameters and results, at
// any depth, as markSlotParams walks them.
func eachNamed(t Type, seen map[Type]bool, f func(*Named)) {
	if t == nil || seen[t] {
		return
	}
	seen[t] = true
	switch u := t.(type) {
	case *Named:
		f(u)
		for _, a := range u.TArgs {
			eachNamed(a, seen, f)
		}
	case *Slice:
		eachNamed(u.Elem, seen, f)
	case *Array:
		eachNamed(u.Elem, seen, f)
	case *Map:
		eachNamed(u.Key, seen, f)
		eachNamed(u.Elem, seen, f)
	case *Pointer:
		eachNamed(u.Elem, seen, f)
	case *Tuple:
		for _, e := range u.Types {
			eachNamed(e, seen, f)
		}
	case *Signature:
		for _, p := range u.Params {
			eachNamed(p.Type, seen, f)
		}
		for _, r := range u.Results {
			eachNamed(r, seen, f)
		}
	}
}

// checkSlotArgs refuses a type that is no error type written for a type's
// failure parameter (rule 2 above): S104 at the argument. A name that does
// not resolve at the package's scope (a type parameter of the enclosing
// declaration) is the type's own business and is left to it.
func (c *checker) checkSlotArgs() {
	for _, fi := range c.files {
		c.cur = fi
		syntax.Inspect(fi.syntax, func(x syntax.Node) bool {
			ix, ok := x.(*syntax.IndexExpr)
			if !ok {
				return true
			}
			bound := c.slotBound(ix.X)
			for i, a := range ix.Indices {
				if i >= len(bound) || bound[i] == "" {
					continue
				}
				for _, leaf := range orLeaves(a) {
					if t := c.slotLeaf(leaf); t != nil && !isStructNamed(t) {
						if n, ok := t.(*Named); ok {
							if _, u := n.Underlying().(*Union); u {
								continue // a union names a row (namedRow)
							}
						}
						_, n := c.namedHead(ix.X)
						fn := bound[i]
						if p := n.Obj.Pkg; p != c.pkg && p != nil {
							fn = p.Name + "." + fn
						}
						c.errorf(leaf, CodeUnknown, "%s is not an error type: %s's parameter %s holds a failure, which %s fails (types.md, Error types 1)",
							t, n.Obj.Name, n.origin().TParams[i].Name, fn)
					}
				}
			}
			return true
		})
	}
}

// slotLeaf is the type a row argument's member names at the package's
// scope, or nil when it names none there.
func (c *checker) slotLeaf(x syntax.Expr) Type {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		if tn, ok := c.lookupType(x.Name, nil).(*TypeName); ok {
			if _, tp := tn.Type.(*TypeParam); !tp {
				return tn.Type
			}
		}
	case *syntax.SelectorExpr:
		if _, n := c.namedHead(x); n != nil {
			return n
		}
	}
	return nil
}
