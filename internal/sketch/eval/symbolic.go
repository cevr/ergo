package eval

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
)

// Symbolic runs (types.md, Laws 14; plan-2 §1 rule 3). A rewrite law of a
// value provider whose state or parameters cannot be listed is proved by
// running the provider's own bodies with a symbol for each value it cannot
// list. The run is the evaluator's: a symbol and a term are values, and
// only these forms take one:
//
//   - a binding, an argument, a result, a record or tuple part, a field
//     write or read of a record that holds one;
//   - `+` on strings and xs.Append(x), which build a concatenation in
//     normal form (flattened, adjacent constants joined, empty parts
//     dropped, order kept: concatenation is associative, not commutative);
//   - a call of a rowless, non-generic extern go function, which builds the
//     uninterpreted term f(args) and never runs: a rowless function is a
//     function of its arguments.
//
// A term anywhere else (a condition, a match, an index, a comparison, any
// other operator, a conversion, len, an interpolation) stops the run, as
// does any stop of the evaluator. A case holds when both sides end in
// identical terms and results; a symbol stands for every value, so the law
// then holds for every value. A run that stops, or whose sides differ,
// proves nothing and refutes nothing.
//
// Prior art: symbolic execution with uninterpreted functions (KLEE,
// Rosette's solver-aided host) decides equality by a solver; Ergo keeps the
// decision syntactic, so it needs no solver and never guesses.

// symbolicCases bounds the listed cases of one symbolic run.
const symbolicCases = 1 << 16

// symbolicBudget bounds the evaluation of one symbolic run.
var symbolicBudget = Budget{Steps: 20_000_000, Depth: 256, Bytes: defaultBytes}

// term is a value of a symbolic run that stands for values it does not
// list.
type term interface{ termText() string }

// symbol is an unlisted field or parameter: every value of its type.
type symbol struct {
	name string
	t    check.Type
}

// extTerm is the uninterpreted call of a rowless extern go function.
type extTerm struct {
	fn   *check.Func
	args []Value
}

// catTerm is a concatenation in normal form: a string's parts are strings
// and terms, a slice's are chunks and terms; no two constants are
// adjacent and no part is empty.
type catTerm struct {
	slice bool
	parts []Value
}

// chunk is a run of known elements in a slice concatenation.
type chunk struct{ elems []Value }

// cell is a provider record lent to its pointer operations: a write
// through it changes the record every alias of the cell sees.
type cell struct{ s *slot }

func (s *symbol) termText() string { return s.name }
func (c *cell) termText() string   { return "&" + describe(c.s.v) }
func (e *extTerm) termText() string {
	var args []string
	for _, a := range e.args {
		args = append(args, describe(a))
	}
	return e.fn.Name + "(" + strings.Join(args, ", ") + ")"
}
func (c *catTerm) termText() string {
	var parts []string
	for _, p := range c.parts {
		if ch, ok := p.(chunk); ok {
			var es []string
			for _, e := range ch.elems {
				es = append(es, describe(e))
			}
			parts = append(parts, "["+strings.Join(es, ", ")+"]")
			continue
		}
		parts = append(parts, describe(p))
	}
	return strings.Join(parts, " + ")
}

// symbolic reports whether v holds a term or a cell anywhere.
func symbolic(v Value) bool {
	switch v := v.(type) {
	case term:
		return true
	case *record:
		for _, f := range v.fields {
			if symbolic(f) {
				return true
			}
		}
	case tuple:
		for _, e := range v.elems {
			if symbolic(e) {
				return true
			}
		}
	case sliceV:
		for _, e := range v.elems() {
			if symbolic(e) {
				return true
			}
		}
	case mapV:
		for _, e := range v.b.m {
			if symbolic(e) {
				return true
			}
		}
	}
	return false
}

// stringTerm reports whether the term v is of a string type.
func stringTerm(v Value) bool {
	switch v := v.(type) {
	case *symbol:
		b := basicOf(v.t)
		return b != nil && b.Kind == check.String
	case *catTerm:
		return !v.slice
	case *extTerm:
		if len(v.fn.Sig.Results) != 1 {
			return false
		}
		b := basicOf(v.fn.Sig.Results[0])
		return b != nil && b.Kind == check.String
	}
	return false
}

// concatString is a + b in normal form.
func concatString(a, b Value) Value {
	var parts []Value
	add := func(v Value) {
		if s, ok := v.(string); ok {
			if s == "" {
				return
			}
			if n := len(parts); n > 0 {
				if prev, ok := parts[n-1].(string); ok {
					parts[n-1] = prev + s
					return
				}
			}
		}
		parts = append(parts, v)
	}
	for _, v := range []Value{a, b} {
		if c, ok := v.(*catTerm); ok {
			for _, p := range c.parts {
				add(p)
			}
			continue
		}
		add(v)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return &catTerm{parts: parts}
}

// concatSlice is xs.Append(x) in normal form, when xs is a term.
func concatSlice(xs, x Value) Value {
	var parts []Value
	add := func(v Value) {
		if ch, ok := v.(chunk); ok {
			if len(ch.elems) == 0 {
				return
			}
			if n := len(parts); n > 0 {
				if prev, ok := parts[n-1].(chunk); ok {
					parts[n-1] = chunk{elems: append(append([]Value(nil), prev.elems...), ch.elems...)}
					return
				}
			}
		}
		parts = append(parts, v)
	}
	switch v := xs.(type) {
	case *catTerm:
		for _, p := range v.parts {
			add(p)
		}
	case sliceV:
		add(chunk{elems: append([]Value(nil), v.elems()...)})
	default:
		add(v)
	}
	add(chunk{elems: []Value{x}})
	if len(parts) == 1 {
		if ch, ok := parts[0].(chunk); ok {
			return newSlice(ch.elems)
		}
		return parts[0]
	}
	return &catTerm{slice: true, parts: parts}
}

// symEqual is identity of terms: the same structure, symbols by identity,
// floats by bits (the state key's rule, X1). It is false whenever it
// cannot decide, never true.
func symEqual(a, b Value) bool {
	switch a := a.(type) {
	case *symbol:
		return a == b
	case *cell, *closure, mapV:
		return false
	case *extTerm:
		bv, ok := b.(*extTerm)
		return ok && a.fn == bv.fn && symEqualAll(a.args, bv.args)
	case *catTerm:
		bv, ok := b.(*catTerm)
		return ok && a.slice == bv.slice && symEqualAll(a.parts, bv.parts)
	case chunk:
		bv, ok := b.(chunk)
		return ok && symEqualAll(a.elems, bv.elems)
	case *record:
		bv, ok := b.(*record)
		return ok && sameNominal(a, bv) && symEqualAll(a.fields, bv.fields)
	case tuple:
		bv, ok := b.(tuple)
		return ok && symEqualAll(a.elems, bv.elems)
	case sliceV:
		bv, ok := b.(sliceV)
		return ok && a.array == bv.array && symEqualAll(a.elems(), bv.elems())
	case float32:
		bv, ok := b.(float32)
		return ok && math.Float32bits(a) == math.Float32bits(bv)
	case float64:
		bv, ok := b.(float64)
		return ok && math.Float64bits(a) == math.Float64bits(bv)
	case unit:
		_, ok := b.(unit)
		return ok
	}
	if _, ok := b.(term); ok {
		return false
	}
	return basicValue(a) && a == b
}

func symEqualAll(as, bs []Value) bool {
	if len(as) != len(bs) {
		return false
	}
	for i := range as {
		if !symEqual(as[i], bs[i]) {
			return false
		}
	}
	return true
}

// symRun is the state of a symbolic run (interp.sym); nil in every other
// evaluation.
type symRun struct {
	// ok holds the expressions whose value may hold a term; branch the
	// conditions, for the stop's text. Every other expression stops on one.
	ok, branch map[syntax.Expr]bool
	marked     map[syntax.Node]bool
}

func newSymRun() *symRun {
	return &symRun{ok: map[syntax.Expr]bool{}, branch: map[syntax.Expr]bool{}, marked: map[syntax.Node]bool{}}
}

// mark records the term positions of a body once.
func (s *symRun) mark(info *check.Info, body syntax.Node) {
	if body == nil || s.marked[body] {
		return
	}
	s.marked[body] = true
	ok := func(xs ...syntax.Expr) {
		for _, x := range xs {
			if x != nil {
				s.ok[x] = true
			}
		}
	}
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			c := info.Calls[n]
			if c == nil || c.Kind == check.CallConversion || c.Kind == check.CallLen {
				break
			}
			ok(n.Args...)
			ok(c.Recv)
		case *syntax.CompositeLit:
			for _, e := range n.Elts {
				if kv, isKV := e.(*syntax.KeyValueExpr); isKV {
					ok(kv.Value)
				} else {
					ok(e)
				}
			}
		case *syntax.AssignStmt:
			ok(n.Rhs...)
		case *syntax.VarSpec:
			ok(n.Values...)
		case *syntax.ReturnStmt:
			ok(n.Results...)
		case *syntax.ExprStmt:
			ok(n.X)
		case *syntax.ParenExpr:
			ok(n.X)
		case *syntax.TupleExpr:
			ok(n.Elts...)
		case *syntax.SelectorExpr:
			ok(n.X)
		case *syntax.BinaryExpr:
			switch n.Op {
			case syntax.LAND, syntax.LOR:
				s.branch[n.X], s.branch[n.Y] = true, true
			case syntax.DOTDOT:
			default:
				// binop takes a term only for + on strings.
				ok(n.X, n.Y)
			}
		case *syntax.IfStmt:
			s.branch[n.Cond] = true
		case *syntax.ForStmt:
			s.branch[n.Cond] = true
		case *syntax.SwitchStmt:
			s.branch[n.Tag] = true
		case *syntax.CaseClause:
			for _, x := range n.List {
				s.branch[x] = true
			}
		}
		return true
	})
}

// stop is the run's stop at x, which reads a term where no term may be.
func (in *interp) symStop(fr *frame, x syntax.Expr, v Value) *abrupt {
	verb := "computes with"
	if in.sym.branch[x] {
		verb = "branches on"
	}
	return fr.abrupt(ctlUnsupported, x, nil, frameName(fr)+" "+verb+" "+exprText(x, v)+" at "+in.siteText(fr, x.Pos()))
}

func (in *interp) siteText(fr *frame, pos syntax.Pos) string {
	s := in.site(fr.pkg, fr.decl, pos)
	return s.File + ":" + strconv.Itoa(s.Line)
}

func frameName(fr *frame) string {
	switch d := fr.decl.(type) {
	case *syntax.FuncDecl:
		return d.Name.Name
	case *syntax.LawDecl:
		return "law " + d.Name.Name
	}
	return "the law"
}

// exprText names x: a name or a field path, else the value it holds.
func exprText(x syntax.Expr, v Value) string {
	switch x := unparen(x).(type) {
	case *syntax.Ident:
		return x.Name
	case *syntax.SelectorExpr:
		if _, ok := unparen(x.X).(*syntax.Ident); ok {
			return exprText(x.X, nil) + "." + x.Sel.Name
		}
		if _, ok := unparen(x.X).(*syntax.SelectorExpr); ok {
			return exprText(x.X, nil) + "." + x.Sel.Name
		}
	}
	if v == nil {
		return "an expression"
	}
	return describe(v)
}

// rowless reports whether fn has no needs and no fails row.
func rowless(fn *check.Func) bool {
	return solvedEmpty(fn.Sig.Uses) && solvedEmpty(fn.Sig.Errors) && !fn.Sig.Fallible
}

// solvedEmpty reports a row with no element once each inferred row
// variable (an unexported function's row) is replaced by its solution; a
// final variable with no solution is empty, an unsolved one is not.
func solvedEmpty(r *check.Row) bool {
	for _, e := range r.Elems() {
		v, ok := e.(*check.RowVar)
		if !ok {
			return false
		}
		sol := v.Solution()
		if sol == nil && v.Final() {
			continue
		}
		if sol == nil || !solvedEmpty(sol) {
			return false
		}
	}
	return true
}

// externTerm is the call of fn when it is an extern go function a
// symbolic run keeps uninterpreted; why says why it is not.
func externTerm(fn *check.Func, args []Value) (Value, string) {
	switch {
	case fn.Decl == nil || fn.Decl.Extern == nil || fn.Pkg.Host():
		return nil, fn.Name + " has no Ergo body and is no extern go function"
	case !rowless(fn):
		return nil, fn.Name + " has a needs or fails row"
	case fn.Ptr || len(fn.Sig.TParams) > 0 || len(fn.Sig.Results) > 1:
		return nil, fn.Name + " is a generic, borrowing or multi-result extern go function"
	}
	for _, a := range args {
		if hidden(a) {
			return nil, fn.Name + " takes a reference or a function value"
		}
	}
	if len(fn.Sig.Results) == 0 {
		return unit{}, ""
	}
	return &extTerm{fn: fn, args: args}, ""
}

// hidden reports whether v holds a cell or a function value: what an
// extern function could read beyond its arguments' values.
func hidden(v Value) bool {
	switch v := v.(type) {
	case *cell, *closure, *operation:
		return true
	case *record:
		for _, f := range v.fields {
			if hidden(f) {
				return true
			}
		}
	case tuple:
		for _, e := range v.elems {
			if hidden(e) {
				return true
			}
		}
	case sliceV:
		for _, e := range v.elems() {
			if hidden(e) {
				return true
			}
		}
	case mapV:
		return true
	case *extTerm:
		for _, a := range v.args {
			if hidden(a) {
				return true
			}
		}
	case *catTerm:
		for _, p := range v.parts {
			if ch, ok := p.(chunk); ok {
				for _, e := range ch.elems {
					if hidden(e) {
						return true
					}
				}
			} else if hidden(p) {
				return true
			}
		}
	}
	return false
}

// SymbolicProof is the verdict of a symbolic run: Proved, the cases its
// guard admitted, the symbols it used, or why it proved nothing.
type SymbolicProof struct {
	Proved  bool
	Cases   int
	Symbols []string
	Why     string
}

// How is the verdict's text for a receipt: "N cases, symbols a b".
func (p SymbolicProof) How() string {
	s := strconv.Itoa(p.Cases) + " cases"
	if len(p.Symbols) > 0 {
		s += ", symbols " + strings.Join(p.Symbols, " ")
	}
	return s
}

// ProveSymbolic runs the rewrite law law for the value provider p: each
// listable field and parameter is listed, each other one is a symbol, and
// every case must end both sides in identical results and states.
func ProveSymbolic(law *check.Law, p check.LawProvider) (out SymbolicProof) {
	no := func(format string, args ...any) SymbolicProof {
		return SymbolicProof{Why: fmt.Sprintf(format, args...)}
	}
	if law.Rewrite == nil || !p.Value || p.Type == nil || len(p.Type.TArgs) > 0 || len(p.Conv.Instances) > 0 {
		return no("the provider is no plain value provider")
	}
	st := structOf(p.Type)
	if st == nil {
		return no("the provider is no record")
	}
	in := &interp{budget: symbolicBudget, root: law.Pkg, files: map[syntax.Decl]*syntax.File{}, initials: map[*check.TypeName]Value{}, globals: map[*check.Var]*slot{}, sym: newSymRun()}
	defer func() {
		if r := recover(); r != nil {
			out = no("the evaluator stopped: %v", r)
		}
	}()
	fr := in.newFrame(law.Pkg, law.Decl)
	in.sym.mark(law.Pkg.Info, law.Decl.Body)
	var symbols []string
	// lists[i] are the listed values of input i (the fields, then the
	// parameters); a symbol's list is the one symbol.
	var lists [][]Value
	input := func(name string, t check.Type) {
		if vs, ok := testplan.Domain(t); ok && len(vs) > 0 {
			l := make([]Value, len(vs))
			for i, v := range vs {
				l[i] = in.listValue(fr, v)
			}
			lists = append(lists, l)
			return
		}
		symbols = append(symbols, name)
		lists = append(lists, []Value{&symbol{name: name, t: t}})
	}
	for _, f := range st.Fields {
		input(f.Name, f.Type)
	}
	for _, v := range law.Params {
		input(v.Name, v.Type)
	}
	total := 1
	for _, l := range lists {
		if total > symbolicCases/len(l) {
			return no("more than %d cases", symbolicCases)
		}
		total *= len(l)
	}
	for _, rc := range append(append([]check.RewriteCall(nil), law.Rewrite.Lhs...), law.Rewrite.Rhs...) {
		for _, a := range rc.Args {
			if a.Param < 0 {
				in.sym.ok[a.X] = true
			}
		}
	}
	nf := len(st.Fields)
	admitted := 0
	choice := make([]Value, len(lists))
	for k := 0; k < total; k++ {
		r := k
		for i := len(lists) - 1; i >= 0; i-- {
			choice[i] = lists[i][r%len(lists[i])]
			r /= len(lists[i])
		}
		state := &record{t: p.Type, fields: append([]Value(nil), choice[:nf]...)}
		params := choice[nf:]
		for i, v := range law.Params {
			fr.bind(v, params[i])
		}
		if g := law.Rewrite.Guard; g != nil {
			c := &cell{s: &slot{v: state}}
			got, why := in.symCall(fr, p, c, g, &syntax.CallExpr{Fun: law.Decl.Name}, nil)
			if why != "" {
				return no("%s", why)
			}
			want, ab := in.expr(fr, law.Rewrite.GuardValue)
			if ab != nil {
				return no("%s", ab.msg)
			}
			if symbolic(got) || symbolic(want) {
				return no("the guard reads a symbol")
			}
			if !symEqual(c.s.v, state) {
				return no("the guard changes the provider state")
			}
			if !symEqual(got, want) {
				continue
			}
		}
		admitted++
		lres, lend, why := in.symSide(fr, p, state, law.Rewrite.Lhs, params)
		if why != "" {
			return no("%s", why)
		}
		rres, rend, why := in.symSide(fr, p, state, law.Rewrite.Rhs, params)
		if why != "" {
			return no("%s", why)
		}
		if !symEqualAll(lres, rres) || !symEqual(lend, rend) {
			return no("the sides end in different terms at state %s: left %s, right %s", describe(state), describe(lend), describe(rend))
		}
	}
	if admitted == 0 {
		return no("no case reaches a state its guard admits")
	}
	return SymbolicProof{Proved: true, Cases: admitted, Symbols: symbols}
}

// symSide runs one side from state: its results and its end state.
func (in *interp) symSide(fr *frame, p check.LawProvider, state Value, calls []check.RewriteCall, params []Value) ([]Value, Value, string) {
	c := &cell{s: &slot{v: state}}
	var results []Value
	for _, rc := range calls {
		got, why := in.symCall(fr, p, c, rc.Op, rc.Call, func() ([]Value, string) {
			var args []Value
			for _, a := range rc.Args {
				if a.Param >= 0 {
					args = append(args, params[a.Param])
					continue
				}
				v, ab := in.expr(fr, a.X)
				if ab != nil {
					return nil, ab.msg
				}
				args = append(args, v)
			}
			return args, ""
		})
		if why != "" {
			return nil, nil, why
		}
		if len(rc.Op.Sig.Results) > 0 {
			results = append(results, got)
		}
	}
	return results, c.s.v, ""
}

// symCall calls the provider's function for op on the cell c.
func (in *interp) symCall(fr *frame, p check.LawProvider, c *cell, op *check.Method, at *syntax.CallExpr, args func() ([]Value, string)) (Value, string) {
	fn := p.Conv.Table[op.Name]
	if fn == nil {
		return nil, "the provider has no function for " + op.Name
	}
	recv := Value(c)
	if !fn.Ptr {
		recv = c.s.v
	}
	all := []Value{recv}
	if args != nil {
		more, why := args()
		if why != "" {
			return nil, why
		}
		all = append(all, more...)
	}
	v, ab := in.call(fr, at, fn, nil, all)
	if ab != nil {
		if ab.msg == "" {
			return nil, fn.Name + " does not end normally"
		}
		return nil, ab.msg
	}
	return v, ""
}
