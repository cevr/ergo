package lower

// IR builder: templates (CF2). A call of a function whose row is empty,
// with an Ergo body, whose arguments are partly constants (it holds a
// call CF1's fold would compute) is computed at build time with the parts
// only the run knows opaque: its holes. The holes are first the largest
// subexpressions that read a local, under the calls and the literals of
// the call; the evaluator runs the call with each hole a symbol it only
// moves (eval.FoldTemplate), and a hole the run reads widens to the
// argument or element around it, until the run ends in a value or no
// hole is left to widen. The value is a constant with holes: the IR
// evaluates the holes in their source order, once each, then builds the
// constant around them. std html's page is such a call: its value is one
// template node whose parts are its static markup, one string between
// each two holes, and the holes in place (stdlib.md, HTML 4), as Marko's
// and Solid's server compilers and Svelte 5's server output split a
// template's static markup from its expressions.
//
// The switch constTemplateLowering = "off" (-ldflags -X or
// SetConstTemplateLoweringForTest) keeps CF1's form: only calls of
// constants fold. The fold's switch and its Go pin gate templates too.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// constTemplateLowering selects templates: "" builds them, "off" keeps
// CF1's form.
var constTemplateLowering = ""

// SetConstTemplateLoweringForTest selects templates ("" or "off") and
// returns the previous choice.
func SetConstTemplateLoweringForTest(mode string) string {
	prev := constTemplateLowering
	constTemplateLowering = mode
	return prev
}

// templateRounds bounds the runs of one call: each run after the first
// widens a hole it read.
const templateRounds = 24

// template is the call x built as its template and the statements that
// evaluate its holes, or nil when x is no candidate or makes none.
func (b *irBuilder) template(x *syntax.CallExpr) (pre []*irStmt, out *irExpr) {
	if constTemplateLowering == "off" || constFoldLowering == "off" || !foldGoPinned() || b.decl == nil || b.pkg == nil || b.law != nil {
		return nil, nil
	}
	info := b.l.info
	c := info.Calls[x]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || x.Trailing != nil || len(c.TypeArgs) > 0 || c.StartsTask {
		return nil, nil
	}
	if c.Func.Pkg == nil || c.Func.Pkg.Host() || !eval.Rowless(c.Func) {
		return nil, nil
	}
	if d := c.Func.Decl; d == nil || d.Body == nil || d.Extern != nil {
		return nil, nil
	}
	t := info.Types[x]
	if t == nil || b.closed(x) || !b.holdsParts(x) {
		return nil, nil
	}
	parent := map[syntax.Expr]syntax.Expr{}
	holes := b.holesOf(x, parent, nil)
	counts := &b.foldCounts
	for round := 0; round < templateRounds; round++ {
		v, culprits, why, _ := eval.FoldTemplate(b.pkg, b.decl, x, holes, constFoldBudget)
		if why == "" {
			pre, out = b.emitTemplate(v, t, x, holes)
			if out == nil {
				counts.TemplateRefused++
				return nil, nil
			}
			counts.Templates++
			return pre, out
		}
		if len(culprits) == 0 {
			break
		}
		wide := map[syntax.Expr]bool{}
		for i, h := range holes {
			wide[h] = true
			for _, k := range culprits {
				if k == i {
					delete(wide, h)
					w := widen(h, x, parent)
					if w == nil {
						counts.TemplateRefused++
						return nil, nil
					}
					wide[w] = true
				}
			}
		}
		holes = b.holesOf(x, parent, wide)
	}
	counts.TemplateRefused++
	return nil, nil
}

// holdsParts reports whether an argument of the call x is a call or a
// literal: a part a template may compute, where a call of locals alone
// (f(a, b)) has none and runs as written.
func (b *irBuilder) holdsParts(x *syntax.CallExpr) bool {
	for _, a := range x.Args {
		switch unparen(a).(type) {
		case *syntax.CallExpr, *syntax.CompositeLit:
			return true
		}
	}
	return false
}

// holesOf is the holes of the call x in source order, and parent each
// visited expression's: with wide nil, the largest subexpressions that
// read a local, under the calls of a closed function and the literals;
// else the expressions of wide, each the outermost of wide on its path.
func (b *irBuilder) holesOf(x *syntax.CallExpr, parent map[syntax.Expr]syntax.Expr, wide map[syntax.Expr]bool) []syntax.Expr {
	var out []syntax.Expr
	var walk func(e, up syntax.Expr)
	walk = func(e, up syntax.Expr) {
		parent[e] = up
		if wide != nil && wide[e] {
			out = append(out, e)
			return
		}
		if b.closed(e) {
			return
		}
		switch e := e.(type) {
		case *syntax.CallExpr:
			if e.Trailing == nil && b.closed(e.Fun) {
				for _, a := range e.Args {
					walk(a, e)
				}
				return
			}
		case *syntax.CompositeLit:
			if e.Base == nil {
				for _, el := range e.Elts {
					if kv, ok := el.(*syntax.KeyValueExpr); ok {
						el = kv.Value
					}
					walk(el, e)
				}
				return
			}
		}
		if wide == nil {
			out = append(out, e)
		}
	}
	for _, a := range x.Args {
		walk(a, x)
	}
	return out
}

// widen is the argument or element of x around h that holds it, nil
// when only x does.
func widen(h syntax.Expr, x *syntax.CallExpr, parent map[syntax.Expr]syntax.Expr) syntax.Expr {
	for n := parent[h]; n != nil && n != syntax.Expr(x); n = parent[n] {
		switch parent[n].(type) {
		case *syntax.CallExpr, *syntax.CompositeLit:
			return n
		}
	}
	return nil
}

// emitTemplate is the IR of the template v of the call x, of type t: the
// holes' statements and values, each once in source order, then the
// constant around them. nil when the IR cannot spell the constant.
func (b *irBuilder) emitTemplate(v eval.Const, t check.Type, x *syntax.CallExpr, holes []syntax.Expr) (pre []*irStmt, out *irExpr) {
	p := b.pos(x, x.Pos())
	types := make([]check.Type, len(holes))
	for i, h := range holes {
		types[i] = b.l.info.Types[h]
		if conv, ok := b.l.info.Implicit[h]; ok && conv.Kind != 0 && conv.To != nil {
			types[i] = conv.To
		}
	}
	// A dry build first: a constant the IR cannot spell leaves the call
	// as written, before any hole's statement is made. A hole's own calls
	// may be templates: each sets the holes it builds around and restores
	// the ones before it.
	prevHoles, prevTypes := b.tmplHoles, b.tmplTypes
	defer func() { b.tmplHoles, b.tmplTypes = prevHoles, prevTypes }()
	dry := make([]*irExpr, len(holes))
	for i := range holes {
		dry[i] = &irExpr{K: "local", Pos: p, I: -1}
	}
	b.tmplHoles, b.tmplTypes = dry, types
	if !b.spells(v, t, x, p) {
		return nil, nil
	}
	pre, vals := b.operands(holes)
	for i, e := range vals {
		if e.K != "const" && e.K != "local" {
			// Each hole once, here, in its order: the constant may
			// place it anywhere, or twice, or not at all.
			l := b.temp("hole", b.goType(types[i], holes[i]), e.Pos)
			pre = append(pre, &irStmt{K: "def", Pos: e.Pos, Local: l, X: e})
			vals[i] = &irExpr{K: "local", Pos: e.Pos, I: l}
		}
	}
	b.tmplHoles, b.tmplTypes = vals, types
	return pre, b.constOf(v, t, x, p)
}

// spells reports whether constOf builds v.
func (b *irBuilder) spells(v eval.Const, t check.Type, x syntax.Expr, p irPos) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, isNo := r.(irUnsupported); !isNo {
				panic(r)
			}
			ok = false
		}
	}()
	b.constOf(v, t, x, p)
	return true
}

// holeInto is the template's hole i in a place of type t.
func (b *irBuilder) holeInto(i int, t check.Type, site syntax.Expr) *irExpr {
	if i < 0 || i >= len(b.tmplHoles) {
		irNo("template hole out of range")
	}
	e, from := b.tmplHoles[i], b.tmplTypes[i]
	if b.goType(from, site) == b.goType(t, site) {
		return e
	}
	if n, ok := t.(*check.Named); ok {
		if _, isUnion := n.Underlying().(*check.Union); isUnion {
			return b.variantInto(from, t, e, site)
		}
	}
	irNo("template hole of type %s in a place of type %s", from, t)
	return nil
}
