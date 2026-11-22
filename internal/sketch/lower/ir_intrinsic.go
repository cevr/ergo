package lower

// IR builder: the bodies of the prelude's collection intrinsics
// (CENSUS-TAIL-2). An extern of the prelude whose body the lowering writes
// (decls.go intrinsic) takes an IR body with the same statements:
//
//	Append(s, x)   return v1.Append(s, x)
//	Put(m, k, v)   return v1.Put(m, k, v)
//	Get(m, k)      v, ok := m[k]; if ok { return Some(v) }; return None
//	Take(m, k)     v, ok := m[k]; delete(m, k)
//	               if ok { return (Some(v), m) }; return (None, m)
//
// Take's body is the value rule's (valueRule): the argument is a copy
// that stays live, so the body takes k out of it in place (rest is m
// itself). Under the alias rule its body calls runtime/v1's MapTake
// through an owned binding, which the IR does not build. That body, any
// other intrinsic (InForce, a host provider's bridge) and a copy of one
// keep their Go text, which is no fallback: a census records them with
// the reason "intrinsic body", and -fallback=off accepts them, as before.
//
// Prior art: Gleam's and ReScript's standard libraries bind such
// operations as externals the compiler never sees inside; Ergo writes
// them, so their bodies are the compiler's own and take its IR.

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
)

// irIntrinsicOp is the intrinsic f's operation when the IR builds its
// body: CollAppend, CollPut, CollGet or CollTake; else CollNone.
func irIntrinsicOp(f *check.Func, copied bool) check.CollOp {
	s := f.Sig
	if copied || len(s.TParams) == 0 || len(s.Results) != 1 {
		return check.CollNone // InForce has no result; a bridge no type parameter
	}
	switch op := check.CollOpOf(f); {
	case op == check.CollAppend && len(s.Params) == 2, op == check.CollPut && len(s.Params) == 3:
		return op
	case preludeGet(f):
		return check.CollGet
	case op == check.CollTake && len(s.Params) == 2 && valueRule():
		return op
	}
	return check.CollNone
}

// intrinsicBody builds the IR body of the prelude intrinsic fd, whose
// parameters are params; copied is a copy of it (mono.go, record.go). It
// reports whether the IR holds the body; a census records it either way.
func (st *irState) intrinsicBody(fc *funcCtx, fd *funcDecl, params []*check.Var, copied bool) (ok bool) {
	f := fd.fn
	op := irIntrinsicOp(f, copied)
	if op == check.CollNone || fd.method != nil || len(fc.witness) > 0 {
		if st.census != nil {
			st.census.Funcs = append(st.census.Funcs, IRCensusFunc{Pkg: f.Pkg.Path, Name: fd.name, Std: f.Pkg.Std(), Reasons: []string{"intrinsic body"}})
		}
		return false
	}
	b := st.newBuilder(fc, nil) // no source statements: nothing for the value rule to analyze
	b.decl, b.pkg = f.Decl, f.Pkg
	b.result = fc.frame.result
	defer st.finish(b, f.Pkg.Path, fd.name, f.Decl, f.Pkg.Std(), &ok)
	p := b.pos(f.Decl, f.Decl.Pos())
	b.fn = &irFunc{Name: fd.name, Pos: p, End: p}
	b.names = newNamer(fc.scope)
	// Go parameter order: the environment, then the parameters (emitBody).
	if !fd.envFree {
		b.env = b.next
		b.next++
	}
	var in []*irExpr
	for _, v := range params {
		if v == nil {
			irNo("intrinsic body")
		}
		b.goType(v.Type, f.Decl)
		b.locals[v] = b.next
		in = append(in, &irExpr{K: "local", Pos: p, I: b.next})
		b.next++
	}
	b.slots = b.next
	b.fn.Body = irScopesIncrease(irUseLocals(b.intrinsicStmts(f, op, in, p)), p) // ir_end.go
	if st.census != nil {
		return false
	}
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, fd.name)
	return true
}

// intrinsicStmts is the body of the intrinsic f, of the operation op, on
// the parameters in.
func (b *irBuilder) intrinsicStmts(f *check.Func, op check.CollOp, in []*irExpr, p irPos) []*irStmt {
	res := f.Sig.Results[0]
	ret := func(x *irExpr) *irStmt { return &irStmt{K: "ret", Pos: p, X: x} }
	switch op {
	case check.CollAppend:
		return []*irStmt{ret(&irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "Append", V: b.ownTypeArgs(f), Args: in})}
	case check.CollPut:
		return []*irStmt{ret(&irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "Put", V: b.ownTypeArgs(f), Args: in})}
	case check.CollGet:
		v, okv := b.mapRead(res, in, p)
		return []*irStmt{v, b.ifSome(okv, ret(b.intrinsicSome(res, v.Local, p)), p), ret(b.machineNone(res, p, f.Decl))}
	}
	// Take: (Option, Map).
	tuple, isTuple := res.(*check.Tuple)
	if !isTuple || len(tuple.Types) != 2 {
		irNo("intrinsic body")
	}
	opt := tuple.Types[0]
	v, okv := b.mapRead(opt, in, p)
	del := &irStmt{K: "expr", Pos: p, X: &irExpr{K: "builtin", Pos: p, Name: "delete", Args: in}}
	pair := func(o *irExpr) *irStmt { return ret(b.tupleLit(tuple, []*irExpr{o, in[0]}, f.Decl, p)) }
	return []*irStmt{v, del, b.ifSome(okv, pair(b.intrinsicSome(opt, v.Local, p)), p), pair(b.machineNone(opt, p, f.Decl))}
}

// mapRead is `v, ok := m[k]` of the Map in[0] at the key in[1], v of the
// Option instance opt's value type, and ok's local.
func (b *irBuilder) mapRead(opt check.Type, in []*irExpr, p irPos) (*irStmt, int) {
	some, _ := optionVariants(opt, b.decl)
	_, vt := onlyField(some)
	v := b.temp("v", b.goType(vt, b.decl), p)
	okv := b.temp("ok", "bool", p)
	return &irStmt{K: "mapget", Pos: p, Local: v, Val: &okv, X: in[0], Y: in[1]}, okv
}

// ifSome is `if ok { then }`.
func (b *irBuilder) ifSome(okv int, then *irStmt, p irPos) *irStmt {
	return &irStmt{K: "if", Pos: p, X: &irExpr{K: "local", Pos: p, I: okv}, Then: []*irStmt{then}, End: p, ElsePos: p, ElseEnd: p}
}

// intrinsicSome is Some of the local v, as the Option instance opt.
func (b *irBuilder) intrinsicSome(opt check.Type, v int, p irPos) *irExpr {
	some, _ := optionVariants(opt, b.decl)
	name, _ := onlyField(some)
	lit := &irExpr{K: "lit", Pos: p, T: b.goType(some, b.decl), Keys: []string{memberName(name)}, Elems: []*irExpr{{K: "local", Pos: p, I: v}}}
	return b.variantInto(some, opt, lit, b.decl) // ir_union.go
}

// ownTypeArgs is the Go type arguments of a runtime call that forwards the
// type parameters of f, in order: the reader of the IR names every
// instance (ergoir's "pcall").
func (b *irBuilder) ownTypeArgs(f *check.Func) string {
	parts := make([]string, len(f.Sig.TParams))
	for i, tp := range f.Sig.TParams {
		parts[i] = b.goType(tp, b.decl)
	}
	return strings.Join(parts, ", ")
}

// onlyField is the name and type of the one field of the record t (the
// prelude's Some, by its shape), or stops the body.
func onlyField(t check.Type) (string, check.Type) {
	if n, ok := t.(*check.Named); ok {
		if s, ok := n.Underlying().(*check.Struct); ok && len(s.Fields) == 1 {
			return s.Fields[0].Name, s.Fields[0].Type
		}
	}
	irNo("intrinsic body")
	return "", nil
}
