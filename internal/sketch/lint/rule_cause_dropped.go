package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// causeDropped is the strict rule for a failure narrowed into another
// with its cause thrown away (anti-slop require-cause-when-narrowing;
// catalog §1b): a catch arm that takes every failure (`case _`), so cannot
// tell them apart, and returns a failure of the function's row. The
// caller sees the new failure and nothing of which failure it was or
// why. silent-catch does not report it: the arm returns.
//
// An arm that names the failures it catches is out of scope: the type
// it names is the cause, and the 2026-11-18 read sample found such arms
// (fs.Denied into "permission denied", strconv.Malformed into a usage
// error) narrowing on purpose. A bound failure is read (the checker
// refuses an unread binding), so the arm carries it.
var causeDropped = &Rule{
	Name:    "cause-dropped",
	Code:    "L136",
	Doc:     "A catch arm that takes every failure and fails with another drops which failure it was and why.",
	Instead: "name the failures the arm expects, or bind the failure and carry what the caller needs of it into the new failure's fields",
	Default: Warning,
	Check:   checkCauseDropped,
}

func checkCauseDropped(p *Pass) {
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Body == nil {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok || fn.Sig == nil || fn.Sig.Errors == nil {
			continue
		}
		walkNode(f.Body, func(n syntax.Node) bool {
			x, ok := n.(*syntax.CatchExpr)
			if !ok || p.Info.Matches[x] == nil {
				return true
			}
			for _, arm := range x.Arms {
				if !takesEvery(p.Info, arm) || readsFailure(p.Info, arm) {
					continue
				}
				for _, ret := range ownReturns(arm.Body) {
					if returnsFailure(p.Info, fn, ret) {
						p.Report(ret.Pos(), ret.End(), "this arm takes every failure and fails with "+p.Info.Types[ret.Results[0]].String()+", dropping which failure it was")
					}
				}
			}
			return true
		})
	}
}

// ownReturns is the return statements of the statements' own frame: a
// function literal's are its own.
func ownReturns(body []syntax.Stmt) []*syntax.ReturnStmt {
	var out []*syntax.ReturnStmt
	for _, s := range body {
		walkNode(s, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit:
				return false
			case *syntax.ReturnStmt:
				out = append(out, n)
			}
			return true
		})
	}
	return out
}
