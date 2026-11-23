package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// The test's ambient (rows.md, Test's ambient; runtime.md, Tests 11; TH1). A
// test never holds the prelude's Ambient, so a law over a real host
// provider could not run under ergo test. the prelude's TestAmbient is the
// test's ambient instead: a fresh folder per case, which only the test
// runner provides. Each rule is an invariant over its whole domain:
//
//  1. A named context whose header is exactly `needs TestAmbient` may
//     require it while it is built; it stays closed to every other
//     capability (README Gap 6). Any other header row is still S109 for
//     The v1 language stage b, as a provides clause and an inline or a local
//     context's header are.
//  2. No context entry binds TestAmbient (S106), as none binds
//     Ambient: its one provider is the runner.
//  3. A with of such a context stands only in a test's body, a function
//     literal in it included (S102 elsewhere); a law runs under it as
//     under any named context (lawcontexts.go). It is never embedded and
//     never converted to a Context value (S108): its folder lives for one
//     build, which only a with or a law case owns.
//
// Prior art: Go's testing.T.TempDir and Rust's tempfile give a test a
// directory removed at its end, as a value any code may misuse; Elixir's
// ExUnit @tag :tmp_dir names it per test. None of them confines the code
// under test to it; here the providers refuse a path out of the folder.

const testAmbientEntryText = "no context binds TestAmbient: its one provider is the test runner, for a named context whose header says needs TestAmbient (rows.md, Test's ambient)"

// contextTestAmbient reports whether the named context w's header is
// exactly `needs TestAmbient` (rule 1). It reads the header once, in
// w's file, reporting nothing: checkContexts reports a header it refuses.
func (c *checker) contextTestAmbient(w *Context) bool {
	if w == nil || w.headerRead || w.Decl == nil || w.pkg != c.pkg {
		return w != nil && w.TestAmbient != nil
	}
	w.headerRead = true
	if w.Decl.Needs == nil || w.Decl.Provides != nil {
		return false
	}
	saved, savedFn := c.cur, c.fn
	c.cur, c.fn = w.file, nil
	c.silent++
	row := c.rowExpr(w.Decl.Needs.Names, KindUses, &typeCtx{params: map[string]*TypeParam{}})
	c.silent--
	c.cur, c.fn = saved, savedFn
	elems := row.Elems()
	if len(elems) == 1 && isTestAmbient(elems[0]) {
		w.TestAmbient = elems[0].(*Named)
	}
	return w.TestAmbient != nil
}

// testAmbientWith refuses a with of a context that needs TestAmbient
// outside a test (rule 3).
func (c *checker) testAmbientWith(at syntax.Node, w *Context) {
	if !c.contextTestAmbient(w) || c.fn != nil && c.fn.test {
		return
	}
	c.errorf(at, CodeMissingCap, "context %s needs TestAmbient; only the test runner provides it: with it in a test, or let a law run under it (rows.md, Test's ambient)", w.Name)
}

// testAmbientEmbed refuses an embedding of a context that needs
// TestAmbient (rule 3).
func (c *checker) testAmbientEmbed(at syntax.Node, inner *Context) {
	if c.contextTestAmbient(inner) {
		c.errorf(at, CodeType, "context %s needs TestAmbient and is not embedded: its folder lives for one with of it (rows.md, Test's ambient)", inner.Name)
	}
}
