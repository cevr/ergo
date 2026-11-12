package derive

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Plan evaluation (patterns.md, Derivation 2, Type plans, Plan functions,
// Budgets 1). The evaluator walks the plan expression of a derive and the
// bodies of the plan functions it calls. It resolves each name by scope —
// a parameter or let, a declaration of the package, an import, and only
// then a predeclared constructor — and never by a package path or a std
// name, so equal plans derive equal bundles however they were built. It
// calls no Go, does no I/O and reads no clock.

// Limits of one derive's evaluation (Budgets 1).
type Limits struct{ Steps, Depth, Bytes, Nodes int }

// DefaultLimits are the plan budgets of patterns.md, Budgets 1.
func DefaultLimits() Limits { return Limits{100000, 64, 65536, 4096} }

// origin is the source position of one byte or value: its file and offset.
type origin struct {
	file *syntax.File
	pos  syntax.Pos
}

// str is a string value and the origin of each of its bytes.
type str struct {
	s  string
	at []origin
}

// plan is a TypePlan or FieldPlan value: one node of the closed algebra.
type plan struct {
	kind   string // Pattern, Refine, Of, Record, Union, Optional, List, Field
	text   str    // Pattern
	base   *plan  // Refine, Optional, List, Field
	lo, hi int64  // Refine
	typ    syntax.Expr
	file   *syntax.File // the file of typ (Of)
	fields []*plan      // Record, Union: Field plans
	name   str          // Field
	at     origin       // the constructor call
}

// evalError is a refusal at a source position.
type evalError struct {
	at  origin
	end syntax.Pos
	msg string
}

// planFunc is a plan function: its declaration, file and package.
type planFunc struct {
	decl *syntax.FuncDecl
	file *syntax.File
	pkg  *Package
}

type evaluator struct {
	limits                     Limits
	steps, depth, bytes, nodes int
	running                    string // the plan function being evaluated, for budget messages
	typeName                   string // the derived type, for messages
}

type frame struct {
	vars map[string]any
	file *syntax.File
	pkg  *Package
}

func (e *evaluator) fail(at origin, end syntax.Pos, format string, args ...any) {
	panic(&evalError{at, end, fmt.Sprintf(format, args...)})
}

func (e *evaluator) charge(at origin, end syntax.Pos, what *int, by, limit int, name string) {
	*what += by
	if *what > limit {
		in := ""
		if e.running != "" {
			in = " in " + e.running
		}
		e.fail(at, end, "the derive of %s stopped at %d %s%s; a plan is built in bounded steps", e.typeName, limit, name, in)
	}
}

// predeclared names the constructors of the closed algebra.
var predeclared = map[string]bool{"Pattern": true, "Refine": true, "Of": true, "Record": true, "Union": true, "Optional": true, "List": true, "Field": true}

func (e *evaluator) eval(f *frame, x syntax.Expr) any {
	at := origin{f.file, x.Pos()}
	e.charge(at, x.End(), &e.steps, 1, e.limits.Steps, "evaluation steps")
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return e.eval(f, x.X)
	case *syntax.BasicLit:
		switch x.Kind {
		case syntax.STRING:
			return e.literal(f.file, x)
		case syntax.INT:
			v, err := strconv.ParseInt(strings.ReplaceAll(x.Value, "_", ""), 0, 64)
			if err != nil {
				e.fail(at, x.End(), "the integer %s is too large for a plan", x.Value)
			}
			return v
		}
	case *syntax.InterpolatedString:
		return e.interpolate(f, x)
	case *syntax.Ident:
		if v, ok := f.vars[x.Name]; ok {
			return v
		}
		if x.Name == "true" || x.Name == "false" {
			return x.Name == "true"
		}
		e.fail(at, x.End(), "the derive of %s needs a TypePlan built from constants; %s reads a value only known at run time", e.typeName, x.Name)
	case *syntax.UnaryExpr:
		v := e.eval(f, x.X)
		switch v := v.(type) {
		case int64:
			if x.Op == syntax.SUB {
				return -v
			}
			if x.Op == syntax.ADD {
				return v
			}
		case bool:
			if x.Op == syntax.NOT {
				return !v
			}
		}
	case *syntax.BinaryExpr:
		return e.binary(f, x)
	case *syntax.CallExpr:
		return e.call(f, x)
	case *syntax.CompositeLit:
		if _, ok := x.Type.(*syntax.ArrayType); ok {
			var out []any
			for _, elt := range x.Elts {
				out = append(out, e.eval(f, elt))
			}
			return out
		}
	}
	e.fail(at, x.End(), "the derive of %s needs a TypePlan built from constants; this expression is not evaluated at check time", e.typeName)
	return nil
}

func (e *evaluator) binary(f *frame, x *syntax.BinaryExpr) any {
	at := origin{f.file, x.OpPos}
	if x.Op == syntax.LAND || x.Op == syntax.LOR {
		l, ok := e.eval(f, x.X).(bool)
		if !ok {
			e.fail(at, x.End(), "%s needs booleans", x.Op)
		}
		if l == (x.Op == syntax.LOR) {
			return l
		}
		r, ok := e.eval(f, x.Y).(bool)
		if !ok {
			e.fail(at, x.End(), "%s needs booleans", x.Op)
		}
		return r
	}
	l, r := e.eval(f, x.X), e.eval(f, x.Y)
	switch l := l.(type) {
	case str:
		r, ok := r.(str)
		if !ok {
			break
		}
		switch x.Op {
		case syntax.ADD:
			e.charge(at, x.End(), &e.bytes, len(l.s)+len(r.s), e.limits.Bytes, "bytes of strings built")
			return str{l.s + r.s, append(append([]origin{}, l.at...), r.at...)}
		case syntax.EQL:
			return l.s == r.s
		case syntax.NEQ:
			return l.s != r.s
		}
	case int64:
		r, ok := r.(int64)
		if !ok {
			break
		}
		switch x.Op {
		case syntax.ADD:
			return l + r
		case syntax.SUB:
			return l - r
		case syntax.MUL:
			return l * r
		case syntax.QUO, syntax.REM:
			if r == 0 {
				e.fail(at, x.End(), "a plan divides by zero")
			}
			if x.Op == syntax.QUO {
				return l / r
			}
			return l % r
		case syntax.EQL:
			return l == r
		case syntax.NEQ:
			return l != r
		case syntax.LSS:
			return l < r
		case syntax.LEQ:
			return l <= r
		case syntax.GTR:
			return l > r
		case syntax.GEQ:
			return l >= r
		}
	}
	e.fail(at, x.End(), "%s is not evaluated on these operands at check time", x.Op)
	return nil
}

// literal decodes a string literal, keeping each byte's source offset:
// an escape's bytes are at its backslash, a raw string's at themselves.
func (e *evaluator) literal(file *syntax.File, x *syntax.BasicLit) str {
	src := x.Value
	if strings.HasPrefix(src, "`") {
		body := src[1 : len(src)-1]
		out := str{s: body}
		for i := range body {
			out.at = append(out.at, origin{file, x.ValuePos + 1 + syntax.Pos(i)})
		}
		return out
	}
	return e.decode(file, src[1:len(src)-1], x.ValuePos+1)
}

// decode decodes the body of an interpreted string that starts at base.
func (e *evaluator) decode(file *syntax.File, body string, base syntax.Pos) str {
	var out str
	add := func(s string, at int) {
		out.s += s
		for range len(s) {
			out.at = append(out.at, origin{file, base + syntax.Pos(at)})
		}
	}
	for i := 0; i < len(body); {
		if body[i] != '\\' {
			add(body[i:i+1], i)
			i++
			continue
		}
		v, multibyte, tail, err := strconv.UnquoteChar(body[i:], '"')
		if err != nil {
			e.fail(origin{file, base + syntax.Pos(i)}, base+syntax.Pos(i+1), "this escape is not decoded at check time")
		}
		if multibyte {
			add(string(v), i)
		} else {
			add(string([]byte{byte(v)}), i)
		}
		i = len(body) - len(tail)
	}
	return out
}

func (e *evaluator) interpolate(f *frame, x *syntax.InterpolatedString) str {
	var out str
	for i, t := range x.Texts {
		v := t.Value
		start := 1 // after the opening quote or the closing )
		end := len(v)
		if i < len(x.Texts)-1 {
			end -= 2 // before \(
		} else {
			end-- // before the closing quote
		}
		part := e.decode(f.file, v[start:end], t.ValuePos+syntax.Pos(start))
		out.s += part.s
		out.at = append(out.at, part.at...)
		if i < len(x.Exprs) {
			at := origin{f.file, x.Exprs[i].Pos()}
			switch v := e.eval(f, x.Exprs[i]).(type) {
			case str:
				out.s += v.s
				out.at = append(out.at, v.at...)
			case int64:
				s := strconv.FormatInt(v, 10)
				out.s += s
				for range len(s) {
					out.at = append(out.at, at)
				}
			default:
				e.fail(at, x.Exprs[i].End(), "only a string or an integer is interpolated in a plan")
			}
		}
	}
	e.charge(origin{f.file, x.Pos()}, x.End(), &e.bytes, len(out.s), e.limits.Bytes, "bytes of strings built")
	return out
}

// callee resolves the function a call names: a plan function of the
// package or of an import, or a predeclared constructor that no
// declaration in scope hides.
func (e *evaluator) callee(f *frame, fun syntax.Expr) (*planFunc, string) {
	switch fun := fun.(type) {
	case *syntax.Ident:
		if _, ok := f.vars[fun.Name]; ok {
			return nil, ""
		}
		if pf, ok := f.pkg.funcs[fun.Name]; ok {
			return pf, ""
		}
		if f.pkg.names[fun.Name] || f.pkg.imported(f.file, fun.Name) {
			return nil, ""
		}
		if predeclared[fun.Name] {
			return nil, fun.Name
		}
	case *syntax.IndexExpr:
		if id, ok := fun.X.(*syntax.Ident); ok && id.Name == "Of" && len(fun.Indices) == 1 {
			if _, c := e.callee(f, id); c == "Of" {
				return nil, "Of"
			}
		}
	case *syntax.SelectorExpr:
		id, ok := fun.X.(*syntax.Ident)
		if !ok {
			return nil, ""
		}
		if _, local := f.vars[id.Name]; local {
			return nil, ""
		}
		if path, ok := f.pkg.importPath(f.file, id.Name); ok && f.pkg.lookup != nil {
			if other := f.pkg.lookup(path); other != nil && isExported(fun.Sel.Name) {
				if pf, ok := other.funcs[fun.Sel.Name]; ok {
					f.pkg.used[f.file] = append(f.pkg.used[f.file], id.Name)
					return pf, ""
				}
			}
		}
	}
	return nil, ""
}

func isExported(name string) bool { return name != "" && unicode.IsUpper(rune(name[0])) }

func (e *evaluator) call(f *frame, x *syntax.CallExpr) any {
	at := origin{f.file, x.Pos()}
	pf, ctor := e.callee(f, x.Fun)
	if pf == nil && ctor == "" {
		name := exprText(f.file, x.Fun)
		e.fail(at, x.End(), "a plan function runs at check time; %s is not a plan function or a plan constructor", name)
	}
	args := make([]any, len(x.Args))
	for i, a := range x.Args {
		args[i] = e.eval(f, a)
	}
	if pf != nil {
		return e.apply(at, x.End(), pf, args)
	}
	e.charge(at, x.End(), &e.nodes, 1, e.limits.Nodes, "plan nodes")
	p := &plan{kind: ctor, at: at}
	want := func(n int) {
		if len(args) != n {
			e.fail(at, x.End(), "%s takes %d arguments", ctor, n)
		}
	}
	planArg := func(i int) *plan {
		v, ok := args[i].(*plan)
		if !ok || v.kind == "Field" {
			e.fail(origin{f.file, x.Args[i].Pos()}, x.Args[i].End(), "%s needs a TypePlan here", ctor)
		}
		return v
	}
	switch ctor {
	case "Pattern":
		want(1)
		s, ok := args[0].(str)
		if !ok {
			e.fail(at, x.End(), "Pattern takes the pattern's text, a string")
		}
		p.text = s
	case "Refine":
		want(3)
		p.base = planArg(0)
		lo, ok1 := args[1].(int64)
		hi, ok2 := args[2].(int64)
		if !ok1 || !ok2 {
			e.fail(at, x.End(), "Refine takes a base plan and two integers")
		}
		p.lo, p.hi = lo, hi
	case "Of":
		want(0)
		p.typ, p.file = x.Fun.(*syntax.IndexExpr).Indices[0], f.file
	case "Optional", "List":
		want(1)
		p.base = planArg(0)
	case "Field":
		want(2)
		s, ok := args[0].(str)
		if !ok {
			e.fail(at, x.End(), "Field takes a name, a string, and a plan")
		}
		p.name, p.base = s, planArg(1)
	case "Record", "Union":
		want(1)
		list, ok := args[0].([]any)
		if !ok {
			e.fail(at, x.End(), "%s takes a []FieldPlan", ctor)
		}
		for i, v := range list {
			fp, ok := v.(*plan)
			if !ok || fp.kind != "Field" {
				e.fail(at, x.End(), "element %d of %s is not a Field", i, ctor)
			}
			p.fields = append(p.fields, fp)
		}
	}
	return p
}

// apply calls the plan function pf with the evaluated args.
func (e *evaluator) apply(at origin, end syntax.Pos, pf *planFunc, args []any) any {
	e.depth++
	defer func() { e.depth-- }()
	if e.depth > e.limits.Depth {
		e.fail(at, end, "the derive of %s stopped at a call depth of %d in %s; a plan is built in bounded steps", e.typeName, e.limits.Depth, pf.decl.Name.Name)
	}
	saved := e.running
	e.running = pf.pkg.qualified(pf.decl.Name.Name)
	defer func() { e.running = saved }()
	fr := &frame{vars: map[string]any{}, file: pf.file, pkg: pf.pkg}
	var names []*syntax.Ident
	for _, field := range pf.decl.Type.Params.List {
		names = append(names, field.Names...)
	}
	if len(names) != len(args) {
		e.fail(at, end, "%s takes %d arguments", pf.decl.Name.Name, len(names))
	}
	for i, n := range names {
		fr.vars[n.Name] = args[i]
	}
	v, _ := e.block(fr, pf.decl.Body)
	return v
}

// block evaluates a plan function's block: lets, ifs and a final value.
func (e *evaluator) block(f *frame, b *syntax.BlockStmt) (any, bool) {
	var last any
	for _, s := range b.List {
		switch s := s.(type) {
		case *syntax.DeclStmt:
			for _, spec := range s.Decl.Specs {
				if len(spec.Names) != 1 || len(spec.Values) != 1 || s.Decl.Mut.IsValid() {
					e.fail(origin{f.file, s.Decl.Var}, s.Decl.Var, "a plan function binds one name with let")
				}
				f.vars[spec.Names[0].Name] = e.eval(f, spec.Values[0])
			}
			last = nil
		case *syntax.ExprStmt:
			last = e.eval(f, s.X)
		case *syntax.ReturnStmt:
			if len(s.Results) != 1 {
				e.fail(origin{f.file, s.Return}, s.Return, "a plan function returns one plan")
			}
			return e.eval(f, s.Results[0]), true
		case *syntax.IfStmt:
			cond, ok := e.eval(f, s.Cond).(bool)
			if !ok {
				e.fail(origin{f.file, s.If}, s.Cond.End(), "an if needs a boolean")
			}
			var branch *syntax.BlockStmt
			switch {
			case cond:
				branch = s.Body
			case s.Else != nil:
				if elif, ok := s.Else.(*syntax.IfStmt); ok {
					branch = &syntax.BlockStmt{List: []syntax.Stmt{elif}}
				} else {
					branch = s.Else.(*syntax.BlockStmt)
				}
			}
			if branch == nil {
				last = nil
				continue
			}
			inner := &frame{vars: map[string]any{}, file: f.file, pkg: f.pkg}
			for k, v := range f.vars {
				inner.vars[k] = v
			}
			v, ret := e.block(inner, branch)
			if ret {
				return v, true
			}
			last = v
		default:
			e.fail(origin{f.file, s.Pos()}, s.End(), "this statement is not evaluated in a plan function: write let, if and a final plan")
		}
	}
	return last, false
}

func exprText(file *syntax.File, x syntax.Expr) string {
	if file == nil || int(x.End()) > len(file.Src) || !x.Pos().IsValid() {
		return "this callee"
	}
	return file.Src[x.Pos():x.End()]
}
