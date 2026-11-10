package check

import (
	"go/constant"
	"go/token"
	"math"
	"math/big"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Refinements (types.md, Refinements). `type Port int where 1 <= it && it
// <= 65535` refines a defined type by a predicate in a decidable fragment:
// comparisons of the subject (it, or len(it) for a string or slice base)
// with integer literals, joined by &&, || and !. The checker normalises the
// predicate to the set of subject values that satisfy it, a sorted list of
// disjoint closed spans, which decides every constant at check time, the
// zero value, the generator's draws and the listed domain. The parser
// derives ParsePort and the law PortWhere (syntax/refine.go); the checker
// checks them as written code, and lets only ParsePort convert a value
// that is not a constant into Port.

// Refinement is the predicate of a refined type or of a law parameter.
type Refinement struct {
	// Len is set when the subject is len(it): a string or slice base.
	Len bool
	// Spans are the subject values that satisfy the predicate: sorted,
	// disjoint, closed and not adjacent, inside the base's range (an
	// integer kind's, or 0 to MaxInt64 for a length). Never empty.
	Spans []Span
	// Text is the predicate's source on one line.
	Text string
}

// Span is the closed interval Lo..Hi.
type Span struct{ Lo, Hi *big.Int }

// Holds reports whether the subject value v satisfies the predicate.
func (r *Refinement) Holds(v *big.Int) bool {
	for _, s := range r.Spans {
		if v.Cmp(s.Lo) >= 0 && v.Cmp(s.Hi) <= 0 {
			return true
		}
	}
	return false
}

// Count is the number of subject values that satisfy the predicate.
func (r *Refinement) Count() *big.Int {
	n := new(big.Int)
	for _, s := range r.Spans {
		n.Add(n, new(big.Int).Sub(s.Hi, s.Lo))
		n.Add(n, big.NewInt(1))
	}
	return n
}

// RefinementOf is the refinement of the type t: of t itself when it is a
// refined named type, else nil.
func RefinementOf(t Type) *Refinement {
	if n, ok := t.(*Named); ok && n.Obj != nil && n.Orig == nil {
		return n.Obj.Refine
	}
	return nil
}

// spanSet is a set of integers as sorted, disjoint, non-adjacent spans.
type spanSet []Span

func one(lo, hi *big.Int) spanSet {
	if lo.Cmp(hi) > 0 {
		return nil
	}
	return spanSet{{Lo: lo, Hi: hi}}
}

func (a spanSet) union(b spanSet) spanSet {
	all := append(append(spanSet{}, a...), b...)
	// Insertion sort by Lo: the sets are small.
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].Lo.Cmp(all[j-1].Lo) < 0; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	var out spanSet
	for _, s := range all {
		if n := len(out); n > 0 {
			next := new(big.Int).Add(out[n-1].Hi, big.NewInt(1))
			if s.Lo.Cmp(next) <= 0 {
				if s.Hi.Cmp(out[n-1].Hi) > 0 {
					out[n-1].Hi = s.Hi
				}
				continue
			}
		}
		out = append(out, Span{Lo: s.Lo, Hi: s.Hi})
	}
	return out
}

// complement is the set's complement within the domain dom.
func (a spanSet) complement(dom Span) spanSet {
	var out spanSet
	lo := dom.Lo
	for _, s := range a {
		out = append(out, one(lo, new(big.Int).Sub(s.Lo, big.NewInt(1)))...)
		lo = new(big.Int).Add(s.Hi, big.NewInt(1))
	}
	return append(out, one(lo, dom.Hi)...)
}

func (a spanSet) intersect(b spanSet, dom Span) spanSet {
	return a.complement(dom).union(b.complement(dom)).complement(dom)
}

// refinementDomain is the range of the subject of a refinement over the
// base t, and whether the subject is len(it); ok is false for a base no
// refinement takes.
func refinementDomain(t Type) (dom Span, isLen, ok bool) {
	switch u := under(t).(type) {
	case *Basic:
		if isInteger(u) {
			lo, hi := intBounds(u.Kind)
			return Span{Lo: bigOf(lo), Hi: bigOf(hi)}, false, true
		}
		if isString(u) {
			return Span{Lo: big.NewInt(0), Hi: big.NewInt(math.MaxInt64)}, true, true
		}
	case *Slice:
		return Span{Lo: big.NewInt(0), Hi: big.NewInt(math.MaxInt64)}, true, true
	}
	return Span{}, false, false
}

func bigOf(v constant.Value) *big.Int {
	b, ok := new(big.Int).SetString(constant.ToInt(v).ExactString(), 10)
	if !ok {
		panic("check: internal compiler error: an integer bound is not an integer")
	}
	return b
}

// whereOutside is the S108 message for a node outside the fragment.
const whereOutside = "a where is decidable: comparisons of %s with integer literals, joined by &&, || and !; %s is outside it (types.md, Refinements 2)"

// refinement decides the predicate where over the base t, whose subject is
// the name subject (it, or a law parameter's name). It reports S108 at the
// first node outside the fragment, and when no value satisfies it.
func (c *checker) refinement(subject string, t Type, where syntax.Expr, what string) *Refinement {
	dom, isLen, ok := refinementDomain(t)
	if !ok {
		c.errorf(where, CodeType, "a where refines an integer type (it) or a string or slice (len(it)); %s is over %s (types.md, Refinements 3)", what, t)
		return nil
	}
	if RefinementOf(t) != nil {
		c.errorf(where, CodeType, "%s is refined already; write one where that holds both predicates (types.md, Refinements 3)", t)
		return nil
	}
	shown := subject
	if isLen {
		shown = "len(" + subject + ")"
	}
	r := &refiner{c: c, subject: subject, isLen: isLen, dom: dom, shown: shown}
	set, ok := r.set(where)
	if !ok {
		return nil
	}
	if len(set) == 0 {
		c.errorf(where, CodeType, "no value of %s satisfies this where (types.md, Refinements 2)", t)
		return nil
	}
	return &Refinement{Len: isLen, Spans: set, Text: syntax.WhereText(c.cur.syntax.Src, where)}
}

type refiner struct {
	c       *checker
	subject string
	isLen   bool
	dom     Span
	shown   string
}

func (r *refiner) refuse(x syntax.Expr) (spanSet, bool) {
	r.c.errorf(x, CodeType, whereOutside, r.shown, "this")
	return nil, false
}

// set is the set of subject values for which x holds.
func (r *refiner) set(x syntax.Expr) (spanSet, bool) {
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return r.set(x.X)
	case *syntax.Ident:
		switch x.Name {
		case "true":
			return spanSet{r.dom}, true
		case "false":
			return nil, true
		}
	case *syntax.UnaryExpr:
		if x.Op == syntax.NOT {
			s, ok := r.set(x.X)
			return s.complement(r.dom), ok
		}
	case *syntax.BinaryExpr:
		switch x.Op {
		case syntax.LAND, syntax.LOR:
			a, ok := r.set(x.X)
			if !ok {
				return nil, false
			}
			b, ok := r.set(x.Y)
			if !ok {
				return nil, false
			}
			if x.Op == syntax.LAND {
				return a.intersect(b, r.dom), true
			}
			return a.union(b), true
		case syntax.EQL, syntax.NEQ, syntax.LSS, syntax.LEQ, syntax.GTR, syntax.GEQ:
			return r.compare(x)
		}
	}
	return r.refuse(x)
}

// flipped is the comparison c OP it written as it OP' c.
var flipped = map[syntax.Token]syntax.Token{
	syntax.EQL: syntax.EQL, syntax.NEQ: syntax.NEQ,
	syntax.LSS: syntax.GTR, syntax.LEQ: syntax.GEQ, syntax.GTR: syntax.LSS, syntax.GEQ: syntax.LEQ,
}

func (r *refiner) compare(x *syntax.BinaryExpr) (spanSet, bool) {
	op, subj, lit := x.Op, x.X, x.Y
	if !r.isSubject(subj) {
		op, subj, lit = flipped[x.Op], x.Y, x.X
	}
	if !r.isSubject(subj) {
		return r.refuse(x)
	}
	v, ok := intLiteral(lit)
	if !ok {
		return r.refuse(lit)
	}
	one1 := big.NewInt(1)
	switch op {
	case syntax.EQL:
		return one(v, v).intersect(spanSet{r.dom}, r.dom), true
	case syntax.NEQ:
		return one(v, v).intersect(spanSet{r.dom}, r.dom).complement(r.dom), true
	case syntax.LSS:
		return clip(r.dom.Lo, new(big.Int).Sub(v, one1), r.dom), true
	case syntax.LEQ:
		return clip(r.dom.Lo, v, r.dom), true
	case syntax.GTR:
		return clip(new(big.Int).Add(v, one1), r.dom.Hi, r.dom), true
	default: // GEQ
		return clip(v, r.dom.Hi, r.dom), true
	}
}

// clip is lo..hi inside dom.
func clip(lo, hi *big.Int, dom Span) spanSet {
	if lo.Cmp(dom.Lo) < 0 {
		lo = dom.Lo
	}
	if hi.Cmp(dom.Hi) > 0 {
		hi = dom.Hi
	}
	return one(lo, hi)
}

// isSubject reports the subject: it, or len(it) for a length.
func (r *refiner) isSubject(x syntax.Expr) bool {
	x = unparen(x)
	if r.isLen {
		call, ok := x.(*syntax.CallExpr)
		if !ok || len(call.Args) != 1 || call.Trailing != nil {
			return false
		}
		fn, ok := call.Fun.(*syntax.Ident)
		id, ok2 := unparen(call.Args[0]).(*syntax.Ident)
		return ok && ok2 && fn.Name == "len" && id.Name == r.subject
	}
	id, ok := x.(*syntax.Ident)
	return ok && id.Name == r.subject
}

// intLiteral is the value of an integer literal with an optional sign.
func intLiteral(x syntax.Expr) (*big.Int, bool) {
	lit, sign, ok := signedLit(x)
	if !ok || lit.Kind != syntax.INT || legacyOctal(lit.Value) {
		return nil, false
	}
	v := constant.MakeFromLiteral(lit.Value, token.INT, 0)
	if v.Kind() != constant.Int {
		return nil, false
	}
	if sign == "-" {
		v = constant.UnaryOp(token.SUB, v, 0)
	}
	return bigOf(v), true
}

// inDerived reports a body the parser derived from the where of the
// refined type t: only there does a value that is no constant convert into
// t (types.md, Refinements 6).
func (c *checker) inDerived(t Type) bool {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Decl == nil || c.fn == nil || c.fn.fn == nil || c.fn.fn.Decl == nil {
		return false
	}
	return c.fn.fn.Decl.Derived == n.Obj.Decl
}

// derivedOf is the refined type that derives one of two functions of one
// name, or nil.
func derivedOf(fs ...*Func) *syntax.TypeDecl {
	for _, f := range fs {
		if f.Decl != nil && f.Decl.Derived != nil {
			return f.Decl.Derived
		}
	}
	return nil
}

// parseFunc is the name of the parse function of the refined type t, as
// the current package writes it.
func (c *checker) parseFunc(t Type) string {
	n := t.(*Named)
	name := syntax.ParseName(n.Obj.Name)
	if n.Obj.Pkg != nil && n.Obj.Pkg != c.pkg {
		return n.Obj.Pkg.Name + "." + name
	}
	return name
}

// refinedConst checks the constant val of the refined type t at x: it
// satisfies t's where (types.md, Refinements 6), else S108.
func (c *checker) refinedConst(x syntax.Node, val constant.Value, t Type) bool {
	r := RefinementOf(t)
	if r == nil || val == nil {
		return true
	}
	var v *big.Int
	shown := val.ExactString()
	switch {
	case r.Len && val.Kind() == constant.String:
		v = big.NewInt(int64(len(constant.StringVal(val))))
		shown += " (length " + v.String() + ")"
	case !r.Len && val.Kind() == constant.Int:
		v = bigOf(val)
	default:
		return true
	}
	if r.Holds(v) {
		return true
	}
	c.errorf(x, CodeType, "%s is not a %s: its where is %s (types.md, Refinements 6)", shown, t, r.Text)
	return false
}

// refinedArith refuses an operator other than a comparison on a value of
// the refined type t (types.md, Refinements 7): its result could leave the
// where.
func (c *checker) refinedArith(x syntax.Node, op syntax.Token, t Type) bool {
	if RefinementOf(t) == nil {
		return true
	}
	c.errorf(x, CodeType, "operator %s on %s could leave its where; convert first: %s(…) %s … (types.md, Refinements 7)", op, t, refinedBase(t), op)
	return false
}

// refinedBase is the type the refined type t is defined over, as written.
func refinedBase(t Type) string {
	if n, ok := t.(*Named); ok && n.Defined() != nil {
		return n.Defined().String()
	}
	return t.String()
}

// checkRefinements decides the where of each refined type of the package
// and refuses a where on a parameter of anything but a law.
func (c *checker) checkRefinements() {
	c.refining = true
	defer func() { c.refining = false }()
	for _, tn := range c.pkg.Types {
		d := tn.Decl
		if d == nil || d.Where == nil {
			continue
		}
		c.cur = tn.file
		n, _ := tn.Type.(*Named)
		switch {
		case d.TypeParams != nil:
			c.errorf(d.Where, CodeType, "a refined type has no type parameters: refine an instance, type N Box[int] where … (types.md, Refinements 3)")
		case n == nil || n.Defined() == nil:
			c.errorf(d.Where, CodeType, "a where refines a defined type, type N T where … (types.md, Refinements 1)")
		case !containsInvalid(n.Defined()):
			tn.Refine = c.refinement("it", n.Defined(), d.Where, tn.Name)
		}
		if tn.Refine == nil {
			if c.refusedWheres == nil {
				c.refusedWheres = map[*fileInfo][][2]syntax.Pos{}
			}
			c.refusedWheres[tn.file] = append(c.refusedWheres[tn.file], [2]syntax.Pos{d.WherePos, d.Where.End()})
		}
	}
	for _, f := range c.files {
		c.cur = f
		laws := map[*syntax.Field]bool{}
		syntax.Inspect(f.syntax, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.LawDecl:
				if n.Params != nil {
					for _, p := range n.Params.List {
						laws[p] = true
					}
				}
			case *syntax.Field:
				if n.Where != nil && !laws[n] {
					c.errorf(n.Where, CodeType, "a where on a parameter stands only in a law; declare a refined type, type N T where … (types.md, Refinements 10)")
				}
			}
			return true
		})
	}
}
