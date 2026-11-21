package lint

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// modeParameter is the indirection rule of the strict preset (catalog
// EF37, a mode argument that selects a code path; anti-slop
// speculative-generality): a bool parameter of a top-level function that
// the body reads only as the condition, or the negated condition, of an
// if written directly in the body, and that every call in the program
// passes as a constant. The function is two functions behind one name,
// and every caller picks one by a literal. A function some caller passes
// a computed bool (a flag, a field) maps data to a value and is out of
// scope, as is one never called or used as a value (its callers are not
// all in the run). Out of scope too: main, a method that provides an
// operation (the operation fixes its parameters), Go bindings, and a
// parameter read anywhere else (passed on, stored, combined with another
// condition).
var modeParameter = &Rule{
	Name:    "mode-parameter",
	Code:    "L123",
	Doc:     "A bool parameter that every caller passes as a constant and the body reads only to pick a branch of its top-level if makes one function two; split it.",
	Instead: "split the function into one per mode, and let each caller call the one it means",
	Default: Warning,
	Check:   checkModeParameter,
}

// callSites is how the program calls each function: its calls'
// arguments, a dot call's receiver first, and whether it is used as a
// value.
type callSites struct {
	calls map[*check.Func][][]syntax.Expr
	value map[*check.Func]bool
	info  map[syntax.Expr]*check.Info
}

func checkModeParameter(p *Pass) {
	sites := p.Memo(func() any { return callSitesOf(p, false) }).(*callSites)
	entry := p.Package.Entry()
	for _, d := range p.File.Decls {
		f, ok := d.(*syntax.FuncDecl)
		if !ok || f.Derived != nil || f.Extern != nil || f.Body == nil || f.Type.Params == nil {
			continue
		}
		fn, ok := p.Info.Defs[f.Name].(*check.Func)
		if !ok || fn == entry || fn.ProvidesOperation() || sites.value[fn] || len(sites.calls[fn]) == 0 {
			continue
		}
		// the conditions of the body's own ifs, by the parameter they read
		modes := map[*syntax.Ident]bool{}
		for _, s := range f.Body.List {
			if is, ok := s.(*syntax.IfStmt); ok {
				if id := condParam(is.Cond); id != nil {
					modes[id] = true
				}
			}
		}
		index := -1
		for _, field := range f.Type.Params.List {
			for _, id := range field.Names {
				index++
				v, ok := p.Info.Defs[id].(*check.Var)
				if !ok || !check.IsBoolean(v.Type) || !sites.constantAt(fn, index) {
					continue
				}
				uses, picks := 0, 0
				syntax.Inspect(f.Body, func(n syntax.Node) bool {
					if x, ok := n.(*syntax.Ident); ok && p.Info.Uses[x] == v {
						uses++
						if modes[x] {
							picks++
						}
					}
					return true
				})
				if uses > 0 && uses == picks {
					p.Report(id.Pos(), id.End(), "the bool parameter "+id.Name+" of "+f.Name.Name+" only picks a branch of its top-level if, and every call passes a constant")
				}
			}
		}
	}
}

// constantAt reports whether every call of fn passes a constant at
// parameter index i.
func (s *callSites) constantAt(fn *check.Func, i int) bool {
	for _, args := range s.calls[fn] {
		if i >= len(args) || s.info[args[i]].Values[args[i]] == nil {
			return false
		}
	}
	return true
}

// callSitesOf reads every call and value use of a function in the
// program; with code, only the calls outside tests and laws are read.
func callSitesOf(p *Pass, code bool) *callSites {
	s := &callSites{calls: map[*check.Func][][]syntax.Expr{}, value: map[*check.Func]bool{}, info: map[syntax.Expr]*check.Info{}}
	for _, u := range p.Program() {
		info := u.Package.Info
		for _, f := range u.Files {
			if f.Syntax == nil {
				continue
			}
			called := map[*syntax.Ident]bool{}
			excluded := map[*syntax.CallExpr]bool{}
			exclude := func(n syntax.Node) bool {
				if x, ok := n.(*syntax.CallExpr); ok {
					excluded[x] = true
				}
				return true
			}
			if code {
				syntax.Inspect(f.Syntax, func(n syntax.Node) bool {
					switch n.(type) {
					case *syntax.TestDecl, *syntax.LawDecl:
						syntax.Inspect(n, exclude)
						return false
					}
					return true
				})
			}
			syntax.Inspect(f.Syntax, func(n syntax.Node) bool {
				x, ok := n.(*syntax.CallExpr)
				if !ok {
					return true
				}
				c := info.Calls[x]
				if c == nil || c.Func == nil {
					return true
				}
				switch fun := unparen(x.Fun).(type) {
				case *syntax.Ident:
					called[fun] = true
				case *syntax.SelectorExpr:
					called[fun.Sel] = true
				case *syntax.IndexExpr:
					if id, ok := fun.X.(*syntax.Ident); ok {
						called[id] = true
					}
				}
				args := x.Args
				if c.Recv != nil {
					args = append([]syntax.Expr{c.Recv}, args...)
				}
				for _, a := range args {
					s.info[a] = info
				}
				if !excluded[x] {
					s.calls[c.Func] = append(s.calls[c.Func], args)
				}
				return true
			})
			syntax.Inspect(f.Syntax, func(n syntax.Node) bool {
				if id, ok := n.(*syntax.Ident); ok && !called[id] {
					if fn, ok := info.Uses[id].(*check.Func); ok {
						s.value[fn] = true
					}
				}
				return true
			})
		}
	}
	return s
}

// condParam is the identifier a condition is, or negates, or nil.
func condParam(x syntax.Expr) *syntax.Ident {
	for {
		switch e := x.(type) {
		case *syntax.ParenExpr:
			x = e.X
		case *syntax.UnaryExpr:
			if e.Op != syntax.NOT {
				return nil
			}
			x = e.X
		case *syntax.Ident:
			return e
		default:
			return nil
		}
	}
}
