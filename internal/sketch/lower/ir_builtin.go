package lower

import (
	"go/constant"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// goBuiltinCall is the call x of an extern that binds Go's builtin
// (check/builtin.go GoBuiltin): the builtin itself on the IR, as the
// emitter's builtinCall writes it (calls.go), with no Go function
// between. Its operands are a call's, with the value rule applied to its
// source (valueSource: Delete's Map cloned, append's slice clipped, unless
// the call may write it in place), and a borrow operand (copy's dst,
// clear's slice) written through.
//
// The IR's form is one kind, "builtin", named by Go's builtin, which the
// decoder checks and writes as Go's call of it (ergoir_builtin.go), so Go
// sees its own ODELETE, OCOPY, OCLEAR, OMIN, OMAX and OCAP. A spread
// append uses "builtin"; append of one element keeps the "append" kind,
// and len the "len" kind.
//
// Delete's value is the Map it wrote (check/builtin.go): a statement call
// (m = m.Delete(k) in place, mapStore, or a call whose value is dropped)
// is Go's delete alone; elsewhere the Map is bound to a temporary first
// and the temporary is the value, as the emitter writes it.
func (b *irBuilder) goBuiltinCall(x *syntax.CallExpr, c *check.Call, f *check.Func, p irPos, stmt bool) ([]*irStmt, *irExpr, bool) {
	name := check.GoBuiltin(f)
	if builtinLowering == "call" {
		irNo("call of a Go builtin under the reference lowering")
	}
	list := x.Args
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	pre, args := b.operands(list) // ir_fail.go
	if check.AppendsString(f) {
		// Go's append(xs, s...): the bytes of s at the end of xs. Its
		// source is clipped as Append's is (valueSource).
		if len(args) != 2 {
			irNo("append form")
		}
		if valueRule() && !b.l.valueFacts().inPlace[x] && valueMutant != "in-place" {
			b.keepImport("var _ = " + b.l.goPkg("slices") + ".Clip[[]byte]")
			args[0] = &irExpr{K: "pcall", Pos: args[0].Pos, T: "slices", Name: "Clip", V: "[]byte, byte", Args: []*irExpr{args[0]}}
		}
		return pre, &irExpr{K: "builtin", Pos: p, Name: "append", Args: args}, false
	}
	b.valueSource(x, c, list, args) // ir_pkg.go
	if c.ReadsClaimReceiver() && len(args) > 0 {
		if _, borrowed := b.l.info.Types[c.Recv].(*check.Pointer); borrowed {
			args[0] = &irExpr{K: "deref", Pos: args[0].Pos, X: args[0]}
		}
	}
	for i, prm := range f.Sig.Params {
		if _, ok := prm.Type.(*check.Pointer); ok && i < len(args) {
			args[i] = &irExpr{K: "deref", Pos: args[i].Pos, X: args[i]}
		}
	}
	if pre, e, effect, ok := b.intMapBuiltin(x, name, list, pre, args, p, stmt); ok { // ir_intmap.go
		return pre, e, effect
	}
	switch name {
	case "append":
		if len(args) != 2 {
			irNo("append form")
		}
		if check.AppendSpreads(f) {
			// Go's append(xs, ys...): keep ys as lowered by operands.
			// The value walk treats it as a read only when its elements
			// hold no slice or Map (values.go).
			e := &irExpr{K: "builtin", Pos: p, Name: name, Args: args}
			if stmt {
				return append(pre, &irStmt{K: "discard", Pos: p, X: e}), nil, false
			}
			return pre, e, false
		}
		return pre, &irExpr{K: "append", Pos: p, Args: args}, false
	case "len":
		if len(args) != 1 {
			irNo("len form")
		}
		if arr, ok := underEqual(b.l.info.Types[list[0]]).(*check.Array); ok {
			if args[0].K != "local" && args[0].K != "const" && args[0].K != "zero" {
				pre = append(pre, &irStmt{K: "discard", Pos: p, X: args[0]})
			}
			return pre, b.constant(b.l.info.Types[x], constant.MakeInt64(arr.Len), x, p), false
		}
		return pre, b.foldConst(x, &irExpr{K: "len", Pos: p, X: args[0]}), false // ir_opfold.go: a constant's len
	case "cap", "min", "max":
		return pre, &irExpr{K: "builtin", Pos: p, Name: name, Args: args}, false
	case "copy":
		// copy writes its first operand from its second: the call runs
		// where it is.
		if len(args) != 2 {
			irNo("copy form")
		}
		return pre, &irExpr{K: "builtin", Pos: p, Name: name, Args: []*irExpr{args[0], args[1]}}, true
	case "clear":
		return pre, &irExpr{K: "builtin", Pos: p, Name: name, Args: args}, true
	case "delete":
		if len(args) != 2 {
			irNo("delete form")
		}
		if stmt {
			return pre, &irExpr{K: "builtin", Pos: p, Name: "delete", Args: args}, false
		}
		m := b.temp("t", b.goType(b.l.info.Types[x], x), p)
		local := func() *irExpr { return &irExpr{K: "local", Pos: p, I: m} }
		pre = append(pre,
			&irStmt{K: "def", Pos: p, Local: m, X: args[0]},
			&irStmt{K: "expr", Pos: p, X: &irExpr{K: "builtin", Pos: p, Name: "delete", Args: []*irExpr{local(), args[1]}}})
		return pre, local(), false
	}
	irNo("call of the Go builtin %s", name)
	return nil, nil, false
}
