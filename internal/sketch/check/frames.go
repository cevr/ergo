package check

import "ergo.local/prototype/internal/sketch/syntax"

// Frame-scope effects outside a function body (stage E2, C18).
//
// A frame scope is the scope of a running function body: it owns the
// tasks the body starts or receives, runs its exit actions, and ends the
// scopes an owning with or a context with opens inside it (runtime.md,
// Tasks; resources.md, Context ownership; runtime.md, G1). A function body,
// a function literal's body, a trailing block, a test block and a law body
// each have one. A package let's value, a named context's entries and a type
// declaration (a machine's initial value) run outside every function
// body, so no frame scope is there to own what such a call makes.
//
// The invariant: no node that runs outside a function body (frameFree, by
// inspectFrame's walk: a function literal's body, a trailing block other
// than a with's and a law in a type declaration are frames of their own) is
//
//  1. a call whose result holds a running task (Call.StartsTask: task.Go,
//     tasks.NewSet, a function, a function value or an operation that
//     returns a Task),
//  2. scope.OnExit, which registers its block on the frame,
//  3. a with that owns a provider with a Close, whose scope opens on the
//     frame, or
//  4. a with of a context value, whose region opens on the frame.
//
// Each is S108 at the call. These are exactly the calls the lowering needs
// a frame scope for (lower's needsFrame and bodyNeedsFrame: the std bodies
// that take the hidden frame are task.Go, which returns a Task, and
// scope.OnExit), so the lowering never meets one without a frame.
//
// Prior art: Gleam's module constants are literals and hold no effects;
// Elixir's module attributes run at compile time, never at load; Go runs a
// package var's initializer in init, where a goroutine it starts has no
// owner, which structured concurrency forbids; MoonBit's top-level lets
// run once with no scope. Ergo keeps the package level effect-free in this
// one sense: nothing at the package level owns a task or a scope.

// frameFree reports whether a file declaration's expressions run outside
// every function body. A function declaration's, a test block's and a
// top-level law's bodies are frames; a let, a context and a type
// declaration are not, though a law a type declaration holds is
// (inspectFrame prunes it).
func frameFree(d syntax.Decl) bool {
	switch d.(type) {
	case *syntax.FuncDecl, *syntax.TestDecl, *syntax.LawDecl:
		return false
	case *syntax.VarDecl, *syntax.ContextDecl, *syntax.TypeDecl, *syntax.ImportDecl:
		return true
	}
	return true // a declaration kind added later is checked, not skipped
}

// checkFrames reports every call of the invariant above outside a function
// body. It runs once Info's calls carry StartsTask (finishInfo) and every
// with its acquisitions (withOwnership). Each file is selected before its
// declarations are inspected, so a diagnostic names the file it stands in.
func (c *checker) checkFrames() {
	info := c.pkg.Info
	saved := c.cur
	defer func() { c.cur = saved }()
	for _, f := range c.files {
		c.cur = f
		for _, d := range f.syntax.Decls {
			if !frameFree(d) {
				continue
			}
			inspectFrame(info, d, func(n syntax.Node, _ []*syntax.CallExpr) bool {
				x, ok := n.(*syntax.CallExpr)
				if !ok {
					return true
				}
				if what := frameNeed(info, x); what != "" && !c.diagnosedIn(f, x) {
					c.errorf(x, CodeType, "%s needs the frame scope of a function body, and a package let, a context entry or a type declaration runs outside one; move it into a function", what)
				}
				return true
			})
		}
	}
}

// diagnosedIn reports whether a diagnostic already stands inside x in the
// file f: the site is refused once (a package let that binds a Task is the
// Task rule's, taskescape.go).
func (c *checker) diagnosedIn(f *fileInfo, x syntax.Node) bool {
	pos, end := int(x.Pos()), int(x.End())
	for _, d := range c.diags {
		if d.File == f.index && d.Offset >= pos && d.Offset < end {
			return true
		}
	}
	return false
}

// frameNeed names why the call x needs a frame scope, or "" when it does
// not.
func frameNeed(info *Info, x *syntax.CallExpr) string {
	if call := info.Calls[x]; call != nil {
		if call.StartsTask {
			return "a call that starts a task"
		}
		if isOnExit(call) {
			return "scope.OnExit"
		}
	}
	if w := info.Withs[x]; w != nil {
		if w.Value != nil {
			return "a with of a context value"
		}
		for _, a := range w.Acquired {
			if a.Close != nil {
				return "a with that owns a provider with a Close"
			}
		}
	}
	return ""
}
