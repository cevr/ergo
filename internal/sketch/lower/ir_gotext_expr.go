package lower

// The statements, expressions and types of IRGoText (ir_gotext.go): each
// IR form spelled as the Go source whose syntax the derivative's decoder
// builds for it (toolchain/ir, cmd/compile/internal/ergoir), typed as the
// decoder types it.

import (
	"fmt"
	"go/types"
	"strconv"
	"strings"
)

// irPrinter prints the body of one IR function.
type irPrinter struct {
	pkg     *types.Package
	file    *irTextFile
	files   []string // the IR file's .ergo file names
	sig     *types.Signature
	locals  []*types.Var
	tparams []*types.TypeParam
	ctxt    *types.Context
	b       strings.Builder
	depth   int
	// inHeader: the expression printed stands in a statement header.
	inHeader bool
}

// gx is a printed expression: its text, its type (nil: a call with no
// result), and its precedence: 7 a primary expression, 6 a unary one,
// 1 to 5 a binary one (Go's levels).
type gx struct {
	s    string
	t    types.Type
	prec int
}

type irTextError struct{ msg string }

func (p *irPrinter) fail(format string, args ...any) {
	panic(irTextError{fmt.Sprintf(format, args...)})
}

// function is the body of fn, declared as obj: its {, its statements, its }.
func (p *irPrinter) function(obj *types.Func, fn *irFunc) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			e, ok := r.(irTextError)
			if !ok {
				panic(r)
			}
			err = fmt.Errorf("%s", e.msg)
		}
	}()
	p.sig = obj.Type().(*types.Signature)
	if r := p.sig.Recv(); r != nil {
		p.locals = append(p.locals, r)
	}
	for i := 0; i < p.sig.Params().Len(); i++ {
		p.locals = append(p.locals, p.sig.Params().At(i))
	}
	for _, l := range []*types.TypeParamList{p.sig.RecvTypeParams(), p.sig.TypeParams()} {
		for i := 0; i < l.Len(); i++ {
			p.tparams = append(p.tparams, l.At(i))
		}
	}
	for _, l := range fn.Locals {
		p.locals = append(p.locals, types.NewVar(0, p.pkg, l.Name, p.typ(l.Type)))
	}
	p.b.WriteString("{\n")
	p.block(fn.Body, 1)
	if !irListTerminates(fn.Body) {
		switch {
		case p.sig.Results().Len() == 1 && irTextFallible(p.sig):
			// The decoder's implicit success return of a unit function.
			p.line(1, fn.End, "return nil")
		case p.sig.Results().Len() > 0:
			p.fail("missing return")
		}
	}
	p.b.WriteString("}")
	return p.b.String(), nil
}

// irTextFallible reports a signature of the failure ABI: its last result
// is the universe's error, after at most one value.
func irTextFallible(sig *types.Signature) bool {
	n := sig.Results().Len()
	return (n == 1 || n == 2) && types.Identical(sig.Results().At(n-1).Type(), types.Universe.Lookup("error").Type())
}

// line writes a statement's line, from the statement's position. gofmt
// puts a space after the directive, so it names the column before the
// statement's.
func (p *irPrinter) line(depth int, pos irPos, s string) {
	p.b.WriteString(strings.Repeat("\t", depth))
	if pos[0] >= 0 && pos[0] < len(p.files) {
		fmt.Fprintf(&p.b, "/*line %s:%d:%d*/", p.files[pos[0]], pos[1], max(pos[2]-1, 1))
	}
	p.b.WriteString(s)
	p.b.WriteByte('\n')
}

// raw writes a line with no position (a case clause, an else).
func (p *irPrinter) raw(depth int, s string) {
	p.b.WriteString(strings.Repeat("\t", depth) + s + "\n")
}

// block writes list's statements (an IR body holds no unreachable one:
// irReachable).
func (p *irPrinter) block(list []*irStmt, depth int) {
	for _, s := range list {
		p.stmt(s, depth)
	}
}

func (p *irPrinter) local(i int) *types.Var {
	if i < 0 || i >= len(p.locals) {
		p.fail("local %d out of range", i)
	}
	return p.locals[i]
}

func (p *irPrinter) name(i int) string { return p.local(i).Name() }

// header is x in a statement header, where each composite literal stands
// in parentheses: gofmt drops the parentheses around a whole header, even
// one whose literal's type is generic (Option[T]{}), which then does not
// parse.
func (p *irPrinter) header(x *irExpr, typed bool) gx {
	outer := p.inHeader
	p.inHeader = true
	defer func() { p.inHeader = outer }()
	return p.expr(x, typed)
}

// composite is a composite literal's text, in parentheses in a header.
func (p *irPrinter) composite(s string) string {
	if p.inHeader {
		return "(" + s + ")"
	}
	return s
}

// primary is x as the operand of a selector, an index, a call or an
// assertion.
func primary(x gx) string {
	if x.prec < 7 {
		return "(" + x.s + ")"
	}
	return x.s
}

func (p *irPrinter) labeled(s *irStmt, depth int) {
	if s.Label != "" {
		p.line(depth, s.Pos, s.Label+":")
	}
}

func (p *irPrinter) stmt(s *irStmt, depth int) {
	p.depth = depth
	switch s.K {
	case "def", "set":
		if s.K == "def" && s.X != nil && (s.X.K == "nil" || s.X.K == "zero") {
			// The decoder's typed nil, not a conversion of Go's untyped one.
			v := p.local(s.Local)
			if z := p.zero(v.Type(), true); z.s == "nil" {
				p.line(depth, s.Pos, "var "+v.Name()+" "+p.typeString(v.Type())+" = nil")
				return
			}
		}
		op := ":="
		if s.K == "set" {
			op = "="
			if s.Op != "" {
				op = s.Op + "="
			}
		}
		p.line(depth, s.Pos, p.name(s.Local)+" "+op+" "+p.expr(s.X, s.K == "set").s)
	case "store":
		var lhs gx
		if s.X != nil && s.X.K == "mapAt" {
			lhs = p.index(s.X)
		} else {
			lhs = p.expr(s.X, false)
		}
		p.line(depth, s.Pos, lhs.s+" "+s.Op+"= "+p.expr(s.Y, true).s)
	case "var":
		v := p.local(s.Local)
		p.line(depth, s.Pos, "var "+v.Name()+" "+p.typeString(v.Type()))
	case "discard":
		p.line(depth, s.Pos, "_ = "+p.expr(s.X, false).s)
	case "ret":
		p.line(depth, s.Pos, p.ret(s))
	case "fail":
		x := p.expr(s.X, true)
		if res := p.sig.Results(); res.Len() == 2 {
			p.line(depth, s.Pos, "return "+p.zero(res.At(0).Type(), true).s+", "+x.s)
		} else {
			p.line(depth, s.Pos, "return "+x.s)
		}
	case "if":
		p.line(depth, s.Pos, "if "+p.header(s.X, true).s+" {")
		p.block(s.Then, depth+1)
		if s.Else != nil {
			p.raw(depth, "} else {")
			p.block(s.Else, depth+1)
		}
		p.raw(depth, "}")
	case "while":
		head := "for "
		if s.X != nil {
			head += p.header(s.X, true).s + " "
		}
		p.loop(s, depth, head)
	case "count":
		i := p.name(s.Local)
		p.loop(s, depth, "for "+i+" := "+p.header(s.X, false).s+"; "+i+" < "+p.header(s.Y, true).s+"; "+i+"++ ")
	case "range":
		hx := p.header(s.X, false)
		x := hx.s
		overFunc := false
		if hx.t != nil {
			_, overFunc = hx.t.Underlying().(*types.Signature)
		}
		head := "for "
		if s.Key != nil && s.Val == nil && overFunc {
			// One variable: Go allows no second one over a func (a Seq
			// loop, ir_seq.go). Over any other domain the decoder binds
			// the value to _, so the text spells k, _ too: Go's tree of
			// for k := range x has no value at all, and the two trees
			// would differ (vt's Keys).
			head += p.name(*s.Key) + " := "
		} else if s.Key != nil || s.Val != nil {
			lhs := []string{"_", "_"}
			if s.Key != nil {
				lhs[0] = p.name(*s.Key)
			}
			if s.Val != nil {
				lhs[1] = p.name(*s.Val)
			}
			head += lhs[0] + ", " + lhs[1] + " := "
		}
		p.loop(s, depth, head+"range "+x+" ")
	case "break", "continue", "goto":
		x := s.K
		if s.Label != "" {
			x += " " + s.Label
		}
		p.line(depth, s.Pos, x)
	case "panic":
		p.line(depth, s.Pos, "panic("+p.expr(s.X, true).s+")")
	case "expr":
		p.line(depth, s.Pos, p.expr(s.X, true).s)
	case "defer":
		p.line(depth, s.Pos, "defer "+p.expr(s.X, true).s)
	case "mapget":
		m, k := p.expr(s.X, false), p.expr(s.Y, true)
		p.line(depth, s.Pos, p.name(s.Local)+", "+p.name(*s.Val)+" := "+primary(m)+"["+k.s+"]")
	case "def2":
		p.line(depth, s.Pos, p.name(s.Local)+", "+p.name(*s.Val)+" := "+p.expr(s.X, true).s)
	case "tdef":
		var lhs []string
		op := "="
		for _, l := range s.Ls {
			if l == -1 {
				lhs = append(lhs, "_")
				continue
			}
			lhs = append(lhs, p.name(l))
			op = ":="
		}
		p.line(depth, s.Pos, strings.Join(lhs, ", ")+" "+op+" "+p.expr(s.X, true).s)
	case "tswitch", "switch":
		p.switchStmt(s, depth)
	case "block":
		p.labeled(s, depth)
		p.line(depth, s.Pos, "{")
		p.block(s.Body, depth+1)
		p.raw(depth, "}")
	default:
		p.fail("unknown statement %q", s.K)
	}
}

// loop writes a loop with its label, its header head (up to its {), its
// body and its }.
func (p *irPrinter) loop(s *irStmt, depth int, head string) {
	p.labeled(s, depth)
	p.line(depth, s.Pos, head+"{")
	p.block(s.Body, depth+1)
	p.raw(depth, "}")
}

func (p *irPrinter) switchStmt(s *irStmt, depth int) {
	x := p.header(s.X, false)
	head := x.s
	if s.K == "tswitch" {
		head = primary(x) + ".(type)"
	}
	p.line(depth, s.Pos, "switch "+head+" {")
	hasDef := false
	for _, k := range s.Cases {
		var list []string
		switch {
		case k.Def:
			hasDef = true
			p.raw(depth, "default:")
			p.block(k.Body, depth+1)
			continue
		case s.K == "tswitch":
			for _, ts := range k.Types {
				list = append(list, p.typeString(p.typ(ts)))
			}
		default:
			for _, v := range k.Vals {
				list = append(list, p.expr(v, true).s)
			}
		}
		p.raw(depth, "case "+strings.Join(list, ", ")+":")
		p.block(k.Body, depth+1)
	}
	// A type switch always has its default clause; a value switch has
	// one where the IR gives it a body (the decoder's).
	if !hasDef && (s.K == "tswitch" || s.Else != nil) {
		p.raw(depth, "default:")
		p.block(s.Else, depth+1)
	}
	p.raw(depth, "}")
}

// ret is a return under the signature (the decoder's ret): a fallible
// function's success value gets a nil error; a value of the whole result
// list (a call's tuple, or a unit function's error) passes as it is.
func (p *irPrinter) ret(s *irStmt) string {
	res := p.sig.Results()
	fallible := irTextFallible(p.sig)
	if s.X == nil {
		switch {
		case res.Len() == 0:
			return "return"
		case fallible && res.Len() == 1:
			return "return nil"
		}
		p.fail("return without its result")
	}
	x := p.expr(s.X, true)
	if fallible && res.Len() == 2 && !types.Identical(x.t, res) {
		return "return " + x.s + ", nil"
	}
	return "return " + x.s
}

// expr prints x. typed reports a context that gives a nil or a zero its
// type (an operand of a comparison, an argument, an assigned value):
// there a nil is spelled nil, as the decoder's typed nil compiles
// (x != error(nil) compiles to an interface comparison, x != nil to a nil
// check), and elsewhere with its type, T(nil).
func (p *irPrinter) expr(x *irExpr, typed bool) gx {
	if x == nil {
		p.fail("missing expression")
	}
	switch x.K {
	case "local":
		v := p.local(x.I)
		return gx{v.Name(), v.Type(), 7}
	case "global":
		v, ok := p.pkg.Scope().Lookup(x.Name).(*types.Var)
		if !ok {
			p.fail("%s is not a variable of the package", x.Name)
		}
		return gx{x.Name, v.Type(), 7}
	case "result":
		if x.I < 0 || x.I >= p.sig.Results().Len() {
			p.fail("result %d out of range", x.I)
		}
		v := p.sig.Results().At(x.I)
		return gx{v.Name(), v.Type(), 7}
	case "recover":
		return gx{"recover()", types.Universe.Lookup("any").Type(), 7}
	case "const":
		return p.constant(x)
	case "bin":
		l, r := p.expr(x.X, true), p.expr(x.Y, true)
		prec := irBinPrec[x.Op]
		if prec == 0 {
			p.fail("operator %q", x.Op)
		}
		ls, rs := l.s, r.s
		if (x.Op == "<<" || x.Op == ">>") && x.Y.K == "const" && r.s == x.Y.V {
			// Go types an untyped shift count uint; the IR's constant has
			// its own type.
			rs = p.typeOperand(r.t) + "(" + rs + ")"
		}
		if l.prec < prec {
			ls = "(" + ls + ")"
		}
		if r.prec <= prec {
			rs = "(" + rs + ")"
		}
		typ := l.t
		if prec == 3 {
			typ = types.Typ[types.Bool]
		}
		return gx{ls + " " + x.Op + " " + rs, typ, prec}
	case "un", "deref", "addr":
		e := p.expr(x.X, true)
		op, typ := x.Op, e.t
		switch x.K {
		case "deref":
			pt, ok := typ.Underlying().(*types.Pointer)
			if !ok {
				p.fail("deref of %v", typ)
			}
			op, typ = "*", pt.Elem()
		case "addr":
			op, typ = "&", types.NewPointer(typ)
		}
		return gx{op + primary(e), typ, 6}
	case "call":
		fn, ok := p.pkg.Scope().Lookup(x.Name).(*types.Func)
		if !ok {
			p.fail("%s is not a function of the package", x.Name)
		}
		sig, targs := p.instance(fn.Type().(*types.Signature), x.T)
		return p.call(x, x.Name+targs, sig)
	case "pcall":
		pkg := p.imported(x.T)
		fn, ok := pkg.Scope().Lookup(x.Name).(*types.Func)
		if !ok {
			p.fail("%s is not a function of %q", x.Name, x.T)
		}
		sig, targs := p.instance(fn.Type().(*types.Signature), x.V)
		return p.call(x, p.qualifier(pkg)+"."+x.Name+targs, sig)
	case "mcall":
		recv := p.expr(x.X, false)
		sel, ok := types.LookupSelection(recv.t, false, p.pkg, x.Name)
		if !ok || sel.Kind() != types.MethodVal {
			p.fail("%v has no method %s", recv.t, x.Name)
		}
		return p.call(x, primary(recv)+"."+x.Name, sel.Type().(*types.Signature))
	case "vcall":
		fun := p.expr(x.X, false)
		sig, ok := fun.t.Underlying().(*types.Signature)
		if !ok {
			p.fail("call of a value of %v", fun.t)
		}
		return p.call(x, primary(fun), sig)
	case "field":
		recv := p.expr(x.X, false)
		sel, ok := types.LookupSelection(recv.t, false, p.pkg, x.Name)
		if !ok || sel.Kind() != types.FieldVal {
			p.fail("%v has no field %s", recv.t, x.Name)
		}
		return gx{primary(recv) + "." + x.Name, sel.Type(), 7}
	case "index":
		return p.index(x)
	case "slice":
		xs := p.expr(x.X, false)
		parts := make([]string, len(x.Args))
		for i, a := range x.Args {
			if a != nil {
				parts[i] = p.expr(a, true).s
			}
		}
		return gx{primary(xs) + "[" + strings.Join(parts, ":") + "]", xs.t, 7}
	case "len":
		return gx{"len(" + p.expr(x.X, false).s + ")", types.Typ[types.Int], 7}
	case "append":
		if len(x.Args) != 2 {
			p.fail("append of %d operands", len(x.Args))
		}
		xs, v := p.expr(x.Args[0], false), p.expr(x.Args[1], true)
		return gx{"append(" + xs.s + ", " + v.s + ")", xs.t, 7}
	case "builtin":
		return p.builtin(x)
	case "conv", "toiface":
		typ := p.typ(x.T)
		return gx{p.typeOperand(typ) + "(" + p.expr(x.X, x.K == "conv").s + ")", typ, 7}
	case "assert":
		e := p.expr(x.X, false)
		typ := p.typ(x.T)
		return gx{primary(e) + ".(" + p.typeString(typ) + ")", typ, 7}
	case "lit":
		return p.lit(x)
	case "nil", "zero":
		return p.zero(p.typ(x.T), typed)
	case "func":
		return p.funcLit(x)
	}
	p.fail("unknown expression %q", x.K)
	return gx{}
}

// irBinPrec is the precedence of each binary operator.
var irBinPrec = map[string]int{
	"||": 1, "&&": 2,
	"==": 3, "!=": 3, "<": 3, "<=": 3, ">": 3, ">=": 3,
	"+": 4, "-": 4, "|": 4, "^": 4,
	"*": 5, "/": 5, "%": 5, "<<": 5, ">>": 5, "&": 5, "&^": 5,
}

// index is x[i]: a map's value by its key, a slice's, an array's or a
// string's element.
func (p *irPrinter) index(x *irExpr) gx {
	xs, i := p.expr(x.X, false), p.expr(x.Y, true)
	var elem types.Type
	switch u := xs.t.Underlying().(type) {
	case *types.Map:
		elem = u.Elem()
	case *types.Slice:
		elem = u.Elem()
	case *types.Array:
		elem = u.Elem()
	case *types.Basic:
		elem = types.Typ[types.Byte]
	default:
		p.fail("index of %v", xs.t)
	}
	return gx{primary(xs) + "[" + i.s + "]", elem, 7}
}

// call is the call of fun (printed) of signature sig with x's arguments.
func (p *irPrinter) call(x *irExpr, fun string, sig *types.Signature) gx {
	args := make([]string, len(x.Args))
	for i, a := range x.Args {
		args[i] = p.expr(a, true).s
	}
	dots := ""
	if x.Op == "..." && sig.Variadic() {
		dots = "..."
	}
	var res types.Type
	switch n := sig.Results().Len(); {
	case n == 1:
		res = sig.Results().At(0).Type()
	case n > 1:
		res = sig.Results()
	}
	return gx{fun + "(" + strings.Join(args, ", ") + dots + ")", res, 7}
}

// instance is sig instantiated with the type arguments targs lists (Go's
// spelling, as the IR gives them), and their text: "" for no arguments.
func (p *irPrinter) instance(sig *types.Signature, targs string) (*types.Signature, string) {
	if targs == "" {
		return sig, ""
	}
	list := p.typeList(targs)
	inst, err := types.Instantiate(p.context(), sig, list, false)
	if err != nil {
		p.fail("[%s]: %v", targs, err)
	}
	names := make([]string, len(list))
	for i, t := range list {
		names[i] = p.typeString(t)
	}
	return inst.(*types.Signature), "[" + strings.Join(names, ", ") + "]"
}

func (p *irPrinter) context() *types.Context {
	if p.ctxt == nil {
		p.ctxt = types.NewContext()
	}
	return p.ctxt
}

func (p *irPrinter) builtin(x *irExpr) gx {
	var args []string
	var ts []types.Type
	for _, a := range x.Args {
		e := p.expr(a, false)
		args, ts = append(args, e.s), append(ts, e.t)
	}
	if len(ts) == 0 {
		p.fail("%s of no operands", x.Name)
	}
	var res types.Type
	dots := ""
	switch x.Name {
	case "append":
		res, dots = ts[0], "..."
	case "make":
		res = p.typ(x.T)
		args = append([]string{p.typeString(res)}, args...)
	case "cap", "copy":
		res = types.Typ[types.Int]
	case "min", "max":
		res = ts[0]
	case "clear", "delete":
	default:
		p.fail("the builtin %q", x.Name)
	}
	return gx{x.Name + "(" + strings.Join(args, ", ") + dots + ")", res, 7}
}

func (p *irPrinter) lit(x *irExpr) gx {
	typ := p.typ(x.T)
	var elems []string
	switch typ.Underlying().(type) {
	case *types.Struct:
		if len(x.Keys) != len(x.Elems) {
			p.fail("%v literal with %d keys of %d fields", typ, len(x.Keys), len(x.Elems))
		}
		for i, e := range x.Elems {
			elems = append(elems, x.Keys[i]+": "+p.expr(e, true).s)
		}
	case *types.Slice, *types.Array:
		for _, e := range x.Elems {
			elems = append(elems, p.expr(e, true).s)
		}
	case *types.Map:
		for i := 0; i+1 < len(x.Elems); i += 2 {
			elems = append(elems, p.expr(x.Elems[i], true).s+": "+p.expr(x.Elems[i+1], true).s)
		}
	default:
		p.fail("literal of %v", typ)
	}
	return gx{p.composite(p.typeString(typ) + "{" + strings.Join(elems, ", ") + "}"), typ, 7}
}

// zero is the zero value of typ, which the IR spells as a nil the decoder
// types (nilOf): T{} for a struct or an array, *new(T) for a type
// parameter, a constant for a basic type, else nil (T(nil) where nothing
// gives it its type).
func (p *irPrinter) zero(typ types.Type, typed bool) gx {
	if _, ok := typ.(*types.TypeParam); ok {
		return gx{"*new(" + p.typeString(typ) + ")", typ, 6}
	}
	switch u := typ.Underlying().(type) {
	case *types.Struct, *types.Array:
		return gx{p.composite(p.typeString(typ) + "{}"), typ, 7}
	case *types.Basic:
		var lit string
		var def types.Type
		switch {
		case u.Info()&types.IsBoolean != 0:
			lit, def = "false", types.Typ[types.Bool]
		case u.Info()&types.IsString != 0:
			lit, def = `""`, types.Typ[types.String]
		case u.Info()&types.IsNumeric != 0:
			lit, def = "0", types.Typ[types.Int]
		default:
			p.fail("zero of %v", typ)
		}
		if typed || types.Identical(typ, def) {
			return gx{lit, typ, 7}
		}
		return gx{p.typeOperand(typ) + "(" + lit + ")", typ, 7}
	}
	if typed {
		return gx{"nil", typ, 7}
	}
	return gx{p.typeOperand(typ) + "(nil)", typ, 7}
}

// constant is a "const": a bare literal when its type is the literal's
// default type, else a conversion.
func (p *irPrinter) constant(x *irExpr) gx {
	typ := p.typ(x.T)
	u, ok := typ.Underlying().(*types.Basic)
	if !ok {
		p.fail("constant of %v", typ)
	}
	lit := x.V
	var def types.Type
	switch {
	case u.Info()&types.IsBoolean != 0:
		def = types.Typ[types.Bool]
	case u.Info()&types.IsString != 0:
		if x.Op != "quoted" {
			lit = strconv.Quote(x.V)
		}
		def = types.Typ[types.String]
	case u.Info()&types.IsInteger != 0:
		def = types.Typ[types.Int]
	case u.Info()&types.IsFloat != 0:
		if !strings.ContainsAny(lit, ".eEn") {
			lit += ".0"
		}
		def = types.Typ[types.Float64]
	default:
		p.fail("constant of %v", typ)
	}
	switch {
	case !types.Identical(typ, def):
		return gx{p.typeOperand(typ) + "(" + lit + ")", typ, 7}
	case strings.HasPrefix(lit, "-"):
		return gx{"(" + lit + ")", typ, 7}
	}
	return gx{lit, typ, 7}
}

// funcLit is a function literal; its body returns under its own signature.
func (p *irPrinter) funcLit(x *irExpr) gx {
	typ := p.typ(x.T)
	sig, ok := typ.(*types.Signature)
	if !ok || sig.Params().Len() != len(x.Params) {
		p.fail("function literal of %v with %d parameters", typ, len(x.Params))
	}
	params := make([]string, len(x.Params))
	for i, li := range x.Params {
		params[i] = p.name(li) + " " + p.typeString(sig.Params().At(i).Type())
	}
	res := ""
	switch n := sig.Results().Len(); {
	case n == 1:
		res = " " + p.typeString(sig.Results().At(0).Type())
	case n > 1:
		list := make([]string, n)
		for i := range list {
			list[i] = p.typeString(sig.Results().At(i).Type())
		}
		res = " (" + strings.Join(list, ", ") + ")"
	}
	depth, outer, text, inHeader := p.depth, p.sig, p.b.String(), p.inHeader
	p.sig, p.inHeader = sig, false
	p.b.Reset()
	p.block(x.Body, depth+1)
	body := p.b.String()
	p.b.Reset()
	p.b.WriteString(text)
	p.sig, p.depth, p.inHeader = outer, depth, inHeader
	return gx{"func(" + strings.Join(params, ", ") + ")" + res + " {\n" + body + strings.Repeat("\t", depth) + "}", sig, 7}
}

// typ resolves the IR's spelling of a type, as the decoder does: struct{},
// struct{F T; …}, func(…) …, []T, *T, [N]T, map[K]V, a generic type's
// instance NAME[T1, T2], PATH.NAME (an exported type of the imported
// package PATH) and NAME (a type parameter of the function, then package
// scope, then universe).
func (p *irPrinter) typ(s string) types.Type {
	switch {
	case s == "struct{}":
		return types.NewStruct(nil, nil)
	case strings.HasPrefix(s, "struct{") && strings.HasSuffix(s, "}"):
		var fields []*types.Var
		for _, part := range irSplitTop(s[len("struct{"):len(s)-1], ';') {
			name, ft, ok := strings.Cut(strings.TrimSpace(part), " ")
			if !ok {
				p.fail("struct type %q", s)
			}
			fields = append(fields, types.NewField(0, p.pkg, name, p.typ(strings.TrimSpace(ft)), false))
		}
		return types.NewStruct(fields, nil)
	case strings.HasPrefix(s, "func("):
		params, rest, ok := irSplitList(s[len("func"):])
		if !ok {
			p.fail("function type %q", s)
		}
		var results []string
		switch rest = strings.TrimSpace(rest); {
		case rest == "":
		case strings.HasPrefix(rest, "("):
			if results, _, ok = irSplitList(rest); !ok {
				p.fail("function type %q", s)
			}
		default:
			results = []string{rest}
		}
		return types.NewSignatureType(nil, nil, nil, p.tuple(params), p.tuple(results), false)
	case strings.HasPrefix(s, "[]"):
		return types.NewSlice(p.typ(s[2:]))
	case strings.HasPrefix(s, "*"):
		return types.NewPointer(p.typ(s[1:]))
	}
	if rest, ok := strings.CutPrefix(s, "map["); ok {
		depth := 1
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case '[':
				depth++
			case ']':
				depth--
			}
			if depth == 0 {
				return types.NewMap(p.typ(rest[:i]), p.typ(rest[i+1:]))
			}
		}
		p.fail("map type %q", s)
	}
	if rest, ok := strings.CutPrefix(s, "["); ok {
		if i := strings.IndexByte(rest, ']'); i > 0 {
			if n, err := strconv.ParseInt(rest[:i], 10, 64); err == nil {
				return types.NewArray(p.typ(rest[i+1:]), n)
			}
		}
	}
	if i := strings.IndexByte(s, '['); i > 0 && strings.HasSuffix(s, "]") {
		named, ok := p.typ(s[:i]).(*types.Named)
		if !ok {
			p.fail("%s is not a generic type", s[:i])
		}
		t, err := types.Instantiate(p.context(), named.Origin(), p.typeList(s[i+1:len(s)-1]), false)
		if err != nil {
			p.fail("%s: %v", s, err)
		}
		return t
	}
	var obj types.Object
	if i := strings.LastIndex(s, "."); i >= 0 {
		obj = p.imported(s[:i]).Scope().Lookup(s[i+1:])
	} else {
		for _, tp := range p.tparams {
			if tp.Obj().Name() == s {
				return tp
			}
		}
		if obj = p.pkg.Scope().Lookup(s); obj == nil {
			obj = types.Universe.Lookup(s)
		}
	}
	tn, ok := obj.(*types.TypeName)
	if !ok {
		p.fail("%q is not a type", s)
	}
	return tn.Type()
}

func (p *irPrinter) tuple(list []string) *types.Tuple {
	var vars []*types.Var
	for _, e := range list {
		vars = append(vars, types.NewParam(0, p.pkg, "", p.typ(e)))
	}
	return types.NewTuple(vars...)
}

func (p *irPrinter) typeList(s string) []types.Type {
	var out []types.Type
	for _, part := range irSplitTop(s, ',') {
		out = append(out, p.typ(strings.TrimSpace(part)))
	}
	return out
}

// irSplitTop splits s at each sep outside brackets.
func irSplitTop(s string, sep byte) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
		case sep:
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// irSplitList splits a parenthesized list "(A, B) rest" into its elements
// and what follows it.
func irSplitList(s string) (list []string, rest string, ok bool) {
	if !strings.HasPrefix(s, "(") {
		return nil, "", false
	}
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				for _, part := range irSplitTop(s[1:i], ',') {
					if part = strings.TrimSpace(part); part != "" {
						list = append(list, part)
					}
				}
				return list, s[i+1:], true
			}
		}
	}
	return nil, "", false
}

// imported is the package of path that the IR's package imports.
func (p *irPrinter) imported(path string) *types.Package {
	for _, pkg := range p.pkg.Imports() {
		if pkg.Path() == path {
			return pkg
		}
	}
	p.fail("the package does not import %q", path)
	return nil
}

// qualifier is the file's name of pkg: the name it imports pkg by.
func (p *irPrinter) qualifier(pkg *types.Package) string {
	if pkg == p.pkg {
		return ""
	}
	if n, ok := p.file.imports[pkg.Path()]; ok {
		return n
	}
	p.fail("the file does not import %q", pkg.Path())
	return ""
}

func (p *irPrinter) typeString(t types.Type) string {
	return types.TypeString(t, p.qualifier)
}

// typeOperand is t spelled to be called (a conversion): a name as it is,
// a slice or a map type as it is, any other type in parentheses.
func (p *irPrinter) typeOperand(t types.Type) string {
	s := p.typeString(t)
	switch t.(type) {
	case *types.Named, *types.Alias, *types.Basic, *types.TypeParam:
		return s
	}
	if (strings.HasPrefix(s, "[]") || strings.HasPrefix(s, "map[")) && !strings.Contains(s, "func") && !strings.Contains(s, "chan") {
		return s
	}
	return "(" + s + ")"
}
