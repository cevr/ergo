package lower

import "ergo.local/prototype/internal/sketch/syntax"

// The growth rule of Ergo-side inlining (inline.go, OPT7). Ergo has no
// profile, so the rule reads two static facts of the source:
//
//  1. Paths, not source order. Each statement list a Go function writes
//     (a body, an if or else block, a match or catch arm, a loop body)
//     has an account that starts with the growth of the list around it.
//     An inlined site spends its body's nodes on its own list's account.
//     Two arms of one branch never spend each other's allowance: a body
//     that inlines much on one arm (lru's hit path) keeps the whole
//     allowance on the other (its eviction path). An inlined body's lists
//     start from its site's account, so nested inlining spends the path
//     of the site.
//  2. Cold lists. A list that ends in a typed failure (return of an error
//     value) or a defect (Unreachable) leaves its function abnormally: as
//     Go marks a panic's path unlikely, and as GCC treats a noreturn call,
//     a site there is not inlined. Its call is left to Go.
//
// The allowance of one path is inlineGrowth: inlineDepth bodies of at
// most inlineBudget nodes, the largest chain of nested inlined sites one
// call may write. It is the product of the two bounds above, not a number
// fitted to one example. Each site on a path spends it, so the growth of a
// Go function is at most inlineGrowth per statement list of its source:
// linear in the source, as GCC's growth bounds are a share of a
// function's size.
//
// The reference is growthLowering "function": one account per Go
// function, spent in source order, with no cold lists (the rule before
// OPT7, which then had an allowance of 1600 nodes). It is set by a test,
// or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.growthLowering=function.
// Both admit only sites the inline rule admits, so the program's
// behaviour is the same (TestInlineAgrees); TestInlineGrowth holds which
// sites each inlines.
//
// Prior art: GCC bounds inlining by large-function-growth and
// inline-unit-growth (shares of the size) and skips cold calls (noreturn,
// unlikely paths); LLVM's inliner lowers its threshold for a cold call
// site; Go's inliner marks a panic path unlikely; OCaml's flambda spends a
// speculation budget per round. Gleam, Elixir and ReScript inline little
// or leave it to their hosts (the BEAM JIT, a JavaScript engine).
var growthLowering string

const (
	// inlineGrowth is the most nodes of inlined bodies one path takes in.
	inlineGrowth = inlineDepth * inlineBudget
	// functionGrowth is the reference's allowance per Go function.
	functionGrowth = 1600
)

// growthAcct is the inline growth the path to one statement list has taken.
type growthAcct struct {
	first *syntax.Stmt // the list's first statement: its identity
	spent int
	cold  bool
}

// enterList opens the account of list, unless list is the one already open
// (yieldList writes a list's prefix, then its last statement), and returns
// the function that closes it.
func (fc *funcCtx) enterList(list []syntax.Stmt) func() {
	if len(list) == 0 {
		return func() {}
	}
	top := fc.sitePath()
	if top[len(top)-1].first == &list[0] {
		return func() {}
	}
	acct := &growthAcct{first: &list[0], spent: top[len(top)-1].spent, cold: top[len(top)-1].cold || fc.coldList(list)}
	n := len(fc.path)
	fc.path = append(fc.path[:n:n], acct)
	return func() { fc.path = fc.path[:n] }
}

// sitePath is the accounts around the code being written, outermost first;
// an inlined body starts from it.
func (fc *funcCtx) sitePath() []*growthAcct {
	if len(fc.path) == 0 {
		fc.path = []*growthAcct{{}}
	}
	return fc.path[:len(fc.path):len(fc.path)]
}

// coldList reports a list that ends in a typed failure or a defect.
func (fc *funcCtx) coldList(list []syntax.Stmt) bool {
	switch s := list[len(list)-1].(type) {
	case *syntax.ReturnStmt:
		return len(s.Results) == 1 && fc.failureValue(s.Results[0])
	case *syntax.ExprStmt:
		return fc.unreachableCall(s.X) != nil
	}
	return false
}

// grow reports whether the growth rule admits an inlined body of size
// nodes at the current site, and spends it when it does.
func (fc *funcCtx) grow(size int) bool {
	if growthLowering == "function" {
		if fc.l.inlineGrown == nil {
			fc.l.inlineGrown = map[*namer]int{}
		}
		if fc.l.inlineGrown[fc.scope]+size > functionGrowth {
			return false
		}
		fc.l.inlineGrown[fc.scope] += size
		return true
	}
	path := fc.sitePath()
	acct := path[len(path)-1]
	if acct.cold || acct.spent+size > inlineGrowth {
		return false
	}
	acct.spent += size
	return true
}
