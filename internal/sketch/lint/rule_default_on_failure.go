package lint

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// defaultOnFailure is the defensive rule of the strict preset (catalog
// CR21, nits' error_to_empty_success and config_fallback; anti-slop
// swallow-without-capture): a catch arm that takes every failure (`case
// _`, or a name bound to the whole failure) and does nothing but give a
// literal: a string or number constant, or an empty composite (`[]T{}`,
// `T{}`), as its value or its function's result. A protocol error then
// reads as an ordinary value, and nothing records that it happened. An
// arm that names the failure it expects (`case NotFound:`) recovers from a
// known outcome and is out of scope, as is a bool (a predicate's false),
// an Option's None (the function's own absent result), and every test and
// law (a law's arm gives its verdict).
var defaultOnFailure = &Rule{
	Name:    "default-on-failure",
	Code:    "L126",
	Doc:     "A catch arm that takes every failure and only gives a literal turns an error into an ordinary value, and nothing records it.",
	Instead: "catch the failures you expect by name and let the others propagate, or record the failure before you give the default",
	Default: Warning,
	Check:   checkDefaultOnFailure,
}

func checkDefaultOnFailure(p *Pass) {
	fi := p.fileIndex()
	for i, x := range fi.catches {
		if fi.catchFlags[i]&nodeInTestLaw != 0 {
			continue
		}
		for _, arm := range x.Arms {
			if takesEvery(p.Info, arm) && len(arm.Body) == 1 && defaultLiteral(p.Info, armValue(arm.Body[0])) {
				p.Report(arm.Case, arm.End(), "this arm takes every failure and gives a literal, so a failure reads as an ordinary value and nothing records it")
			}
		}
	}
}

// takesEvery reports an arm with a pattern that matches any failure: a
// wildcard or a name bound to the whole failure.
func takesEvery(info *check.Info, arm *syntax.CatchArm) bool {
	for _, te := range arm.Types {
		if pt := info.Patterns[te]; pt != nil && (pt.Kind == check.PatWildcard || pt.Kind == check.PatBind) {
			return true
		}
	}
	return false
}

// armValue is the value a one-statement arm gives: its expression, or
// the one result it returns; nil otherwise.
func armValue(s syntax.Stmt) syntax.Expr {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		return s.X
	case *syntax.ReturnStmt:
		if len(s.Results) == 1 {
			return s.Results[0]
		}
	}
	return nil
}

// defaultLiteral reports a string or number constant, or a composite
// literal with no element and no base.
func defaultLiteral(info *check.Info, x syntax.Expr) bool {
	if x == nil {
		return false
	}
	x = unparen(x)
	if v := info.Values[x]; v != nil {
		return v.Kind() != constant.Bool && v.Kind() != constant.Unknown
	}
	c, ok := x.(*syntax.CompositeLit)
	return ok && len(c.Elts) == 0 && c.Base == nil
}
