package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// dropRelease is the callback for OwningParts. The owner supplies the
// mask of fields taken in place; nested values have no separate mask.
func (b *irBuilder) dropRelease(d *check.Drop, typ string, site syntax.Node, p irPos) *irExpr {
	g := irDrop{b: b, site: site, p: p}
	val := b.temp("owned", typ, p)
	taken := b.temp("taken", "uint64", p)
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: g.local(b.env)}
	b.keepImport("var _ = " + b.l.rt() + ".NewDrop")
	drop := &irExpr{K: "pcall", Pos: p, T: runtimeImport, Name: "NewDrop", Args: []*irExpr{task}}
	return &irExpr{K: "func", Pos: p, T: "func(" + typ + ", uint64)", Params: []int{val, taken}, End: &p,
		Body: g.body(drop, d, g.local(val), g.local(taken))}
}

// takeField mirrors the checked move in release.go. The mask belongs
// to the owning entry, not the record value, and updates before the
// consuming expression runs.
func (b *irBuilder) takeField(x *syntax.SelectorExpr, p irPos) {
	f, taken := b.l.info.FieldTakes[x]
	if !taken {
		return
	}
	if b.own == nil || !b.own.list {
		irNo("owned local")
	}
	h, known := b.own.handle[f.Var]
	if !known {
		irNo("owned local")
	}
	b.runFirst([]*irStmt{{K: "expr", Pos: p, X: &irExpr{
		K: "mcall", Pos: p, Name: "TakeField", X: &irExpr{K: "local", Pos: p, I: h},
		Args: []*irExpr{{K: "const", Pos: p, T: "int", V: strconv.Itoa(f.Index)}},
	}}})
}

// irDrop writes the checker's Drop graph. One runtime Drop settles all
// leaf Close calls, so a defect never suppresses a later release.
type irDrop struct {
	b    *irBuilder
	site syntax.Node
	p    irPos
	drop int
	defs []*irStmt
	work []*irStmt
	// A function for each distinct nested structural drop gives recursive
	// types a back edge, not infinite expansion. Declare before assigning
	// any of them so mutually recursive closures can refer to each other.
	nested map[*check.Drop]int
}

func (g *irDrop) local(i int) *irExpr {
	return &irExpr{K: "local", Pos: g.p, I: i}
}

func (g *irDrop) expr(x *irExpr) *irStmt {
	return &irStmt{K: "expr", Pos: g.p, X: x}
}

func (g *irDrop) method(name string, recv *irExpr, args ...*irExpr) *irExpr {
	return &irExpr{K: "mcall", Pos: g.p, Name: name, X: recv, Args: args}
}

func (g *irDrop) body(ctor *irExpr, d *check.Drop, x, taken *irExpr) []*irStmt {
	g.drop = g.b.temp("drop", "*"+g.b.spelling(g.b.l.rt()+".Drop"), g.p)
	g.nested = map[*check.Drop]int{}
	top := g.parts(d, x, taken)
	out := append([]*irStmt{{K: "def", Pos: g.p, Local: g.drop, X: ctor}}, g.defs...)
	out = append(out, g.work...)
	out = append(out, top...)
	return append(out, g.expr(g.method("End", g.local(g.drop))))
}

func (g *irDrop) part(d *check.Drop, x *irExpr) *irStmt {
	b, p := g.b, g.p
	typ := b.goType(d.Type, g.site)
	v := b.temp("value", typ, p)
	ptr := b.temp("ptr", "*"+typ, p)
	call, ok := b.closeCall(irSlot{t: d.Type, d: d, site: g.site}, b.env, ptr, p)
	if !ok {
		irNo("owned local") // the existing list-release Close refusal
	}
	lit := &irExpr{K: "func", Pos: p, T: "func()", End: &p, Body: []*irStmt{
		{K: "def", Pos: p, Local: v, X: x},
		{K: "def", Pos: p, Local: ptr, X: b.addrOf(v, p)},
		g.expr(call),
	}}
	return g.expr(g.method("Part", g.local(g.drop), lit))
}

func (g *irDrop) release(d *check.Drop, x *irExpr) *irStmt {
	if !d.Glue() {
		return g.part(d, x)
	}
	p := g.p
	f, found := g.nested[d]
	if !found {
		typ := g.b.goType(d.Type, g.site)
		sig := "func(" + typ + ")"
		f = g.b.temp("release", sig, p)
		g.nested[d] = f
		g.defs = append(g.defs, &irStmt{K: "var", Pos: p, Local: f})
		v := g.b.temp("v", typ, p)
		body := g.parts(d, g.local(v), nil)
		g.work = append(g.work, &irStmt{K: "set", Pos: p, Local: f, X: &irExpr{
			K: "func", Pos: p, T: sig, Params: []int{v}, End: &p, Body: body,
		}})
	}
	return g.expr(&irExpr{K: "vcall", Pos: p, X: g.local(f), Args: []*irExpr{x}})
}

func (g *irDrop) parts(d *check.Drop, x, taken *irExpr) []*irStmt {
	p, b := g.p, g.b
	var out []*irStmt
	if d.Close != nil {
		out = append(out, g.part(d, x))
	}
	if len(d.Fields) > 0 {
		// Use the same field names as the source emitter, including tuples
		// and defined types whose record lives under another named type.
		names := (&glueGen{fc: b.fc, site: g.site}).fieldNames(d.Type)
		for _, f := range d.Fields {
			s := g.release(f.Drop, &irExpr{K: "field", Pos: p, Name: names(f), X: x})
			if taken != nil && f.Index < 64 {
				bit := &irExpr{K: "bin", Pos: p, Op: "&", X: taken, Y: b.bitConst(f.Index, p)}
				test := &irExpr{K: "bin", Pos: p, Op: "==", X: bit, Y: &irExpr{K: "const", Pos: p, T: "uint64", V: "0"}}
				s = &irStmt{K: "if", Pos: p, End: p, X: test, Then: []*irStmt{s}}
			}
			out = append(out, s)
		}
	}
	if d.Elem != nil {
		switch u := underOf(d.Type).(type) {
		case *check.Slice:
			out = append(out, g.reverse(d.Elem, x, &irExpr{K: "len", Pos: p, X: x})...)
		case *check.Array:
			out = append(out, g.reverse(d.Elem, x, &irExpr{K: "const", Pos: p, T: "int", V: strconv.FormatInt(u.Len, 10)})...)
		case *check.Map:
			b.l.intMapConflict(d.Type)
			e := b.temp("element", b.goType(d.Elem.Type, g.site), p)
			out = append(out, &irStmt{K: "range", Pos: p, End: p, X: x, Val: &e, Body: []*irStmt{g.release(d.Elem, g.local(e))}})
		}
	}
	if d.Drain != nil {
		call := g.drain(d, x)
		left := b.temp("left", "[]"+b.goType(d.Drain.Type, g.site), p)
		out = append(out, &irStmt{K: "def", Pos: p, Local: left, X: call})
		out = append(out, g.reverse(d.Drain, g.local(left), &irExpr{K: "len", Pos: p, X: g.local(left)})...)
	}
	if len(d.Variants) > 0 {
		out = append(out, g.variants(d, x))
	}
	return out
}

func (g *irDrop) reverse(d *check.Drop, x, length *irExpr) []*irStmt {
	p := g.p
	i := g.b.temp("remaining", "int", p)
	zero := &irExpr{K: "const", Pos: p, T: "int", V: "0"}
	one := &irExpr{K: "const", Pos: p, T: "int", V: "1"}
	return []*irStmt{
		{K: "def", Pos: p, Local: i, X: length},
		{K: "while", Pos: p, End: p, X: &irExpr{K: "bin", Pos: p, Op: ">", X: g.local(i), Y: zero}, Body: []*irStmt{
			{K: "set", Pos: p, Local: i, X: &irExpr{K: "bin", Pos: p, Op: "-", X: g.local(i), Y: one}},
			g.release(d, &irExpr{K: "index", Pos: p, X: x, Y: g.local(i)}),
		}},
	}
}

func (g *irDrop) variants(d *check.Drop, x *irExpr) *irStmt {
	p, b := g.p, g.b
	sw := &irStmt{K: "tswitch", Pos: p, End: p, X: x}
	fu := b.flatOf(d.Type)
	if fu != nil {
		sw.K = "switch"
		sw.X = b.flatTag(fu, x.I, p, g.site)
	}
	for _, vd := range d.Variants {
		c := &irCase{Pos: p}
		t := b.goType(vd.Type, g.site)
		v := &irExpr{K: "assert", Pos: p, T: t, X: x}
		if fu == nil {
			c.Types = []string{t}
		} else {
			tag := fu.tagOf(vd.Type)
			if tag < 0 {
				irNo("owned local")
			}
			c.Vals = []*irExpr{{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(tag)}}
			local := x.I // compound drops have a bound parameter as the subject
			v = b.narrow(fu, local, tag, p, g.site)
		}
		c.Body = []*irStmt{g.release(vd, v)}
		sw.Cases = append(sw.Cases, c)
	}
	return sw
}

// drain dispatches the operation named by the Drop graph, for any type
// which keeps values. Runtime bindings use their checked ABI; userland
// operations take the task like any other operation call.
func (g *irDrop) drain(d *check.Drop, x *irExpr) *irExpr {
	b, p := g.b, g.p
	op := check.DrainOp(d.Type)
	if op == nil {
		ice(g.site, "a drain of %s, which declares no drain operation", d.Type)
	}
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: g.local(b.env)}
	if n, ok := d.Type.(*check.Named); ok && b.l.runtimeType(n, g.site) != "" {
		method := strings.ToUpper(op.Name[:1]) + op.Name[1:]
		name := check.RuntimeTypeName(n) + "." + method
		rb, err := runtimeBody(name)
		if err != nil || len(rb.Params) != 0 || len(rb.Results) != 1 {
			irNo("owned local")
		}
		switch {
		case len(rb.Hidden) == 0:
			return g.method(method, x)
		case len(rb.Hidden) == 1 && rb.Hidden[0] == v1.CallerTask:
			return g.method(method, x, task)
		default:
			irNo("owned local")
		}
	}
	return g.method(memberName(op.Name), x, task)
}
