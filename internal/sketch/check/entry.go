package check

import (
	"ergo.local/prototype/internal/sketch/syntax"
	"slices"
	"strconv"
	"strings"
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

// checkRecursiveStructs implements R2' over interned type instances. Identity
// instances establish intrinsic guilt; mutually dependent origins report as
// one group. After removing their intrinsic edges (the field paths that close
// a cycle at the identity instance) at every instantiation, each remaining
// cycle is attributed to the declarations that wrote its closing
// instantiations.
func (c *checker) checkRecursiveStructs() {
	// path names the field or tuple element, within origin(n)'s body, that
	// generates the edge; it is the same at every instantiation.
	type edge struct {
		to              int
		value, argument bool
		path            string
	}
	var nodes []*Named
	var graph [][]edge
	indices := map[*Named]int{}
	var origins []*Named
	declIndex := map[*Named]int{}
	var identities []int
	// The origin and D[T_D] denote the same identity node. Instantiating
	// through the existing interner also unifies written self references.
	canonical := func(n *Named) *Named {
		if n.Orig == nil && len(n.TParams) != 0 {
			args := make([]Type, len(n.TParams))
			for i, p := range n.TParams {
				args[i] = p
			}
			return c.instantiate(n, args)
		}
		return n
	}
	intern := func(n *Named) int {
		n = canonical(n)
		if i, ok := indices[n]; ok {
			return i
		}
		i := len(nodes)
		indices[n] = i
		nodes = append(nodes, n)
		graph = append(graph, nil)
		return i
	}
	for _, tn := range c.pkg.Types {
		n := tn.Type.(*Named)
		if n.foreign || tn.Decl == nil || c.monoBad(n.TParams) {
			continue
		}
		declIndex[n] = len(origins)
		origins = append(origins, n)
		identities = append(identities, intern(n))
	}
	for i := 0; i < len(nodes); i++ {
		n := nodes[i]
		// Walk the substituted body beside its origin body. A bare
		// parameter (also inside a tuple) transfers authorship to the
		// argument's writer. A written named instance remains literal.
		var hold func(Type, Type, bool, bool, string)
		hold = func(written, actual Type, value, argument bool, path string) {
			if _, ok := written.(*TypeParam); ok {
				argument = true
			}
			switch actual := actual.(type) {
			case *Named:
				if actual.foreign || actual.Obj.Decl == nil || c.monoBad(actual.origin().TParams) {
					return
				}
				if actual.Defined() == nil {
					switch actual.underlying().(type) {
					case *Struct, *Tuple:
					default:
						return
					}
				}
				to := intern(actual)
				graph[i] = append(graph[i], edge{to, value, argument, path})
			case *Tuple:
				w, _ := written.(*Tuple)
				for j, elem := range actual.Types {
					var template Type
					if w != nil && j < len(w.Types) {
						template = w.Types[j]
					}
					hold(template, elem, true, argument, path+"("+strconv.Itoa(j))
				}
			case *Struct:
				w, _ := written.(*Struct)
				for j, f := range actual.Fields {
					var template Type
					if w != nil && j < len(w.Fields) {
						template = w.Fields[j].Type
					}
					hold(template, f.Type, true, argument, path+"{"+strconv.Itoa(j))
				}
			}
		}
		if base := n.Defined(); base != nil {
			hold(n.origin().Defined(), base, false, false, "")
		} else {
			hold(n.origin().underlying(), n.underlying(), true, false, "")
		}
	}
	// Iterative Tarjan: neither a long finite chain nor a large component
	// consumes the Go call stack. Compute every component before reporting
	// or invalidating anything. The predicate selects edges for R2's two
	// derived relations without rebuilding any type instance.
	components := func(g [][]edge, keep func(int, edge) bool) ([][]int, []int, []bool) {
		type frame struct{ node, next int }
		index, low, at := make([]int, len(g)), make([]int, len(g)), make([]int, len(g))
		on := make([]bool, len(g))
		var stack []int
		var all [][]int
		var infinite []bool
		serial := 0
		for root := range g {
			if index[root] != 0 {
				continue
			}
			var work []frame
			push := func(n int) {
				serial++
				index[n], low[n], on[n] = serial, serial, true
				stack = append(stack, n)
				work = append(work, frame{node: n})
			}
			push(root)
			for len(work) > 0 {
				top := &work[len(work)-1]
				if top.next < len(g[top.node]) {
					e := g[top.node][top.next]
					top.next++
					if !keep(top.node, e) {
						continue
					}
					if index[e.to] == 0 {
						push(e.to)
					} else if on[e.to] {
						low[top.node] = min(low[top.node], index[e.to])
					}
					continue
				}
				n := top.node
				work = work[:len(work)-1]
				if len(work) > 0 {
					p := work[len(work)-1].node
					low[p] = min(low[p], low[n])
				}
				if low[n] != index[n] {
					continue
				}
				var group []int
				for {
					m := stack[len(stack)-1]
					stack = stack[:len(stack)-1]
					on[m], at[m] = false, len(all)
					group = append(group, m)
					if m == n {
						break
					}
				}
				cyclic := len(group) > 1
				for _, e := range g[n] {
					cyclic = cyclic || e.to == n && keep(n, e)
				}
				all = append(all, group)
				infinite = append(infinite, cyclic)
			}
		}
		return all, at, infinite
	}
	allEdges := func(int, edge) bool { return true }
	cycles, home, infinite := components(graph, allEdges)
	intrinsic := make([]bool, len(origins))
	for d, id := range identities {
		intrinsic[d] = infinite[home[id]]
	}
	relation := make([][]edge, len(origins))
	for d, id := range identities {
		if !intrinsic[d] {
			continue
		}
		for _, n := range cycles[home[id]] {
			if e, ok := declIndex[nodes[n].origin()]; ok && intrinsic[e] {
				relation[d] = append(relation[d], edge{to: e})
			}
		}
	}
	type group struct {
		members []int
		value   bool
	}
	var reports []group
	groups, _, _ := components(relation, allEdges)
	for _, members := range groups {
		if !intrinsic[members[0]] {
			continue
		}
		g := group{members: members}
		for _, d := range members {
			for _, n := range cycles[home[identities[d]]] {
				for _, e := range graph[n] {
					g.value = g.value || e.value && home[n] == home[e.to]
				}
			}
		}
		reports = append(reports, g)
	}
	// R2' step 3. A field path of D in K is intrinsic when its edge at the
	// identity instance stays inside home(D); that edge is the reported
	// cycle. Remove the edges such a path generates at every instantiation,
	// and nothing else: D's other literal edges may still close a concrete
	// cycle that repairing the intrinsic one would leave in place. Argument
	// edges never exist at an identity instance, so they are never removed.
	type fieldPath struct {
		decl int
		path string
	}
	intrinsicPaths := map[fieldPath]bool{}
	for d, id := range identities {
		if !intrinsic[d] {
			continue
		}
		for _, e := range graph[id] {
			if home[e.to] == home[id] {
				intrinsicPaths[fieldPath{d, e.path}] = true
			}
		}
	}
	keepOrphan := func(n int, e edge) bool {
		d, ok := declIndex[nodes[n].origin()]
		return e.argument || !ok || !intrinsicPaths[fieldPath{d, e.path}]
	}
	orphans, orphanAt, orphanInfinite := components(graph, keepOrphan)
	parent := make([]int, len(origins))
	for d := range parent {
		parent[d] = d
	}
	find := func(d int) int {
		for parent[d] != d {
			parent[d] = parent[parent[d]]
			d = parent[d]
		}
		return d
	}
	// Every infinite component that remains is an orphan. No identity
	// instance of K can be in one: each of its edges back into home(D) was
	// intrinsic and is gone.
	var orphanGroups []group
	for id, cyc := range orphans {
		if !orphanInfinite[id] {
			continue
		}
		g := group{}
		for _, n := range cyc {
			for _, e := range graph[n] {
				if !keepOrphan(n, e) || orphanAt[n] != orphanAt[e.to] {
					continue
				}
				g.value = g.value || e.value
				if d, ok := declIndex[nodes[n].origin()]; ok && !e.argument {
					g.members = append(g.members, d)
				}
			}
		}
		if len(g.members) == 0 {
			continue
		}
		for _, d := range g.members[1:] {
			parent[find(d)] = find(g.members[0])
		}
		orphanGroups = append(orphanGroups, g)
	}
	merged := map[int]*group{}
	for _, g := range orphanGroups {
		root := find(g.members[0])
		if merged[root] == nil {
			merged[root] = &group{}
		}
		merged[root].members = append(merged[root].members, g.members...)
		merged[root].value = merged[root].value || g.value
	}
	for _, g := range merged {
		reports = append(reports, *g)
	}
	// A declaration is in at most one intrinsic group and at most one orphan
	// group. Mutual generic identity components merge into one group, while
	// independent intrinsic declarations such as Aa and Zz stay separate.
	// When an intrinsic group and an orphan group have the same members and
	// kind, their diagnostics are the same text at the same name and give
	// the same repair surface, so the pass prints it once, itself, before
	// errorf sees anything.
	type report struct {
		at      *TypeName
		message string
	}
	var out []report
	for _, g := range reports {
		slices.SortFunc(g.members, func(a, b int) int { return strings.Compare(origins[a].Obj.Name, origins[b].Obj.Name) })
		g.members = slices.Compact(g.members)
		first := origins[g.members[0]].Obj
		var others []string
		for _, d := range g.members[1:] {
			others = append(others, origins[d].Obj.Name)
		}
		with := ""
		if len(others) > 0 {
			with = " with " + strings.Join(others, ", ")
		}
		message := first.Name + " is defined over itself" + with + " (types.md, Defined types)"
		if g.value {
			message = first.Name + " contains itself by value" + with + ", through its fields or tuple elements; its size would be infinite (types.md, Structs 4)"
		}
		out = append(out, report{first, message})
		for _, d := range g.members {
			origins[d].def, origins[d].under = nil, invalid
		}
	}
	slices.SortFunc(out, func(a, b report) int { return strings.Compare(a.message, b.message) })
	out = slices.CompactFunc(out, func(a, b report) bool { return a.message == b.message })
	saved := c.cur
	defer func() { c.cur = saved }()
	for _, r := range out {
		c.cur = r.at.file
		c.errorf(r.at.Decl.Name, CodeType, "%s", r.message)
	}
	// Invalidate every original cyclic instance, including cascades. Do
	// this after classification so one invalidation cannot hide a cycle.
	for n, typ := range nodes {
		if infinite[home[n]] {
			typ.def, typ.under = nil, invalid
		}
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
		// A refused type (one a cycle captured before its declaration was
		// refused, too) adds nothing here (D3; rows.md, Totality 6, P4).
		if !containsInvalid(part.t) && asUnion(part.t) == nil {
			c.errorf(part.x, CodeType, "a machine's %s type must be a union, not %s", part.w, part.t)
		}
	}
	valid := !isInvalid(state) && !isInvalid(signal) && asUnion(state) != nil && asUnion(signal) != nil
	if spec.Initial == nil || spec.Step == nil {
		c.errorf(tn.Decl.Name, CodeType, "machine %s declares both initial and step (runtime.md, Machines and actors 1)", tn.Name)
	}
	if start := machineStart(c.pkg, spec); start != nil {
		c.checkMachineStart(tn, start, spec.Initial.(*syntax.Ident), state, signal, valid)
	} else if spec.Initial != nil {
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
	if isExtern(f) && !externValue(f) {
		// A step is a function value; a call-only extern is refused (P3).
		// An extern value (G1) is checked as a step like any function; its
		// basic-typed signature is never a step's.
		c.errorf(id, CodeType, "%s", externCallOnlyText)
		return
	}
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
	if spec.After != nil {
		c.checkMachineAfter(tn, spec.After, state, signal)
	}
	if spec.Work != nil {
		c.checkMachineWork(tn, spec.Work, state, signal)
	}
}

// machineStart is the function a machine declaration's initial names, when
// it names one: such a machine starts from the function of an input (V4,
// owner decision O3b), since no function is a value of the state union.
func machineStart(pkg *Package, spec *syntax.MachineType) *Func {
	id, ok := spec.Initial.(*syntax.Ident)
	if !ok || pkg == nil {
		return nil
	}
	f, _ := pkg.Scope.names[id.Name].(*Func)
	return f
}

// checkMachineStart checks a machine's `initial: start`: start is a package
// function `func(i I) S`, one input and no rows, so that a run computes
// the first state from the input it is given (runtime.md, Machines and
// actors 10).
func (c *checker) checkMachineStart(tn *TypeName, f *Func, id *syntax.Ident, state, signal Type, valid bool) {
	f.called = true
	c.pkg.Info.Uses[id] = f
	// A call-only extern is refused (P3); an extern value (G1) is checked
	// as a start like any function, and its basic-typed signature is never
	// a start's.
	if isExtern(f) && !externValue(f) {
		c.errorf(id, CodeType, "%s", externCallOnlyText)
		return
	}
	if !valid || f.Sig == nil {
		return
	}
	sig := f.Sig
	if machineInput(sig, state, c) == nil {
		c.errorf(id, CodeType, "the start %s of machine %s must have the type func(…) %s: its parameters are the machine's input, and it has no type parameters and no error or capability row (runtime.md, Machines and actors 10)", id.Name, tn.Name, state)
		return
	}
	// Its rows: the value must be assignable to the same function type with
	// empty rows; an inferred row is solved later, so assignment checks it.
	want := &Signature{Params: sig.Params, Results: sig.Results, Errors: newRow(), Uses: newRow()}
	t := &target{name: "the start of " + tn.Name, declErrors: newRow(), declUses: newRow(), resultKnown: true}
	prev := c.fn
	c.fn = &funcCtx{t: t, scope: newScope(c.pkg.Scope)}
	op := c.valueExpr(id, want)
	c.assign(&op, want, "machine "+tn.Name+"'s start")
	c.fn = prev
}

// machineInput is the input type of a machine whose start function has the
// signature sig: its one parameter's type, or the tuple of its parameters'
// types. It is nil when sig is not a start of the state type S.
func machineInput(sig *Signature, state Type, c *checker) Type {
	if sig == nil || len(sig.TParams) > 0 || len(sig.Params) == 0 || len(sig.Results) != 1 || !c.identical(sig.Results[0], state) {
		return nil
	}
	if len(sig.Params) == 1 {
		return sig.Params[0].Type
	}
	tup := &Tuple{Types: make([]Type, len(sig.Params))}
	for i, p := range sig.Params {
		tup.Types[i] = p.Type
	}
	return tup
}

// checkMachineAfter checks a machine's `after: g` entry: g names a package
// function from the state to Option[Timeout[E]] with no rows, which the
// runner calls after each commit that opens an entry (runtime.md, Machines
// and actors 9). It returns g, or nil when the entry is refused.
func (c *checker) checkMachineAfter(tn *TypeName, x syntax.Expr, state, signal Type) *Func {
	want := "func(" + state.String() + ") Option[Timeout[" + signal.String() + "]]"
	id, ok := x.(*syntax.Ident)
	if !ok {
		c.errorf(x, CodeType, "a machine's after names a function of this package of the type %s (runtime.md, Machines and actors 9)", want)
		return nil
	}
	f, ok := c.pkg.Scope.names[id.Name].(*Func)
	if !ok {
		if c.pkg.Scope.names[id.Name] == nil {
			c.errorf(id, CodeUnknown, "unknown function %s", id.Name)
		} else {
			c.errorf(id, CodeType, "%s is not a function; a machine's after names a function (runtime.md, Machines and actors 9)", id.Name)
		}
		return nil
	}
	f.called = true
	c.pkg.Info.Uses[id] = f
	// A call-only extern is refused (P3); an extern value (G1) is checked
	// as an after like any function, and its basic-typed signature is never
	// an after's.
	if isExtern(f) && !externValue(f) {
		c.errorf(id, CodeType, "%s", externCallOnlyText)
		return nil
	}
	if f.Sig == nil {
		return nil
	}
	if !c.machineAfterShape(f.Sig, state, signal) {
		c.errorf(id, CodeType, "the after %s of machine %s must have the type %s, with no error or capability row: the runner calls it after a commit (runtime.md, Machines and actors 9)", id.Name, tn.Name, want)
		return nil
	}
	// Its rows: the value must be assignable to the Machine's After, whose
	// rows are empty; an inferred row is solved later, so assignment
	// checks it (rows.md, Error rows).
	machine := preludeType("Machine")
	if machine == nil {
		return nil
	}
	view := c.instantiate(machine, []Type{state, signal, state, &RowArg{Row: newRow()}, &RowArg{Row: newRow()}})
	st, _ := view.Underlying().(*Struct)
	if st == nil {
		return nil
	}
	field, _ := st.field("After")
	if field == nil {
		return nil
	}
	t := &target{name: "the after of " + tn.Name, declErrors: newRow(), declUses: newRow(), resultKnown: true}
	prev := c.fn
	c.fn = &funcCtx{t: t, scope: newScope(c.pkg.Scope)}
	op := c.valueExpr(x, field.Type)
	ok = c.assign(&op, field.Type, "machine "+tn.Name+"'s after")
	c.fn = prev
	if !ok {
		return nil
	}
	return f
}

// machineAfterShape reports whether sig is func(S) Option[Timeout[E]] with
// no type parameters; its rows are checked where it is assigned.
func (c *checker) machineAfterShape(sig *Signature, state, signal Type) bool {
	option, timeout := preludeType("Option"), preludeType("Timeout")
	if option == nil || timeout == nil || len(sig.TParams) > 0 || len(sig.Params) != 1 || len(sig.Results) != 1 {
		return false
	}
	want := c.instantiate(option, []Type{c.instantiate(timeout, []Type{signal})})
	return c.identical(sig.Params[0].Type, state) && c.identical(sig.Results[0], want)
}

// machineAfter is the after function of the machine declaration t, or nil
// when it has none or it is refused (checkMachineAfter reports that).
func (c *checker) machineAfter(t Type) *Func {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Decl == nil || n.Obj.Pkg == nil {
		return nil
	}
	m, ok := n.Underlying().(*MachineType)
	spec, ok2 := n.Obj.Decl.Spec.(*syntax.MachineType)
	if !ok || !ok2 {
		return nil
	}
	id, ok := spec.After.(*syntax.Ident)
	if !ok {
		return nil
	}
	f, ok := n.Obj.Pkg.Scope.names[id.Name].(*Func)
	if !ok || f.Sig == nil || isExtern(f) || !c.machineAfterShape(f.Sig, m.State, m.Signal) {
		return nil
	}
	return f
}

// isPreludeMachine reports whether t is an instance of the prelude's
// Machine.
func isPreludeMachine(t Type) bool {
	n, ok := t.(*Named)
	m := preludeType("Machine")
	return ok && m != nil && n.origin() == m
}

// machineValue is the prelude Machine[S, E, O, F, R] that a value of the
// machine declaration t converts to (README Gap 46), with the machine's
// step: O is the step's output, F its error row and R its capability row.
// It is nil when t is not a machine declaration, or when its step is not
// a function of the checked shape (checkMachine reports that).
func (c *checker) machineValue(t Type) (*Named, *Func) {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Decl == nil {
		return nil, nil
	}
	m, ok := n.Underlying().(*MachineType)
	if !ok {
		return nil, nil
	}
	spec, ok := n.Obj.Decl.Spec.(*syntax.MachineType)
	if !ok || spec.Step == nil || n.Obj.Pkg == nil {
		return nil, nil
	}
	id, ok := spec.Step.(*syntax.Ident)
	if !ok {
		return nil, nil
	}
	f, ok := n.Obj.Pkg.Scope.names[id.Name].(*Func)
	machine, decision := preludeType("Machine"), preludeType("Decision")
	if !ok || f.Sig == nil || machine == nil || decision == nil || isExtern(f) {
		return nil, nil
	}
	sig := f.Sig
	if len(sig.TParams) > 0 || len(sig.Params) != 2 || len(sig.Results) != 1 ||
		!c.identical(sig.Params[0].Type, m.State) || !c.identical(sig.Params[1].Type, m.Signal) {
		return nil, nil
	}
	r, ok := sig.Results[0].(*Named)
	if !ok || r.origin() != decision || len(r.TArgs) != 2 || !c.identical(r.TArgs[0], m.State) {
		return nil, nil
	}
	errs, uses := sig.Errors, sig.Uses
	if errs == nil {
		errs = newRow()
	}
	if uses == nil {
		uses = newRow()
	}
	if _, wr := c.machineWork(t); wr != nil {
		// The work's capability row is charged to the machine (MU4).
		uses = uses.union(c.types, wr)
	}
	if start := machineStart(n.Obj.Pkg, spec); start != nil {
		// A machine with an input is an InputMachine (V4); a start of the
		// wrong shape was reported by checkMachineStart.
		im := preludeType("InputMachine")
		input := machineInput(start.Sig, m.State, c)
		if im == nil || input == nil || isExtern(start) {
			return nil, nil
		}
		return c.instantiate(im, []Type{input, m.State, m.Signal, r.TArgs[1], &RowArg{Row: errs}, &RowArg{Row: uses}}), f
	}
	return c.instantiate(machine, []Type{m.State, m.Signal, r.TArgs[1], &RowArg{Row: errs}, &RowArg{Row: uses}}), f
}

// isPreludeMachineValue reports whether t is an instance of the prelude's
// Machine or InputMachine: the types a machine declaration's value
// converts to.
func isPreludeMachineValue(t Type) bool {
	n, ok := t.(*Named)
	return ok && (isPreludeMachine(t) || preludeType("InputMachine") != nil && n.origin() == preludeType("InputMachine"))
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

// checkErrorMethods reports a function Error on an error type: the emitter
// generates it from the fields (types.md, Methods 6). Where the conflict is
// reported depends on whose row makes the type an error type (owners.go):
//
//   - a production row: at the function, as the program itself is wrong;
//   - only a test's row: once, at the first such row in source order, and
//     the function stays clean, because the run build gives the type no
//     Error method and a test never changes the program's verdict. The test
//     build would lower both the function and the generated method.
func (c *checker) checkErrorMethods() {
	blamed := map[*Named]bool{}
	for _, f := range c.funcs {
		owner := f.owner // Error(e E) string, reached as e.Error() (196m)
		if owner == nil || f.Name != "Error" {
			continue
		}
		if c.errorTypeIn(owner, OwnerProduction) {
			c.cur = f.file
			c.errorf(f.Decl.Name, CodeType, "%s is an error type, so it declares no Error method: its text comes from its fields (types.md, Methods 6)", owner.Obj.Name)
			continue
		}
		row, ok := c.errorTypes[OwnerTest][owner.origin()]
		if !ok || blamed[owner.origin()] {
			continue
		}
		blamed[owner.origin()] = true
		c.cur = c.fileOf(row)
		c.errorf(row, CodeType, "this row makes %s an error type in the tests, but the program declares a function Error on it, which the test build would lower beside the generated Error method; an error type's text comes from its fields (types.md, Methods 6)", owner.Obj.Name)
	}
}
