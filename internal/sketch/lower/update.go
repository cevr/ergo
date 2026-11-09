package lower

import (
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A call of std maps.Update (vr-elide-2). Update's body reads the old
// value through the prelude's Get, an Option, which is an interface, and
// passes it to its callback f, a function value: Go cannot see that f
// keeps nothing, so each Some escapes to the heap, and the call of f is
// indirect. When the call passes its Map unshared (an owned argument,
// values.go V5), the site cannot fail, and f is a package function named
// at the call, with no type parameter, no Task result and no witness, the
// lowering writes Update's body at the site with f called directly:
//
//	m, k := <m>, <k>
//	var o Option[V]
//	if v, ok := m[k]; ok { o = Some[V]{Value: v} } else { o = None{} }
//	m[k] = f(o)
//
// and the call's value is m. Go's escape analysis then sees f's parameter:
// when f keeps nothing of it, the Option stays on the stack, so the call
// allocates what hand Go's v, ok := m[k]; m[k] = f(v, ok) does. The
// statements are Update's in its order: the operands once, left to right,
// then Get, then f, then the write; the write needs no copy, since the Map
// is the caller's unshared argument (Update's let mut out = m, which V5
// already elides). Update's entry poll is dropped: it has no loop, and the
// caller's own polls stay.
//
// The reference lowering calls Update's body (UpdateOwned): updateLowering
// "call", set by a test or by a whole build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.updateLowering=call.
// TestValueRuleAgrees runs its programs, which call Update with a named
// callback, under both.
//
// Prior art: Go's own maps package has no Update, and a hand writer
// writes the comma-ok read; Rust's HashMap::entry(k).and_modify(f)
// .or_insert(v) is monomorphized over f, so f inlines; Gleam's dict.upsert
// and Elixir's Map.update take a closure through the BEAM's indirect call,
// with no unboxing; GHC's Data.Map.alter specializes on a known function
// by inlining.
var updateLowering string

// The mutants of the rewrite are valueMutant's (values.go), for
// TestValueRuleMutants only: "update-none" (the Option is always None)
// and "update-no-write" (the call's value is the Map without the write).

// mapsUpdate reports std maps.Update.
func mapsUpdate(f *check.Func) bool {
	return f != nil && f.Pkg != nil && f.Pkg.Path == "maps" && f.Name == "Update" && f.Decl != nil && f.Decl.Body != nil && len(f.Sig.Params) == 3
}

// updateCallback is the package function a call of maps.Update names as
// its callback, when the rewrite above may call it directly, or nil.
func (fc *funcCtx) updateCallback(arg syntax.Expr) *check.Func {
	var id *syntax.Ident
	switch x := unparen(arg).(type) {
	case *syntax.Ident:
		id = x
	case *syntax.SelectorExpr:
		pkg, ok := unparen(x.X).(*syntax.Ident)
		if !ok {
			return nil
		}
		if _, ok := fc.l.info.Uses[pkg].(*check.PkgName); !ok {
			return nil
		}
		id = x.Sel
	default:
		return nil
	}
	g, ok := fc.l.info.Uses[id].(*check.Func)
	if !ok || g.Decl == nil || g.Pkg == nil || g.Pkg.Info == nil || g.Sig == nil {
		return nil
	}
	if len(g.Sig.TParams) > 0 || len(g.Sig.Params) != 1 || len(g.Sig.Results) != 1 || len(rowElems(g.Sig.Errors)) > 0 || g.Pkg.Info.TaskResults[g] || len(witnessParams(g.Sig)) > 0 {
		return nil
	}
	if g.Decl.Extern != nil || g.Decl.Body == nil {
		return nil
	}
	return g
}

// updateFused lowers the call x of maps.Update as above and reports
// whether it did; the text is the call's value.
func (fc *funcCtx) updateFused(x *syntax.CallExpr, c *check.Call) (string, bool) {
	if updateLowering == "call" || fc.stmtCall == x || !mapsUpdate(c.Func) || fc.l.vals == nil || !fc.l.vals.ownCall[x] || failsHere(c) || x.Trailing != nil {
		return "", false
	}
	if len(c.TypeArgs) < 2 || len(freeTypeParams(c.TypeArgs[0])) > 0 || len(freeTypeParams(c.TypeArgs[1])) > 0 {
		return "", false
	}
	list := append([]syntax.Expr{}, x.Args...)
	if c.Recv != nil {
		list = append([]syntax.Expr{c.Recv}, list...)
	}
	if len(list) != 3 {
		return "", false
	}
	g := fc.updateCallback(list[2])
	if g == nil {
		return "", false
	}
	gd := fc.l.useFunc(g, x)
	if gd.method != nil || gd.fallible {
		return "", false
	}
	opt := g.Sig.Params[0].Type
	if !isPreludeType(opt, "Option") {
		return "", false
	}
	some, none := optionVariants(opt, x)
	vals := fc.callArgs(list[:2], x)
	m, k := fc.temp("m"), fc.temp("k")
	fc.line(m + ", " + k + " := " + vals[0] + ", " + vals[1])
	o, v, ok := fc.temp("o"), fc.temp("v"), fc.temp("ok")
	fc.line("var " + o + " " + fc.goType(opt))
	if valueMutant == "update-none" {
		fc.line(o + " = " + fc.goType(none) + "{}")
	} else {
		fc.line("if " + v + ", " + ok + " := " + m + "[" + k + "]; " + ok + " {")
		fc.line("\t" + o + " = " + fc.goType(some) + "{" + memberName("Value") + ": " + v + "}")
		fc.line("} else {")
		fc.line("\t" + o + " = " + fc.goType(none) + "{}")
		fc.line("}")
	}
	args := []string{o}
	if !gd.pure {
		args = append([]string{fc.envExpr(x)}, args...)
	}
	call := gd.name + "(" + strings.Join(args, ", ") + ")"
	if valueMutant == "update-no-write" {
		fc.line("_ = " + call)
		return m, true
	}
	fc.line(m + "[" + k + "] = " + call)
	return m, true
}
