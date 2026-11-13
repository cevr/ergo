package lower

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Unfold by definition (types.md, Laws 14). A window call of a provider
// function whose body is a straight line of calls of the provider's
// functions on the same receiver is the same as that line: the call is
// replaced by it, and no law is needed for that. Each call of the line
// passes a parameter of the function or a constant. An `if` whose
// condition the check-time evaluator folds to a constant from the call's
// constant arguments is its taken arm; any other `if` (one that reads the
// provider's state, or an argument that is not constant), and any other
// statement, leaves the call as it is. So `Key("id")` becomes `sep();
// raw("\""); raw("id"); raw("\""); raw(":"); setComma(false)`, since
// `plain("id")` folds to true, and the laws then join its constant text.
//
// A parameter that no call of the line passes is dropped: a window's
// arguments are temporaries or pure reads (rewriteWindow, wiretext.go),
// so no evaluation is lost.
//
// Prior art: GHC's inliner unfolds a definition and then lets its rules
// fire on the result (the "simplifier" interleaves inlining and RULES);
// Lean's `simp` unfolds a definition by its equation lemma.

// rewriteUnfoldPerCall bounds the unfolds of a window: per call of the
// window as the source wrote it. A recursive definition stops there.
const rewriteUnfoldPerCall = 8

// unfoldDef is c replaced by its function's body, when that body is a
// straight line of provider calls (above).
func unfoldDef(c rwCall) ([]rwCall, bool) {
	f := c.fn
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil {
		return nil, false
	}
	params := paramVars(f)
	if len(params) != len(c.args)+1 || params[0] == nil {
		return nil, false
	}
	binds := map[*check.Var]rwArg{}
	consts := map[*check.Var]constant.Value{}
	for j, a := range c.args {
		p := params[j+1]
		if p == nil {
			continue
		}
		binds[p] = a
		if a.c != nil {
			consts[p] = a.c
		}
	}
	return straightLine(f, binds, consts, f.Decl.Body.List)
}

// straightLine is list, statements of f's body, as the provider calls it
// makes on f's receiver, each argument a constant or binds' argument for
// a parameter of f; an `if` the evaluator folds from consts is its taken
// arm. ok is false for any other statement.
func straightLine(f *check.Func, binds map[*check.Var]rwArg, consts map[*check.Var]constant.Value, list []syntax.Stmt) ([]rwCall, bool) {
	info := f.Pkg.Info
	params := paramVars(f)
	var out []rwCall
	var line func(list []syntax.Stmt) bool
	line = func(list []syntax.Stmt) bool {
		for _, s := range list {
			if is, ok := s.(*syntax.IfStmt); ok {
				v, why := eval.Fold(f.Pkg, f.Decl, is.Cond, consts, rewriteFoldBudget)
				if why != "" || v == nil || v.Kind() != constant.Bool {
					return false
				}
				switch {
				case constant.BoolVal(v):
					if !line(is.Body.List) {
						return false
					}
				case is.Else == nil:
				default:
					var arm []syntax.Stmt
					if b, ok := is.Else.(*syntax.BlockStmt); ok {
						arm = b.List
					} else {
						arm = []syntax.Stmt{is.Else}
					}
					if !line(arm) {
						return false
					}
				}
				continue
			}
			call, try := sideCall(s)
			if call == nil || try || call.Trailing != nil {
				return false
			}
			ic := info.Calls[call]
			if ic == nil || ic.Kind != check.CallFunc || ic.Recv == nil || !rewritable(ic.Func) {
				return false
			}
			id, ok := unparen(ic.Recv).(*syntax.Ident)
			if !ok || info.Uses[id] != check.Object(params[0]) {
				return false
			}
			next := rwCall{fn: ic.Func}
			for _, a := range call.Args {
				if v, ok := info.Values[a]; ok {
					next.args = append(next.args, rwArg{c: v})
					continue
				}
				aid, ok := unparen(a).(*syntax.Ident)
				if !ok {
					return false
				}
				p, ok := info.Uses[aid].(*check.Var)
				if !ok {
					return false
				}
				b, ok := binds[p]
				if !ok {
					return false
				}
				next.args = append(next.args, b)
			}
			out = append(out, next)
		}
		return true
	}
	if !line(list) {
		return nil, false
	}
	return out, true
}

// branchDef reads c's function when its body is one `if` whose condition
// is a call of a pure function of the package on parameters (plain(s)),
// with an else, and each arm a straight line (straightLine): the call is
// that `if`, by definition. Each argument of cond and of the arms' calls
// is a constant or c's argument for the parameter, as c passes it.
func branchDef(c rwCall) (cond *check.Func, condArgs []rwArg, then, els []rwCall, ok bool) {
	f := c.fn
	if f == nil || f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Pkg == nil || f.Pkg.Info == nil || len(f.Decl.Body.List) != 1 {
		return nil, nil, nil, nil, false
	}
	is, isIf := f.Decl.Body.List[0].(*syntax.IfStmt)
	if !isIf || is.Else == nil {
		return nil, nil, nil, nil, false
	}
	info := f.Pkg.Info
	params := paramVars(f)
	if len(params) != len(c.args)+1 || params[0] == nil {
		return nil, nil, nil, nil, false
	}
	binds := map[*check.Var]rwArg{}
	for j, a := range c.args {
		if p := params[j+1]; p != nil {
			binds[p] = a
		}
	}
	call, isCall := unparen(is.Cond).(*syntax.CallExpr)
	if !isCall || call.Trailing != nil {
		return nil, nil, nil, nil, false
	}
	ic := info.Calls[call]
	if ic == nil || ic.Kind != check.CallFunc || ic.Recv != nil || ic.Func == nil || ic.Func.Pkg != f.Pkg || ic.Func.Sig == nil || ic.Func.Sig.Fallible || len(ic.Func.Sig.TParams) > 0 {
		return nil, nil, nil, nil, false
	}
	for _, a := range call.Args {
		if v, isConst := info.Values[a]; isConst {
			condArgs = append(condArgs, rwArg{c: v})
			continue
		}
		id, isID := unparen(a).(*syntax.Ident)
		if !isID {
			return nil, nil, nil, nil, false
		}
		p, isVar := info.Uses[id].(*check.Var)
		b, bound := binds[p]
		if !isVar || !bound {
			return nil, nil, nil, nil, false
		}
		condArgs = append(condArgs, b)
	}
	then, ok = straightLine(f, binds, nil, is.Body.List)
	if !ok {
		return nil, nil, nil, nil, false
	}
	var arm []syntax.Stmt
	if b, isBlock := is.Else.(*syntax.BlockStmt); isBlock {
		arm = b.List
	} else {
		arm = []syntax.Stmt{is.Else}
	}
	els, ok = straightLine(f, binds, nil, arm)
	if !ok {
		return nil, nil, nil, nil, false
	}
	return ic.Func, condArgs, then, els, true
}
