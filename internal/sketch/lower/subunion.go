package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Sub-unions (UNION-FLAT; types.md, Unions 1, 2 and 4). A union may list
// a union of its package, so a value of one declared union may be a value
// of another: widened into a union that holds each of its variants
// (check.ConvWiden), or narrowed by `case A as a` to a union whose
// variants the subject all holds. Both are unionInto: the variant stays
// the same, and only its Go form changes.
//
// Two sealed interfaces need no switch: every variant implements both
// markers, so a Go type assertion always holds. A flat union on either
// side is a tag switch over the variants the two unions share, each arm
// narrowing the variant (flatNarrow) and building it in the target
// (flatValue), as the hand rebuild `match a { case A1 as v: v … }`
// compiles to; Go inlines the literal. Prior art: ReScript's `:>` from a
// variant spread is free when the representations agree, which listing
// order gives for interfaces; OCaml's `#a` pattern tests a polymorphic
// variant's tags.

// unionInto is val, a value of the declared union from, as a value of the
// declared union to, at site: its variant is one to holds (a widening, or
// a narrowing a test proved). val is evaluated once.
func (fc *funcCtx) unionInto(from, to check.Type, val string, site syntax.Node) string {
	ff, tf := fc.l.flatUnionOf(from), fc.l.flatUnionOf(to)
	for _, fu := range []*flatUnion{ff, tf} {
		if fu != nil && (fu.niche || fu.generic) {
			flatBail(site, fu) // a generic or niche form: an interface for now
		}
	}
	self := fc.goType(to)
	if ff == nil && tf == nil {
		if fc.goType(from) == self {
			return val
		}
		return val + ".(" + self + ")"
	}
	into := func(vt *check.Named, rec string) string {
		if tf == nil {
			return rec // a record into its sealed interface
		}
		k := tf.tagOf(vt)
		if k < 0 {
			ice(site, "a variant %s that %s does not hold", vt, to)
		}
		return tf.flatValue(&fc.typeCtx, self, k, rec)
	}
	x := fc.scope.alloc("x")
	var b strings.Builder
	b.WriteString("func(" + x + " " + fc.goType(from) + ") " + self + " {\n")
	if ff != nil {
		b.WriteString("switch " + ff.tagExpr(&fc.typeCtx, x) + " {\n")
		for k, vt := range ff.variants {
			if subUnionHolds(to, vt) {
				b.WriteString("case " + strconv.Itoa(k) + ":\nreturn " + into(vt, ff.flatNarrow(&fc.typeCtx, x, k)) + "\n")
			}
		}
	} else {
		v := fc.scope.alloc("v")
		b.WriteString("switch " + v + " := " + x + ".(type) {\n")
		for _, vt := range unionVariants(from) {
			if subUnionHolds(to, vt) {
				b.WriteString("case " + fc.goType(vt) + ":\nreturn " + into(vt, v) + "\n")
			}
		}
	}
	b.WriteString("}\npanic(\"ergo: a sub-union value of another variant\")\n}(" + val + ")")
	return b.String()
}

// subUnionTests is the Go test that val, of the union t, holds one of the
// variants of the sub-union want: an or of the tag tests (t flat) or of
// the type tests (t an interface).
func (fc *funcCtx) subUnionTests(want, t check.Type, val string, site syntax.Node) string {
	fu := fc.l.flatUnionOf(t)
	if fu != nil && (fu.niche || fu.generic) {
		flatBail(site, fu)
	}
	var tests []string
	for _, vt := range unionVariants(want) {
		if fu != nil {
			k := fu.tagOf(vt)
			if k < 0 {
				ice(site, "a variant %s of %s that %s does not hold", vt, want, t)
			}
			tests = append(tests, fu.test(val, k))
			continue
		}
		tests = append(tests, fc.l.isHelper()+"["+fc.goType(vt)+"]("+val+")")
	}
	return "(" + strings.Join(tests, " || ") + ")"
}

// unionVariants is the variants of the declared union t, in its order.
func unionVariants(t check.Type) []*check.Named {
	u, ok := underlying(t).(*check.Union)
	if !ok {
		return nil
	}
	out := make([]*check.Named, 0, len(u.Variants))
	for _, v := range u.Variants {
		if n, ok := v.(*check.Named); ok {
			out = append(out, n)
		}
	}
	return out
}

// subUnionHolds reports whether the declared union u holds the variant v.
func subUnionHolds(u check.Type, v *check.Named) bool {
	for _, w := range unionVariants(u) {
		if w.Obj == v.Obj {
			return true
		}
	}
	return false
}
