// A Go package variable read on the IR (IR-GOCALLS-2): sql.ErrNoRows,
// io.EOF, os.Args, os.Stdout. The decoder's "global" names a variable of
// the program's own package only, so the read is a call of a getter the
// lowerer declares once beside the copy functions (cloneFunc): its body
// is the emitter's selector form (expr.go), and Go inlines it, so each
// read loads the variable as the emitter's does.

package lower

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// hostVarPath is the Go package path of x, a selector pkg.Name of an
// import go package's variable, or "" for any other selector.
func hostVarPath(info *check.Info, x *syntax.SelectorExpr) string {
	id, ok := x.X.(*syntax.Ident)
	if !ok {
		return ""
	}
	pn, ok := info.Uses[id].(*check.PkgName)
	if !ok || pn.Pkg == nil || !pn.Pkg.Host() {
		return ""
	}
	if _, ok := check.QualifiedMember(info, x).(*check.Var); !ok {
		return ""
	}
	return pn.Pkg.Path
}

// goVar is x, a read of the Go package variable o of path, as the
// emitter's selector reads it:
//   - a go.Error is the go.Error that keeps Go's error (GoErrorFrom, IG5);
//   - an Option is Go's pointer read once, then goResult's Option
//     (hostResult);
//   - any other type is Go's value in the binding's type (a constant's
//     default type would differ: math.MaxInt64 as int).
func (b *irBuilder) goVar(o *check.Var, path string, x *syntax.SelectorExpr, p irPos) ([]*irStmt, *irExpr) {
	raw := b.optionResult(o.Type)
	if raw {
		if _, ref := check.GoRefElem(optionElem(o.Type)); !ref {
			irNo("Go package variable of an Option of another type")
		}
	}
	name := b.l.goVarGetter(o, path, raw, x)
	call := &irExpr{K: "call", Pos: p, Name: name}
	if !raw {
		b.goType(o.Type, x)
		return nil, call
	}
	r := b.temp("borrowed", b.rawGoType(o.Type, x), p)
	pre := []*irStmt{{K: "def", Pos: p, Local: r, X: call}}
	vpre, v := b.hostResult(path+"."+o.Name, o.Type, r, -1, x, p) // ir_golib.go: the Option
	return append(pre, vpre...), v
}

// hostField is x, a field of a Go struct read through its go.Ref or its
// value (IR-GOCALLS-3), as the emitter's selector reads it: Go's field,
// then goResult's conversion with no target name (hostResult): a go.Ref
// checked for nil, an Option built, a slice or Map copied. ok is false
// when x's operand is no Go type.
func (b *irBuilder) hostField(x *syntax.SelectorExpr, p irPos) ([]*irStmt, *irExpr, bool) {
	if !hostRecv(b.l.info.Types[x.X]) {
		return nil, nil, false
	}
	pre, recv := b.value(x.X)
	t := b.l.info.Types[x]
	b.goType(t, x)
	f := &irExpr{K: "field", Pos: b.pos(x.Sel, x.Sel.Pos()), Name: x.Sel.Name, X: recv}
	if !isGoRef(t) && !b.optionResult(t) {
		return pre, b.fieldValue(t, f, x), true
	}
	r := b.temp("f", b.rawGoType(t, x), p)
	pre = append(pre, &irStmt{K: "def", Pos: p, Local: r, X: f})
	vpre, v := b.hostResult("", t, r, -1, x, p) // ir_golib.go
	return append(pre, vpre...), v, true
}

// fieldValue is f, Go's field of type t read at x, under goResult's copy
// of a slice or Map (goResultCopy; no Go target makes it new).
func (b *irBuilder) fieldValue(t check.Type, f *irExpr, x syntax.Node) *irExpr {
	if b.l.holdsValue(t) {
		return b.goResultCopy("", t, f, x) // ir_golib.go
	}
	return f
}

// goVarKey is the naming key of the getter of the Go variable o.
func goVarKey(o *check.Var) pkgKey { return pkgKey{"go var", o} }

// goVarGetter is the name of the getter of o, written once: Go's pointer
// when raw (an Option's), else the emitter's value of the selector.
func (l *lowerer) goVarGetter(o *check.Var, path string, raw bool, site syntax.Node) string {
	key := goVarKey(o)
	name := l.pkgName(key, "ergoGo"+o.Name)
	if l.goVars[key] {
		return name
	}
	if l.goVars == nil {
		l.goVars = map[pkgKey]bool{}
	}
	l.goVars[key] = true
	tc := &typeCtx{l: l, site: site}
	goName := l.hostPkg(path, site) + "." + o.Name
	var goType, val string
	switch n, _ := o.Type.(*check.Named); {
	case raw:
		goType, val = tc.goType(optionElem(o.Type)), goName
	case n != nil && check.IsGoError(n):
		goType, val = tc.goType(o.Type), l.goErrorFrom()+"("+goName+")"
	default:
		goType = tc.goType(o.Type)
		val = "(" + goType + ")(" + goName + ")"
	}
	l.gen.WriteString("func " + name + "() " + goType + " {\n\treturn " + val + "\n}\n\n")
	return name
}

// reserveGoVars gives, in the naming pass, the key and preferred name of
// the getter of each Go package variable the packages read (goVarGetter).
func reserveGoVars(pkgs []*check.Package, reserve func(key pkgKey, sort, base string)) {
	for _, p := range pkgs {
		for x := range p.Info.Types {
			sel, ok := x.(*syntax.SelectorExpr)
			if !ok {
				continue
			}
			if path := hostVarPath(p.Info, sel); path != "" {
				o := check.QualifiedMember(p.Info, sel).(*check.Var)
				reserve(goVarKey(o), "go var "+path+"."+o.Name, "ergoGo"+o.Name)
			}
		}
	}
}
