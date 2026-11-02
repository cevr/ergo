package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
)

// checkTypeDecls checks what a type declaration states beyond its body: a
// struct does not contain itself by value (types.md, Structs 4), and a
// machine's initial value and step function fit its state and signal unions
// (runtime.md, Machines and actors 1).
func (c *checker) checkTypeDecls() {
	c.checkRecursiveStructs()
	for _, tn := range c.machines {
		if tn.Pkg != c.pkg {
			continue
		}
		c.checkMachine(tn)
	}
}

// checkRecursiveStructs classifies strongly connected components of the
// direct-definition and by-value containment graph together. Indirection
// through pointers, slices, Maps, functions, operation types or unions
// ends a containment path. A component made only of definition edges is
// a defined-type cycle; any record or tuple edge makes it an infinite-size
// cycle. The diagnostic names and locates the lexically first declared
// type in the component, independent of declaration and resolution order.
func (c *checker) checkRecursiveStructs() {
	var fields func(Type, *[]*Named)
	fields = func(t Type, out *[]*Named) {
		switch t := t.(type) {
		case *Named:
			if t.foreign || t.Obj.Decl == nil || c.monoBad(t.origin().TParams) {
				return
			}
			if t.Defined() != nil {
				*out = append(*out, t)
				return
			}
			switch t.underlying().(type) {
			case *Struct, *Tuple:
				*out = append(*out, t)
			}
		case *Struct:
			for _, f := range t.Fields {
				fields(f.Type, out)
			}
		case *Tuple:
			for _, e := range t.Types {
				fields(e, out)
			}
		}
	}
	// Iterative Tarjan walk: long finite chains need no recursive walk.
	type frame struct {
		n    *Named
		next []*Named
		i    int
	}
	index, low := map[*Named]int{}, map[*Named]int{}
	on, self := map[*Named]bool{}, map[*Named]bool{}
	var stack []*Named
	var cycles [][]*Named
	serial := 0
	for _, tn := range c.pkg.Types {
		root := tn.Type.(*Named)
		if index[root] != 0 || c.monoBad(root.TParams) {
			continue
		}
		var work []frame
		push := func(n *Named) {
			serial++
			index[n], low[n] = serial, serial
			on[n] = true
			stack = append(stack, n)
			var next []*Named
			if base := n.Defined(); base != nil {
				fields(base, &next)
			} else {
				switch u := n.underlying().(type) {
				case *Struct:
					fields(u, &next)
				case *Tuple:
					fields(u, &next)
				}
			}
			work = append(work, frame{n: n, next: next})
		}
		push(root)
		for len(work) > 0 {
			top := &work[len(work)-1]
			if top.i < len(top.next) {
				m := top.next[top.i]
				top.i++
				self[top.n] = self[top.n] || m == top.n
				if index[m] == 0 {
					push(m)
				} else if on[m] {
					low[top.n] = min(low[top.n], index[m])
				}
				continue
			}
			n := top.n
			work = work[:len(work)-1]
			if len(work) > 0 {
				p := work[len(work)-1].n
				low[p] = min(low[p], low[n])
			}
			if low[n] != index[n] {
				continue
			}
			var component []*Named
			for {
				m := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				on[m] = false
				component = append(component, m)
				if m == n {
					break
				}
			}
			if len(component) > 1 || self[n] {
				cycles = append(cycles, component)
			}
		}
	}
	reported := map[*Named]bool{}
	saved := c.cur
	defer func() { c.cur = saved }()
	for _, cyc := range cycles {
		var first *Named
		defined, duplicate := true, false
		for _, m := range cyc {
			o := m.origin()
			duplicate = duplicate || reported[o]
			defined = defined && m.Defined() != nil
			if o.Obj.Pkg == c.pkg && o.Obj.Decl != nil &&
				(first == nil || o.Obj.Name < first.Obj.Name) {
				first = o
			}
		}
		for _, m := range cyc {
			reported[m.origin()] = true
		}
		if duplicate || first == nil {
			continue
		}
		c.cur = first.Obj.file
		if defined {
			c.errorf(first.Obj.Decl.Name, CodeType, "%s is defined over itself (types.md, Defined types)", first.Obj.Name)
		} else {
			c.errorf(first.Obj.Decl.Name, CodeType, "%s contains itself by value, through its fields or tuple elements; its size would be infinite (types.md, Structs 4)", first.Obj.Name)
		}
	}
	// No later body check follows a cyclic definition chain. The package
	// already has its one source diagnostic for each invalid component.
	for n := range reported {
		n.def, n.under = nil, invalid
	}
}

// checkMachine checks `type X machine[S, E] { initial v; step f }`: v is a
// value of S, checked in a closed context like a package var; f names a
// package function from S and E to Decision[S, O] with any rows
// (runtime.md, Machines and actors 1).
func (c *checker) checkMachine(tn *TypeName) {
	c.cur = tn.file
	spec := tn.Decl.Spec.(*syntax.MachineType)
	n := tn.Type.(*Named)
	m, ok := n.under.(*MachineType)
	if !ok {
		return
	}
	state, signal := m.State, m.Signal
	for _, part := range []struct {
		t Type
		x syntax.Expr
		w string
	}{{state, spec.State, "state"}, {signal, spec.Signal, "signal"}} {
		if !isInvalid(part.t) && asUnion(part.t) == nil {
			c.errorf(part.x, CodeType, "a machine's %s type must be a union, not %s", part.w, part.t)
		}
	}
	valid := !isInvalid(state) && !isInvalid(signal) && asUnion(state) != nil && asUnion(signal) != nil
	if spec.Initial == nil || spec.Step == nil {
		c.errorf(tn.Decl.Name, CodeType, "machine %s declares both initial and step (runtime.md, Machines and actors 1)", tn.Name)
	}
	if spec.Initial != nil {
		t := &target{name: "the initial value of " + tn.Name, declErrors: newRow(), declUses: newRow(), resultKnown: true}
		prev := c.fn
		c.fn = &funcCtx{t: t, scope: newScope(c.pkg.Scope)}
		var hint Type
		if valid {
			hint = state
		}
		op := c.valueExpr(spec.Initial, hint)
		if valid {
			c.assign(&op, state, "machine "+tn.Name+"'s initial value")
		}
		c.fn = prev
	}
	if spec.Step == nil {
		return
	}
	id, ok := spec.Step.(*syntax.Ident)
	if !ok {
		c.errorf(spec.Step, CodeType, "a machine's step names a function of this package (runtime.md, Machines and actors 1)")
		return
	}
	o := c.pkg.Scope.names[id.Name]
	if o == nil {
		c.errorf(id, CodeUnknown, "unknown function %s", id.Name)
		return
	}
	f, ok := o.(*Func)
	if !ok {
		c.errorf(id, CodeType, "%s is not a function; a machine's step names a function (runtime.md, Machines and actors 1)", id.Name)
		return
	}
	f.called = true
	c.pkg.Info.Uses[id] = f
	if !valid || f.Sig == nil {
		return
	}
	sig := f.Sig
	decision := preludeType("Decision")
	want := "func(" + state.String() + ", " + signal.String() + ") Decision[" + state.String() + ", O]"
	bad := len(sig.TParams) > 0 || len(sig.Params) != 2 || len(sig.Results) != 1
	if !bad {
		bad = !c.identical(sig.Params[0].Type, state) || !c.identical(sig.Params[1].Type, signal)
	}
	if !bad {
		r, ok := sig.Results[0].(*Named)
		bad = !ok || decision == nil || r.origin() != decision || len(r.TArgs) == 0 || !c.identical(r.TArgs[0], state)
	}
	if bad {
		c.errorf(id, CodeType, "the step %s of machine %s must have the type %s, with any error and capability rows (runtime.md, Machines and actors 1)", id.Name, tn.Name, want)
	}
}

// checkEntry checks main (rows.md, Capability rows 4; README Gaps 4 and 5):
// no parameters, no type parameters, and no value result; an error row is
// allowed. Its capability row is not restricted at check time (Q4). A call
// of main is refused where it is checked (S113).
func (c *checker) checkEntry() {
	f, ok := c.pkg.Scope.names["main"].(*Func)
	if !ok {
		return
	}
	c.cur = f.file
	d := f.Decl
	if d.TypeParams != nil && len(d.TypeParams.List) > 0 {
		c.errorf(d.TypeParams, CodeEntry, "main takes no type parameters; the runtime calls it (rows.md, Capability rows 4)")
	}
	if d.Type.Params != nil && len(d.Type.Params.List) > 0 {
		c.errorf(d.Type.Params, CodeEntry, "main takes no parameters; the runtime calls it (rows.md, Capability rows 4)")
	}
	if d.Type.Results != nil {
		for _, r := range d.Type.Results.List {
			if _, isRow := r.Type.(*syntax.ErrorRow); isRow {
				continue
			}
			c.errorf(r.Type, CodeEntry, "main returns no value; only an error row is allowed, as Go's main returns nothing (rows.md, Capability rows 4; README Gap 4)")
			break
		}
	}
}

// checkErrorMethods reports a function Error on an error type on an error type: the
// emitter generates it from the fields (types.md, Methods 5).
func (c *checker) checkErrorMethods() {
	for _, f := range c.funcs {
		owner := f.owner // Error(e E) string, reached as e.Error() (196m)
		if owner == nil || f.Name != "Error" {
			continue
		}
		if _, ok := c.errorTypes[owner.origin()]; !ok {
			continue
		}
		c.cur = f.file
		c.errorf(f.Decl.Name, CodeType, "%s is an error type, so it declares no Error method: its text comes from its fields (types.md, Methods 5)", owner.Obj.Name)
	}
}
