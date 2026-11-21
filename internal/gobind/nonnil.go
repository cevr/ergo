package gobind

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// Non-nil results (GOBIND-NIL, GOBIND-PROVE, GOBIND-FLOW;
// docs/spec/interop.md, Type mapping). A Go function whose results are
// (*T, error) binds its pointer as go.Ref[T], a value the caller reads
// without a check, only where the binder proves from the Go source that
// the pointer is not nil whenever the error is; otherwise as
// Option[go.Ref[T]]. Go returns nil with no error where it means "none"
// (http.ProxyFromEnvironment for a request that takes no proxy), so the
// type alone promises nothing.
//
// The proof is syntactic and conservative, over the bodies of every
// function of the packages Derive lists, on each platform, to a least
// fixed point. A forward flow over each body (nonnil_flow.go) knows at
// each point which local variables and field paths are non-nil and which
// pairs (x, err) one proven call returned. Each return statement outside
// a function literal must give, where it stands, one of:
//
//   - a non-nil pointer: &x (&T{...} too), new(T), (*T)(p) of a non-nil
//     p or unsafe.Pointer(p), the call of a proven function of one
//     pointer result, a package variable set once to &x or new(T) that
//     no function assigns (time.UTC), or a variable or field path the
//     flow proves;
//   - a non-nil error: errors.New(...), fmt.Errorf(...), a composite
//     literal or its address, a conversion T(v) to a non-interface type
//     of a listed package (KeySizeError(k)), a constant (syscall.EINVAL),
//     the call of a proven error function (one error result, every
//     return a non-nil error), a package variable set once to a non-nil
//     error that no function assigns (io.EOF), any expression whose
//     static type is concrete (a nil *E in an error is Go's typed nil, a
//     non-nil error), or a variable or field path the flow proves;
//   - the two results of one proven call: `return g(...)`, or
//     `return x, err` where the flow holds the pair (x, err).
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
// Every other form (a function value, an interface method, a body only
// assembly holds) proves nothing, so its result is an Option. A function
// with no return (its body panics) never returns nil.

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
	// ptrMethods are the names of the methods with a pointer receiver:
	// x.M of one evaluates without dereferencing x.
	ptrMethods map[string]bool
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
	w := &nnWorld{funcs: map[string]*nnFunc{}, proven: map[string]bool{}, vars: map[string]*nnVar{}, consts: map[string]bool{}, types: map[string]bool{}, ptrMethods: map[string]bool{}}
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
					if d.Recv != nil && len(d.Recv.List) == 1 {
						if _, ptr := d.Recv.List[0].Type.(*ast.StarExpr); ptr {
							w.ptrMethods[d.Name.Name] = true
						}
					}
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
	// ids are the function's local variables by identifier
	// (nonnil_flow.go); types are their types by name, "" for none or
	// several.
	ids   map[*ast.Ident]string
	types map[string]string
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

// pointer reports x a non-nil pointer where st stands.
func (p *nnProof) pointer(x ast.Expr, st nnState) bool {
	switch x := ast.Unparen(x).(type) {
	case *ast.UnaryExpr:
		return x.Op == token.AND
	case *ast.CallExpr:
		if isIdentNamed(x.Fun, "new") && len(x.Args) == 1 && !p.local("new") {
			return true
		}
		if _, ok := ast.Unparen(x.Fun).(*ast.StarExpr); ok && len(x.Args) == 1 {
			// (*T)(v), v a non-nil pointer or unsafe.Pointer of one.
			return p.pointer(p.unsafePointer(x.Args[0]), st)
		}
		return p.provenCall(x, nnPtr)
	case *ast.Ident:
		if id := p.ids[x]; id != "" {
			return st.nn[id]
		}
		return !p.local(x.Name) && p.w.pointerVar(p.fn.pkg+"."+x.Name)
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] != "" && !p.local(pkg.Name) {
			return p.w.pointerVar(p.fn.imports[pkg.Name] + "." + x.Sel.Name)
		}
		return st.nn[p.path(x)]
	}
	return false
}

// unsafePointer is v for unsafe.Pointer(v), else x.
func (p *nnProof) unsafePointer(x ast.Expr) ast.Expr {
	call, ok := ast.Unparen(x).(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return x
	}
	sel, ok := ast.Unparen(call.Fun).(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Pointer" {
		return x
	}
	if pkg, ok := sel.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] == "unsafe" && !p.local(pkg.Name) {
		return call.Args[0]
	}
	return x
}

// errorSet reports x a non-nil error at its return where st stands.
func (p *nnProof) errorSet(x ast.Expr, st nnState) bool {
	if errorValue(x, p.fn.imports) {
		return true
	}
	if t := p.exprType(x); t != "" && p.w.types[t] {
		// A value of a concrete type in an error is a non-nil
		// interface, a nil *T too (Go's typed nil).
		return true
	}
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident, *ast.SelectorExpr:
		return p.errorName(x, st)
	case *ast.CallExpr:
		if p.conversion(x) {
			return true
		}
		return p.provenCall(x, nnErr)
	}
	return false
}

// errorName reports the variable, constant or field path x a non-nil
// error: a local variable st proves, a package variable set once to a
// non-nil error, a constant, or a field path st proves.
func (p *nnProof) errorName(x ast.Expr, st nnState) bool {
	switch x := ast.Unparen(x).(type) {
	case *ast.Ident:
		if id := p.ids[x]; id != "" {
			return st.nn[id]
		}
		key := p.fn.pkg + "." + x.Name
		return !p.local(x.Name) && (p.w.sentinel(key) || p.w.consts[key])
	case *ast.SelectorExpr:
		if pkg, ok := x.X.(*ast.Ident); ok && p.fn.imports[pkg.Name] != "" && !p.local(pkg.Name) {
			key := p.fn.imports[pkg.Name] + "." + x.Sel.Name
			return p.w.sentinel(key) || p.w.consts[key]
		}
		return st.nn[p.path(x)]
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
