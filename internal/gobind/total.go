package gobind

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/importer"
	"go/token"
	"go/types"
	"io"
	"os"
)

// Total functions (GOBIND-TOTAL; docs/spec/interop.md, Foreign facts;
// the format and rules are GO-NOPANIC's total-fact.md). A bound Go
// function is total when every call of it returns normally, for every
// value of every operand: no panic of its own and no run-time panic.
// Out-of-memory, stack exhaustion and non-termination are outside the
// fact. The IR drops the panic edge of such a call and the cleanup it
// needs, so a total line on a function that can panic is a miscompile:
// the proof is conservative. It runs over go/types on the package's own
// source, on each platform (meet keeps a fact every platform proves), to
// a least fixed point (recursion is never total), and admits a body,
// outside nothing (a function literal is refused), made only of
//
//  1. locals and parameters (read, assign, define, var), named results,
//     return, if/else, switch on a basic value, for with a basic
//     condition, range over an integer, blocks, labels, break, continue,
//     fallthrough, constants;
//  2. reads of package variables of a basic type (a write is refused);
//  3. unary + - ^ ! and binary + - * & | ^ &^ && || == != < <= > >= on
//     basic operands;
//  4. shifts by a constant count or by a count of an unsigned type;
//  5. / and %: on floats and complex numbers always, on integers only by
//     a non-zero constant divisor;
//  6. conversions between numeric types, or between types of one basic
//     underlying type;
//  7. calls of total functions and value-receiver methods of the same
//     package, non-generic, and of the builtins min and max, and len and
//     cap of a string or an array value.
//
// Everything else proves nothing: an index or slice expression, a map, a
// composite literal, a field selection, a dereference, an address, a type
// assertion or type switch, a channel, select, go, defer, goto, panic,
// recover, a function value or literal, a method value, an interface
// method, a call into another package, unsafe, a generic function, a
// range over anything but an integer, append, copy, clear, new, make, and
// a function with no Go body on the platform (assembly, a bodiless
// linkname). A compiler intrinsic (bits.Mul64) has its Go body, whose
// verdict holds for the intrinsic: Go's intrinsic keeps the specified
// panics (Div64's).

// totalFact is the Facts key of a total function. Parameter names are
// identifiers, so the key names none.
const totalFact = "(total)"

// totalPkg is one listed package, type-checked from its source.
type totalPkg struct {
	path  string
	files []*ast.File
	pkg   *types.Package
	info  *types.Info
}

// checkSource type-checks a listed package's parsed files against the
// export data of its dependencies (imp; importMap the package's vendor
// mapping), on the platform's sizes. A package that does not check
// proves nothing total.
func checkSource(fset *token.FileSet, path string, files []*ast.File, importMap map[string]string, arch string, imp types.Importer) *totalPkg {
	info := &types.Info{
		Types: map[ast.Expr]types.TypeAndValue{},
		Uses:  map[*ast.Ident]types.Object{},
		Defs:  map[*ast.Ident]types.Object{},
	}
	var failed bool
	conf := types.Config{
		Importer: mappedImporter{imp, importMap},
		Sizes:    types.SizesFor("gc", arch),
		Error:    func(error) { failed = true },
	}
	pkg, _ := conf.Check(path, fset, files, info)
	if failed || pkg == nil {
		return nil
	}
	return &totalPkg{path: path, files: files, pkg: pkg, info: info}
}

// mappedImporter imports a path as the package's ImportMap names it
// (std's vendored golang.org/x packages).
type mappedImporter struct {
	imp types.Importer
	m   map[string]string
}

func (i mappedImporter) Import(path string) (*types.Package, error) {
	if to, ok := i.m[path]; ok {
		path = to
	}
	return i.imp.Import(path)
}

// exportImporter reads the export data go list -export wrote.
func exportImporter(fset *token.FileSet, exports map[string]string) types.Importer {
	return importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		file := exports[path]
		if file == "" {
			return nil, fmt.Errorf("no export data for %s", path)
		}
		return os.Open(file)
	})
}

// total adds the totalFact of every total function of pkgs to out.
func total(pkgs []*totalPkg, out Facts) {
	for _, p := range pkgs {
		for target := range totalFuncs(p) {
			if out[target] == nil {
				out[target] = map[string]bool{}
			}
			out[target][totalFact] = true
		}
	}
}

// totalFuncs proves p's functions to a least fixed point and returns the
// total ones by target.
func totalFuncs(p *totalPkg) map[string]bool {
	decls := map[*types.Func]*ast.FuncDecl{}
	targets := map[*types.Func]string{}
	for _, f := range p.files {
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Body == nil || fd.Type.TypeParams != nil {
				continue
			}
			fn, ok := p.info.Defs[fd.Name].(*types.Func)
			if !ok {
				continue
			}
			target := p.path + "." + fd.Name.Name
			if fd.Recv != nil {
				rt := recvTypeName(fd.Recv.List[0].Type)
				if rt == "" || isPointer(fn.Type().(*types.Signature).Recv().Type()) {
					continue // generic, or a pointer receiver
				}
				target = p.path + "." + rt + "." + fd.Name.Name
			}
			decls[fn], targets[fn] = fd, target
		}
	}
	t := &totality{p: p, proven: map[*types.Func]bool{}}
	for changed := true; changed; {
		changed = false
		for fn, fd := range decls {
			if !t.proven[fn] && t.block(fd.Body.List) {
				t.proven[fn] = true
				changed = true
			}
		}
	}
	out := map[string]bool{}
	for fn := range t.proven {
		out[targets[fn]] = true
	}
	return out
}

// totality judges one package's statements and expressions.
type totality struct {
	p      *totalPkg
	proven map[*types.Func]bool
}

func (t *totality) block(list []ast.Stmt) bool {
	for _, s := range list {
		if !t.stmt(s) {
			return false
		}
	}
	return true
}

func (t *totality) stmt(s ast.Stmt) bool {
	switch s := s.(type) {
	case nil, *ast.EmptyStmt:
		return true
	case *ast.BlockStmt:
		return t.block(s.List)
	case *ast.LabeledStmt:
		return t.stmt(s.Stmt)
	case *ast.ExprStmt:
		c, ok := ast.Unparen(s.X).(*ast.CallExpr)
		return ok && t.call(c)
	case *ast.AssignStmt:
		return t.assign(s)
	case *ast.IncDecStmt:
		return t.local(s.X) && t.basic(s.X)
	case *ast.DeclStmt:
		g, ok := s.Decl.(*ast.GenDecl)
		if !ok {
			return false
		}
		for _, sp := range g.Specs {
			if vs, ok := sp.(*ast.ValueSpec); ok && g.Tok == token.VAR && !t.values(len(vs.Names), vs.Values) {
				return false
			}
		}
		return true
	case *ast.ReturnStmt:
		return t.values(len(s.Results), s.Results)
	case *ast.IfStmt:
		return t.stmt(s.Init) && t.cond(s.Cond) && t.block(s.Body.List) && t.stmt(s.Else)
	case *ast.SwitchStmt:
		if !t.stmt(s.Init) {
			return false
		}
		if s.Tag != nil && (!t.expr(s.Tag) || !t.basic(s.Tag)) {
			return false // an interface tag compares by its dynamic type
		}
		for _, c := range s.Body.List {
			for _, x := range c.(*ast.CaseClause).List {
				if !t.expr(x) || !t.basic(x) {
					return false
				}
			}
			if !t.block(c.(*ast.CaseClause).Body) {
				return false
			}
		}
		return true
	case *ast.BranchStmt:
		return s.Tok == token.BREAK || s.Tok == token.CONTINUE || s.Tok == token.FALLTHROUGH
	case *ast.ForStmt:
		return t.stmt(s.Init) && (s.Cond == nil || t.cond(s.Cond)) && t.stmt(s.Post) && t.block(s.Body.List)
	case *ast.RangeStmt:
		if s.Tok == token.ASSIGN && (s.Key != nil && !t.local(s.Key) || s.Value != nil && !t.local(s.Value)) {
			return false
		}
		return t.integer(s.X) && t.expr(s.X) && t.block(s.Body.List)
	}
	return false // go, defer, select, send, goto, a type switch
}

func (t *totality) cond(x ast.Expr) bool { return t.expr(x) && t.basic(x) }

// values reports n values from xs total: n expressions, or one call of
// a total function with n results.
func (t *totality) values(n int, xs []ast.Expr) bool {
	if len(xs) == 1 && n > 1 {
		c, ok := ast.Unparen(xs[0]).(*ast.CallExpr)
		return ok && t.call(c)
	}
	for _, x := range xs {
		if !t.expr(x) {
			return false
		}
	}
	return true
}

// assign judges lhs = rhs, lhs := rhs and lhs op= rhs, to locals only.
func (t *totality) assign(s *ast.AssignStmt) bool {
	for _, l := range s.Lhs {
		if !t.local(l) {
			return false
		}
	}
	switch s.Tok {
	case token.ASSIGN, token.DEFINE:
		return t.values(len(s.Lhs), s.Rhs)
	}
	if len(s.Lhs) != 1 || len(s.Rhs) != 1 {
		return false
	}
	op, ok := assignOps[s.Tok]
	return ok && t.expr(s.Rhs[0]) && t.binary(op, s.Lhs[0], s.Rhs[0])
}

var assignOps = map[token.Token]token.Token{
	token.ADD_ASSIGN: token.ADD, token.SUB_ASSIGN: token.SUB, token.MUL_ASSIGN: token.MUL,
	token.QUO_ASSIGN: token.QUO, token.REM_ASSIGN: token.REM, token.AND_ASSIGN: token.AND,
	token.OR_ASSIGN: token.OR, token.XOR_ASSIGN: token.XOR, token.SHL_ASSIGN: token.SHL,
	token.SHR_ASSIGN: token.SHR, token.AND_NOT_ASSIGN: token.AND_NOT,
}

// local reports x _ or a local variable (a parameter or result too).
func (t *totality) local(x ast.Expr) bool {
	id, ok := ast.Unparen(x).(*ast.Ident)
	if !ok {
		return false
	}
	if id.Name == "_" {
		return true
	}
	v, ok := t.obj(id).(*types.Var)
	return ok && v.Parent() != t.p.pkg.Scope() && !v.IsField()
}

func (t *totality) obj(id *ast.Ident) types.Object {
	if o := t.p.info.Uses[id]; o != nil {
		return o
	}
	return t.p.info.Defs[id]
}

func (t *totality) typ(x ast.Expr) types.Type {
	return t.p.info.Types[x].Type
}

func isPointer(typ types.Type) bool {
	if typ == nil {
		return true
	}
	_, ok := typ.Underlying().(*types.Pointer)
	return ok
}

// basicOf is x's basic underlying type, or nil.
func (t *totality) basicOf(x ast.Expr) *types.Basic {
	typ := t.typ(x)
	if typ == nil {
		return nil
	}
	b, _ := typ.Underlying().(*types.Basic)
	if b != nil && (b.Kind() == types.UnsafePointer || b.Kind() == types.UntypedNil) {
		return nil
	}
	return b
}

func (t *totality) basic(x ast.Expr) bool { return t.basicOf(x) != nil }

func (t *totality) integer(x ast.Expr) bool {
	b := t.basicOf(x)
	return b != nil && b.Info()&types.IsInteger != 0
}

// expr reports that evaluating x cannot panic.
func (t *totality) expr(x ast.Expr) bool {
	if tv, ok := t.p.info.Types[x]; ok && tv.Value != nil {
		return true // a constant
	}
	switch x := x.(type) {
	case *ast.ParenExpr:
		return t.expr(x.X)
	case *ast.Ident:
		switch o := t.obj(x).(type) {
		case *types.Var:
			if o.Parent() == t.p.pkg.Scope() {
				return t.basic(x) // a package variable
			}
			return !o.IsField()
		case *types.Const:
			return true
		}
		return false // a function value, nil
	case *ast.SelectorExpr:
		return false // a field, a method value, another package's name
	case *ast.UnaryExpr:
		switch x.Op {
		case token.ADD, token.SUB, token.XOR, token.NOT:
			return t.basic(x.X) && t.expr(x.X)
		}
		return false // &x, <-ch
	case *ast.BinaryExpr:
		return t.expr(x.X) && t.expr(x.Y) && t.binary(x.Op, x.X, x.Y)
	case *ast.CallExpr:
		return t.call(x)
	}
	return false
}

// binary judges x op y once both are total.
func (t *totality) binary(op token.Token, x, y ast.Expr) bool {
	if !t.basic(x) || !t.basic(y) {
		return false
	}
	switch op {
	case token.LAND, token.LOR, token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ,
		token.ADD, token.SUB, token.MUL, token.AND, token.OR, token.XOR, token.AND_NOT:
		return true
	case token.SHL, token.SHR:
		// A negative count panics: a constant one does not compile.
		return t.p.info.Types[y].Value != nil || t.basicOf(y).Info()&types.IsUnsigned != 0
	case token.QUO, token.REM:
		if t.basicOf(x).Info()&types.IsInteger == 0 {
			return true // a float or complex division never panics
		}
		v := t.p.info.Types[y].Value
		return v != nil && constant.Sign(v) != 0
	}
	return false
}

// call judges a conversion, a builtin's call, or a call of a total
// function or value-receiver method of the package.
func (t *totality) call(c *ast.CallExpr) bool {
	fun := ast.Unparen(c.Fun)
	if tv := t.p.info.Types[fun]; tv.IsType() {
		return len(c.Args) == 1 && t.expr(c.Args[0]) && conversion(t.typ(c.Args[0]), tv.Type)
	}
	if c.Ellipsis.IsValid() {
		return false // xs... passes a slice
	}
	for _, a := range c.Args {
		if !t.expr(a) {
			return false
		}
	}
	switch f := fun.(type) {
	case *ast.Ident:
		switch o := t.obj(f).(type) {
		case *types.Builtin:
			return t.builtin(o.Name(), c)
		case *types.Func:
			// proven holds only the package's own functions and
			// value-receiver methods: another package's proves nothing.
			return t.proven[o]
		}
	case *ast.SelectorExpr:
		// x.M() of a value-receiver method of the package on a value x
		// of its receiver type (a pointer x would be dereferenced).
		fn, ok := t.obj(f.Sel).(*types.Func)
		if !ok || !t.proven[fn] {
			return false
		}
		recv := fn.Type().(*types.Signature).Recv()
		return recv != nil && types.Identical(t.typ(f.X), recv.Type()) && t.expr(f.X)
	}
	return false
}

func (t *totality) builtin(name string, c *ast.CallExpr) bool {
	switch name {
	case "min", "max":
		for _, a := range c.Args {
			if !t.basic(a) {
				return false
			}
		}
		return true
	case "len", "cap":
		if len(c.Args) != 1 {
			return false
		}
		if t.basicOf(c.Args[0]) != nil {
			return t.basicOf(c.Args[0]).Info()&types.IsString != 0
		}
		typ := t.typ(c.Args[0])
		if typ == nil {
			return false
		}
		_, array := typ.Underlying().(*types.Array)
		return array
	}
	return false // panic, recover, make, new, append, copy, delete, close, clear, print, real, imag, complex
}

// conversion reports a conversion from to to between numeric types, or
// between types of one basic underlying type.
func conversion(from, to types.Type) bool {
	if from == nil || to == nil {
		return false
	}
	f, ok1 := from.Underlying().(*types.Basic)
	g, ok2 := to.Underlying().(*types.Basic)
	if !ok1 || !ok2 || f.Kind() == types.UnsafePointer || g.Kind() == types.UnsafePointer {
		return false
	}
	if f.Info()&types.IsNumeric != 0 && g.Info()&types.IsNumeric != 0 {
		return true
	}
	return types.Identical(types.Default(f), g)
}
