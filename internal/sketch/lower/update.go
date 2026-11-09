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
// The callback's comma-ok variant (fs7, batch 12). When f's body is one
// match on its Option parameter, the parameter is used nowhere else, and
// every arm takes a shape getMatch lowers (getmatch.go), the call is
// written with no Option at all:
//
//	v, ok := m[k]
//	m[k] = fOk(v, ok)
//
// fOk is f's body lowered once more with the parameter replaced by the
// value and the flag of the Map read, as getMatch writes a match on
// m.Get(k): `if ok { Some's arm } else { None's arm }`. That is hand Go's
// v, ok := m[k]; m[k] = f(v, ok), with no interface and no type switch.
// The arms are the same statements in the same order; only the Option
// that carried v and ok is gone. f itself is kept for its other uses.
//
// The reference lowerings: updateLowering "option" writes the call at its
// site with the Option (the form above, vr-elide-2), and "call" calls
// Update's body (UpdateOwned). Each is set by a test or by a whole build
// with -ldflags=-X=ergo.local/prototype/internal/sketch/lower.updateLowering=….
// TestValueRuleAgrees runs its programs, which call Update with a
// callback that has the variant and with one that has not, under all
// three.
//
// Prior art: Go's own maps package has no Update, and a hand writer
// writes the comma-ok read; Rust's HashMap::entry(k).and_modify(f)
// .or_insert(v) is monomorphized over f, so f inlines and its Option is a
// niche; Gleam's dict.upsert and Elixir's Map.update take a closure
// through the BEAM's indirect call, with no unboxing; GHC's Data.Map.alter
// specializes on a known function by inlining, and its worker/wrapper
// split unboxes a Maybe argument into a constructor test, as fOk does;
// ReScript compiles option to undefined or the value (an unboxed option).
var updateLowering string

// The mutants of the rewrite are valueMutant's (values.go), for
// TestValueRuleMutants only: "update-none" (the Option is always None),
// "update-no-write" (the call's value is the Map without the write),
// "update-ok-none" (the variant is always called with ok false) and
// "update-ok-swap" (the variant takes each variant's arm for the other);
// "update-ok-any" (okShape admits a body that is not one match on its
// parameter, which then does not build) and "update-ok-never" (no
// callback has the variant, which every run passes) are run by the
// request's mutant script against TestValueRuleAgrees and
// TestValueRuleElides, since TestValueRuleMutants counts only a changed
// output.

// okSubject is the Option parameter of a callback's comma-ok variant and
// the Go names of the value and the flag that stand for it.
type okSubject struct {
	v       *check.Var
	val, ok string
}

// isOkSubject reports whether the match s is on fc's comma-ok parameter.
func (fc *funcCtx) isOkSubject(s *syntax.TypeSwitchStmt) bool {
	id, ok := unparen(s.Subjects[0].X).(*syntax.Ident)
	return ok && fc.okSubject != nil && fc.l.info.Uses[id] == fc.okSubject.v
}

// okShape is g's Option parameter when g has the comma-ok variant above:
// a package function with one parameter, an Option, and one result, no
// type parameter, an empty error row, no Task result and no witness,
// whose body is one match on the parameter, used nowhere else, that
// getMatch lowers. Else nil.
func (l *lowerer) okShape(g *check.Func) *check.Var {
	if updateLowering != "" || getLowering == "switch" || !valueOwned() || valueMutant == "update-ok-never" {
		return nil
	}
	if g == nil || g.Decl == nil || g.Decl.Body == nil || g.Decl.Extern != nil || g.Pkg == nil || g.Pkg.Info == nil || g.Sig == nil {
		return nil
	}
	if len(g.Sig.TParams) > 0 || len(g.Sig.Params) != 1 || len(g.Sig.Results) != 1 || len(rowElems(g.Sig.Errors)) > 0 || g.Pkg.Info.TaskResults[g] || len(witnessParams(g.Sig)) > 0 {
		return nil
	}
	if !isPreludeType(g.Sig.Params[0].Type, "Option") || g.Decl.Type == nil || g.Decl.Type.Params == nil {
		return nil
	}
	var param *check.Var
	for _, field := range g.Decl.Type.Params.List {
		for _, id := range field.Names {
			param, _ = g.Pkg.Info.Defs[id].(*check.Var)
		}
	}
	if param == nil {
		return nil
	}
	if valueMutant == "update-ok-any" {
		return param
	}
	list := g.Decl.Body.List
	if len(list) != 1 {
		return nil
	}
	s, ok := list[0].(*syntax.TypeSwitchStmt)
	if !ok || len(s.Subjects) != 1 {
		return nil
	}
	if id, ok := unparen(s.Subjects[0].X).(*syntax.Ident); !ok || g.Pkg.Info.Uses[id] != param {
		return nil
	}
	uses := 0
	syntax.Inspect(g.Decl.Body, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Ident); ok && g.Pkg.Info.Uses[id] == param {
			uses++
		}
		return true
	})
	if uses != 1 {
		return nil
	}
	if _, _, ok := getArmsOf(g.Pkg.Info, s.Clauses); !ok {
		return nil
	}
	return param
}

// okKey is the package-scope key of g's comma-ok variant.
func okKey(g *check.Func) pkgKey { return pkgKey{"okvariant", g} }

// reserveOk gives, in the naming pass, the name of the comma-ok variant of
// each function of pkgs that has one.
func (l *lowerer) reserveOk(pkgs []*check.Package) {
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if l.okShape(f) != nil {
				l.pkgName(okKey(f), f.Name+"Ok")
			}
		}
	}
}

// okVariant is the Go name of g's comma-ok variant, queued for emission
// once (emitFuncBody with variant set), or "" when g has none.
func (l *lowerer) okVariant(g *check.Func, gd *funcDecl) string {
	if gd.ok != "" {
		return gd.ok
	}
	if l.okShape(g) == nil {
		return ""
	}
	gd.ok = l.pkgName(okKey(g), g.Name+"Ok")
	l.pending = append(l.pending, func() { l.emitFuncBody(gd, true) })
	return gd.ok
}

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
	if name := fc.l.okVariant(g, gd); name != "" {
		// The callback's comma-ok variant: no Option.
		v, ok := fc.temp("v"), fc.temp("ok")
		fc.line(v + ", " + ok + " := " + m + "[" + k + "]")
		if valueMutant == "update-ok-none" {
			fc.line("_ = " + ok)
			ok = "false"
		}
		args := []string{v, ok}
		if !gd.pure {
			args = append([]string{fc.envExpr(x)}, args...)
		}
		call := name + "(" + strings.Join(args, ", ") + ")"
		if valueMutant == "update-no-write" {
			fc.line("_ = " + call)
			return m, true
		}
		fc.line(m + "[" + k + "] = " + call)
		return m, true
	}
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
