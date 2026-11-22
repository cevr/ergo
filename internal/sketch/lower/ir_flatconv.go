package lower

// IR builder: conversions into or out of a flat union (MSB3) that are no
// Go assertion (the emitter's convert for ConvRow and its unionInto for
// ConvWiden, subunion.go, whose rule this is). The value goes into a
// local; a switch on what it holds puts each variant the target holds
// into the target's form (variantInto). The IR writes the switch as
// statements before the expression (runFirst, ir_order.go) where the
// emitter calls a literal in place: the statements need no closure.
//
// The switch has no default of its own: the checker admits the
// conversion only when the target holds every variant the value may hold
// (a row's error types, a sub-union's variants), so the last variant is
// the default. A flat value's tag is one of its variants'; an interface
// value or a row's error that is none of them (a nil interface, which no
// Ergo value is) fails the last case's Go assertion, Go's own defect,
// where the emitter's literal panics with its text.
//
// Prior art: Rust's From between enums and OCaml's polymorphic variant
// coercion (`(x :> t)`) are each a match that rebuilds the variant; a
// flat union has no shared tag space, so its conversion is the same
// match, not a reinterpretation.

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// flatInto is e, a value of conv.From, as one of conv.To, when either is
// a flat union. A row's value (ConvRow) is a Go error, tested by its
// error variants' Go types; a sub-union's (ConvWiden) is its tag when it
// is flat, else its Go type.
func (b *irBuilder) flatInto(conv check.Conversion, e *irExpr, x syntax.Node) *irExpr {
	row := conv.Kind == check.ConvRow
	ff, tf := b.flatOf(conv.From), b.flatOf(conv.To)
	if row {
		ff = nil
	}
	for _, fu := range []*flatUnion{ff, tf} {
		if fu != nil && (fu.niche || fu.generic) {
			// The emitter bails too (flatBail): an interface for now.
			if row {
				irNo("row into a flat union")
			}
			irNo("sub-union into a flat union")
		}
	}
	p := e.Pos
	gt := b.goType(conv.To, x)
	gf := "error"
	if !row {
		gf = b.goType(conv.From, x)
	}
	s := b.temp("x", gf, p)
	r := b.temp("v", gt, p)
	into := func(vt *check.Named, v *irExpr) []*irStmt {
		return []*irStmt{{K: "set", Pos: p, Local: r, X: b.variantInto(vt, conv.To, v, x)}}
	}
	var sw *irStmt
	if ff != nil {
		sw = &irStmt{K: "switch", Pos: p, End: p, ElsePos: p, X: b.flatTag(ff, s, p, x)}
		for k, vt := range ff.variants {
			if subUnionHolds(conv.To, vt) {
				sw.Cases = append(sw.Cases, &irCase{Pos: p, Vals: []*irExpr{{K: "const", Pos: p, T: "uint8", V: strconv.Itoa(k)}}, Body: into(vt, b.narrow(ff, s, k, p, x))})
			}
		}
	} else {
		sw = &irStmt{K: "tswitch", Pos: p, End: p, ElsePos: p, X: &irExpr{K: "local", Pos: p, I: s}}
		var vs []*check.Named
		if row {
			for _, vt := range tf.variants {
				if b.l.errorType(vt) {
					vs = append(vs, vt)
				}
			}
		} else {
			for _, vt := range unionVariants(conv.From) {
				if subUnionHolds(conv.To, vt) {
					vs = append(vs, vt)
				}
			}
		}
		for _, vt := range vs {
			gv := b.goType(vt, x)
			sw.Cases = append(sw.Cases, &irCase{Pos: p, Types: []string{gv}, Body: into(vt, &irExpr{K: "assert", Pos: p, T: gv, X: &irExpr{K: "local", Pos: p, I: s}})})
		}
	}
	if len(sw.Cases) == 0 {
		irNo("conversion of a union with no variant") // the checker admits none
	}
	// The last variant is the default (above): its case's body, whose
	// assertion is the defect of a value that is none of them.
	last := sw.Cases[len(sw.Cases)-1]
	sw.Cases, sw.Else = sw.Cases[:len(sw.Cases)-1], last.Body
	b.runFirst([]*irStmt{{K: "def", Pos: p, Local: s, X: e}, {K: "var", Pos: p, Local: r}, sw})
	return &irExpr{K: "local", Pos: p, I: r}
}
