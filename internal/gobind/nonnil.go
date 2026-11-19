package gobind

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// Non-nil results (GOBIND-NIL, GOBIND-PROVE; docs/spec/interop.md, Type
// mapping). A Go function whose results are (*T, error) binds its pointer
// as go.Ref[T], a value the caller reads without a check, only where the
// binder proves from the Go source that the pointer is not nil whenever
// the error is; otherwise as Option[go.Ref[T]]. Go returns nil with no
// error where it means "none" (http.ProxyFromEnvironment for a request
// that takes no proxy), so the type alone promises nothing.
//
// The proof is syntactic and conservative, over the bodies of every
// function of the packages Derive lists, on each platform, to a least
// fixed point. Each return statement outside a function literal must be
// one of:
//
//   - a non-nil pointer: &x (&T{...} too), new(T), the call of a proven
//     function of one pointer result, a package variable set once to
//     &x or new(T) that no function of its package assigns (time.UTC), a
//     variable an enclosing `if x != nil` (or an && holding it) proves, a
//     variable an enclosing `if x, err := g(...); err == nil` proves (g
//     proven), or a local variable every assignment of which is one of
//     these or `x, err := g(...)`, g proven, followed at once by an
//     `if err != nil` that returns;
//   - a non-nil error: errors.New(...), fmt.Errorf(...), a composite
//     literal or its address, a conversion T(v) to a non-interface type
//     of a listed package (KeySizeError(k)), a constant (syscall.EINVAL),
//     the call of a proven error function (one error result, every
//     return a non-nil error), the variable of an enclosing
//     `if err != nil` (or an && holding it) whose body does not assign
//     it, a field path `x.f` of an `if x.f != nil` whose body returns at
//     once, or a package variable set once to a non-nil error that no
//     function of its package assigns (io.EOF), or any expression whose
//     static type is concrete (a nil *E in an error is Go's typed nil, a
//     non-nil error);
//   - `return g(...)`, g a proven (*T, error) function, or `return x, err`
//     right after `x, err := g(...)` in the same statement list, g
//     proven, with no statement between them that assigns x or err;
//   - a parameter x after a top-level `if x == nil { x = <non-nil> }` (or
//     a body that returns), when the function has no goto.
//
// "No function assigns" spans every listed package: a write `pkg.X = …`
// from another package counts. Ergo writes no Go package variable.
//
// A call names its function by the package (F), an import (pkg.F), or the
// type of its receiver expression: the method's receiver, a parameter or
// local variable whose declarations all give it one type (a composite
// literal, new(T), a declared type, or a function's result type), or a
// package variable of one type (http.DefaultClient). A method the type
// does not declare itself (an embedded one, an interface's) proves
// nothing.
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

// nnKind is what a function's results let the proof say of it.
type nnKind int

const (
	nnOther  nnKind = iota // no proof: only its result type is read
	nnPtr                  // *T
	nnPtrErr               // (*T, error)
	nnErr                  // error
)

// nnFunc is one function or method of a listed package.
type nnFunc struct {
	pkg     string
	imports map[string]string // the file's import names to paths
	fd      *ast.FuncDecl
	kind    nnKind
	result  string // the type of its first result, path.T, or ""
	recv    string // the receiver's name, or ""
	recvTyp string // the receiver's type name, or ""
}

func (fn *nnFunc) target() string {
	if fn.recvTyp != "" {
		return fn.pkg + "." + fn.recvTyp + "." + fn.fd.Name.Name
	}
	return fn.pkg + "." + fn.fd.Name.Name
}

// nnVar is a package variable: its value and type expression.
type nnVar struct {
	pkg     string
	imports map[string]string
	typ     ast.Expr
	value   ast.Expr
	set     bool // a function of its package assigns it or takes its address
}

// nnWorld is what the listed packages declare, by path.Name.
type nnWorld struct {
	funcs  map[string]*nnFunc
	proven map[string]bool
	vars   map[string]*nnVar
	consts map[string]bool
	types  map[string]bool // a non-interface, non-alias type
}

// nonNil adds the nonNilResult fact of every proven (*T, error) function
// of pkgs to out.
func nonNil(pkgs []nnPkg, out Facts) {
	w := nnAnalyze(pkgs)
	for target, fn := range w.funcs {
		if w.proven[target] && fn.kind == nnPtrErr {
			if out[target] == nil {
				out[target] = map[string]bool{}
			}
			out[target][nonNilResult] = true
		}
	}
}

// nnAnalyze reads pkgs' declarations and proves their functions to a
// least fixed point.
func nnAnalyze(pkgs []nnPkg) *nnWorld {
	w := &nnWorld{funcs: map[string]*nnFunc{}, proven: map[string]bool{}, vars: map[string]*nnVar{}, consts: map[string]bool{}, types: map[string]bool{}}
	written := map[string]bool{} // pkg.X a listed package writes from outside
	for _, p := range pkgs {
		assigned := map[string]bool{}
		var vars []string
		for _, f := range p.files {
			imports := fileImports(f)
			// mark records a write or an address of a package variable:
			// by its name in its package, by pkg.X in another.
			mark := func(x ast.Expr) {
				switch x := ast.Unparen(x).(type) {
				case *ast.Ident:
					assigned[x.Name] = true
				case *ast.SelectorExpr:
					if pkg, ok := x.X.(*ast.Ident); ok && imports[pkg.Name] != "" {
						written[imports[pkg.Name]+"."+x.Sel.Name] = true
					}
				}
			}
			for _, d := range f.Decls {
				switch d := d.(type) {
				case *ast.GenDecl:
					for _, s := range d.Specs {
						switch s := s.(type) {
						case *ast.TypeSpec:
							if s.Assign == 0 && s.TypeParams == nil && concrete(s.Type) {
								w.types[p.path+"."+s.Name.Name] = true
							}
						case *ast.ValueSpec:
							for i, n := range s.Names {
								key := p.path + "." + n.Name
								if d.Tok == token.CONST {
									w.consts[key] = true
									continue
								}
								v := &nnVar{pkg: p.path, imports: imports, typ: s.Type}
								if len(s.Values) == len(s.Names) {
									v.value = s.Values[i]
								}
								w.vars[key] = v
								vars = append(vars, n.Name)
							}
						}
					}
				case *ast.FuncDecl:
					if d.Body != nil {
						ast.Inspect(d.Body, func(n ast.Node) bool {
							switch n := n.(type) {
							case *ast.AssignStmt:
								for _, l := range n.Lhs {
									mark(l)
								}
							case *ast.IncDecStmt:
								mark(n.X)
							case *ast.UnaryExpr:
								if n.Op == token.AND {
									mark(n.X)
								}
							}
							return true
						})
					}
					if fn := readFunc(p.path, d, imports); fn != nil {
						w.funcs[fn.target()] = fn
					}
				}
			}
		}
		for _, name := range vars {
			if assigned[name] {
				w.vars[p.path+"."+name].set = true
			}
		}
	}
	for key := range written {
		if v := w.vars[key]; v != nil {
			v.set = true
		}
	}
	for changed := true; changed; {
		changed = false
		for target, fn := range w.funcs {
			if w.proven[target] || fn.kind == nnOther || fn.fd.Body == nil {
				continue
			}
			if (&nnProof{w: w, fn: fn}).body() {
				w.proven[target] = true
				changed = true
			}
		}
	}
	return w
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

// readFunc is fd with its kind and result type, or nil for a generic one.
func readFunc(path string, fd *ast.FuncDecl, imports map[string]string) *nnFunc {
	if fd.Type.TypeParams != nil {
		return nil
	}
	fn := &nnFunc{pkg: path, imports: imports, fd: fd}
	if fd.Recv != nil {
		fn.recvTyp = recvTypeName(fd.Recv.List[0].Type)
		if fn.recvTyp == "" {
			return nil
		}
		if len(fd.Recv.List[0].Names) == 1 {
			fn.recv = fd.Recv.List[0].Names[0].Name
		}
	}
	var types []ast.Expr
	if fd.Type.Results != nil {
		for _, f := range fd.Type.Results.List {
			for i := 0; i < max(len(f.Names), 1); i++ {
				types = append(types, f.Type)
			}
		}
	}
	if len(types) == 0 {
		return fn
	}
	fn.result = typeKey(types[0], path, imports)
	_, ptr := types[0].(*ast.StarExpr)
	switch {
	case ptr && len(types) == 1:
		fn.kind = nnPtr
	case ptr && len(types) == 2 && isIdentNamed(types[1], "error"):
		fn.kind = nnPtrErr
	case len(types) == 1 && isIdentNamed(types[0], "error"):
		fn.kind = nnErr
	}
	return fn
}

// typeKey is path.T for a type expression T, *T or pkg.T, else "".
func typeKey(x ast.Expr, path string, imports map[string]string) string {
	if s, ok := x.(*ast.StarExpr); ok {
		x = s.X
	}
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		if token.IsExported(x.Name) || !isPredeclared(x.Name) {
			return path + "." + x.Name
		}
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && imports[pkg.Name] != "" {
			return imports[pkg.Name] + "." + x.Sel.Name
		}
	}
	return ""
}

// concrete reports a type declaration's type expression surely no
// interface: a struct, pointer, function, array, slice, map or channel
// type, or a predeclared type other than error, any and comparable. A
// named type's name (type E error, type R io.Reader) is not read.
func concrete(t ast.Expr) bool {
	switch t := ast.Unparen(t).(type) {
	case *ast.StructType, *ast.StarExpr, *ast.FuncType, *ast.ArrayType, *ast.MapType, *ast.ChanType:
		return true
	case *ast.Ident:
		return isPredeclared(t.Name) && t.Name != "error" && t.Name != "any" && t.Name != "comparable"
	}
	return false
}

func isPredeclared(name string) bool {
	switch name {
	case "bool", "byte", "complex64", "complex128", "error", "float32", "float64", "int", "int8", "int16", "int32", "int64",
		"rune", "string", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "any", "comparable":
		return true
	}
	return false
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

// pointerValue is an expression that is a non-nil pointer wherever it is
// evaluated: &x or new(T).
func pointerValue(x ast.Expr) bool {
	switch x := ast.Unparen(x).(type) {
	case *ast.UnaryExpr:
		return x.Op == token.AND
	case *ast.CallExpr:
		return isIdentNamed(x.Fun, "new") && len(x.Args) == 1
	}
	return false
}

// sentinel reports the package variable key set once to a non-nil error.
func (w *nnWorld) sentinel(key string) bool {
	v := w.vars[key]
	return v != nil && !v.set && v.value != nil && errorValue(v.value, v.imports)
}

// pointerVar reports the package variable key set once to &x or new(T).
func (w *nnWorld) pointerVar(key string) bool {
	v := w.vars[key]
	return v != nil && !v.set && v.value != nil && pointerValue(v.value)
}

// varType is the type of the package variable key, or "".
func (w *nnWorld) varType(key string) string {
	v := w.vars[key]
	switch {
	case v == nil:
		return ""
	case v.typ != nil:
		return typeKey(v.typ, v.pkg, v.imports)
	case v.value == nil:
		return ""
	}
	switch x := ast.Unparen(v.value).(type) {
	case *ast.UnaryExpr:
		if lit, ok := ast.Unparen(x.X).(*ast.CompositeLit); ok && x.Op == token.AND {
			return typeKey(lit.Type, v.pkg, v.imports)
		}
	case *ast.CompositeLit:
		return typeKey(x.Type, v.pkg, v.imports)
	case *ast.CallExpr:
		if isIdentNamed(x.Fun, "new") && len(x.Args) == 1 {
			return typeKey(x.Args[0], v.pkg, v.imports)
		}
		var target string
		switch f := x.Fun.(type) {
		case *ast.Ident:
			target = v.pkg + "." + f.Name
		case *ast.SelectorExpr:
			if pkg, ok := f.X.(*ast.Ident); ok && v.imports[pkg.Name] != "" {
				target = v.imports[pkg.Name] + "." + f.Sel.Name
			}
		}
		if fn := w.funcs[target]; fn != nil {
			return fn.result
		}
	}
	return ""
}

// nnProof proves one function's result (body).
type nnProof struct {
	w  *nnWorld
	fn *nnFunc
	// vars are the local variables proven non-nil or not, by name; a
	// variable whose proof is under way is false there. types are the
	// local variables' types, "" for none or several.
	vars, typing map[string]bool
	types        map[string]string
}

// guards are what an enclosing if proves: variables non-nil (names), and
// field paths non-nil for a return that is the body's first statement.
type guards struct {
	names, paths []string
}

func (g guards) has(name string) bool {
	for _, n := range g.names {
		if n == name {
			return true
		}
	}
	return false
}

// body proves every return of the function outside its function
// literals.
func (p *nnProof) body() bool {
	ok := true
	var walk func(n ast.Node, g guards)
	walk = func(n ast.Node, g guards) {
		ast.Inspect(n, func(c ast.Node) bool {
			switch c := c.(type) {
			case *ast.FuncLit:
				return false
			case *ast.ReturnStmt:
				if !p.ret(c, g) {
					ok = false
				}
				return false
			case *ast.IfStmt:
				if c.Init != nil {
					walk(c.Init, guards{names: g.names})
				}
				inner := guards{names: g.names[:len(g.names):len(g.names)]}
				names, paths := nonNilGuards(c.Cond)
				names = append(names, p.initGuard(c)...)
				for _, n := range names {
					if !assigns(c.Body, n) {
						inner.names = append(inner.names, n)
					}
				}
				list := c.Body.List
				if r, isRet := firstStmt(list).(*ast.ReturnStmt); isRet && len(paths) > 0 && len(r.Results) == 2 && !calls(r.Results[0]) {
					// Nothing runs between the check and the read.
					if !p.ret(r, guards{names: inner.names, paths: paths}) {
						ok = false
					}
					list = list[1:]
				}
				for _, s := range list {
					walk(s, inner)
				}
				if c.Else != nil {
					walk(c.Else, guards{names: g.names})
				}
				return false
			}
			return true
		})
	}
	// A top-level `if x == nil { … }` that ends the function or sets x
	// to a non-nil pointer makes x non-nil in the statements after it,
	// when every assignment of x is non-nil (a parameter's default).
	var fixed []string
	gotos := hasGoto(p.fn.fd.Body)
	for _, s := range p.fn.fd.Body.List {
		walk(s, guards{names: fixed})
		if x := p.nilFix(s); x != "" && !gotos {
			fixed = append(fixed, x)
		}
	}
	return ok
}

// nilFix is x for `if x == nil { … }`, no init and no else, whose body
// ends in a return or a panic or sets x at its top level, when every
// assignment of x in the function is a non-nil pointer; else "".
func (p *nnProof) nilFix(s ast.Stmt) string {
	is, ok := s.(*ast.IfStmt)
	if !ok || is.Init != nil || is.Else != nil || len(is.Body.List) == 0 {
		return ""
	}
	c, ok := ast.Unparen(is.Cond).(*ast.BinaryExpr)
	if !ok || c.Op != token.EQL || !isIdentNamed(ast.Unparen(c.Y), "nil") {
		return ""
	}
	x, ok := ast.Unparen(c.X).(*ast.Ident)
	if !ok || !p.local(x.Name) || !p.setsNonNil(x.Name, false) {
		return ""
	}
	if ends(is.Body.List[len(is.Body.List)-1]) {
		return x.Name
	}
	for _, b := range is.Body.List {
		if a, ok := b.(*ast.AssignStmt); ok && len(a.Lhs) == 1 && isIdentNamed(a.Lhs[0], x.Name) {
			return x.Name // setsNonNil proved the assignment
		}
	}
	return ""
}

// ends reports s a return or a panic.
func ends(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ReturnStmt:
		return true
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && isIdentNamed(call.Fun, "panic")
	}
	return false
}

// hasGoto reports a goto in n: a jump back past a top-level check.
func hasGoto(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(c ast.Node) bool {
		if b, ok := c.(*ast.BranchStmt); ok && b.Tok == token.GOTO {
			found = true
		}
		return !found
	})
	return found
}

// calls reports a call (or a receive) in x.
func calls(x ast.Expr) bool {
	found := false
	ast.Inspect(x, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			found = true
		case *ast.UnaryExpr:
			found = found || n.Op == token.ARROW
		}
		return !found
	})
	return found
}

func firstStmt(list []ast.Stmt) ast.Stmt {
	if len(list) == 0 {
		return nil
	}
	return list[0]
}

// initGuard is x for `if x, err := g(...); err == nil`, g a proven
// (*T, error) function: x is non-nil in the body.
func (p *nnProof) initGuard(c *ast.IfStmt) []string {
	a, ok := c.Init.(*ast.AssignStmt)
	if !ok || len(a.Lhs) != 2 || len(a.Rhs) != 1 {
		return nil
	}
	x, _ := a.Lhs[0].(*ast.Ident)
	e, _ := a.Lhs[1].(*ast.Ident)
	b, _ := ast.Unparen(c.Cond).(*ast.BinaryExpr)
	if x == nil || e == nil || b == nil || b.Op != token.EQL || !isIdentNamed(ast.Unparen(b.X), e.Name) || !isIdentNamed(ast.Unparen(b.Y), "nil") || !p.provenCall(a.Rhs[0], nnPtrErr) {
		return nil
	}
	return []string{x.Name}
}

// nonNilGuards are the variables and field paths cond proves non-nil in
// an if's body: `x != nil`, `x.f != nil`, or either side of an &&.
func nonNilGuards(cond ast.Expr) (names, paths []string) {
	c, ok := ast.Unparen(cond).(*ast.BinaryExpr)
	if !ok {
		return nil, nil
	}
	switch c.Op {
	case token.LAND:
		n1, p1 := nonNilGuards(c.X)
		n2, p2 := nonNilGuards(c.Y)
		return append(n1, n2...), append(p1, p2...)
	case token.NEQ:
		if !isIdentNamed(ast.Unparen(c.Y), "nil") {
			return nil, nil
		}
		if id, ok := ast.Unparen(c.X).(*ast.Ident); ok {
			return []string{id.Name}, nil
		}
		if path := fieldPath(c.X); path != "" {
			return nil, []string{path}
		}
	}
	return nil, nil
}

// fieldPath is "x.f.g" for a selector chain of identifiers, else "".
func fieldPath(x ast.Expr) string {
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if base := fieldPath(x.X); base != "" {
			return base + "." + x.Sel.Name
		}
	}
	return ""
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
func (p *nnProof) ret(r *ast.ReturnStmt, g guards) bool {
	switch p.fn.kind {
	case nnPtr:
		return len(r.Results) == 1 && p.pointer(r.Results[0], g)
	case nnErr:
		return len(r.Results) == 1 && p.errorSet(r.Results[0], g)
	}
	switch len(r.Results) {
	case 1:
		return p.provenCall(r.Results[0], nnPtrErr)
	case 2:
		return p.pointer(r.Results[0], g) || p.errorSet(r.Results[1], g) || p.pair(r)
	}
	return false // a named result's bare return
}

// pair reports `return x, err` that a statement `x, err := g(...)` (or
// =), g proven, comes before in one statement list, with nothing after
// it in the list that assigns, declares or takes the address of x or
// err: the two are that call's results.
func (p *nnProof) pair(r *ast.ReturnStmt) bool {
	x, ok1 := ast.Unparen(r.Results[0]).(*ast.Ident)
	e, ok2 := ast.Unparen(r.Results[1]).(*ast.Ident)
	if !ok1 || !ok2 || x.Name == e.Name || x.Name == "_" || e.Name == "_" {
		return false
	}
	found := false
	ast.Inspect(p.fn.fd.Body, func(n ast.Node) bool {
		var list []ast.Stmt
		switch n := n.(type) {
		case *ast.FuncLit:
			return false
		case *ast.BlockStmt:
			list = n.List
		case *ast.CaseClause:
			list = n.Body
		case *ast.CommClause:
			list = n.Body
		}
		for i, s := range list {
			a, ok := s.(*ast.AssignStmt)
			if !ok || len(a.Lhs) != 2 || len(a.Rhs) != 1 || !isIdentNamed(a.Lhs[0], x.Name) || !isIdentNamed(a.Lhs[1], e.Name) || !p.provenCall(a.Rhs[0], nnPtrErr) {
				continue
			}
			rest := list[i+1:]
			holds := false
			for _, t := range rest {
				if assigns(t, x.Name) || assigns(t, e.Name) {
					break
				}
				if t.Pos() <= r.Pos() && r.End() <= t.End() {
					holds = true
					break
				}
			}
			found = found || holds
		}
		return !found
	})
	return found
}

// pointer reports x a non-nil pointer.
func (p *nnProof) pointer(x ast.Expr, g guards) bool {
	switch x := ast.Unparen(x).(type) {
	case *ast.UnaryExpr:
		return x.Op == token.AND
	case *ast.CallExpr:
		if isIdentNamed(x.Fun, "new") && len(x.Args) == 1 && !p.local("new") {
			return true
		}
		return p.provenCall(x, nnPtr)
	case *ast.Ident:
		if g.has(x.Name) {
			return true
		}
		if p.local(x.Name) {
			return p.variable(x.Name)
		}
		return p.w.pointerVar(p.fn.pkg + "." + x.Name)
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] != "" && !p.local(pkg.Name) {
			return p.w.pointerVar(p.fn.imports[pkg.Name] + "." + x.Sel.Name)
		}
	}
	return false
}

// errorSet reports x a non-nil error at its return.
func (p *nnProof) errorSet(x ast.Expr, g guards) bool {
	if errorValue(x, p.fn.imports) {
		return true
	}
	if t := p.exprType(x); t != "" && p.w.types[t] {
		// A value of a concrete type in an error is a non-nil
		// interface, a nil *T too (Go's typed nil).
		return true
	}
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		if g.has(x.Name) {
			return true
		}
		key := p.fn.pkg + "." + x.Name
		return !p.local(x.Name) && (p.w.sentinel(key) || p.w.consts[key])
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] != "" && !p.local(pkg.Name) {
			key := p.fn.imports[pkg.Name] + "." + x.Sel.Name
			return p.w.sentinel(key) || p.w.consts[key]
		}
		path := fieldPath(x)
		for _, gp := range g.paths {
			if path != "" && gp == path {
				return true
			}
		}
	case *ast.CallExpr:
		if p.conversion(x) {
			return true
		}
		return p.provenCall(x, nnErr)
	}
	return false
}

// conversion reports x a conversion T(v) to a non-interface type of a
// listed package: an error interface holding it is non-nil.
func (p *nnProof) conversion(x *ast.CallExpr) bool {
	if len(x.Args) != 1 {
		return false
	}
	switch f := ast.Unparen(x.Fun).(type) {
	case *ast.Ident:
		return !p.local(f.Name) && p.w.types[p.fn.pkg+"."+f.Name]
	case *ast.SelectorExpr:
		if pkg, ok := f.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] != "" && !p.local(pkg.Name) {
			return p.w.types[p.fn.imports[pkg.Name]+"."+f.Sel.Name]
		}
	}
	return false
}

// callee is the target of a call x: a function F of the package, pkg.F,
// or a method M of its receiver expression's type; else "".
func (p *nnProof) callee(x ast.Expr) string {
	call, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok {
		return ""
	}
	switch f := ast.Unparen(call.Fun).(type) {
	case *ast.Ident:
		if p.local(f.Name) {
			return ""
		}
		return p.fn.pkg + "." + f.Name
	case *ast.SelectorExpr:
		if id, ok := f.X.(*ast.Ident); ok && !p.local(id.Name) {
			if path := p.fn.imports[id.Name]; path != "" {
				return path + "." + f.Sel.Name
			}
		}
		if t := p.exprType(f.X); t != "" {
			return t + "." + f.Sel.Name
		}
	}
	return ""
}

// exprType is the type of x, path.T, where the proof can tell it.
func (p *nnProof) exprType(x ast.Expr) string {
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		if p.fn.recv != "" && x.Name == p.fn.recv {
			if assigns(p.fn.fd.Body, x.Name) {
				return ""
			}
			return p.fn.pkg + "." + p.fn.recvTyp
		}
		if p.local(x.Name) {
			return p.localType(x.Name)
		}
		return p.w.varType(p.fn.pkg + "." + x.Name)
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] != "" && !p.local(pkg.Name) {
			return p.w.varType(p.fn.imports[pkg.Name] + "." + x.Sel.Name)
		}
	case *ast.UnaryExpr:
		if lit, ok := ast.Unparen(x.X).(*ast.CompositeLit); ok && x.Op == token.AND {
			return p.typeKey(lit.Type)
		}
	case *ast.CompositeLit:
		return p.typeKey(x.Type)
	case *ast.TypeAssertExpr:
		if x.Type != nil {
			return p.typeKey(x.Type)
		}
	case *ast.CallExpr:
		if isIdentNamed(x.Fun, "new") && len(x.Args) == 1 && !p.local("new") {
			return p.typeKey(x.Args[0])
		}
		if fn := p.w.funcs[p.callee(x)]; fn != nil {
			return fn.result
		}
	}
	return ""
}

// typeKey is typeKey in the function: a type name it declares itself, or
// an import name it hides, types nothing.
func (p *nnProof) typeKey(x ast.Expr) string {
	if s, ok := x.(*ast.StarExpr); ok {
		x = s.X
	}
	switch t := ast.Unparen(x).(type) {
	case *ast.Ident:
		if p.local(t.Name) {
			return ""
		}
	case *ast.SelectorExpr:
		if pkg, ok := t.X.(*ast.Ident); ok && p.local(pkg.Name) {
			return ""
		}
	}
	return typeKey(x, p.fn.pkg, p.fn.imports)
}

// localType is the one type every declaration of the local name (a
// parameter, a :=, a var) gives it, or "".
func (p *nnProof) localType(name string) string {
	if t, ok := p.types[name]; ok {
		return t
	}
	if p.types == nil {
		p.types = map[string]string{}
	}
	p.types[name] = "" // a declaration that reads itself types nothing
	var found []string
	fd := p.fn.fd
	for _, fl := range []*ast.FieldList{fd.Type.Params, fd.Type.Results} {
		if fl == nil {
			continue
		}
		for _, f := range fl.List {
			for _, n := range f.Names {
				if n.Name == name {
					found = append(found, p.typeKey(f.Type))
				}
			}
		}
	}
	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.AssignStmt:
			if n.Tok != token.DEFINE {
				return true
			}
			for i, l := range n.Lhs {
				if !isIdentNamed(l, name) {
					continue
				}
				switch {
				case len(n.Lhs) == len(n.Rhs):
					found = append(found, p.exprType(n.Rhs[i]))
				case i == 0 && len(n.Rhs) == 1:
					found = append(found, p.exprType(n.Rhs[0]))
				default:
					found = append(found, "")
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if id.Name != name {
					continue
				}
				switch {
				case n.Type != nil:
					found = append(found, p.typeKey(n.Type))
				case len(n.Values) == len(n.Names):
					found = append(found, p.exprType(n.Values[i]))
				default:
					found = append(found, "")
				}
			}
		case *ast.RangeStmt:
			if isIdentNamed(n.Key, name) || isIdentNamed(n.Value, name) {
				found = append(found, "")
			}
		case *ast.FuncLit:
			for _, f := range n.Type.Params.List {
				for _, id := range f.Names {
					if id.Name == name {
						found = append(found, "")
					}
				}
			}
		case *ast.TypeSwitchStmt:
			if a, ok := n.Assign.(*ast.AssignStmt); ok && len(a.Lhs) == 1 && isIdentNamed(a.Lhs[0], name) {
				found = append(found, "")
			}
		}
		return true
	})
	t := ""
	for i, f := range found {
		if f == "" || i > 0 && f != t {
			t = ""
			break
		}
		t = f
	}
	p.types[name] = t
	return t
}

// provenCall reports x a call of a proven function of kind k.
func (p *nnProof) provenCall(x ast.Expr, k nnKind) bool {
	t := p.callee(x)
	fn := p.w.funcs[t]
	return fn != nil && fn.kind == k && p.w.proven[t]
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
		case *ast.TypeSwitchStmt:
			if a, ok := c.Assign.(*ast.AssignStmt); ok && len(a.Lhs) == 1 && isIdentNamed(a.Lhs[0], name) {
				found = true
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
	return !p.param(name) && p.setsNonNil(name, true)
}

// setsNonNil reports every assignment of name in the body (function
// literals included) a non-nil pointer, its address never taken, and,
// with needOne, at least one assignment.
func (p *nnProof) setsNonNil(name string, needOne bool) bool {
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
					ok = ok && len(n.Values) == len(n.Names) && p.pointer(n.Values[i], guards{})
				}
			}
		case *ast.RangeStmt:
			if isIdentNamed(n.Key, name) || isIdentNamed(n.Value, name) {
				ok = false
			}
		case *ast.TypeSwitchStmt:
			if a, isAssign := n.Assign.(*ast.AssignStmt); isAssign && len(a.Lhs) == 1 && isIdentNamed(a.Lhs[0], name) {
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
	return ok && (any || !needOne)
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
		return p.pointer(a.Rhs[at], guards{})
	case at == 0 && len(a.Lhs) == 2 && len(a.Rhs) == 1:
		e, _ := a.Lhs[1].(*ast.Ident)
		return e != nil && e.Name != "_" && p.provenCall(a.Rhs[0], nnPtrErr) && len(rest) > 0 && endsOnError(rest[0], e.Name)
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
