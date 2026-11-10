package check

import (
	"sort"

	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Context values (rows.md, Context values; #237 stage 2, L5). The prelude
// type Context[R] is a context as a value: the providers of the capability
// row R, built where a context converts to it.
//
//  1. Conversion. A context (an inline literal, a local, a named or an
//     imported named context) where a Context[R] is expected converts to
//     one. It is checked as a with operand is (withArg), so an inline
//     literal's providers are checked and charged in the frame that makes
//     the value, and every capability of R must be one the context binds
//     (S102 names a missing one). R must be concrete: nothing builds a row
//     parameter (S108). The conversion records a With fact
//     (Info.ContextValues) that the lowering builds the providers from; a
//     provider with a Close is S109, since no with owns its release.
//  2. Installing. `with v { b }` with v a Context[R] discharges R in b and
//     builds nothing (With.Installs). R may hold a row parameter, which the
//     with discharges as it discharges a capability (keyOf).
//  3. Types. Context[R] is invariant in R. No other expression makes one:
//     a composite literal of it is S108.

// contextRow is R of a Context[R] instance, and false for any other type.
func contextRow(t Type) (*Row, bool) {
	n, ok := t.(*Named)
	// The name and package first: the prelude's own check calls this too,
	// before preludeType can answer.
	if !ok || n.Orig == nil || n.Obj == nil || n.Obj.Name != "Context" || n.Obj.Pkg == nil || n.Obj.Pkg.Path != std.PreludePath || len(n.TArgs) != 1 {
		return nil, false
	}
	ra, ok := n.TArgs[0].(*RowArg)
	if !ok {
		return nil, false
	}
	return ra.Row, true
}

// IsContext reports whether t is a Context[R] (rows.md, Context values).
func IsContext(t Type) bool {
	_, ok := contextRow(t)
	return ok
}

// A contextValueSite is a conversion, for the ownership pass.
type contextValueSite struct {
	x    syntax.Expr
	w    *With
	file *fileInfo
}

// isContextForm reports whether x names or spells a context: a context
// literal, a local or a package context, or an imported one.
func (c *checker) isContextForm(x syntax.Expr) bool {
	switch a := unparen(x).(type) {
	case *syntax.ContextLit:
		return true
	case *syntax.Ident:
		s := c.operandScope(a.Name)
		if c.withNamedContext(a.Name, s) != nil {
			return true
		}
		w, ok := c.lookup(a.Name).(*Context)
		return ok && w.Local
	case *syntax.SelectorExpr:
		if id, ok := a.X.(*syntax.Ident); ok {
			if pn, ok := c.lookup(id.Name).(*PkgName); ok && !pn.broken {
				return pn.Pkg.Contexts[a.Sel.Name] != nil
			}
		}
	}
	return false
}

// contextConversion converts the context x to the value type want, a
// Context[R] (rule 1); ok is false when x is no context, so it is typed as
// any other expression.
func (c *checker) contextConversion(x syntax.Expr, want *Named, row *Row) (operand, bool) {
	if !c.isContextForm(x) {
		return operand{}, false
	}
	at := unparen(x)
	binds, known, w, _, _ := c.withArg(x)
	if w == nil {
		return bad(x), true
	}
	fact := &With{Contexts: []*Context{w}}
	ok := true
	for _, cap := range row.Elems() {
		if _, param := cap.(*TypeParam); param {
			c.errorf(x, CodeType, "a context converts only to a Context of concrete capabilities, and %s names the row parameter %s: nothing builds an unknown row (rows.md, Context values)", want, cap)
			ok = false
			continue
		}
		if !known || isInvalid(cap) {
			continue
		}
		found, node := binds.find(c.types, cap)
		if !found {
			c.errorf(x, CodeMissingCap, "this context binds no %s, which %s provides (rows.md, Context values)", cap, want)
			ok = false
			continue
		}
		fact.Discharged = append(fact.Discharged, cap)
		fact.Entries = append(fact.Entries, node.entry)
	}
	if !ok || !known {
		return bad(x), true
	}
	order := make([]int, len(fact.Discharged))
	keys := make([]capKey, len(fact.Discharged))
	for i, cap := range fact.Discharged {
		order[i] = i
		keys[i], _ = keyOf(cap)
	}
	sort.Slice(order, func(a, b int) bool { return keys[order[a]].compare(keys[order[b]]) < 0 })
	caps, entries := fact.Discharged, fact.Entries
	fact.Discharged, fact.Entries = nil, nil
	for _, i := range order {
		fact.Discharged = append(fact.Discharged, caps[i])
		fact.Entries = append(fact.Entries, entries[i])
	}
	if c.silent == 0 {
		c.pkg.Info.ContextValues[at] = fact
		c.contextValueSites = append(c.contextValueSites, contextValueSite{x: at, w: fact, file: c.cur})
	}
	return operand{mode: modeValue, typ: want}, true
}

// valueOperand checks a with operand that is a value: a Context[R], which
// binds R (rule 2), or a context value of the binding protocol
// (contextvalue.go).
func (c *checker) valueOperand(arg syntax.Expr) (*capSet, bool, *Context, *ContextBinding, *Named) {
	op := c.valueExpr(arg, nil)
	if op.invalid() || op.typ == nil || isInvalid(op.typ) {
		return nil, false, nil, nil, nil
	}
	if row, ok := contextRow(op.typ); ok {
		// R is a written row, so its set is small; it is no context
		// pass work and has a meter of its own.
		m := &contextMeter{limit: 1 << 30}
		var binds *capSet
		for _, cap := range row.Elems() {
			if isInvalid(cap) {
				return nil, false, nil, nil, nil
			}
			var fine bool
			if binds, fine = binds.put(m, cap, nil); !fine {
				return nil, false, nil, nil, nil
			}
		}
		if binds == nil {
			binds = &capSet{}
		}
		return binds, true, nil, nil, op.typ.(*Named)
	}
	cb, known := c.contextArgOf(arg, op, "")
	return nil, known, nil, cb, nil
}

// contextValueOwnership refuses a conversion whose build acquires a
// provider with a Close (rule 1): a with owns a release, and a value is
// installed by many withs or none.
func (c *checker) contextValueOwnership(owners map[*ContextEntry]*Context) {
	for _, s := range c.contextValueSites {
		w := s.w
		if len(w.Entries) == 0 {
			continue
		}
		w.Acquired = c.acquire(w.Contexts[0], w.Entries, owners)
		for _, a := range w.Acquired {
			if a.Close != nil {
				c.cur = s.file
				c.errorf(s.x, CodeOutside, "a Context value cannot hold a provider with a Close: no with owns its release; bind this context with with (rows.md, Context values)")
				break
			}
		}
	}
	c.contextValueSites = nil
}
