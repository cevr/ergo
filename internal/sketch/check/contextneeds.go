package check

import "ergo.local/prototype/internal/sketch/syntax"

// A named context's needs row (rows.md, Context 4; CTX-ROWS-1, owner q254
// and q266). A named context's header `needs C | D` writes the
// capabilities its providers may require from the with site, as a
// function's needs row writes its own (Capability rows 2):
//
//  1. Its build is checked against the row: a provider that requires a
//     capability the row does not list, and the context does not bind
//     itself, is S102 at the entry (rows.go, charge).
//  2. Each with that builds it charges the row there, as a call charges
//     its callee's row: a capability no enclosing with provides and the
//     function's row does not list is S102 at the with operand. An
//     embedding charges it where it is embedded, a conversion to a
//     Context[R] value where the value is made (both are builds of it).
//  3. No header is the empty row, so a context with none is closed, as
//     before. Nothing is inferred across declarations: the row is what the
//     header writes (the explicit-signature rule for package-level
//     declarations, q266).
//  4. TestAmbient in the row is the test runner's (testambient.go): no with
//     site provides it, so it is not charged there.
//
// A law case provides no capability from a with site, so a law does not
// run under a context whose row is not empty (lawcontexts.go).
//
// Prior art: Effect's Layer<ROut, E, RIn> and ZIO's ZLayer[RIn, E, ROut]:
// providing a layer charges its RIn to the effect that provides it. Wire's
// injector takes its inputs as parameters; Gleam and ReScript pass the
// dependencies a constructor needs by hand.

// readContextHeader reads the named context w's needs header once, in w's
// file, reporting nothing (checkContexts reports what the header names
// wrongly): w.needs is its row but TestAmbient, and w.TestAmbient the
// test's ambient when the row lists it. A context of another package was
// read by its own package's pass.
func (c *checker) readContextHeader(w *Context) {
	if w == nil || w.headerRead || w.Decl == nil || w.Local || w.pkg != c.pkg {
		return
	}
	w.headerRead = true
	if w.Decl.Needs == nil {
		return
	}
	saved, savedFn := c.cur, c.fn
	c.cur, c.fn = w.file, nil
	c.silent++
	row := c.rowExpr(w.Decl.Needs.Names, KindUses, &typeCtx{params: map[string]*TypeParam{}})
	c.silent--
	c.cur, c.fn = saved, savedFn
	for _, e := range row.Elems() {
		if isTestAmbient(e) {
			w.TestAmbient = e.(*Named)
			continue
		}
		w.needs = w.needs.with(c.types, e)
	}
}

// chargeContextNeeds charges, at n, the needs row of the named context w
// that a with, an embedding or a conversion builds there (rule 2).
func (c *checker) chargeContextNeeds(n syntax.Node, w *Context) {
	if w == nil || w.Local || w.Lit != nil {
		return
	}
	c.readContextHeader(w)
	for _, cap := range w.needs.Elems() {
		c.chargeAs(n, cap, "context "+w.Name)
	}
}
