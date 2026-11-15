package lower

// IR builder: interpolated strings. See ir.go.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// text is the interpolated string x: its literal segments and the text of
// each value, joined by +, as the emitter's interpolated writes it. A
// value's text is the emitter's rule (funcCtx.text): a string itself, a
// duration its String, a bool strconv.FormatBool, an integer FormatInt or
// FormatUint, a float FormatFloat(v, 'g', -1, bits). The emitter appends a
// number into a stack buffer when the string holds a literal segment;
// FormatInt writes the same text (one allocation more).
func (b *irBuilder) text(x *syntax.InterpolatedString, p irPos) *irExpr {
	if len(x.Texts) != len(x.Exprs)+1 {
		irNo("interpolated string form")
	}
	var parts []*irExpr
	for i, seg := range x.Texts {
		if s := b.fc.segment(seg, i == 0, i == len(x.Texts)-1); s != "" {
			parts = append(parts, &irExpr{K: "const", Pos: b.pos(seg, seg.Pos()), T: "string", V: s})
		}
		if i < len(x.Exprs) {
			parts = append(parts, b.textOf(x.Exprs[i]))
		}
	}
	if len(parts) == 0 {
		return &irExpr{K: "const", Pos: p, T: "string", V: ""}
	}
	out := parts[0]
	for _, e := range parts[1:] {
		out = &irExpr{K: "bin", Pos: p, Op: "+", X: out, Y: e}
	}
	return out
}

// textOf is the text of the interpolated value x.
func (b *irBuilder) textOf(x syntax.Expr) *irExpr {
	t := b.l.info.Types[x]
	b.goType(t, x)
	// A named scalar's text is its basic type's, through a Go conversion
	// (ir_union.go's scalarBasic; the emitter's text, as).
	bt := b.scalarBasic(t)
	if bt == nil {
		irNo("interpolated value of type %s", t)
	}
	named := basicOf(t) == nil
	if named && bt.Kind == check.Duration {
		irNo("interpolated named duration")
	}
	v := b.expr(x)
	p := v.Pos
	as := func(goType string) *irExpr {
		if !named && bt.Name == goType {
			return v
		}
		return &irExpr{K: "conv", Pos: p, T: goType, X: v}
	}
	strconvCall := func(name string, args ...*irExpr) *irExpr {
		sc := b.l.strconvPkg()
		b.keepImport("var _ = " + sc + ".Itoa")
		return &irExpr{K: "pcall", Pos: p, T: "strconv", Name: name, Args: args}
	}
	ten := &irExpr{K: "const", Pos: p, T: "int", V: "10"}
	switch {
	case bt.Kind == check.String:
		return as("string")
	case bt.Kind == check.Duration:
		// Duration's String is its %v text (1h0m0s).
		return &irExpr{K: "mcall", Pos: p, Name: "String", X: v}
	case bt.Kind == check.Bool:
		return strconvCall("FormatBool", as("bool"))
	case bt.Kind >= check.Int && bt.Kind <= check.Int64:
		return strconvCall("FormatInt", as("int64"), ten)
	case bt.Kind >= check.Uint && bt.Kind <= check.Uint64:
		return strconvCall("FormatUint", as("uint64"), ten)
	case bt.Kind == check.Float32, bt.Kind == check.Float64:
		bits := "64"
		if bt.Kind == check.Float32 {
			bits = "32"
		}
		g := &irExpr{K: "const", Pos: p, T: "uint8", V: "103"} // 'g'
		prec := &irExpr{K: "const", Pos: p, T: "int", V: "-1"}
		return strconvCall("FormatFloat", as("float64"), g, prec, &irExpr{K: "const", Pos: p, T: "int", V: bits})
	}
	irNo("interpolated value of type %s", t)
	return nil
}
