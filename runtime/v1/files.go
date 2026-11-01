package v1

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"sync"

	runtimefiles "ergo.local/prototype/runtime"
)

// Source holds this package's runtime files as the one embed owner,
// runtime/embed.go, holds them. This file is not among them: it serves
// only the compiler.
var Source = runtimefiles.Package("v1")

// SourceFiles names the files of Source that the lowering copies today:
// runtime.go alone, which imports no other runtime package. The lowering
// of the std packages task, scope and queue (check-v1 stage D) copies
// runtimefiles.Files("runtime/v1") instead, every file of this package and
// of runtime/rt, which it imports.
var SourceFiles = []string{"runtime.go"}

// rtPath is the import path of runtime/rt, whose *Task is the hidden task.
const rtPath = runtimefiles.Module + "/runtime/rt"

// Hidden is a hidden parameter of a std extern's Go body: an argument the
// Ergo signature does not show and the lowering supplies at the call
// (docs/spec/stdlib.md, Hidden arguments). Its Go type alone names it: no
// Ergo type lowers to *Scope, Site or *rt.Task.
type Hidden string

const (
	// Frame, a *Scope, is the frame scope of the innermost source function
	// at the call (in an exit action's block, the frame the lowering made
	// for that function; the scope routes to the action's frame itself), or,
	// in the block of a with that owns a resource, that with's scope
	// (Scope.OpenWith): the tasks the block starts belong to the with, and
	// an exit action still registers on the function's frame (OnExit).
	Frame Hidden = "frame"
	// CallSite, a Site, is the call's Ergo identity: the called declaration
	// and the call's source position.
	CallSite Hidden = "site"
	// CallerTask, an *rt.Task, is the task that runs the call.
	CallerTask Hidden = "task"
)

// hiddenOrder is the order hidden parameters take, each at most once.
var hiddenOrder = []Hidden{Frame, CallSite, CallerTask}

// Body is the Go shape of one std extern's body: a function of this
// package, or, for an operation of a type that a std extern returns, the
// method of that name on the Go type the extern's body returns.
type Body struct {
	// TypeParams are the Go type parameters of a function, or of the
	// method's receiver type, in order. An Ergo row parameter has none
	// (docs/spec/stdlib.md, Types 2). Nil for a function type parameter.
	TypeParams []TypeParam
	// Hidden are the leading hidden parameters, in hiddenOrder.
	Hidden []Hidden
	// Params are the parameters after them, one per Ergo parameter, in
	// order.
	Params []Param
	// Results are the Go result types, as written: the Ergo value results,
	// then error when the Ergo signature has an error row.
	Results []string
}

// TypeParam is one Go type parameter of a body: its name and its
// constraint as written (any, comparable).
type TypeParam struct {
	Name, Constraint string
}

// Param is one Go parameter that stands for an Ergo parameter.
type Param struct {
	// Type is the parameter's Go type, as written in the body's source.
	Type string
	// Func is the shape of a function-typed parameter, the Go function the
	// body calls: its own leading hidden parameters (the task that runs the
	// block, for task.Go) follow the same convention. Nil otherwise.
	Func *Body
}

// BodyOf returns the Go shape of the body named name in Source: "Go" for
// the function Go, "Queue.Push" for the method Push of Queue. It fails when
// there is no such exported declaration, or when its parameters break the
// convention: a hidden type after an Ergo parameter, out of order or twice.
func BodyOf(name string) (Body, error) {
	decls, err := sourceDecls()
	if err != nil {
		return Body{}, err
	}
	return decls.body(name)
}

// sourceDecls parses Source once per process.
var sourceDecls = sync.OnceValues(func() (goDecls, error) { return parseDecls(Source) })

// goDecl is one exported function, method or type of a parsed package,
// with the local name its file gives runtime/rt.
type goDecl struct {
	fn  *ast.FuncDecl
	typ *ast.TypeSpec
	rt  string
}

// goDecls are the exported functions, methods and types of a package's
// non-test files, by name ("Go", "Queue.Push", "Closed").
type goDecls map[string]goDecl

// StructFields returns the field names, in order, of the exported struct
// type named name in Source. A record type declared in a v1 std package
// that is not an operations type, such as queue.Closed, is this Go type of
// the same name (docs/spec/stdlib.md, Types), so its fields are the
// record's. It fails when there is no such struct type.
func StructFields(name string) ([]string, error) {
	decls, err := sourceDecls()
	if err != nil {
		return nil, err
	}
	return decls.structFields(name)
}

func (d goDecls) structFields(name string) ([]string, error) {
	g, ok := d[name]
	if !ok || g.typ == nil {
		return nil, fmt.Errorf("runtime/v1: no exported Go type %s", name)
	}
	st, ok := g.typ.Type.(*ast.StructType)
	if !ok || g.typ.TypeParams != nil || g.typ.Assign.IsValid() {
		return nil, fmt.Errorf("runtime/v1: %s is not a non-generic struct type", name)
	}
	fields := []string{}
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			return nil, fmt.Errorf("runtime/v1: %s embeds a field", name)
		}
		for _, n := range f.Names {
			fields = append(fields, n.Name)
		}
	}
	return fields, nil
}

// parseDecls parses every non-test Go file at the root of fsys.
func parseDecls(fsys fs.FS) (goDecls, error) {
	names, err := fs.Glob(fsys, "*.go")
	if err != nil {
		return nil, err
	}
	decls := goDecls{}
	fset := token.NewFileSet()
	for _, name := range names {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, name, data, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		rt := ""
		for _, imp := range f.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == rtPath {
				rt = "rt"
				if imp.Name != nil {
					rt = imp.Name.Name
				}
			}
		}
		for _, d := range f.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.TYPE {
				for _, s := range gd.Specs {
					if ts := s.(*ast.TypeSpec); ts.Name.IsExported() {
						decls[ts.Name.Name] = goDecl{typ: ts, rt: rt}
					}
				}
				continue
			}
			fn, ok := d.(*ast.FuncDecl)
			if !ok || !fn.Name.IsExported() {
				continue
			}
			key := fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) == 1 {
				key = recvName(fn.Recv.List[0].Type) + "." + key
			}
			decls[key] = goDecl{fn: fn, rt: rt}
		}
	}
	return decls, nil
}

// recvName is the base type name of a receiver: Queue for *Queue[T].
func recvName(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.IndexExpr:
			e = x.X
		case *ast.IndexListExpr:
			e = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

func (d goDecls) body(name string) (Body, error) {
	g, ok := d[name]
	if !ok || g.fn == nil {
		return Body{}, fmt.Errorf("runtime/v1: no exported Go body %s", name)
	}
	b, err := goShape(g.fn.Type, g.rt)
	if err != nil {
		return Body{}, fmt.Errorf("runtime/v1: %s: %w", name, err)
	}
	if b.TypeParams, err = d.typeParams(g.fn); err != nil {
		return Body{}, fmt.Errorf("runtime/v1: %s: %w", name, err)
	}
	return b, nil
}

// typeParams are a function's own type parameters, or a method's: the
// names its receiver gives them, with the constraints of the receiver
// type's declaration.
func (d goDecls) typeParams(fn *ast.FuncDecl) ([]TypeParam, error) {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return fieldParams(fn.Type.TypeParams, nil), nil
	}
	recv := fn.Recv.List[0].Type
	base := recvName(recv)
	g, ok := d[base]
	if !ok || g.typ == nil {
		return nil, fmt.Errorf("the receiver type %s is not declared", base)
	}
	if star, ok := recv.(*ast.StarExpr); ok {
		recv = star.X
	}
	var names []string
	switch x := recv.(type) {
	case *ast.IndexExpr:
		names = append(names, types.ExprString(x.Index))
	case *ast.IndexListExpr:
		for _, ix := range x.Indices {
			names = append(names, types.ExprString(ix))
		}
	}
	decl := fieldParams(g.typ.TypeParams, nil)
	if len(names) != len(decl) {
		return nil, fmt.Errorf("the receiver names %d type parameters of %s's %d", len(names), base, len(decl))
	}
	return fieldParams(g.typ.TypeParams, names), nil
}

// fieldParams lists a type-parameter list, renamed by names when given.
func fieldParams(list *ast.FieldList, names []string) []TypeParam {
	if list == nil {
		return nil
	}
	var out []TypeParam
	for _, f := range list.List {
		for _, n := range f.Names {
			name := n.Name
			if names != nil {
				name = names[len(out)]
			}
			out = append(out, TypeParam{Name: name, Constraint: types.ExprString(f.Type)})
		}
	}
	return out
}

// goShape reads a Go function type under the hidden-argument convention.
func goShape(ft *ast.FuncType, rt string) (Body, error) {
	var b Body
	next := 0 // the index in hiddenOrder a further hidden parameter may take
	for _, field := range ft.Params.List {
		n := max(len(field.Names), 1)
		for range n {
			h, hidden := hiddenOf(field.Type, rt)
			switch {
			case hidden && len(b.Params) > 0:
				return Body{}, fmt.Errorf("the hidden %s parameter follows an Ergo parameter", h)
			case hidden:
				i := slices.Index(hiddenOrder, h)
				if i < next {
					return Body{}, fmt.Errorf("the hidden %s parameter is out of order or repeated (order: %v)", h, hiddenOrder)
				}
				next = i + 1
				b.Hidden = append(b.Hidden, h)
			default:
				p := Param{Type: types.ExprString(field.Type)}
				if inner, ok := field.Type.(*ast.FuncType); ok {
					fb, err := goShape(inner, rt)
					if err != nil {
						return Body{}, fmt.Errorf("function parameter %d: %w", len(b.Params)+1, err)
					}
					p.Func = &fb
				}
				b.Params = append(b.Params, p)
			}
		}
	}
	if ft.Results != nil {
		for _, field := range ft.Results.List {
			for range max(len(field.Names), 1) {
				b.Results = append(b.Results, types.ExprString(field.Type))
			}
		}
	}
	return b, nil
}

// hiddenOf names the hidden parameter whose Go type e is: *Scope, Site or
// *rt (the file's name for runtime/rt).Task.
func hiddenOf(e ast.Expr, rt string) (Hidden, bool) {
	switch x := e.(type) {
	case *ast.Ident:
		if x.Name == "Site" {
			return CallSite, true
		}
	case *ast.StarExpr:
		switch y := x.X.(type) {
		case *ast.Ident:
			if y.Name == "Scope" {
				return Frame, true
			}
		case *ast.SelectorExpr:
			if id, ok := y.X.(*ast.Ident); ok && rt != "" && id.Name == rt && y.Sel.Name == "Task" {
				return CallerTask, true
			}
		}
	}
	return "", false
}
