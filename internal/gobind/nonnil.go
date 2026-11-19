package gobind

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// Non-nil results (GOBIND-NIL; docs/spec/interop.md, Type mapping). A Go
// function whose results are (*T, error) binds its pointer as go.Ref[T],
// a value the caller reads without a check, only where the binder proves
// from the Go source that the pointer is not nil whenever the error is;
// otherwise as Option[go.Ref[T]]. Go returns nil with no error where it
// means "none" (http.ProxyFromEnvironment for a request that takes no
// proxy), so the type alone promises nothing.
//
// The proof is syntactic and conservative, over the bodies of every
// function of the packages Derive lists, on each platform, to a least
// fixed point. Each return statement outside a function literal must be
// one of:
//
//   - a pointer that is &x (&T{...} too), new(T), the call of a proven
//     function of one pointer result, or a local variable every
//     assignment of which is one of these or `x, err := g(...)`, g
//     proven, followed at once by an `if err != nil` that returns;
//   - an error that is errors.New(...), fmt.Errorf(...), a composite
//     literal or its address, the variable of an enclosing
//     `if err != nil` (or an && holding it) whose body does not assign
//     it, or a package variable of a listed package set once to one of
//     those and assigned by none of its functions;
//   - `return g(...)`, g a proven (*T, error) function of a listed
//     package (a function of the package, a method of the receiver, or
//     pkg.F).
//
// Every other form (a function value, an interface method, a named
// result's bare return, a body only assembly holds) proves nothing, so
// its result is an Option. A function with no return (its body panics)
// never returns nil.

// nonNilResult is the Facts key of a (*T, error) function's proven
// pointer result. Parameter names are identifiers, so the key names none.
const nonNilResult = "(result)"

// nnPkg is one listed package's parsed Go files.
type nnPkg struct {
	path  string
	files []*ast.File
}

// nnFunc is one function or method whose first result is a pointer.
type nnFunc struct {
	pkg     string
	imports map[string]string // the file's import names to paths
	fd      *ast.FuncDecl
	fails   bool   // its second and last result is error
	recv    string // the receiver's name, or ""
	recvTyp string // the receiver's type name, or ""
}

func (fn *nnFunc) target() string {
	if fn.recvTyp != "" {
		return fn.pkg + "." + fn.recvTyp + "." + fn.fd.Name.Name
	}
	return fn.pkg + "." + fn.fd.Name.Name
}

// nonNil adds the nonNilResult fact of every proven (*T, error) function
// of pkgs to out.
func nonNil(pkgs []nnPkg, out Facts) {
	funcs := map[string]*nnFunc{}
	sentinels := map[string]bool{} // path.Name of a package error variable set once
	for _, p := range pkgs {
		assigned := map[string]bool{}
		candidates := map[string]bool{}
		for _, f := range p.files {
			imports := fileImports(f)
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.GenDecl:
					if d.Tok != token.VAR {
						continue
					}
					for _, s := range d.Specs {
						vs := s.(*ast.ValueSpec)
						if len(vs.Values) != len(vs.Names) {
							continue
						}
						for i, n := range vs.Names {
							if errorValue(vs.Values[i], imports) {
								candidates[n.Name] = true
							}
						}
					}
				case *ast.FuncDecl:
					if d.Body != nil {
						ast.Inspect(d.Body, func(n ast.Node) bool {
							switch n := n.(type) {
							case *ast.AssignStmt:
								for _, l := range n.Lhs {
									if id, ok := l.(*ast.Ident); ok {
										assigned[id.Name] = true
									}
								}
							case *ast.UnaryExpr:
								if id, ok := ast.Unparen(n.X).(*ast.Ident); ok && n.Op == token.AND {
									assigned[id.Name] = true
								}
							}
							return true
						})
					}
					if fn := pointerFunc(p.path, d, imports); fn != nil {
						funcs[fn.target()] = fn
					}
				}
			}
		}
		for name := range candidates {
			if !assigned[name] {
				sentinels[p.path+"."+name] = true
			}
		}
	}
	proven := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for target, fn := range funcs {
			if proven[target] {
				continue
			}
			if (&nnProof{fn: fn, funcs: funcs, proven: proven, sentinels: sentinels}).body() {
				proven[target] = true
				changed = true
			}
		}
	}
	for target, fn := range funcs {
		if proven[target] && fn.fails {
			if out[target] == nil {
				out[target] = map[string]bool{}
			}
			out[target][nonNilResult] = true
		}
	}
}

func fileImports(f *ast.File) map[string]string {
	m := map[string]string{}
	for _, s := range f.Imports {
		path, err := strconv.Unquote(s.Path.Value)
		if err != nil {
			continue
		}
		name := path[strings.LastIndex(path, "/")+1:]
		if s.Name != nil {
			name = s.Name.Name
		}
		m[name] = path
	}
	return m
}

// pointerFunc is fd when it has a body, no type parameters (nor a
// generic receiver) and the results *T or (*T, error); else nil.
func pointerFunc(path string, fd *ast.FuncDecl, imports map[string]string) *nnFunc {
	if fd.Body == nil || fd.Type.TypeParams != nil || fd.Type.Results == nil {
		return nil
	}
	var types []ast.Expr
	for _, f := range fd.Type.Results.List {
		n := max(len(f.Names), 1)
		for i := 0; i < n; i++ {
			types = append(types, f.Type)
		}
	}
	if len(types) == 0 {
		return nil // func f() ()
	}
	if _, ok := types[0].(*ast.StarExpr); !ok {
		return nil
	}
	fn := &nnFunc{pkg: path, imports: imports, fd: fd}
	switch {
	case len(types) == 1:
	case len(types) == 2 && isIdentNamed(types[1], "error"):
		fn.fails = true
	default:
		return nil
	}
	if fd.Recv != nil {
		fn.recvTyp = recvTypeName(fd.Recv.List[0].Type)
		if fn.recvTyp == "" {
			return nil
		}
		if len(fd.Recv.List[0].Names) == 1 {
			fn.recv = fd.Recv.List[0].Names[0].Name
		}
	}
	return fn
}

func isIdentNamed(x ast.Expr, name string) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == name
}

// errorValue is an expression that is a non-nil error wherever it is
// evaluated: errors.New(...), fmt.Errorf(...), a composite literal or its
// address.
func errorValue(x ast.Expr, imports map[string]string) bool {
	switch x := ast.Unparen(x).(type) {
	case *ast.CompositeLit:
		return true
	case *ast.UnaryExpr:
		_, lit := ast.Unparen(x.X).(*ast.CompositeLit)
		return x.Op == token.AND && lit
	case *ast.CallExpr:
		sel, ok := x.Fun.(*ast.SelectorExpr)
		if !ok {
			return false
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return false
		}
		switch imports[pkg.Name] + "." + sel.Sel.Name {
		case "errors.New", "fmt.Errorf":
			return true
		}
	}
	return false
}

// nnProof proves one function's pointer result non-nil (body).
type nnProof struct {
	fn        *nnFunc
	funcs     map[string]*nnFunc
	proven    map[string]bool
	sentinels map[string]bool
	// vars are the local variables proven non-nil or not, by name; a
	// variable whose proof is under way is false there.
	vars map[string]bool
}

// body proves every return of the function outside its function
// literals.
func (p *nnProof) body() bool {
	ok := true
	var walk func(n ast.Node, guards []string)
	walk = func(n ast.Node, guards []string) {
		ast.Inspect(n, func(c ast.Node) bool {
			switch c := c.(type) {
			case *ast.FuncLit:
				return false
			case *ast.ReturnStmt:
				if !p.ret(c, guards) {
					ok = false
				}
				return false
			case *ast.IfStmt:
				if c.Init != nil {
					walk(c.Init, guards)
				}
				inner := guards[:len(guards):len(guards)]
				for _, g := range nonNilGuards(c.Cond) {
					if !assigns(c.Body, g) {
						inner = append(inner, g)
					}
				}
				walk(c.Body, inner)
				if c.Else != nil {
					walk(c.Else, guards)
				}
				return false
			}
			return true
		})
	}
	walk(p.fn.fd.Body, nil)
	return ok
}

// nonNilGuards are the variables cond proves non-nil in an if's body:
// `x != nil`, or either side of an &&.
func nonNilGuards(cond ast.Expr) []string {
	c, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok {
		return nil
	}
	switch c.Op {
	case token.LAND:
		return append(nonNilGuards(c.X), nonNilGuards(c.Y)...)
	case token.NEQ:
		if id, ok := ast.Unparen(c.X).(*ast.Ident); ok && isIdentNamed(ast.Unparen(c.Y), "nil") {
			return []string{id.Name}
		}
	}
	return nil
}

// assigns reports an assignment to, a declaration of, or the address of
// name anywhere in n.
func assigns(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		switch c := c.(type) {
		case *ast.AssignStmt:
			for _, l := range c.Lhs {
				if isIdentNamed(l, name) {
					found = true
				}
			}
		case *ast.ValueSpec:
			for _, id := range c.Names {
				if id.Name == name {
					found = true
				}
			}
		case *ast.RangeStmt:
			if isIdentNamed(c.Key, name) || isIdentNamed(c.Value, name) {
				found = true
			}
		case *ast.UnaryExpr:
			if c.Op == token.AND && isIdentNamed(ast.Unparen(c.X), name) {
				found = true
			}
		case *ast.FuncLit:
			for _, f := range c.Type.Params.List {
				for _, id := range f.Names {
					if id.Name == name {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}

// ret proves one return statement.
func (p *nnProof) ret(r *ast.ReturnStmt, guards []string) bool {
	switch len(r.Results) {
	case 1:
		if p.fn.fails {
			return p.provenCall(r.Results[0], true)
		}
		return p.pointer(r.Results[0])
	case 2:
		return p.pointer(r.Results[0]) || p.errorSet(r.Results[1], guards)
	}
	return false // a named result's bare return
}

// pointer reports x a non-nil pointer.
func (p *nnProof) pointer(x ast.Expr) bool {
	switch x := ast.Unparen(x).(type) {
	case *ast.UnaryExpr:
		return x.Op == token.AND
	case *ast.CallExpr:
		if isIdentNamed(x.Fun, "new") && len(x.Args) == 1 && !p.local("new") {
			return true
		}
		return p.provenCall(x, false)
	case *ast.Ident:
		return p.variable(x.Name)
	}
	return false
}

// errorSet reports x a non-nil error at its return.
func (p *nnProof) errorSet(x ast.Expr, guards []string) bool {
	if errorValue(x, p.fn.imports) {
		return true
	}
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		for _, g := range guards {
			if g == x.Name {
				return true
			}
		}
		return p.sentinels[p.fn.pkg+"."+x.Name] && !p.local(x.Name)
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] != "" && !p.local(pkg.Name) {
			return p.sentinels[p.fn.imports[pkg.Name]+"."+x.Sel.Name]
		}
	}
	return false
}

// callee is the target of a call x of a function F of the package, a
// method recv.M of the receiver's type, or a function pkg.F; else "".
func (p *nnProof) callee(x ast.Expr) string {
	call, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok {
		return ""
	}
	switch f := call.Fun.(type) {
	case *ast.Ident:
		if p.local(f.Name) {
			return ""
		}
		return p.fn.pkg + "." + f.Name
	case *ast.SelectorExpr:
		id, ok := f.X.(*ast.Ident)
		if !ok {
			return ""
		}
		if p.fn.recv != "" && id.Name == p.fn.recv {
			if assigns(p.fn.fd.Body, id.Name) {
				return ""
			}
			return p.fn.pkg + "." + p.fn.recvTyp + "." + f.Sel.Name
		}
		if path := p.fn.imports[id.Name]; path != "" && !p.local(id.Name) {
			return path + "." + f.Sel.Name
		}
	}
	return ""
}

// provenCall reports x a call of a proven function of the result shape
// fails says.
func (p *nnProof) provenCall(x ast.Expr, fails bool) bool {
	t := p.callee(x)
	fn := p.funcs[t]
	return fn != nil && fn.fails == fails && p.proven[t]
}

// param reports name a parameter, a result or the receiver.
func (p *nnProof) param(name string) bool {
	fd := p.fn.fd
	for _, fl := range []*ast.FieldList{fd.Recv, fd.Type.Params, fd.Type.Results} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				if n.Name == name {
					return true
				}
			}
		}
	}
	return false
}

// local reports name declared in the function, which hides a name of
// the package or of an import.
func (p *nnProof) local(name string) bool {
	return p.param(name) || declares(p.fn.fd.Body, name)
}

// declares reports a declaration of name anywhere in n.
func declares(n ast.Node, name string) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		switch c := c.(type) {
		case *ast.AssignStmt:
			if c.Tok == token.DEFINE {
				for _, l := range c.Lhs {
					if isIdentNamed(l, name) {
						found = true
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range c.Names {
				if id.Name == name {
					found = true
				}
			}
		case *ast.TypeSpec:
			found = found || c.Name.Name == name
		case *ast.RangeStmt:
			if isIdentNamed(c.Key, name) || isIdentNamed(c.Value, name) {
				found = true
			}
		case *ast.FuncLit:
			for _, f := range c.Type.Params.List {
				for _, id := range f.Names {
					if id.Name == name {
						found = true
					}
				}
			}
		}
		return !found
	})
	return found
}

// variable reports the local variable name non-nil wherever the body
// reads it: no parameter, result or receiver, its address never taken,
// and every assignment of it (function literals included) a non-nil
// pointer, or `name, err := g(...)`, g proven, followed at once by
// `if err != nil` whose body returns or panics.
func (p *nnProof) variable(name string) bool {
	if v, ok := p.vars[name]; ok {
		return v
	}
	if p.vars == nil {
		p.vars = map[string]bool{}
	}
	p.vars[name] = false // a proof that reads itself proves nothing
	v := p.variableOnce(name)
	p.vars[name] = v
	return v
}

func (p *nnProof) variableOnce(name string) bool {
	if p.param(name) {
		return false
	}
	body := p.fn.fd.Body
	// approved are the assignments a statement list proves.
	approved := map[*ast.AssignStmt]bool{}
	ast.Inspect(body, func(n ast.Node) bool {
		var list []ast.Stmt
		switch n := n.(type) {
		case *ast.BlockStmt:
			list = n.List
		case *ast.CaseClause:
			list = n.Body
		case *ast.CommClause:
			list = n.Body
		}
		for i, s := range list {
			if a, ok := s.(*ast.AssignStmt); ok && p.assignment(a, list[i+1:], name) {
				approved[a] = true
			}
		}
		return true
	})
	ok, any := true, false
	ast.Inspect(body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			for _, l := range n.Lhs {
				if isIdentNamed(l, name) {
					any = true
					ok = ok && approved[n]
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if id.Name == name {
					any = true
					ok = ok && len(n.Values) == len(n.Names) && p.pointer(n.Values[i])
				}
			}
		case *ast.RangeStmt:
			if isIdentNamed(n.Key, name) || isIdentNamed(n.Value, name) {
				ok = false
			}
		case *ast.UnaryExpr:
			if n.Op == token.AND && isIdentNamed(ast.Unparen(n.X), name) {
				ok = false
			}
		case *ast.FuncLit:
			for _, f := range n.Type.Params.List {
				for _, id := range f.Names {
					if id.Name == name {
						ok = false
					}
				}
			}
		}
		return ok
	})
	return ok && any
}

// assignment reports a, a statement of a list followed by rest, setting
// name only to a non-nil pointer: `name = <pointer>`, or
// `name, err := g(...)` with g proven and rest starting with an
// `if err != nil` that returns or panics.
func (p *nnProof) assignment(a *ast.AssignStmt, rest []ast.Stmt, name string) bool {
	if a.Tok != token.ASSIGN && a.Tok != token.DEFINE {
		return false
	}
	at := -1
	for i, l := range a.Lhs {
		if isIdentNamed(l, name) {
			at = i
		}
	}
	switch {
	case at < 0:
		return false
	case len(a.Lhs) == len(a.Rhs):
		return p.pointer(a.Rhs[at])
	case at == 0 && len(a.Lhs) == 2 && len(a.Rhs) == 1:
		e, _ := a.Lhs[1].(*ast.Ident)
		return e != nil && e.Name != "_" && p.provenCall(a.Rhs[0], true) && len(rest) > 0 && endsOnError(rest[0], e.Name)
	}
	return false
}

// endsOnError reports s `if err != nil { ...; return }` (or a panic last),
// with no init and no else.
func endsOnError(s ast.Stmt, err string) bool {
	is, ok := s.(*ast.IfStmt)
	if !ok || is.Init != nil || is.Else != nil || len(is.Body.List) == 0 {
		return false
	}
	c, ok := ast.Unparen(is.Cond).(*ast.BinaryExpr)
	if !ok || c.Op != token.NEQ || !isIdentNamed(ast.Unparen(c.X), err) || !isIdentNamed(ast.Unparen(c.Y), "nil") {
		return false
	}
	switch last := is.Body.List[len(is.Body.List)-1].(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.ExprStmt:
		call, ok := last.X.(*ast.CallExpr)
		return ok && isIdentNamed(call.Fun, "panic")
	}
	return false
}
