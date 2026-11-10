package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/std"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Builders (check/builders.go). A generator draws a value of an operation
// type by its builder: the call of the builder with generated arguments,
// converted to the operation type. A builder with no parameters builds the
// case's world: $Rand.built keeps its one value per case, and the runner
// starts each case with an empty world ($Rand.start).

// isOps reports whether n is an operation type.
func isOps(n *check.Named) bool {
	_, ok := n.Underlying().(*check.Interface)
	return ok
}

// opBuilder is the builder of n when n is an operation type, else nil. The
// checker admits a generator of an operation type only with a builder.
func (l *lowerer) opBuilder(n *check.Named, site syntax.Node) *check.Builder {
	if _, isOps := n.Underlying().(*check.Interface); !isOps {
		return nil
	}
	b := l.pkg.Builder(n)
	if b == nil {
		ice(site, "a generator of the operation type %s, which has no builder; the checker admits only generatable types", n)
	}
	return b
}

// stdBuilt reports whether b is a std extern with a runtime/v1 body
// (sync.NewRef), which a generator calls with no environment.
func (l *lowerer) stdBuilt(b *check.Builder, site syntax.Node) bool {
	_, _, isStd := l.stdBody(l.useFunc(b.Func, site))
	return isStd
}

// genBuilt writes the body of n's generator that builds by b. ev is the
// runner's environment the builder runs in, which genFunc binds when b is
// not stdBuilt: a builder with no rows may still read the task (fu6a).
func (l *lowerer) genBuilt(n *check.Named, b *check.Builder, r, d, ev string, scope *namer, site syntax.Node, body *strings.Builder) {
	fc := newFuncCtx(l, scope, false)
	fc.site = site
	f := b.Func
	targs, params := b.Instance(n)
	stdFn, stdName, isStd := l.stdBody(l.useFunc(f, site))
	var vals []string
	for i, p := range params {
		a := scope.alloc(f.Sig.Params[i].Name)
		body.WriteString("\t" + a + " := " + l.genExpr(p, r, d+"-1", scope, site) + "\n")
		vals = append(vals, a)
	}
	result := f.Sig.Results[0]
	if len(targs) > 0 {
		result = n
	}
	var call string
	if isStd {
		// A std extern (sync.NewRef) is called as its runtime/v1 body is.
		call = fc.stdResult(stdFn, result, "", site, func(env string) string {
			return fc.runtimeCall(stdName, stdFn, fc.stdTypeArgs(stdFn, f.Sig, targs, site), "", vals, env, nil, nil, site)
		})
	} else {
		var inst *check.Instance
		if len(targs) > 0 {
			inst = &check.Instance{Func: f, TypeArgs: targs}
		}
		v := scope.alloc("v")
		call = "func() " + fc.goType(result) + " { " + v + ", _ := " + fc.funcValue(f, f.Sig, site, inst) + "(" + strings.Join(append([]string{ev}, vals...), ", ") + "); return " + v + " }()"
	}
	if b.World() {
		// The case's one value of the builder (rule 4).
		key := strconv.Quote(f.Pkg.Path + "." + f.Name)
		call = r + ".built(" + key + ", func() any { return " + call + " }).(" + fc.goType(result) + ")"
	}
	body.WriteString("\treturn " + fc.convert(b.Conv, call, site) + "\n")
}

// builtText is the Ergo text of a value of n built by b: the builder's
// call, `fstest.NewMemory()`, with `…` for generated arguments.
func (l *lowerer) builtText(n *check.Named, b *check.Builder) string {
	f := b.Func
	text := f.Name
	if p := f.Pkg; p != nil && p != l.pkg && p.Path != std.PreludePath {
		text = l.importName(p) + "." + text
	}
	targs, _ := b.Instance(n)
	if len(targs) > 0 {
		parts := make([]string, len(targs))
		for i, a := range targs {
			parts[i] = l.ergoType(a)
		}
		text += "[" + strings.Join(parts, ", ") + "]"
	}
	if b.World() {
		return text + "()"
	}
	return text + "(…)"
}
