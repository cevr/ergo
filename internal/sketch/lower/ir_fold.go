package lower

// The fold pass (LAWFFI LF3c; plan-r1 §2 `pure`: "a call on constants
// folds at check time"), a pass over a built body before its forms are
// erased (IR-FORMS: passes run on the forms). See ir.go.
//
// A call of a reviewed-pure Go function whose operands are all constants,
// and which has a check-time native (eval.FoldGo: the same Go function,
// called by the compiler), becomes the constant it gives:
//
//	q := strconv.Quote("lit")    ⇒    q := "\"lit\""
//
// The native's value is the compiler's Go's, and some of them read Go's
// tables (strconv.Quote and the case mappings read the Unicode tables). So
// the pass folds only when the compiler runs on the pinned release
// (toolchain.Version), the one that builds the program; on any other Go it
// leaves the call and says why. A reviewed-pure Go function calls back no
// Ergo code, raises no defect on a basic operand and has no effect, so the
// call and its constant are the same value at the same place.
//
// -X lower.pureFold=off turns it off: the reference, the IR without the
// pass.

import (
	"go/constant"
	"go/token"
	"runtime"
	"strings"
	"unicode/utf8"

	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/toolchain"
)

// pureFold, "off", turns the fold pass off: set with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.pureFold=off.
var pureFold string

// SetPureFold sets the fold pass on or off for the lowerings that follow
// and returns what restores the setting before it: the reference of
// TestPureFoldAgrees (internal/experimental/irspike).
func SetPureFold(on bool) (restore func()) {
	saved := pureFold
	pureFold = "off"
	if on {
		pureFold = ""
	}
	return func() { pureFold = saved }
}

// compilerGo is the Go release the compiler runs on.
var compilerGo = runtime.Version()

// SetCompilerGo makes the lowerings that follow see the compiler running
// on the Go release v, and returns what restores the release before it:
// a test of the pinned-release guard.
func SetCompilerGo(v string) (restore func()) {
	saved := compilerGo
	compilerGo = v
	return func() { compilerGo = saved }
}

// The fold pass's reasons (ir_forms_report.go). Only a pure Go call that
// has a check-time native gets a site.
var (
	foldFired    = &FormReason{"call", "fold", true, "computed at compile time by the same Go function"}
	foldOtherGo  = &FormReason{"call", "fold", false, "the compiler does not run on the pinned Go release"}
	foldNotConst = &FormReason{"call", "fold", false, "an operand is not a constant of a basic type"}
)

// foldCalls is body with each pure Go call on constants that has a
// check-time native replaced by its value.
func (b *irBuilder) foldCalls(body []*irStmt) []*irStmt {
	if pureFold == "off" {
		return body
	}
	// consts are the locals bound once, never written, to a constant
	// (ir_merge.go's fixedLocals): an operand that reads one is that
	// constant, so a call on a folded call folds too.
	fixed, consts := b.fixedLocals(body), map[int]*irExpr{}
	var stmts func([]*irStmt)
	var expr func(*irExpr) *irExpr
	expr = func(x *irExpr) *irExpr {
		if x == nil {
			return nil
		}
		x.X = expr(x.X)
		x.Y = expr(x.Y)
		for i, a := range x.Args {
			x.Args[i] = expr(a)
		}
		for i, e := range x.Elems {
			x.Elems[i] = expr(e)
		}
		stmts(x.Body)
		if x.C == nil || x.C.fn == nil || !x.C.fn.Pkg.Host() || !eval.HasGoFold(x.C.fn.Pkg.Path, x.C.fn.Name) {
			return x
		}
		if c := b.foldCall(x, consts); c != nil {
			return c
		}
		return x
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			s.X = expr(s.X)
			if s.K == "def" && s.X != nil && fixed[s.Local] {
				switch {
				case s.X.K == "const":
					consts[s.Local] = s.X
				case s.X.K == "local" && consts[s.X.I] != nil:
					// A let of a hoisted call's temporary.
					consts[s.Local] = consts[s.X.I]
				}
			}
			s.Y = expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				for i, v := range c.Vals {
					c.Vals[i] = expr(v)
				}
				stmts(c.Body)
			}
		}
	}
	stmts(body)
	return body
}

// foldCall is the constant the call x gives, or nil, with x's site
// reported either way; an operand local of consts is its constant.
func (b *irBuilder) foldCall(x *irExpr, consts map[int]*irExpr) *irExpr {
	args := make([]constant.Value, len(x.Args))
	for i, a := range x.Args {
		if c := consts[a.I]; a.K == "local" && c != nil {
			a = c
		}
		if args[i] = irConstValue(a); args[i] == nil {
			b.formSite(x.C.site, foldNotConst) // ir_forms_report.go
			return nil
		}
	}
	if compilerGo != toolchain.Version {
		b.formSite(x.C.site, foldOtherGo)
		return nil
	}
	v, why := eval.FoldGo(x.C.fn.Pkg.Path, x.C.fn.Name, args)
	if v == nil {
		ice(x.C.site, "the check-time native of a reviewed-pure Go call: %s", why)
	}
	out := &irExpr{K: "const", Pos: x.Pos}
	switch v.Kind() {
	case constant.String:
		s := constant.StringVal(v)
		if !utf8.ValidString(s) {
			ice(x.C.site, "the check-time native gave a string that is not UTF-8")
		}
		out.T, out.V = "string", s
	case constant.Bool:
		out.T, out.V = "bool", v.ExactString()
	default:
		ice(x.C.site, "the check-time native gave a %s", v.Kind())
	}
	b.formSite(x.C.site, foldFired)
	b.foldSites++
	return out
}

// irConstValue is the constant of a "const" expression of a basic Go type,
// or nil.
func irConstValue(a *irExpr) constant.Value {
	if a.K != "const" {
		return nil
	}
	switch a.T {
	case "string":
		return constant.MakeString(a.V)
	case "bool":
		return constant.MakeBool(a.V == "true")
	case "int", "int8", "int16", "int32", "int64":
		digits, neg := strings.CutPrefix(a.V, "-")
		v := constant.MakeFromLiteral(digits, token.INT, 0)
		if v.Kind() != constant.Int {
			return nil
		}
		if neg {
			v = constant.UnaryOp(token.SUB, v, 0)
		}
		return v
	}
	return nil
}
