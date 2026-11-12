package lower

// EXPERIMENTAL (wf10 IR1 spike, owner q128 2026-11-11). Not the default
// build. LowerIR lowers a unit as Lower does, except that each function
// whose body is in the spike's subset gets a typed IR body instead of a Go
// body: its Go declaration has no body, and the IR file carries the body
// for the go1.27.0 compiler derivative's -ergoir entry
// (/home/exedev/Developer/personal/.worktrees/go-irspike). Every other
// function keeps its source body, and the reason is recorded. Source
// emission stays the reference: Lower never reads any of this.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/constant"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// IRFileName is the IR file LowerIR adds to the module.
const IRFileName = "ergoir.json"

// IRReport is what LowerIR did: the functions given IR bodies, and why the
// others kept their source bodies (reason -> count).
type IRReport struct {
	IR       []string       `json:"ir"`
	Fallback map[string]int `json:"fallback"`
	Hash     string         `json:"hash"`
}

// LowerIR is Lower with the experimental IR bodies. The report's Hash
// names the IR file's content. The build keys the main package's compile
// on the IR through one key only: the -ergoir flag names a copy of the IR
// file named by its content (irspike's stableIR), so main.go carries no
// hash of its own (batch-18 verify M2: a second key was redundant).
func LowerIR(u Unit) ([]File, IRReport, error) {
	if err := u.Validate(); err != nil {
		return nil, IRReport{}, err
	}
	l := newLowerer(u)
	l.ir = &irState{files: map[string]int{}, report: IRReport{Fallback: map[string]int{}}}
	code, err := l.program()
	if err != nil {
		return nil, IRReport{}, err
	}
	sort.Slice(l.ir.out.Funcs, func(i, j int) bool { return l.ir.out.Funcs[i].Name < l.ir.out.Funcs[j].Name })
	l.ir.out.V = 1
	data, err := json.Marshal(&l.ir.out)
	if err != nil {
		return nil, IRReport{}, err
	}
	sum := sha256.Sum256(data)
	l.ir.report.Hash = hex.EncodeToString(sum[:])
	files := append(moduleFiles(u.Go, code), l.assetFiles()...)
	files = append(files, File{Path: IRFileName, Data: data})
	sort.Strings(l.ir.report.IR)
	return files, l.ir.report, nil
}

// irState is the IR a LowerIR run collects.
type irState struct {
	out    irFile
	files  map[string]int
	report IRReport
}

// The IR format; the derivative's noder/ergoir.go reads the same shape.
type irFile struct {
	V     int      `json:"v"`
	Files []string `json:"files"`
	Funcs []irFunc `json:"funcs"`
}

type irFunc struct {
	Name   string    `json:"name"`
	Pos    irPos     `json:"pos"`
	End    irPos     `json:"end"`
	Locals []irLocal `json:"locals,omitempty"`
	Body   []*irStmt `json:"body"`
}

type irLocal struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Pos  irPos  `json:"pos"`
}

type irPos [3]int

type irStmt struct {
	K     string    `json:"k"`
	Pos   irPos     `json:"pos"`
	Local int       `json:"local,omitempty"`
	Key   *int      `json:"key,omitempty"`
	Val   *int      `json:"val,omitempty"`
	X     *irExpr   `json:"x,omitempty"`
	Then  []*irStmt `json:"then,omitempty"`
	Else  []*irStmt `json:"else,omitempty"`
	Body  []*irStmt `json:"body,omitempty"`
	End   irPos     `json:"end"`
	// ElsePos and ElseEnd bound an if's else block (DWARF scopes must
	// increase).
	ElsePos irPos `json:"epos"`
	ElseEnd irPos `json:"eend"`
}

type irExpr struct {
	K     string    `json:"k"`
	Pos   irPos     `json:"pos"`
	I     int       `json:"i,omitempty"`
	T     string    `json:"t,omitempty"`
	V     string    `json:"v,omitempty"`
	Op    string    `json:"op,omitempty"`
	X     *irExpr   `json:"x,omitempty"`
	Y     *irExpr   `json:"y,omitempty"`
	Name  string    `json:"name,omitempty"`
	Args  []*irExpr `json:"args,omitempty"`
	Elems []*irExpr `json:"elems,omitempty"`
	Keys  []string  `json:"keys,omitempty"`
}

// irUnsupported stops an IR body: the function keeps its source body.
type irUnsupported struct{ reason string }

func irNo(format string, args ...any) { panic(irUnsupported{fmt.Sprintf(format, args...)}) }

// irBuilder builds one function's IR body.
type irBuilder struct {
	fc     *funcCtx
	l      *lowerer
	st     *irState
	fn     *irFunc
	locals map[*check.Var]int
	next   int
	env    int // the environment's local, or -1 in a pure function
	result check.Type
}

// body tries an IR body for fd. params are the Ergo parameters in order; it
// reports whether fd got one. The caller has written fd's Go head.
func (st *irState) body(fc *funcCtx, fd *funcDecl, params []*check.Var) (ok bool) {
	f := fd.fn
	b := &irBuilder{fc: fc, l: fc.l, st: st, locals: map[*check.Var]int{}, env: -1, result: fc.frame.result}
	defer func() {
		if r := recover(); r != nil {
			u, isNo := r.(irUnsupported)
			if !isNo {
				panic(r)
			}
			st.report.Fallback[u.reason]++
			ok = false
		}
	}()
	switch {
	case !fc.directives:
		irNo("library body")
	case fd.fallible:
		irNo("fallible function")
	case fd.ptr:
		irNo("pointer receiver")
	case len(fc.witness) > 0:
		irNo("witness parameter")
	case f.Decl.Body == nil:
		irNo("no body")
	}
	// Go parameter order: receiver, environment, parameters (emitBody).
	for i, v := range params {
		if v == nil {
			irNo("unnamed parameter")
		}
		if i == 0 && fd.method != nil {
			b.locals[v] = b.next
			b.next++
			if !fd.pure {
				b.env = b.next
				b.next++
			}
			continue
		}
		if i == 0 && !fd.pure {
			b.env = b.next
			b.next++
		}
		b.locals[v] = b.next
		b.next++
		b.goType(v.Type, f.Decl)
	}
	if len(params) == 0 && !fd.pure {
		b.env = b.next
		b.next++
	}
	if b.result != nil {
		b.goType(b.result, f.Decl)
	}
	name := fd.name
	if fd.method != nil {
		name = fd.method.name + "." + fd.name
	}
	b.fn = &irFunc{Name: name, Pos: b.pos(f.Decl, f.Decl.Pos()), End: b.pos(f.Decl, f.Decl.Body.Rbrace)}
	if b.env >= 0 && frameFootprint(f.Pkg.Info, f.Decl).EntryPoll && !b.l.knownClosed(f) && fc.frame.polls {
		b.fn.Body = append(b.fn.Body, b.poll(f.Decl))
	}
	b.fn.Body = append(b.fn.Body, b.tail(f.Decl.Body.List, b.result != nil)...)
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.IR = append(st.report.IR, name)
	return true
}

// pos is n's position as the IR spells it.
func (b *irBuilder) pos(n syntax.Node, p syntax.Pos) irPos {
	if _, ok := b.l.sourceOf(n); !ok {
		irNo("node outside the program's sources")
	}
	_, path, line, col := b.l.at(n, p)
	i, ok := b.st.files[path]
	if !ok {
		i = len(b.st.out.Files)
		b.st.files[path] = i
		b.st.out.Files = append(b.st.out.Files, path)
	}
	return irPos{i, line, col}
}

// goType is t's Go spelling, for the types the subset admits: int, bool,
// string, records of those and slices of those. It declares the Go type.
func (b *irBuilder) goType(t check.Type, site syntax.Node) string {
	b.admit(t, 0)
	s := b.fc.goType(t)
	for _, r := range strings.TrimLeft(s, "[]") {
		if !(r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			irNo("Go type spelling %s", s)
		}
	}
	return s
}

func (b *irBuilder) admit(t check.Type, depth int) {
	if depth > 4 {
		irNo("deep type")
	}
	switch t := t.(type) {
	case *check.Basic:
		switch t.Kind {
		case check.Int, check.Bool, check.String:
			return
		}
		irNo("basic type %s", t.Name)
	case *check.Slice:
		b.admit(t.Elem, depth+1)
		return
	case *check.Named:
		if len(t.TArgs) > 0 || len(t.TParams) > 0 || t.Obj.Decl == nil {
			irNo("generic or declared-elsewhere type")
		}
		if _, ok := t.Obj.Decl.Spec.(*syntax.StructType); !ok {
			irNo("named type that is not a record")
		}
		s, ok := t.Underlying().(*check.Struct)
		if !ok {
			irNo("record without a struct")
		}
		for _, f := range s.Fields {
			b.admit(f.Type, depth+1)
		}
		return
	}
	irNo("type %T", t)
}

func (b *irBuilder) local(v *check.Var, id *syntax.Ident) int {
	if _, dup := b.locals[v]; dup {
		irNo("local declared twice")
	}
	i := b.next
	b.next++
	b.locals[v] = i
	b.fn.Locals = append(b.fn.Locals, irLocal{Name: v.Name, Type: b.goType(v.Type, id), Pos: b.pos(id, id.Pos())})
	return i
}

func (b *irBuilder) poll(site syntax.Node) *irStmt {
	p := b.pos(site, site.Pos())
	task := &irExpr{K: "field", Pos: p, Name: envTask, X: &irExpr{K: "local", Pos: p, I: b.env}}
	return &irStmt{K: "expr", Pos: p, X: &irExpr{K: "mcall", Pos: p, Name: "Check", X: task}}
}

// tail is a statement list whose last statement yields the function's
// result when ret is true.
func (b *irBuilder) tail(list []syntax.Stmt, ret bool) []*irStmt {
	var out []*irStmt
	for i, s := range list {
		last := i == len(list)-1
		if last && ret {
			out = append(out, b.yield(s)...)
			continue
		}
		out = append(out, b.stmt(s)...)
	}
	if ret && len(list) == 0 {
		irNo("empty body with a result")
	}
	return out
}

// yield is s in result position.
func (b *irBuilder) yield(s syntax.Stmt) []*irStmt {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		pre, x := b.value(s.X)
		return append(pre, &irStmt{K: "ret", Pos: b.pos(s.X, s.X.Pos()), X: x})
	case *syntax.ReturnStmt:
		return b.stmt(s)
	case *syntax.IfStmt:
		if s.Else == nil {
			irNo("if without else in result position")
		}
		return []*irStmt{b.ifStmt(s, true)}
	}
	irNo("result statement %T", s)
	return nil
}

func (b *irBuilder) ifStmt(s *syntax.IfStmt, ret bool) *irStmt {
	pre, cond := b.value(s.Cond)
	if len(pre) > 0 {
		irNo("operation call in a condition")
	}
	st := &irStmt{K: "if", Pos: b.pos(s, s.Pos()), X: cond, End: b.pos(s.Body, s.Body.Rbrace)}
	st.Then = b.tail(s.Body.List, ret)
	switch e := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		st.ElsePos, st.ElseEnd = b.pos(e, e.Lbrace), b.pos(e, e.Rbrace)
		st.Else = b.tail(e.List, ret)
	case *syntax.IfStmt:
		// The else block of an else-if wraps the chain to its last brace.
		last := syntax.Stmt(e)
		for {
			n, ok := last.(*syntax.IfStmt)
			if !ok || n.Else == nil {
				break
			}
			last = n.Else
		}
		end := e.Body.Rbrace
		switch l := last.(type) {
		case *syntax.BlockStmt:
			end = l.Rbrace
		case *syntax.IfStmt:
			end = l.Body.Rbrace
		}
		st.ElsePos, st.ElseEnd = b.pos(e, e.Pos()), b.pos(e, end)
		st.Else = []*irStmt{b.ifStmt(e, ret)}
	default:
		irNo("else %T", e)
	}
	return st
}

func (b *irBuilder) stmt(s syntax.Stmt) []*irStmt {
	switch s := s.(type) {
	case *syntax.DeclStmt:
		var out []*irStmt
		for _, spec := range s.Decl.Specs {
			if spec.Pattern != nil || len(spec.Names) != 1 || len(spec.Values) != 1 {
				irNo("let pattern")
			}
			id := spec.Names[0]
			v, ok := b.l.info.Defs[id].(*check.Var)
			if !ok || id.Name == "_" {
				irNo("let without a variable")
			}
			if p := b.l.info.Patterns[id]; p != nil && p.Kind != check.PatBind {
				irNo("let pattern test")
			}
			if b.fc.scalarSet[v] {
				irNo("scalar-form local")
			}
			pre, x := b.value(spec.Values[0])
			out = append(out, pre...)
			out = append(out, &irStmt{K: "def", Pos: b.pos(s.Decl, s.Decl.Pos()), Local: b.local(v, id), X: x})
		}
		return out
	case *syntax.AssignStmt:
		if len(s.Lhs) != 1 || len(s.Rhs) != 1 || s.Tok != syntax.ASSIGN {
			irNo("assignment form %s", s.Tok)
		}
		id, ok := s.Lhs[0].(*syntax.Ident)
		if !ok {
			irNo("assignment to a place")
		}
		v, ok := b.l.info.Uses[id].(*check.Var)
		i, known := b.locals[v]
		if !ok || !known {
			irNo("assignment to a non-local")
		}
		pre, x := b.value(s.Rhs[0])
		return append(pre, &irStmt{K: "set", Pos: b.pos(s, s.Pos()), Local: i, X: x})
	case *syntax.ReturnStmt:
		p := b.pos(s, s.Pos())
		if len(s.Results) == 0 {
			if b.result != nil {
				irNo("bare return")
			}
			return []*irStmt{{K: "ret", Pos: p}}
		}
		if len(s.Results) != 1 {
			irNo("several results")
		}
		pre, x := b.value(s.Results[0])
		return append(pre, &irStmt{K: "ret", Pos: p, X: x})
	case *syntax.ExprStmt:
		call, ok := unparen(s.X).(*syntax.CallExpr)
		if !ok {
			irNo("expression statement that is not a call")
		}
		pre, x := b.call(call, true)
		return append(pre, &irStmt{K: "expr", Pos: b.pos(s.X, s.X.Pos()), X: x})
	case *syntax.IfStmt:
		return []*irStmt{b.ifStmt(s, false)}
	case *syntax.ForStmt:
		if s.Cond == nil {
			irNo("for without a condition")
		}
		if b.env >= 0 {
			irNo("loop in an impure frame")
		}
		pre, cond := b.value(s.Cond)
		if len(pre) > 0 {
			irNo("operation call in a condition")
		}
		return []*irStmt{{K: "while", Pos: b.pos(s, s.Pos()), X: cond, Body: b.tail(s.Body.List, false), End: b.pos(s.Body, s.Body.Rbrace)}}
	case *syntax.RangeStmt:
		if b.env >= 0 {
			irNo("loop in an impure frame")
		}
		lp := b.l.info.Loops[s]
		if lp == nil || lp.Domain != check.LoopSlice {
			irNo("for-in over a non-slice")
		}
		pre, x := b.value(s.X)
		if len(pre) > 0 {
			irNo("operation call in a range")
		}
		st := &irStmt{K: "range", Pos: b.pos(s, s.Pos()), X: x, End: b.pos(s.Body, s.Body.Rbrace)}
		bind := func(e syntax.Expr) *int {
			if !bound(e) {
				return nil
			}
			id, ok := e.(*syntax.Ident)
			if !ok {
				irNo("for-in pattern")
			}
			v, ok := b.l.info.Defs[id].(*check.Var)
			if !ok {
				irNo("for-in binding without a variable")
			}
			i := b.local(v, id)
			return &i
		}
		st.Key = bind(s.Key)
		st.Val = bind(s.Value)
		st.Body = b.tail(s.Body.List, false)
		return []*irStmt{st}
	}
	irNo("statement %T", s)
	return nil
}

// value is x as an IR expression, after the statements (polls) that must
// run before it.
func (b *irBuilder) value(x syntax.Expr) ([]*irStmt, *irExpr) {
	if call, ok := unparen(x).(*syntax.CallExpr); ok {
		return b.call(call, false)
	}
	return nil, b.expr(x)
}

func (b *irBuilder) expr(x syntax.Expr) *irExpr {
	if _, ok := b.l.info.Implicit[x]; ok {
		irNo("implicit conversion")
	}
	p := b.pos(x, x.Pos())
	t, ok := b.l.info.Types[x]
	if !ok {
		irNo("expression without a type")
	}
	if v, ok := b.l.info.Values[x]; ok {
		gt := b.goType(t, x)
		switch v.Kind() {
		case constant.Int:
			n, exact := constant.Int64Val(v)
			if !exact {
				irNo("big integer constant")
			}
			return &irExpr{K: "const", Pos: p, T: gt, V: fmt.Sprint(n)}
		case constant.Bool:
			return &irExpr{K: "const", Pos: p, T: gt, V: fmt.Sprint(constant.BoolVal(v))}
		case constant.String:
			return &irExpr{K: "const", Pos: p, T: gt, V: constant.StringVal(v)}
		}
		irNo("constant kind %s", v.Kind())
	}
	switch x := x.(type) {
	case *syntax.ParenExpr:
		return b.expr(x.X)
	case *syntax.Ident:
		v, ok := b.l.info.Uses[x].(*check.Var)
		i, known := b.locals[v]
		if !ok || !known {
			irNo("name that is not a local")
		}
		return &irExpr{K: "local", Pos: p, I: i}
	case *syntax.BinaryExpr:
		op := x.Op.String()
		switch op {
		case "+", "-", "*", "/", "%", "==", "!=", "<", "<=", ">", ">=", "&&", "||":
		default:
			irNo("operator %s", op)
		}
		if _, basic := b.l.info.Types[x.X].(*check.Basic); !basic {
			irNo("operator %s on a non-basic type", op)
		}
		b.goType(b.l.info.Types[x.X], x)
		return &irExpr{K: "bin", Pos: b.pos(x, x.OpPos), Op: op, X: b.expr(x.X), Y: b.expr(x.Y)}
	case *syntax.UnaryExpr:
		op := x.Op.String()
		if op != "!" && op != "-" {
			irNo("unary %s", op)
		}
		return &irExpr{K: "un", Pos: p, Op: op, X: b.expr(x.X)}
	case *syntax.SelectorExpr:
		n, ok := b.l.info.Types[x.X].(*check.Named)
		if !ok {
			irNo("selector on a non-record")
		}
		b.goType(n, x)
		td := b.l.useType(b.l.typeDecl(n.Obj, x))
		field, ok := td.fieldName[x.Sel.Name]
		if !ok {
			irNo("selector that is not a field")
		}
		return &irExpr{K: "field", Pos: b.pos(x.Sel, x.Sel.Pos()), Name: field, X: b.expr(x.X)}
	case *syntax.IndexExpr:
		if len(x.Indices) != 1 {
			irNo("index form")
		}
		if _, ok := b.l.info.Types[x.X].(*check.Slice); !ok {
			irNo("index of a non-slice")
		}
		return &irExpr{K: "index", Pos: p, X: b.expr(x.X), Y: b.expr(x.Indices[0])}
	case *syntax.CompositeLit:
		if x.Base != nil || x.Type == nil {
			irNo("record update or elided literal type")
		}
		if _, ok := b.l.info.Spreads[x]; ok {
			irNo("spread")
		}
		lit := &irExpr{K: "lit", Pos: p, T: b.goType(t, x)}
		switch tt := t.(type) {
		case *check.Slice:
			for _, e := range x.Elts {
				if _, kv := e.(*syntax.KeyValueExpr); kv {
					irNo("keyed slice literal")
				}
				lit.Elems = append(lit.Elems, b.expr(e))
			}
		case *check.Named:
			td := b.l.useType(b.l.typeDecl(tt.Obj, x))
			s := tt.Underlying().(*check.Struct)
			if len(x.Elts) != len(s.Fields) {
				irNo("record literal without every field")
			}
			for _, e := range x.Elts {
				kv, ok := e.(*syntax.KeyValueExpr)
				if !ok {
					irNo("unkeyed record literal")
				}
				k, ok := kv.Key.(*syntax.Ident)
				if !ok {
					irNo("record key")
				}
				lit.Keys = append(lit.Keys, td.fieldName[k.Name])
				lit.Elems = append(lit.Elems, b.expr(kv.Value))
			}
		default:
			irNo("literal of %T", t)
		}
		return lit
	case *syntax.CallExpr:
		pre, e := b.call(x, false)
		if len(pre) > 0 {
			irNo("operation call inside an expression")
		}
		return e
	}
	irNo("expression %T", x)
	return nil
}

// call is a call x; stmt reports a call in statement position, where a
// call with no result may stand.
func (b *irBuilder) call(x *syntax.CallExpr, stmt bool) ([]*irStmt, *irExpr) {
	if x.Trailing != nil {
		irNo("trailing block")
	}
	c := b.l.info.Calls[x]
	if c == nil {
		irNo("call without a Calls entry")
	}
	if len(c.TypeArgs) > 0 || c.StartsTask {
		irNo("generic or task call")
	}
	if !stmt && b.l.info.Types[x] == nil {
		irNo("call without a value")
	}
	p := b.pos(x, x.Pos())
	switch c.Kind {
	case check.CallLen:
		if len(x.Args) != 1 {
			irNo("len form")
		}
		switch t := b.l.info.Types[x.Args[0]].(type) {
		case *check.Slice:
		case *check.Basic:
			if t.Kind != check.String {
				irNo("len of a non-slice")
			}
		default:
			irNo("len of a non-slice")
		}
		return nil, &irExpr{K: "len", Pos: p, X: b.expr(x.Args[0])}
	case check.CallFunc:
		f := c.Func
		if f == nil || f.Pkg != b.l.pkg || check.GoBuiltin(f) != "" || userExtern(f) {
			irNo("call of a function outside the package")
		}
		fd := b.l.useFunc(f, x)
		if !fd.pure || fd.fallible || fd.extern != "" || fd.ptr || len(b.l.ownedParams(f)) > 0 || len(witnessParams(f.Sig)) > 0 {
			irNo("call of an impure, fallible or special function")
		}
		args := x.Args
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
		var irArgs []*irExpr
		for _, a := range args {
			irArgs = append(irArgs, b.expr(a))
		}
		if fd.method != nil {
			return nil, &irExpr{K: "mcall", Pos: p, Name: fd.name, X: irArgs[0], Args: irArgs[1:]}
		}
		return nil, &irExpr{K: "call", Pos: p, Name: fd.name, Args: irArgs}
	case check.CallProviderOp:
		if b.env < 0 || c.Op == nil || len(rowElems(c.Op.Sig.Errors)) > 0 || b.fc.entersRelease(x) || b.l.tests != nil {
			irNo("operation call form")
		}
		sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
		if !ok {
			irNo("operation call spelling")
		}
		named, ok := b.l.info.Types[sel.X].(*check.Named)
		if !ok {
			irNo("capability without a named type")
		}
		env := &irExpr{K: "local", Pos: p, I: b.env}
		args := []*irExpr{{K: "field", Pos: p, Name: envTask, X: env}}
		for _, a := range x.Args {
			if _, isCall := unparen(a).(*syntax.CallExpr); isCall {
				irNo("call in an operation's argument")
			}
			args = append(args, b.expr(a))
		}
		var pre []*irStmt
		if b.fc.frame.polls {
			pre = append(pre, b.poll(x))
		}
		capField := &irExpr{K: "field", Pos: p, Name: b.l.envField(named), X: env}
		return pre, &irExpr{K: "mcall", Pos: p, Name: memberName(c.Op.Name), X: capField, Args: args}
	}
	irNo("call kind %d", c.Kind)
	return nil, nil
}
