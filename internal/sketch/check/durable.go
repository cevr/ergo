package check

import (
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// Pure parameters (rows.md, Footprints 6; grammar.md, Declarations 15): a
// function declaration's parameter written `m pure T` requires of each
// call that the checker sees the function its argument runs, and that the
// function is pure: its footprint is empty (Footprints 2 and 3), no
// capability, no task start and no opaque call. For a machine (a prelude
// Machine or a machine declaration's type) that function is the step; for
// a function type it is the function itself. The diagnostic names the
// first call in that function's body that makes it impure.
//
// The checker sees the function of an argument that is:
//   - a machine declaration's value X{}, whose step its declaration names;
//   - a Machine literal whose Step names a package function (a generic one
//     instantiated, step[S, E], too), or the Step of a machine it sees;
//   - a function's name, for a function type;
//   - a call of a function whose body is one expression it sees, with the
//     callee's parameters bound to the call's arguments (machines.Start's
//     body is such a literal, so machines.Start(X{}, input) is seen);
//   - a parameter of the enclosing function that is written pure itself:
//     its own callers' calls are checked.
//
// Anything else is S108 too. The rule reads the declaration only, so it
// holds for a runner in any package alike: std's durable runners
// (actor.StartDurable, actor.SuperviseDurable and journal.Start; runtime.md,
// Machines and actors 17) write the clause on their machine.

// pureArg is an argument of a call to a pure parameter, kept for
// checkPureArgs, which runs once the footprints are known.
type pureArg struct {
	arg    syntax.Expr
	callee string
	param  string
	fn     bool  // the parameter's type is a function type, not a machine
	encl   *Func // the function declaration whose body holds the call
	file   *fileInfo
}

// pureParams are the indexes of the parameters of f's declaration written
// pure, in order.
func pureParams(f *Func) []int {
	if f == nil || f.Decl == nil || f.Decl.Type == nil || f.Decl.Type.Params == nil {
		return nil
	}
	var out []int
	i := 0
	for _, fld := range f.Decl.Type.Params.List {
		n := max(len(fld.Names), 1)
		for range n {
			if fld.Pure {
				out = append(out, i)
			}
			i++
		}
	}
	return out
}

// pureKind reports whether t is a type a pure parameter may have, and
// whether it is a function type.
func pureKind(t Type) (ok, fn bool) {
	if _, ok := under(t).(*Signature); ok {
		return true, true
	}
	n, ok := t.(*Named)
	if !ok {
		return false, false
	}
	if m := preludeType("Machine"); m != nil && n.origin() == m {
		return true, false
	}
	_, ok = n.under.(*MachineType)
	return ok, false
}

// callArg is the argument a call x passes for parameter i of f: a dot
// call x.f(…) of a function with no receiver passes x for the first one.
func callArg(x *syntax.CallExpr, k *Call, f *Func, i int) syntax.Expr {
	if k.Recv != nil && f.Decl.Recv == nil {
		if i == 0 {
			return k.Recv
		}
		i--
	}
	if i < len(x.Args) {
		return x.Args[i]
	}
	return nil
}

// recordPureArgs keeps each argument the call x passes to a pure parameter
// of its callee.
func (c *checker) recordPureArgs(x *syntax.CallExpr) {
	k := c.pkg.Info.Calls[x]
	if k == nil || k.Kind != CallFunc || k.Func == nil {
		return
	}
	f := k.Func
	idx := pureParams(f)
	if len(idx) == 0 {
		return
	}
	sig := f.Sig
	if sig == nil {
		return
	}
	name := f.Name
	if f.Pkg != nil && f.Pkg != c.pkg {
		name = f.Pkg.Path + "." + f.Name
	}
	var encl *Func
	if c.fn != nil {
		encl = c.fn.fn
	}
	for _, i := range idx {
		arg := callArg(x, k, f, i)
		if arg == nil || i >= len(sig.Params) {
			continue
		}
		ok, fn := pureKind(sig.Params[i].Type)
		if !ok {
			continue // the declaration's own S108 (checkPureClauses)
		}
		c.pureArgs = append(c.pureArgs, pureArg{arg: arg, callee: name, param: sig.Params[i].Name, fn: fn, encl: encl, file: c.cur})
	}
}

// checkPureArgs reports each argument of a pure parameter whose function
// the checker does not see, or sees impure; and each clause written where
// it means nothing.
func (c *checker) checkPureArgs() {
	c.checkPureClauses()
	for _, d := range c.pureArgs {
		c.cur = d.file
		s := pureSeer{encl: d.encl}
		var f *Func
		if d.fn {
			f = s.function(c.pkg.Info, d.arg, nil, 0)
		} else {
			f = s.step(c.pkg.Info, d.arg, nil, 0)
		}
		what, role := "step", "step"
		if d.fn {
			what, role = "function", "function"
		}
		if s.forwarded {
			continue
		}
		if f == nil {
			c.errorf(d.arg, CodeType, "%s requires its parameter %s to be pure, so the checker must see its %s: pass %s (rows.md, Footprints 6)", d.callee, d.param, what, seenForms(d.fn))
			continue
		}
		info := f.Pkg.Info
		if info == nil {
			continue
		}
		fp, ok := info.Footprints[f.Decl]
		if !ok || fp.Empty() {
			continue
		}
		c.errorf(d.arg, CodeType, "%s requires its parameter %s to be pure: %s (rows.md, Footprints 6)", d.callee, d.param, impureCall(role, f, fp))
	}
}

// seenForms names the arguments whose function the checker sees.
func seenForms(fn bool) string {
	if fn {
		return "a function's name, a call of a function whose body is one, or a parameter written pure"
	}
	return "a machine declaration's value X{}, a Machine literal whose Step names a function, a call of a function whose body is one of these, or a parameter written pure"
}

// checkPureClauses reports the clause pure written on anything but a
// function declaration's parameter, and on a parameter whose type is no
// function type and no machine.
func (c *checker) checkPureClauses() {
	for _, fi := range c.files {
		decls := map[*syntax.FieldList]*syntax.FuncDecl{}
		for _, d := range fi.syntax.Decls {
			if fd, ok := d.(*syntax.FuncDecl); ok && fd.Type != nil && fd.Type.Params != nil {
				decls[fd.Type.Params] = fd
			}
		}
		// The names a call calls: a use of a function with a pure
		// parameter anywhere else takes it as a value, and a call of
		// the value would pass its arguments unchecked.
		called := map[*syntax.Ident]bool{}
		syntax.Inspect(fi.syntax, func(n syntax.Node) bool {
			if x, ok := n.(*syntax.CallExpr); ok {
				fun := unparen(x.Fun)
				if ix, ok := fun.(*syntax.IndexExpr); ok {
					fun = unparen(ix.X)
				}
				switch fun := fun.(type) {
				case *syntax.Ident:
					called[fun] = true
				case *syntax.SelectorExpr:
					called[fun.Sel] = true
				}
			}
			return true
		})
		syntax.Inspect(fi.syntax, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Ident); ok {
				if f, ok := c.pkg.Info.Uses[id].(*Func); ok && !called[id] && len(pureParams(f)) > 0 {
					c.cur = fi
					c.errorf(id, CodeType, "%s has a pure parameter, so it is only called: a call checks its argument, and a call of a value could not (rows.md, Footprints 6)", f.Name)
				}
				return true
			}
			l, ok := n.(*syntax.FieldList)
			if !ok {
				return true
			}
			fd := decls[l]
			i := 0
			for _, fld := range l.List {
				n := max(len(fld.Names), 1)
				if fld.Pure {
					c.cur = fi
					if fd == nil {
						c.errorIn(fld, fld.PurePos, fld.PurePos+syntax.Pos(len("pure")), CodeType, "pure is written on a parameter of a function declaration, which its callers' arguments must meet (rows.md, Footprints 6)")
					} else if f, _ := c.pkg.Info.Defs[fd.Name].(*Func); f != nil && f.Sig != nil && i < len(f.Sig.Params) {
						if ok, _ := pureKind(f.Sig.Params[i].Type); !ok {
							c.errorIn(fld, fld.PurePos, fld.PurePos+syntax.Pos(len("pure")), CodeType, "pure requires a function the checker sees, so it is written before a function type or a machine, not %s (rows.md, Footprints 6)", f.Sig.Params[i].Type)
						}
					}
				}
				i += n
			}
			return true
		})
	}
}

// pureSeer finds the function an argument of a pure parameter runs. Its
// bindings map a callee's parameter names to the arguments of the call
// being seen through (a body of one expression has no other names).
type pureSeer struct {
	encl      *Func
	forwarded bool
}

// pureBinding is the argument x a call passed for a parameter, read in
// info under the bindings of its own body.
type pureBinding struct {
	info *Info
	x    syntax.Expr
	env  map[string]*pureBinding
}

// maxSeeThrough bounds the calls seen through: a function whose body
// calls itself is no machine the checker sees.
const maxSeeThrough = 8

// param resolves the identifier id: a parameter bound by a call seen
// through, or a pure parameter of the enclosing function (forwarded).
func (s *pureSeer) param(info *Info, id *syntax.Ident, env map[string]*pureBinding) (*pureBinding, bool) {
	v, ok := info.Uses[id].(*Var)
	if !ok {
		return nil, false
	}
	if env != nil {
		b := env[id.Name]
		return b, b != nil
	}
	if v.param && v.id != nil && s.encl != nil && s.encl.Decl != nil && s.encl.Decl.Type != nil && s.encl.Decl.Type.Params != nil {
		for _, fld := range s.encl.Decl.Type.Params.List {
			if fld.Pure && slices.Contains(fld.Names, v.id) {
				s.forwarded = true
			}
		}
	}
	return nil, false
}

// through is the result expression of the function a call x runs, with
// its parameters bound to x's arguments, when its body is one expression.
func (s *pureSeer) through(info *Info, x *syntax.CallExpr, env map[string]*pureBinding) (*Info, syntax.Expr, map[string]*pureBinding) {
	k := info.Calls[x]
	if k == nil || k.Kind != CallFunc || k.Func == nil || k.Func.Decl == nil || k.Func.Decl.Body == nil || k.Func.Pkg == nil || k.Func.Pkg.Info == nil {
		return nil, nil, nil
	}
	f := k.Func
	body := f.Decl.Body.List
	if len(body) != 1 {
		return nil, nil, nil
	}
	var res syntax.Expr
	switch st := body[0].(type) {
	case *syntax.ExprStmt:
		res = st.X
	case *syntax.ReturnStmt:
		if len(st.Results) == 1 {
			res = st.Results[0]
		}
	}
	if res == nil {
		return nil, nil, nil
	}
	inner := map[string]*pureBinding{}
	i := 0
	for _, fld := range f.Decl.Type.Params.List {
		for _, n := range fld.Names {
			if a := callArg(x, k, f, i); a != nil {
				inner[n.Name] = &pureBinding{info: info, x: a, env: env}
			}
			i++
		}
		if len(fld.Names) == 0 {
			i++
		}
	}
	return f.Pkg.Info, res, inner
}

// step is the step function of the machine x, or nil.
func (s *pureSeer) step(info *Info, x syntax.Expr, env map[string]*pureBinding, depth int) *Func {
	if depth > maxSeeThrough {
		return nil
	}
	x = unparen(x)
	switch x := x.(type) {
	case *syntax.Ident:
		if b, ok := s.param(info, x, env); ok {
			return s.step(b.info, b.x, b.env, depth+1)
		}
		return nil
	case *syntax.CallExpr:
		in, res, inner := s.through(info, x, env)
		if res == nil {
			return nil
		}
		return s.step(in, res, inner, depth+1)
	case *syntax.CompositeLit:
		n, ok := info.Types[x].(*Named)
		if !ok {
			return nil
		}
		if m := preludeType("Machine"); m != nil && n.origin() == m {
			for _, e := range x.Elts {
				kv, ok := e.(*syntax.KeyValueExpr)
				if !ok {
					continue
				}
				if k, ok := kv.Key.(*syntax.Ident); !ok || k.Name != "Step" {
					continue
				}
				return s.stepValue(info, kv.Value, env, depth)
			}
			return nil
		}
		return declaredStep(n)
	}
	return nil
}

// stepValue is the function a Machine literal's Step names: a function's
// name, or m.Step of a machine m the checker sees.
func (s *pureSeer) stepValue(info *Info, v syntax.Expr, env map[string]*pureBinding, depth int) *Func {
	v = unparen(v)
	if sel, ok := v.(*syntax.SelectorExpr); ok && sel.Sel.Name == "Step" {
		return s.step(info, sel.X, env, depth+1)
	}
	return s.function(info, v, env, depth)
}

// function is the package function x names (a generic one instantiated,
// too), or nil.
func (s *pureSeer) function(info *Info, x syntax.Expr, env map[string]*pureBinding, depth int) *Func {
	if depth > maxSeeThrough {
		return nil
	}
	x = unparen(x)
	if ix, ok := x.(*syntax.IndexExpr); ok {
		x = unparen(ix.X)
	}
	switch x := x.(type) {
	case *syntax.Ident:
		if f, ok := info.Uses[x].(*Func); ok {
			if f.Decl == nil || f.Decl.Body == nil {
				return nil
			}
			return f
		}
		if b, ok := s.param(info, x, env); ok {
			return s.function(b.info, b.x, b.env, depth+1)
		}
	case *syntax.CallExpr:
		in, res, inner := s.through(info, x, env)
		if res == nil {
			return nil
		}
		return s.function(in, res, inner, depth+1)
	}
	return nil
}

// declaredStep is the step function a machine declaration names, for its
// value X{}, or nil.
func declaredStep(n *Named) *Func {
	if n.Obj == nil || n.Obj.Decl == nil || n.Obj.Pkg == nil {
		return nil
	}
	if _, ok := n.under.(*MachineType); !ok {
		return nil
	}
	spec, ok := n.Obj.Decl.Spec.(*syntax.MachineType)
	if !ok {
		return nil
	}
	id, ok := spec.Step.(*syntax.Ident)
	if !ok || n.Obj.Pkg.Scope == nil {
		return nil
	}
	f, _ := n.Obj.Pkg.Scope.names[id.Name].(*Func)
	if f == nil || f.Decl == nil || f.Decl.Body == nil {
		return nil
	}
	return f
}

// impureCall names the first call in step's own frame that makes its
// footprint fp not empty, with its position; when none of its own calls
// does (a literal it calls in place does), it names what fp holds. role
// names what step is to its caller: "step", "function", or "predicate"
// for an invariant law's (machinelaw.go).
func impureCall(role string, step *Func, fp Footprint) string {
	info := step.Pkg.Info
	var found syntax.Node
	var why string
	inspectFrame(info, step.Decl.Body, func(n syntax.Node, _ []*syntax.CallExpr) bool {
		if found != nil {
			return false
		}
		switch n := n.(type) {
		case *syntax.RangeStmt:
			if l := info.Loops[n]; l == nil || l.Domain == LoopSeq {
				found, why = n, "a for over a Seq, which calls unknown code"
			}
		case *syntax.CallExpr:
			if w := impureWhy(info, n); w != "" {
				found, why = n, w
			}
		}
		return found == nil
	})
	if found != nil && step.file != nil {
		line, col := step.file.syntax.Position(found.Pos())
		return fmt.Sprintf("%s %s calls %s at %d:%d", role, step.Name, why, line, col)
	}
	switch {
	case len(fp.Caps) > 0:
		return fmt.Sprintf("%s %s uses the capability %s", role, step.Name, fp.Caps[0])
	case fp.Tasks:
		return fmt.Sprintf("%s %s starts a task", role, step.Name)
	default:
		return fmt.Sprintf("%s %s calls code whose footprint is unknown", role, step.Name)
	}
}

// PureParams are the indexes of the parameters of f's declaration written
// pure (Footprints 6), in order: what a call of f must pass a function
// the checker sees.
func PureParams(f *Func) []int { return pureParams(f) }

// MakesHost reports an extern whose bound body is inert (Body.Inert), such
// as actor.NewHost's, the one host function a pure step may call (wf10
// DH1; runtime.md, Machines and actors 17): it makes an empty cell and
// acts on nothing outside. A host is entry data, not durable data: one
// made in a replay is unbound until the recovered actor's first entry
// binds it, so a replay that makes it again acts twice on nothing. Every
// other host function stays S108. The fact is the body's, for an extern of
// any package (NP4b).
func MakesHost(f *Func) bool { return BodyOf(f).Inert }

// impureWhy is the reason the call x adds to its frame's footprint, or ""
// when it adds nothing (footprinter.call is the rule it follows).
func impureWhy(info *Info, x *syntax.CallExpr) string {
	k := info.Calls[x]
	if k == nil {
		return "a call whose footprint is unknown"
	}
	switch k.Kind {
	case CallProviderOp:
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
			switch r := sel.X.(type) {
			case *syntax.Ident:
				return "the capability operation " + r.Name + "." + sel.Sel.Name
			case *syntax.SelectorExpr:
				if id, ok := r.X.(*syntax.Ident); ok {
					return "the capability operation " + id.Name + "." + r.Sel.Name + "." + sel.Sel.Name
				}
			}
			return "the capability operation " + sel.Sel.Name
		}
		return "a capability operation"
	case CallFunc:
		f := k.Func
		switch {
		case f == nil || f.Decl == nil:
			return "a function whose footprint is unknown"
		case pureStd(f):
			if k.StartsTask {
				return f.Name + ", which starts a task"
			}
			return ""
		case MakesHost(f):
			return ""
		case f.Decl.Extern != nil || f.Decl.Body == nil:
			return "the host function " + f.Name
		}
		if f.Pkg != nil && f.Pkg.Info != nil {
			if g, ok := f.Pkg.Info.Footprints[f.Decl]; ok && g.Empty() {
				return ""
			}
		}
		return f.Name + ", which is not pure"
	case CallFuncValue:
		if _, ok := unparen(x.Fun).(*syntax.FuncLit); ok {
			return ""
		}
		return "a function value"
	case CallOp, CallLawOp:
		return "an operation of a value"
	case CallWith:
		return "a with, which runs its providers"
	}
	return ""
}
