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
//  1. A named context whose header's needs row lists TestAmbient may
//     require it while it is built. The rest of the row is the with
//     site's (contextneeds.go); TestAmbient is the runner's alone.
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

// contextTestAmbient reports whether the named context w's header needs
// TestAmbient (rule 1), read once with the rest of its row
// (readContextHeader).
func (c *checker) contextTestGrant(w *Context) (name, life, section string) {
	c.readContextHeader(w)
	if w != nil && w.TestAmbient != nil {
		return "TestAmbient", "its folder lives for one with of it", "Test's ambient"
	}
	if w != nil && w.TestLoopback != nil {
		return "TestLoopback", "its grant lives for one build", "Test loopback"
	}
	return "", "", ""
}

// testAmbientWith refuses a with of a context that needs TestAmbient
// outside a test (rule 3).
func (c *checker) testAmbientWith(at syntax.Node, w *Context) {
	name, _, section := c.contextTestGrant(w)
	if name == "" || c.fn != nil && c.fn.test {
		return
	}
	c.errorf(at, CodeMissingCap, "context %s needs %s; only the test runner provides it: with it in a test, or let a law run under it (rows.md, %s)", w.Name, name, section)
}

// testAmbientEmbed refuses an embedding of a context that needs
// TestAmbient (rule 3).
func (c *checker) testAmbientEmbed(at syntax.Node, inner *Context) {
	if name, life, section := c.contextTestGrant(inner); name != "" {
		c.errorf(at, CodeType, "context %s needs %s and is not embedded: %s (rows.md, %s)", inner.Name, name, life, section)
	}
}
