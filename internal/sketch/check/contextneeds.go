package check

import "ergo.local/prototype/internal/sketch/syntax"

// A named context's row (rows.md, Context 4; CTX-ROWS-1 and CTX-ROWS-2,
// owner q254, q266 and q280). A named context's header `fails E | F` and
// `needs C | D` write the failures its build may raise and the capabilities
// its providers may require from the with site, as a function's rows write
// its own (Error rows 6, Capability rows 2):
//
//  1. Its build is checked against the row: a provider that requires a
//     capability the needs row does not list, and the context does not
//     bind itself, is S102 at the entry; a failure that escapes an entry
//     and the fails row does not list is S101 at the entry (rows.go,
//     charge and escape).
//  2. Each with that builds it charges the needs row there, as a call
//     charges its callee's row: a capability no enclosing with provides and
//     the function's row does not list is S102 at the with operand. An
//     embedding charges it where it is embedded, a conversion to a
//     Context[R] value where the value is made (both are builds of it).
//  3. A with that builds a context whose fails row is not empty writes try
//     on the operand, `with try Live { … }`, as every other site a failure
//     can leave a function at carries try (owner q280); the row escapes
//     there (withBuildFails). An embedding in another named context's
//     build escapes it into that build, which its own fails row bounds.
//  4. No header is the empty row, so a context with none is closed, as
//     before. Nothing is inferred across declarations: the row is what the
//     header writes (the explicit-signature rule for package-level
//     declarations, q266).
//  5. TestAmbient in the needs row is the test runner's (testambient.go):
//     no with site provides it, so it is not charged there.
//
// A law case provides no capability from a with site, so a law does not
// run under a context whose needs row is not empty, nor yet under one whose
// fails row is not empty: a case whose build fails is CTX-ROWS-2b's
// (lawcontexts.go).
//
// Prior art: Effect's Layer<ROut, E, RIn> and ZIO's ZLayer[RIn, E, ROut]:
// providing a layer charges its RIn to the effect that provides it and adds
// its E to the effect's failures. Wire's injector takes its inputs as
// parameters and returns (T, cleanup, error); Gleam and ReScript pass the
// dependencies a constructor needs by hand and return its Result.

// readContextHeader reads the named context w's header rows once, in w's
// file, reporting nothing (checkContexts reports what the header names
// wrongly): w.needs is its needs row but TestAmbient, w.TestAmbient the
// test's ambient when the row lists it, and w.fails its fails row. A
// context of another package was read by its own package's pass.
func (c *checker) readContextHeader(w *Context) {
	if w == nil || w.headerRead || w.Decl == nil || w.Local || w.pkg != c.pkg {
		return
	}
	w.headerRead = true
	if w.Decl.Needs == nil && w.Decl.Fails == nil {
		return
	}
	saved, savedFn := c.cur, c.fn
	c.cur, c.fn = w.file, nil
	c.silent++
	if w.Decl.Fails != nil {
		w.fails = c.rowExpr(w.Decl.Fails.Types, KindErrors, &typeCtx{params: map[string]*TypeParam{}}).known(c.types)
	}
	var row *Row
	if w.Decl.Needs != nil {
		row = c.rowExpr(w.Decl.Needs.Names, KindUses, &typeCtx{params: map[string]*TypeParam{}})
	}
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

// buildFails is the fails row of the named context w, which a with, an
// embedding or a conversion builds; nil for any other context (an inline
// or a local context's entries carry their own try, rows.md Context 3, 9).
func (c *checker) buildFails(w *Context) *Row {
	if w == nil || w.Local || w.Lit != nil {
		return nil
	}
	c.readContextHeader(w)
	return w.fails
}

// requiresTryAtWith is the marker rule (owner q280): a with that builds a
// context whose build may fail writes try on that operand. It is the one
// place the rule is decided.
func requiresTryAtWith() bool { return true }

// withBuildFails decides the with operand at, which builds the context w,
// tried when it is written `try W` (rule 3): the build's failures escape at
// the try; without one they are S105, as any unhandled failure is; a try on
// a context whose build cannot fail is the needless try's S108.
func (c *checker) withBuildFails(at syntax.Expr, tried *syntax.TryExpr, w *Context) {
	fails := c.buildFails(w)
	switch {
	case fails.Empty() && tried != nil:
		c.errorf(tried, CodeType, "try on a context whose build cannot fail: drop the try (rows.md, With 6)")
	case fails.Empty():
	case tried != nil:
		c.escape(tried, fails)
	case !requiresTryAtWith():
		c.escape(at, fails)
	default:
		c.errorf(at, CodeUnhandled, "context %s can fail with %s while it is built, and this with does not handle it: write with try %s (rows.md, With 6)", w.Name, rowText(fails), w.Name)
	}
}
