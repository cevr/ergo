package lower

// IR builder: with A, …, Z { block } (G6). The emitter's with (calls.go):
// each operand of a named context copies the environment, builds the
// context's providers into the copy by the context's builder, and runs
// what it holds against the copy, with no closure (rows.md, With 7 and 8).
// The block's tail is the with's value. See ir.go.

import (
	"fmt"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// isWith reports a with expression: a call of kind CallWith.
func (b *irBuilder) isWith(x syntax.Expr) (*syntax.CallExpr, bool) {
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	c := b.l.info.Calls[call]
	return call, c != nil && c.Kind == check.CallWith
}

// withStmt is the with x; ret says its block's tail is the function's
// result, to (when set) the local it yields into. A with whose block
// passes an untried failure on, one that owns a provider with a checked
// Close, a context value's and an inline or local context's stay the
// emitter's.
func (b *irBuilder) withStmt(x *syntax.CallExpr, ret bool, to *yieldTo) []*irStmt {
	w := b.l.info.Withs[x]
	if w == nil || len(x.Args) == 0 || x.Trailing == nil {
		irNo("with form")
	}
	if b.env < 0 {
		irNo("with in a pure function")
	}
	if b.fc.fallibleExpr(x) {
		irNo("with whose block fails")
	}
	return b.withOperand(x, 0, w, ret, to)
}

func (b *irBuilder) withOperand(x *syntax.CallExpr, i int, w *check.With, ret bool, to *yieldTo) []*irStmt {
	arg := x.Args[i]
	if (w.Next == nil) != (i == len(x.Args)-1) {
		irNo("with operands without their With facts")
	}
	switch {
	case w.Value != nil:
		irNo("with of a context owner")
	case w.Installs != nil:
		irNo("with of a context value")
	case len(w.Contexts) == 0:
		irNo("with form")
	case ownsResources(w):
		irNo("with that owns resources")
	case w.Contexts[0].Lit == nil && !b.l.declaredContext(w.Contexts[0]):
		irNo("with of an undeclared context")
	}
	lit := w.Contexts[0].Lit != nil // an inline or a local context
	p := b.pos(arg, arg.Pos())
	env := b.l.env()
	// env2 := *env; envP := &env2: the copy the block runs against (the
	// emitter's `env2 := *env` and `(&env2)`); a named context's builder
	// builds its providers into it, an inline or a local context's are
	// built in place (publish).
	cp := b.temp("env", env, p)
	ptr := b.temp("envp", "*"+env, p)
	out := []*irStmt{
		{K: "def", Pos: p, Local: cp, X: &irExpr{K: "deref", Pos: p, X: &irExpr{K: "local", Pos: p, I: b.env}}},
		{K: "def", Pos: p, Local: ptr, X: &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: cp}}},
	}
	if lit {
		b.l.localContext(w.Contexts[0], arg)
		out = append(out, b.publish(w, ptr, b.env, arg)...)
	} else {
		build := b.l.contextBuilder(w, arg)
		out = append(out, &irStmt{K: "expr", Pos: p, X: &irExpr{K: "call", Pos: p, Name: build, Args: []*irExpr{{K: "local", Pos: p, I: ptr}}}})
	}
	saved := b.env
	b.env = ptr
	defer func() { b.env = saved }()
	if w.Next != nil {
		return append(out, b.withOperand(x, i+1, w.Next, ret, to)...)
	}
	return append(out, b.tailTo(x.Trailing.List, ret && to == nil, to)...)
}

// opsConv is e, the value of x, through the operations conversion conv:
// the emitter's adapter (operations, sealedOperations), &A{v: e, env: env}
// for a value of the program whose type's functions provide the
// operations, &A{v: e} for a sealed std value, as the operation type.
// The emitter declares the adapter; the IR builds its literal. A value
// whose type is already an interface is itself; a script's, a type
// parameter's, a generic adapter's and one with witnesses stay the
// emitter's.
func (b *irBuilder) opsConv(conv check.Conversion, e *irExpr, x syntax.Expr) *irExpr {
	l := b.l
	if check.ScriptOf(conv.From) != nil {
		irNo("operations conversion of a script")
	}
	from, _ := conv.From.(*check.Named)
	sealed := from != nil && l.runtimeType(from, x) != ""
	if !sealed && from != nil && from.Underlying() != nil {
		if _, ops := from.Underlying().(*check.Interface); ops {
			return e
		}
	}
	if tp, _ := paramSource(conv.From); tp != nil {
		irNo("operations conversion of a type parameter")
	}
	if len(convParams(conv)) > 0 {
		irNo("generic operations conversion")
	}
	for _, f := range conv.Table {
		if f != nil && len(witnessParams(f.Sig)) > 0 {
			irNo("operations conversion with witnesses")
		}
	}
	// The emitter declares the adapter (its text is the emitter's form
	// of this same value, unused here).
	b.fc.operations(conv, "_", x)
	key, _, ok := l.adapterName(conv)
	name := l.adapters[key]
	if !ok || name == "" {
		irNo("operations conversion without an adapter")
	}
	p := e.Pos
	lit := &irExpr{K: "lit", Pos: p, T: name, Keys: []string{"v"}, Elems: []*irExpr{e}}
	if !sealed {
		env := &irExpr{K: "zero", Pos: p, T: "*" + l.env()}
		if b.env >= 0 {
			env = &irExpr{K: "local", Pos: p, I: b.env}
		}
		lit.Keys, lit.Elems = append(lit.Keys, "env"), append(lit.Elems, env)
	}
	return &irExpr{K: "toiface", Pos: p, T: b.goType(conv.To, x), X: &irExpr{K: "addr", Pos: p, X: lit}}
}

// publish builds the inline or local context's providers of w into the
// environment the local ptr points at (the emitter's buildProviders and
// publishProvider): each acquisition's provider, in the checker's order,
// against the environment outside the with (outer) for an entry of the
// literal, the one being built for an embedded named context's, then its
// value, converted, into each capability's field.
func (b *irBuilder) publish(w *check.With, ptr, outer int, site syntax.Node) []*irStmt {
	local := map[*check.ContextEntry]bool{}
	var lexical func(*check.Context)
	lexical = func(c *check.Context) {
		if c.Lit == nil {
			return
		}
		for _, e := range c.Entries {
			if e.Embedded != nil {
				lexical(e.Embedded)
			} else {
				local[e] = true
			}
		}
	}
	lexical(w.Contexts[0])
	var out []*irStmt
	for _, a := range w.Acquired {
		e := a.Entry
		if !local[e] {
			irNo("with of a context that embeds a named one")
		}
		if _, ok := b.l.sourceOf(e.Provider); !ok {
			irNo("with of a context entry outside the program's sources")
		}
		p := b.pos(e.Provider, e.Provider.Pos())
		saved := b.env
		b.env = outer
		pre, val := b.value(e.Provider)
		b.env = saved
		out = append(out, pre...)
		// The checker may record the entry's conversion at the provider
		// too (Info.Implicit), which value applied: the value is the
		// capability's already.
		_, converted := b.l.info.Implicit[e.Provider]
		if converted && len(a.Caps) > 1 {
			irNo("context entry of several capabilities with a conversion")
		}
		if len(a.Caps) > 1 {
			t := b.l.info.Types[e.Provider]
			if t == nil {
				irNo("context entry without a type")
			}
			v := b.temp("prov", b.goType(t, e.Provider), p)
			out = append(out, &irStmt{K: "def", Pos: p, Local: v, X: val})
			val = &irExpr{K: "local", Pos: p, I: v}
		}
		for _, i := range a.Caps {
			capT, ok := e.Caps[i].(*check.Named)
			if !ok {
				irNo("context entry key that is not a named capability")
			}
			conv := check.Conversion{}
			if i < len(e.Conversions) {
				conv = e.Conversions[i]
			}
			v := val
			switch {
			case converted:
			case conv.Kind == 0:
				// A value of a type that implements the capability's Go
				// interface goes into the field as Go assigns it.
				if pt := b.l.info.Types[e.Provider]; pt != nil && b.goType(pt, e.Provider) != b.goType(capT, e.Provider) {
					v = &irExpr{K: "toiface", Pos: p, T: b.goType(capT, e.Provider), X: val}
				}
			case conv.Kind == check.ConvOperations:
				b.env = outer
				v = b.opsConv(conv, val, e.Provider)
				b.env = saved
			default:
				irNo("context entry conversion %d", conv.Kind)
			}
			field := &irExpr{K: "field", Pos: p, Name: b.l.envField(capT), X: &irExpr{K: "local", Pos: p, I: ptr}}
			out = append(out, &irStmt{K: "store", Pos: p, X: field, Y: v})
		}
	}
	return out
}

// withValue is the with x in a value position: a local of its type,
// declared first, and the with yielding into it (ifValue).
func (b *irBuilder) withValue(x *syntax.CallExpr) ([]*irStmt, *irExpr) {
	t, ok := b.l.info.Types[x]
	if !ok || b.fc.noValue(x) {
		irNo("with without a value")
	}
	p := b.pos(x, x.Pos())
	i := b.temp(fmt.Sprintf("with%d", b.next), b.goType(t, x), p)
	pre := append([]*irStmt{{K: "var", Pos: p, Local: i}}, b.withStmt(x, true, &yieldTo{local: i, t: t})...)
	return pre, &irExpr{K: "local", Pos: p, I: i}
}
