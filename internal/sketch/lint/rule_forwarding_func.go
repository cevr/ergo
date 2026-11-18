package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// forwardingFunc is the indirection rule of the strict preset (catalog
// CR14, CR15; anti-slop middle-man, lazy-element): a top-level function
// whose body is one call of another function that passes its parameters
// on, all of them, in order, and nothing else. It only renames the
// callee. A dot call's receiver counts as the first argument, and a try
// around the call changes nothing. Out of scope: main, a method that
// provides an operation (its name is the operation's), a call that
// writes type arguments (it binds a type, so it says more than the
// callee), a call of a generic function from a function with no type
// parameters (it names one instance), a function used as a value (a
// machine's entries, a callback: the language may need the name there), a
// callee that is a Go binding (an adapter's wrapper) and a call of the
// function itself. A function with no parameters names a\n// value, not a second name, and is out of scope too.
var forwardingFunc = &Rule{
	Name:    "forwarding-func",
	Code:    "L121",
	Doc:     "A function whose body only calls another with its parameters, in order, is a second name for it; call the other directly.",
	Instead: "call the function it forwards to directly, and delete it",
	Default: Warning,
	Check:   checkForwardingFunc,
}

func checkForwardingFunc(p *Pass) {
	sites := p.Memo(func() any { return callSitesOf(p, false) }).(*callSites)
	entry := p.Package.Entry()
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Recv != nil || f.Derived != nil || f.Extern != nil || f.Body == nil || len(f.Body.List) != 1 {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok || fn == entry || fn.ProvidesOperation() || sites.value[fn] {
			continue
		}
		call := onlyCall(f.Body.List[0])
		if call == nil || call.Trailing != nil {
			continue
		}
		if _, typed := call.Fun.(*syntax.IndexExpr); typed {
			continue
		}
		c := p.Info.Calls[call]
		if c == nil || c.Func == nil || c.Func == fn || c.Func.Decl == nil || c.Func.Decl.Body == nil {
			continue
		}
		if len(c.TypeArgs) > 0 && f.TypeParams == nil {
			continue
		}
		args := call.Args
		if c.Recv != nil {
			args = append([]syntax.Expr{c.Recv}, args...)
		}
		if !passesParams(p.Info, f, args) {
			continue
		}
		p.ReportInstead(f.Name.Pos(), f.Name.End(), f.Name.Name+" only calls "+c.Func.Name+" with its parameters, in order",
			"call "+c.Func.Name+" where "+f.Name.Name+" is called, and delete "+f.Name.Name, nil)
	}
}

// onlyCall is the call a function's one statement is, a try or a return
// around it allowed, or nil.
func onlyCall(s syntax.Stmt) *syntax.CallExpr {
	var x syntax.Expr
	switch s := s.(type) {
	case *syntax.ExprStmt:
		x = s.X
	case *syntax.ReturnStmt:
		if len(s.Results) != 1 {
			return nil
		}
		x = s.Results[0]
	default:
		return nil
	}
	if t, ok := x.(*syntax.TryExpr); ok {
		x = t.X
	}
	call, _ := x.(*syntax.CallExpr)
	return call
}

// passesParams reports whether args are f's parameters, every one, in
// order, each a plain use.
func passesParams(info *check.Info, f *syntax.FuncDecl, args []syntax.Expr) bool {
	var params []check.Object
	if f.Type.Params != nil {
		for _, field := range f.Type.Params.List {
			if len(field.Names) == 0 {
				return false
			}
			for _, id := range field.Names {
				o := info.Defs[id]
				if o == nil {
					return false
				}
				params = append(params, o)
			}
		}
	}
	if len(params) == 0 || len(args) != len(params) {
		return false
	}
	for i, a := range args {
		id, ok := a.(*syntax.Ident)
		if !ok || info.Uses[id] != params[i] {
			return false
		}
	}
	return true
}
