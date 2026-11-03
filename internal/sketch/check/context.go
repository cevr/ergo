package check

import (
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/syntax"
)

// with checks `with(W) { block }`: W is a named context or an inline context
// literal; inside the block every capability W binds is discharged; the
// block yields its last expression, whose failures pass through (rows.md,
// With 1 to 6). An inline context's providers are evaluated in the enclosing
// scope, before the discharge, so they charge the enclosing function and do
// not see their own context (With 3).
func (c *checker) with(x *syntax.CallExpr, hint Type) operand {
	use := c.useOf(x)
	if x.Trailing == nil {
		c.errorf(x, CodeType, "with takes a context and a block: with(W) { … } (rows.md, With 1)")
		c.silentArgs(x)
		return bad(x)
	}
	var binds *capSet
	var context *Context
	var value *ContextBinding
	known := true
	switch {
	case len(x.Args) == 0:
		c.errorf(x, CodeType, "with takes one context (rows.md, With 1)")
		known = false
	case len(x.Args) > 1:
		// One operand per with in v1 (runtime.md, G1): several contexts are
		// 196x's, several context values a G1 follow-up.
		c.errorf(x.Args[1], CodeOutside, "a with takes one operand: several operands are a follow-up, of 196x for contexts and of G1 for context values (runtime.md, G1); nest one with per operand: with a { with b { … } }")
		known = false
		// The values among them are checked silently, as the reference
		// walk visits them, so their imports and names count as used and
		// nothing cascades (D3). A context is not a value.
		c.silent++
		for _, a := range x.Args {
			switch u := unparen(a).(type) {
			case *syntax.ContextLit:
				continue
			case *syntax.Ident:
				if c.withNamedContext(u.Name, c.operandScope(u.Name)) != nil {
					continue
				}
			}
			c.valueExpr(a, nil)
		}
		c.silent--
	default:
		binds, known, context, value = c.withArg(x.Args[0])
	}
	t := c.fn.t
	// unknown: discharge everything, so no cascade. A context binding
	// discharges nothing: its block's row is the enclosing one's.
	frame := &withFrame{binds: binds, unknown: !known, context: context, value: value}
	t.withs = append(t.withs, frame)
	done := c.openScope()
	c.fn.branches = append(c.fn.branches, branch{kind: brBarrier})
	var errs *Row
	// The with's expected type is the yielded expression's, as a hint: the
	// block's own result stays its last expression's type (M16).
	// A with whose value is discarded is a body with no result: its tails
	// are dropped, as in every such body (D-C1; review m4). One whose value
	// is used reports a path without a value at that path (review O1).
	var expect []Type
	if hint != nil {
		expect = []Type{hint}
	}
	yieldKnown := use == useDiscard
	if yieldKnown {
		expect = nil
	}
	c.typedFrame(x.Trailing)
	res := c.yieldingBlock(x.Trailing, x.Trailing.List, expect, yieldKnown, use == useValue, func(op *operand) {
		errs = errs.union(c.types, op.errs)
	})
	c.fn.branches = c.fn.branches[:len(c.fn.branches)-1]
	done()
	t.withs = t.withs[:len(t.withs)-1]
	c.recordWith(x, frame)
	// A with's Fails is the row its block passes through (rows.md, With 6).
	c.callInfo(x).Fails = errs
	if res == nil {
		return bad(x)
	}
	if res == NoValue {
		return operand{mode: modeNoValue, typ: NoValue, errs: errs}
	}
	op := operand{mode: modeValue, typ: res, errs: errs}
	return op
}

// A withFrame is one with scope of a target while its block is checked:
// what the context binds (unknown: a context that is not known, reported, which
// discharges everything so nothing cascades; D3), and the capabilities the
// block's row needs that this with discharges, each once with the entry
// that provides it (charge). With.Discharged is that list, so a with's
// Info is bounded by its body, not by its context (round-4 review M1).
type withFrame struct {
	context *Context         // the resolved named or inline context
	value   *ContextBinding  // the context binding, for a with of a context value
	site    *syntax.CallExpr // the with call, once recorded
	binds   *capSet
	unknown bool
	at      map[capKey]int // index in caps
	caps    []Type
	entries []*ContextEntry // nil for an inline context's own entry
}

// discharge records that the frame discharges cap, provided by n's entry
// (n is nil for an inline context's own entry).
func (f *withFrame) discharge(cap Type, n *capNode) {
	k, ok := keyOf(cap)
	if !ok {
		return
	}
	if _, seen := f.at[k]; seen {
		return
	}
	if f.at == nil {
		f.at = map[capKey]int{}
	}
	f.at[k] = len(f.caps)
	f.caps = append(f.caps, cap)
	var e *ContextEntry
	if n != nil {
		e = n.entry
	}
	f.entries = append(f.entries, e)
}

// operandScope is what a with operand's bare name resolves to lexically,
// before the package's separate context namespace (runtime.md, G1 rule 2).
// Typing (checker.operandScope) and the reference walk
// (refWalker.operandScope) each resolve it from their own scopes, and
// withNamedContext applies one predicate to it, so the two passes cannot
// bind one operand two ways (runtime-199 stage 4a r2 m1, r3 m3).
type operandScope uint8

const (
	// operandPackage: no local binds the name, so a package context may.
	operandPackage operandScope = iota
	// operandValue: a local value (a let, a parameter), a context operand.
	operandValue
	// operandParam: a type parameter, which is no with operand.
	operandParam
	// operandRow: a bare name of the function's written needs row, the
	// capability that row binds, which is no with operand.
	operandRow
)

// operandScope resolves name lexically in typing's scope: the locals and
// the needs row, then the enclosing function's type parameters, which no
// scope holds (localTypeCtx), then the package.
func (c *checker) operandScope(name string) operandScope {
	switch o := c.lookup(name).(type) {
	case *Var:
		if !o.pkg {
			return operandValue
		}
	case *capBinding:
		return operandRow
	}
	if c.fn != nil && c.fn.fn != nil && c.fn.fn.Sig != nil {
		for _, tp := range c.fn.fn.Sig.TParams {
			if tp.Name == name {
				return operandParam
			}
		}
	}
	return operandPackage
}

// withNamedContext is the package context a with operand's name binds: only a
// name no local binds may name one.
func (c *checker) withNamedContext(name string, s operandScope) *Context {
	if s != operandPackage {
		return nil
	}
	return c.pkg.Contexts[name]
}

// withArg resolves with's argument to the capabilities it binds, or, for
// a value whose type satisfies the context-binding protocol, to its
// context binding (contextvalue.go).
func (c *checker) withArg(arg syntax.Expr) (*capSet, bool, *Context, *ContextBinding) {
	switch a := unparen(arg).(type) {
	case *syntax.ContextLit:
		binds, known, context := c.contextLit(a)
		return binds, known, context, nil
	case *syntax.Ident:
		s := c.operandScope(a.Name)
		w := c.withNamedContext(a.Name, s)
		if w == nil && (s == operandParam || s == operandRow) && c.pkg.Contexts[a.Name] != nil {
			// A local non-value hides the package context, as every lexical
			// binding does (runtime.md, G1 rule 2), and is itself no
			// operand: binding the context would make one name mean two
			// things in one scope.
			what := "a type parameter"
			if s == operandRow {
				what = "the capability of this function's needs row"
			}
			c.errorf(a, CodeType, "%s names %s here, which hides the package context %s and is not a with operand: a with operand resolves lexically first (runtime.md, G1); rename the context or the local", a.Name, what, a.Name)
			return nil, false, nil, nil
		}
		if w == nil {
			if o := c.lookup(a.Name); o != nil {
				if _, isVar := o.(*Var); isVar {
					// A local holding a context value binds it; one holding
					// a context value is 196x's. An invalid value is S109
					// already, and nothing cascades (D3).
					cb, known := c.contextArg(arg, "with over a value (196x, extern and context values)")
					return nil, known, nil, cb
				}
			}
			c.errorf(a, CodeUnknown, "unknown context %s", a.Name)
			return nil, false, nil, nil
		}
		if c.fn != nil && c.fn.reach != nil {
			c.fn.reach.contexts = append(c.fn.reach.contexts, w)
		}
		binds := c.contextBinds(w)
		return binds, !w.unknown, w, nil
	case *syntax.SelectorExpr:
		if id, ok := a.X.(*syntax.Ident); ok {
			if pn, ok := c.lookup(id.Name).(*PkgName); ok {
				pn.used = true
				if pn.broken {
					return nil, false, nil, nil
				}
				if w := pn.Pkg.Contexts[a.Sel.Name]; w != nil && isExported(a.Sel.Name) {
					// Its package's pass summarised it; a set keyed by
					// capability names needs no conversion.
					return w.binds, !w.unknown, w, nil
				}
				c.errorf(a.Sel, CodeUnknown, "%s.%s is not an exported context of package %s", id.Name, a.Sel.Name, pn.Pkg.Path)
				return nil, false, nil, nil
			}
		}
	}
	cb, known := c.contextArg(arg, "")
	return nil, known, nil, cb
}

// contextLit checks an inline context's entries and returns what it binds; known
// is false when it embeds an unknown context (reported) or its summary was
// refused (contextsum.go).
func (c *checker) contextLit(w *syntax.ContextLit) (binds *capSet, known bool, context *Context) {
	// The header of an inline context is refused as a named context's is
	// (checkContexts): never read partly (rows.md, Context 8).
	if w.Provides != nil {
		c.errorf(w.Provides, CodeOutside, "a context's provides clause is #196 stage b's (the context header, rows.md Context 8); it is not checked yet")
	}
	if w.Needs != nil {
		c.errorf(w.Needs, CodeOutside, "a context's needs clause is #196 stage b's (the context header, rows.md Context 8); it is not checked yet")
	}
	context = &Context{Name: "inline", Pos: w.Pos(), Lit: w, file: c.cur, pkg: c.pkg}
	binds, known = c.contextBody(w.Body, context)
	context.binds, context.unknown = binds, !known
	return binds, known, context
}

// contextBody checks the body of a named or an inline context and returns what
// it binds. The duplicate and replace rules are decided once
// per body by its summary (bodySummary), whose findings are reported on
// every pass; the embedded names, the keys and the providers are checked
// here, on every pass. For a named context (named is not nil) it makes the
// context's Entries (an inline context's entries are stage E's, 196x).
func (c *checker) contextBody(body *syntax.ContextBody, named *Context) (*capSet, bool) {
	sum := c.bodySummary(body)
	if named != nil && c.silent == 0 {
		named.Entries = nil
	}
	for _, f := range sum.findings {
		c.errorf(f.at, f.code, "%s", f.msg)
	}
	var build *target
	if c.fn != nil && c.fn.t != nil && c.fn.t.context {
		build = c.fn.t
	}
	deps := &contextDeps{tab: c.types, needs: map[int32]*Row{}, at: map[int32]syntax.Expr{}}
	for _, e := range body.Entries {
		if e.Kind == syntax.ContextEmbed {
			inner := c.embeddedContext(e)
			if inner == nil {
				c.errorf(e.Key, CodeUnknown, "unknown context %s", exprText(e.Key))
			} else if c.fn != nil && c.fn.reach != nil {
				c.fn.reach.contexts = append(c.fn.reach.contexts, inner)
			}
			if inner != nil && named != nil && c.silent == 0 {
				named.Entries = append(named.Entries, &ContextEntry{Embedded: inner})
			}
			continue
		}
		if build != nil {
			build.charged = newRow()
		}
		var made *ContextEntry
		if named != nil && c.silent == 0 {
			made = c.contextEntries[e] // the record the context summary's nodes carry
		}
		caps, entry := c.contextEntry(e, made)
		if named != nil && c.silent == 0 {
			named.Entries = append(named.Entries, entry)
		}
		if build != nil {
			deps.add(caps, build.charged, e.Key)
			if entry != nil && named != nil && c.silent == 0 {
				entry.Uses = build.charged.Elems()
			}
		}
	}
	if build != nil {
		c.checkEntryCycles(build.name, deps)
	}
	return sum.binds, sum.known
}

// embeddedContext is the named context an embedding entry names, or nil.
func (c *checker) embeddedContext(e *syntax.ContextEntry) *Context {
	if id, ok := e.Key.(*syntax.Ident); ok {
		return c.pkg.Contexts[id.Name]
	}
	return nil
}

// contextDeps records, for a named context's entries, the capabilities of the
// context each entry's provider uses while it is built.
type contextDeps struct {
	tab   *typeTable
	order []Type
	needs map[int32]*Row // by the capability's interned id
	at    map[int32]syntax.Expr
}

func (d *contextDeps) add(caps []Type, needs *Row, at syntax.Expr) {
	for _, cap := range caps {
		id := d.tab.id(cap)
		if _, seen := d.needs[id]; !seen {
			d.order = append(d.order, cap)
			d.at[id] = at
			d.needs[id] = newRow()
		}
		d.needs[id] = d.needs[id].union(d.tab, needs)
	}
}

// checkEntryCycles reports entries of a named context that need each other
// (S108, once per cycle): a provider that uses a capability of its own context
// is built after that capability's provider, so a cycle has no first entry
// (rows.md, Context 4 and 5).
func (c *checker) checkEntryCycles(name string, d *contextDeps) {
	const (
		white = iota
		grey
		black
	)
	colour := map[int32]int{}
	key := c.types.id
	var stack []Type
	var visit func(cap Type) []Type
	visit = func(cap Type) []Type {
		colour[key(cap)] = grey
		stack = append(stack, cap)
		defer func() {
			stack = stack[:len(stack)-1]
			colour[key(cap)] = black
		}()
		for _, next := range d.needs[key(cap)].Elems() {
			if _, entry := d.needs[key(next)]; !entry {
				continue // bound by an embedded context, built in its own context
			}
			switch colour[key(next)] {
			case grey:
				for i, s := range stack {
					if key(s) == key(next) {
						return append([]Type(nil), stack[i:]...)
					}
				}
			case white:
				if cycle := visit(next); cycle != nil {
					return cycle
				}
			}
		}
		return nil
	}
	for _, cap := range d.order {
		if colour[key(cap)] != white {
			continue
		}
		cycle := visit(cap)
		if cycle == nil {
			continue
		}
		for _, m := range cycle {
			colour[key(m)] = black
		}
		parts := make([]string, 0, len(cycle)+1)
		for _, m := range cycle {
			parts = append(parts, m.String())
		}
		parts = append(parts, cycle[0].String())
		c.errorf(d.at[key(cycle[0])], CodeType, "the entries of %s need each other (%s), so none can be built first (rows.md, Context 4 and 5)", name, strings.Join(parts, " -> "))
	}
}

// contextEntry checks the provider of one `Cap = provider` or `replace Cap =
// provider` entry: it must implement each capability by method-row
// compatibility (S106); one value may serve several capabilities (README
// Gap 28 and 34). A constructor entry is #37's (S109). The second-entry
// and replace rules are the body summary's (contextsum.go). It returns the
// capabilities the entry names and its Info record: made, the one the context
// pass made for a named context's entry, filled in here, or a new one.
func (c *checker) contextEntry(e *syntax.ContextEntry, made *ContextEntry) ([]Type, *ContextEntry) {
	var caps []Type
	for _, k := range append([]syntax.Expr{e.Key}, e.Keys...) {
		if t := c.contextEntryKey(k); t != nil {
			caps = append(caps, t)
		}
	}
	entry := made
	if entry == nil {
		entry = &ContextEntry{Provider: e.Value, Replace: e.Replace.IsValid()}
	}
	entry.Caps, entry.Conversions = caps, nil
	if c.isConstructor(e.Value) {
		c.errorf(e.Value, CodeOutside, "a constructor entry is #37's (context constructors); write a provider value, not a function")
		return caps, entry
	}
	op := c.valueExpr(e.Value, nil)
	for _, cap := range caps {
		if op.invalid() {
			continue
		}
		if op.mode == modeNoValue {
			c.errorf(e.Value, CodeType, "this provider has no value")
			continue
		}
		pt := c.typeOf(&op)
		if isInvalid(pt) {
			continue
		}
		// The provider goes into the capability by assignability (types.md,
		// Assignability), whose conversion holds its operations' functions
		// (196m); the lowering reads it and never re-derives it.
		a, why := c.assignability(pt, cap)
		if !a.ok() {
			if why == "" {
				why = pt.String() + " is not assignable to " + cap.String()
			}
			c.errorf(e.Value, CodeProvider, "%s cannot provide %s: %s (rows.md, Context 2)", pt, cap, why)
			continue
		}
		if pn, ok := pt.(*Named); ok {
			cn := cap.(*Named)
			c.providers[cn] = append(c.providers[cn], pn)
		}
		entry.Conversions = append(entry.Conversions, a.conv)
	}
	if len(caps) == 1 && len(entry.Conversions) == 1 && entry.Conversions[0].Kind != 0 && c.silent == 0 {
		c.pkg.Info.Implicit[e.Value] = entry.Conversions[0]
	}
	return caps, entry
}

// isConstructor reports whether a provider expression names a function
// without calling it (rows.md, Context 6: constructor entries, #37).
func (c *checker) isConstructor(x syntax.Expr) bool {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		_, ok := c.lookup(x.Name).(*Func)
		return ok
	case *syntax.SelectorExpr:
		if id, ok := x.X.(*syntax.Ident); ok {
			if pn, ok := c.lookup(id.Name).(*PkgName); ok {
				_, isFunc := pn.Pkg.Scope.names[x.Sel.Name].(*Func)
				return isFunc
			}
		}
	}
	return false
}

// contextEntryKey resolves a context entry's key to a capability (S104 otherwise).
func (c *checker) contextEntryKey(k syntax.Expr) Type {
	t := c.rowElem(k, KindUses, c.localTypeCtx())
	if t == nil {
		return nil
	}
	if _, isParam := t.(*TypeParam); isParam {
		c.errorf(k, CodeType, "a context entry names a capability, not a row parameter")
		return nil
	}
	return t
}

// checkContexts checks the named contexts: their entries, in a closed build
// context (README Gap 6: a named context requires nothing from the with site
// and lets no failure escape).
func (c *checker) checkContexts() {
	var contexts []*Context
	for _, w := range c.pkg.Contexts {
		contexts = append(contexts, w)
	}
	sort.Slice(contexts, func(i, j int) bool {
		if contexts[i].file.index != contexts[j].file.index {
			return contexts[i].file.index < contexts[j].file.index
		}
		return contexts[i].Pos < contexts[j].Pos
	})
	for _, w := range contexts {
		c.cur = w.file
		d := w.Decl
		// The context header (#217) is checked by #196's second stage; until
		// then it is refused, never read partly (rows.md, Context 8).
		if d.Provides != nil {
			c.errorf(d.Provides, CodeOutside, "a context's provides clause is #196 stage b's (the context header, rows.md Context 8); it is not checked yet")
		}
		if d.Needs != nil {
			c.errorf(d.Needs, CodeOutside, "a context's needs clause is #196 stage b's (the context header, rows.md Context 8); it is not checked yet")
		}
		t := &target{name: "context " + w.Name, declErrors: newRow(), declUses: newRow(), context: true}
		// The context's providers may use what the context binds (Context 4).
		t.internal = c.contextBinds(w)
		if w.budget {
			c.errorf(d.Name, CodeDuplicateCap, "%s", contextBudgetMessage)
		}
		if t.internal == nil {
			// Its summary was refused (contextsum.go): nothing it charges is
			// reported (D3).
			t.withs = []*withFrame{{unknown: true}}
		}
		c.fn = &funcCtx{t: t, scope: newScope(c.pkg.Scope), reach: c.reachOf(w)}
		c.contextBody(d.Body, w)
		c.fn = nil
	}
}
