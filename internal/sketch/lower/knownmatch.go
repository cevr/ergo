package lower

import (
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Case of the known constructor across an inlined call (VI2). A simple
// match (type tests, a wildcard, `V as x`) whose subject is a call of a
// flat union (flatunion.go) that the inliner inlines (inline.go) builds no
// union value where it can avoid one: each return of the inlined body
// that is a variant's value (a constructor such as `Some{Value: v}` or
// `None`) writes the variant into the payload variable of the arm that
// takes it, when that arm binds it, and jumps straight to that arm, so the
// union's tag is never written and never tested:
//
//	var kp Some[ID[Style]]
//	var v Option[ID[Style]]
//	_ = v
//	{
//		if o.n == 0 {
//			_ = None{}
//			goto kn_1
//		}
//		kp = Some[ID[Style]]{Value: ID[Style]{n: o.n - 1}}
//		goto kn
//	}
//	kn:
//	{
//		held := kp
//		…
//	}
//	goto kn_2
//	kn_1:
//	{ … }
//	kn_2:
//
// Any other return (a variable, a call) writes the union as before and
// reaches a tag switch that sends each variant to its arm the same way.
// The arms run as blocks after the call, in source order; an arm no return
// and no switch reaches is not written. Go needs nothing here that it does
// not check: every jump is forward, out of blocks, over no declaration.
// A fallible call, a niche Option, a subject released after its read and
// a call the inliner does not inline keep the switch.
//
// The reference is foldLowering "off", set by a test or by a build with
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.foldLowering=off.
// TestKnownConstructorsAgree runs generated programs under both.
//
// Prior art: GHC's case-of-known-constructor and case-of-case with join
// points send each constructor a function returns straight to its
// alternative once the function is inlined; MLton and OCaml's flambda
// simplify a match on a known block the same way. Go's SSA has no jump
// threading through a phi of constants, so the tag switch on an inlined
// result stays unless the lowering removes it. Gleam, Elixir and ReScript
// leave the value to the host (a tagged tuple, a JS object) and its JIT.
var foldLowering string

// knownMatch is a match being folded: its subject call, the union's
// variants' Go types, each tag's arm, each arm's label and the payload
// variable of each tag a binding arm takes.
type knownMatch struct {
	call      *syntax.CallExpr
	variantGo []string
	tagArm    []int
	binds     []bool // by arm: the arm binds its one variant
	labels    []string
	payload   map[int]string
	used      []bool // by arm: a jump reaches it
	taken     bool   // the call was inlined and its returns jump
	general   bool   // some return wrote the union value
}

// knownMatchFor is the fold of a match on x of the type t whose arms take
// the Go types armTypes (by arm), def marking a wildcard arm and binds an
// arm of one bound variant; nil when it does not apply.
func (fc *funcCtx) knownMatchFor(x syntax.Expr, t check.Type, armTypes [][]string, def, binds []bool) *knownMatch {
	if foldLowering == "off" || fc.out == nil {
		return nil
	}
	call, ok := unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil
	}
	fu := fc.l.flatUnionOf(t)
	if fu == nil || fu.niche {
		return nil
	}
	tc := &fc.typeCtx
	km := &knownMatch{call: call, tagArm: make([]int, len(fu.variants)), payload: map[int]string{}, binds: binds, used: make([]bool, len(armTypes))}
	for k, v := range fu.variants {
		km.variantGo = append(km.variantGo, tc.goType(v))
		km.tagArm[k] = -1
	}
	for i, types := range armTypes {
		for _, g := range types {
			k := fu.tagOfGo(tc, g)
			if k < 0 {
				return nil
			}
			km.tagArm[k] = i
		}
	}
	for i, d := range def {
		if !d {
			continue
		}
		for k := range km.tagArm {
			if km.tagArm[k] < 0 {
				km.tagArm[k] = i
			}
		}
	}
	for _, a := range km.tagArm {
		if a < 0 {
			return nil
		}
	}
	for range armTypes {
		km.labels = append(km.labels, fc.temp("kn"))
	}
	return km
}

// knownTake makes the inlined call x's returns jump to the arms of the
// pending fold, when x is its subject: it declares the payloads before
// the inlined block. ret is the call's result.
func (fc *funcCtx) knownTake(x *syntax.CallExpr, ret *inlineRet, fallible bool) {
	km := fc.pendingKnown
	if km == nil || km.call != x || fallible || ret.join != nil || ret.res == "" {
		return
	}
	fc.pendingKnown = nil
	km.taken = true
	ret.known = km
	// The result is read only when some return writes the union
	// (knownGeneral); with none, the code after the inlined block is
	// unreachable (go vet), so the use goes here.
	fc.line("_ = " + ret.res)
	for k, a := range km.tagArm {
		if km.binds[a] {
			p := fc.temp("kp")
			km.payload[k] = p
			fc.line("var " + p + " " + km.variantGo[k])
			fc.line("_ = " + p)
		}
	}
}

// knownReturn writes the return of x from an inlined body whose site is
// the subject of the fold km: a variant's value into its arm's payload
// and the jump to the arm; any other value as inlineReturn writes it.
func (fc *funcCtx) knownReturn(km *knownMatch, x syntax.Expr) {
	g := fc.goType(fc.typeOf(x))
	k := -1
	for j, v := range km.variantGo {
		if v == g {
			k = j
		}
	}
	if k < 0 {
		km.general = true
		fc.inlineReturn([]string{fc.assigned(x)})
		return
	}
	// The variant itself, not its implicit conversion to the union.
	val := fc.value(x)
	a := km.tagArm[k]
	if p, ok := km.payload[k]; ok {
		fc.line(p + " = " + val)
	} else {
		fc.line("_ = " + val)
	}
	km.used[a] = true
	fc.line("goto " + km.labels[a])
}

// knownGeneral writes the tag switch on the union value subject (of the
// checked type t, over the arms' Go types all) that sends each variant to
// its arm, for the returns that wrote the value.
func (fc *funcCtx) knownGeneral(km *knownMatch, subject string, t check.Type, all []string, s syntax.Node) {
	fc.l.stats.FoldedGeneral++
	fc.line("{")
	fc.depth++
	v := fc.temp("s")
	fc.line(v + " := " + subject)
	ts := fc.tagSwitchOf(v, t, all, s)
	if ts == nil {
		ice(s, "a folded match on a union with no tag switch")
	}
	fc.line(ts.head)
	for k, a := range km.tagArm {
		fc.line("case " + strconv.Itoa(k) + ":")
		if p, ok := km.payload[k]; ok {
			fc.line("\t" + p + " = " + ts.variant(k))
		}
		fc.line("\tgoto " + km.labels[a])
		km.used[a] = true
	}
	fc.line("}")
	fc.noCase(s)
	fc.depth--
	fc.line("}")
}
