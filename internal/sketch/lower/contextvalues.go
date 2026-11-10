package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Context values (rows.md, Context values). A Context[R] is the Go
// function `func(*Env)` that writes the providers of R into the
// environment copy a with makes. A conversion builds the providers once,
// where the value is made, into a snapshot of the environment, as a with
// of that context builds them (its builder, or its providers in place for
// an inline or a local context), and the value copies R's fields from the
// snapshot. `with v { b }` copies the environment, calls v on the copy and
// runs b against it: it builds nothing. Hand-written Go would pass a
// struct of the providers and assign its fields; this is one closure call
// per with and one snapshot per value.

// contextValue lowers the conversion of a context to a Context[R] value,
// whose fact is w (Info.ContextValues).
func (fc *funcCtx) contextValue(x syntax.Expr, w *check.With) string {
	if len(w.Contexts) == 0 {
		ice(x, "a Context value with no context")
	}
	snap := fc.temp("ctx")
	outer := fc.env
	if outer == "" {
		// No capability is read where no environment is bound: the
		// providers see only what the snapshot binds.
		fc.line("var " + snap + " " + fc.l.env())
		outer = "(&" + snap + ")"
	} else {
		fc.line(snap + " := *" + outer)
	}
	if w.Contexts[0].Lit != nil {
		fc.l.localContext(w.Contexts[0], x)
		fc.buildProviders(w, "(&"+snap+")", outer, "")
	} else {
		if w.Contexts[0].Lit == nil && !fc.l.declaredContext(w.Contexts[0]) {
			ice(x, "a Context value of the context %s, which no source file of the program declares", w.Contexts[0].Name)
		}
		fc.line(fc.l.contextBuilder(w, x) + "(&" + snap + ")")
	}
	e := fc.temp("e")
	var b strings.Builder
	b.WriteString("func(" + e + " *" + fc.l.env() + ") {")
	for i, cap := range w.Discharged {
		n, ok := cap.(*check.Named)
		if !ok {
			ice(x, "a Context value of the row element %s, which is no capability", cap)
		}
		if i > 0 {
			b.WriteString(";")
		}
		f := fc.l.envField(n)
		b.WriteString(" " + e + "." + f + " = " + snap + "." + f)
	}
	b.WriteString(" }")
	return b.String()
}

// installWith lowers operand arg of a with whose value is a Context[R]
// (With.Installs) around inner.
func (fc *funcCtx) installWith(arg syntax.Expr, inner func()) {
	v := fc.expr(arg)
	if v.err != "" {
		ice(arg, "a fallible Context operand; the checker handles its failure first")
	}
	env := fc.temp("env")
	fc.line("{")
	fc.depth++
	outer := fc.envExpr(arg)
	fc.line(env + " := *" + outer)
	fc.line(v.val + "(&" + env + ")")
	saved := fc.env
	fc.env = "(&" + env + ")"
	inner()
	fc.env = saved
	fc.depth--
	fc.line("}")
}
