package check

import "ergo.local/prototype/internal/sketch/syntax"

// etaMethodValue records the literal x when it is the exact eta closure
// func(k…) { r.f(k…) } of a method value r.f whose meaning is the same
// (types.md, Methods 2), for lint's eta-method-value fix (CV1-FIX). It
// is one only when:
//
//   - its body is one call r.f(…), with no try and no trailing block, of a
//     function f on r's type (a dot call; no extern), passing the
//     literal's parameters, every one, in order, as plain names;
//   - r is a name bound outside the literal that no write can follow: a
//     parameter or a let, not a let mut, and not a borrow (mut T). The
//     method value binds r's value where it is taken, and the literal reads
//     r when it runs (Function values 2 and 3), so the two agree only when
//     r cannot change between the take and a call (CV1 capture-rule.md);
//   - r.f as a value is no S108 (methodValueRefused), and its type is the
//     literal's, rows included, so it fits wherever the literal did.
func (c *checker) etaMethodValue(x *syntax.FuncLit, sig *Signature) {
	if c.silent != 0 || x.Body == nil || len(x.Body.List) != 1 || x.Type == nil {
		return
	}
	call := etaCall(x.Body.List[0])
	if call == nil || call.Trailing != nil {
		return
	}
	sel, ok := call.Fun.(*syntax.SelectorExpr)
	if !ok {
		return
	}
	recv, ok := sel.X.(*syntax.Ident)
	if !ok {
		return
	}
	info := c.pkg.Info
	cl := info.Calls[call]
	if cl == nil || cl.Func == nil || cl.Recv == nil || cl.Func.owner == nil || isExtern(cl.Func) {
		return
	}
	fn := cl.Func
	v, ok := info.Uses[recv].(*Var)
	if !ok || v.mut || v.bindRow || isPointer(v.Type) || !isNamedType(v.Type) {
		return
	}
	if !etaPassesParams(info, x.Type, call.Args) {
		return
	}
	for _, f := range paramFields(x.Type) {
		for _, id := range f.Names {
			if info.Defs[id] == v {
				return // r is a parameter of the literal itself
			}
		}
	}
	want := c.selfSig(v.Type, fn)
	if c.methodValueRefused(sel, operand{mode: modeValue, typ: v.Type}, want, fn) != "" {
		return
	}
	if !c.identical(sig, want) {
		return
	}
	if info.etaMethodValues == nil {
		info.etaMethodValues = map[*syntax.FuncLit]*syntax.SelectorExpr{}
	}
	info.etaMethodValues[x] = sel
}

// isNamedType reports a declared type: a function on a type is reached
// through one (196m), never through a type parameter.
func isNamedType(t Type) bool {
	_, ok := t.(*Named)
	return ok
}

// etaCall is the call a literal's one statement is, as an expression
// statement or a return of one value, with no try; nil otherwise.
func etaCall(s syntax.Stmt) *syntax.CallExpr {
	var x syntax.Expr
	switch s := s.(type) {
	case *syntax.ExprStmt:
		x = s.X
	case *syntax.ReturnStmt:
		if len(s.Results) != 1 {
			return nil
		}
		x = s.Results[0]
	default:
		return nil
	}
	call, _ := x.(*syntax.CallExpr)
	return call
}

func paramFields(ft *syntax.FuncType) []*syntax.Field {
	if ft == nil || ft.Params == nil {
		return nil
	}
	return ft.Params.List
}

// etaPassesParams reports whether args are the literal's parameters, every
// one, in order, each a plain name.
func etaPassesParams(info *Info, ft *syntax.FuncType, args []syntax.Expr) bool {
	var params []Object
	for _, f := range paramFields(ft) {
		if len(f.Names) == 0 {
			return false
		}
		for _, id := range f.Names {
			o := info.Defs[id]
			if o == nil {
				return false
			}
			params = append(params, o)
		}
	}
	if len(args) != len(params) {
		return false
	}
	for i, a := range args {
		id, ok := a.(*syntax.Ident)
		if !ok || info.Uses[id] != params[i] {
			return false
		}
	}
	return true
}

// EtaMethodValue is the method value r.f the literal x is the exact eta
// closure of, when writing r.f keeps its meaning (etaMethodValue); nil
// otherwise.
func (info *Info) EtaMethodValue(x *syntax.FuncLit) *syntax.SelectorExpr {
	return info.etaMethodValues[x]
}
