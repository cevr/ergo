package lower

import (
	"regexp"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A tuple let of an if or match (LWR-TUPLE-MATCH, a form of formLowering).
// `let (a, b, c) = match f { … }` lowered as a value is a Go variable of an
// anonymous struct that each arm assigns, then a copy of it and a read of
// each field (letPattern). Here the names are Go variables declared first,
// and each arm's tail writes them: a tuple literal element by element
// (`a, b, c = x, y, z`, the elements evaluated in order as a struct
// literal's are), any other tail through its fields. No tuple value is
// made or copied (T4b: civil's render, 2,207 → 1,494 instructions a call
// by hand). The reference (formLowering "reference") keeps letPattern.
//
// The form takes only what writes the names as the reference's binding
// does: a tuple pattern of names and _ over an if or match whose value no
// implicit conversion changes, whose tails cannot fail, and which no
// ownership of the value reaches (no owned name, no arm that holds, lends
// or registers the value), outside a law's control expression (its result
// site). Any other let keeps letPattern.

// tupleLet lowers spec, `let P = x` with p the checked tuple pattern P, by
// the form when it fits, and reports whether it did.
func (fc *funcCtx) tupleLet(spec *syntax.VarSpec, p *check.Pattern) bool {
	if !formsOn() || p.Kind != check.PatTuple || len(spec.Values) != 1 {
		return false
	}
	x := spec.Values[0]
	form := unparen(x)
	var s syntax.Stmt
	switch f := form.(type) {
	case *syntax.IfStmt, *syntax.SwitchStmt, *syntax.TypeSwitchStmt:
		s = f.(syntax.Stmt)
	default:
		return false
	}
	info := fc.l.info
	if _, ok := info.Implicit[x]; ok {
		return false
	}
	t := fc.typeOf(x)
	tup, ok := underlying(t).(*check.Tuple)
	if !ok || len(tup.Types) != len(p.Elems) {
		return false
	}
	for _, e := range p.Elems {
		switch e.Kind {
		case check.PatWildcard:
		case check.PatBind:
			if e.Var != nil && info.OwnedVars[e.Var] != nil {
				return false
			}
		default:
			return false
		}
	}
	if fc.tailFallible(s) || info.ArmHeld[form] || info.ArmValues[form] != nil || info.HeldIn[form] != nil || info.OwnedTemps[form] != nil {
		return false
	}
	if fc.frame != nil && fc.frame.law {
		return false
	}
	names := make([]string, len(p.Elems))
	var bound []*check.Var
	for i, e := range p.Elems {
		if e.Kind == check.PatBind && e.Var != nil {
			names[i] = fc.declare(e.Var)
			fc.line("var " + names[i] + " " + fc.goType(e.Var.Type))
			bound = append(bound, e.Var)
		}
	}
	fc.yieldStmt(s, dest{kind: destTuple, names: names, typ: t})
	for _, v := range bound {
		if !fc.l.reads[v] {
			fc.line("_ = " + fc.locals[v])
		}
	}
	fc.releaseAt(spec) // the let's temporaries (R43)
	fc.l.stats.TupleLets++
	return true
}

// goName matches a Go identifier: a tail whose value is one is read field
// by field with no temporary.
var goName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// tupleYield writes the tail x of an arm into d's names: a tuple literal of
// d's type element by element, any other value through its fields.
func (fc *funcCtx) tupleYield(x syntax.Expr, d dest) {
	lhs := make([]string, len(d.names))
	for i, n := range d.names {
		lhs[i] = n
		if n == "" {
			lhs[i] = "_"
		}
	}
	if tl, ok := unparen(x).(*syntax.TupleExpr); ok && len(tl.Elts) == len(d.names) && fc.tupleFits(x, d.typ) {
		vals := fc.operands(tl.Elts, true)
		fc.line(strings.Join(lhs, ", ") + " = " + strings.Join(vals, ", "))
		return
	}
	val := fc.assignedTo(x, fc.value(x), d.typ)
	if !goName.MatchString(val) {
		v := fc.temp("p")
		fc.line(v + " := " + val)
		val = v
	}
	rhs := make([]string, len(d.names))
	for i := range d.names {
		rhs[i] = val + "." + tupleField(i)
	}
	fc.line(strings.Join(lhs, ", ") + " = " + strings.Join(rhs, ", "))
}

// tupleFits reports whether the tuple literal x is of the type to as Go
// spells it, with no conversion at x: its elements are then to's elements.
func (fc *funcCtx) tupleFits(x syntax.Expr, to check.Type) bool {
	if _, ok := fc.l.info.Implicit[x]; ok {
		return false
	}
	return fc.goType(fc.typeOf(x)) == fc.goType(to)
}
