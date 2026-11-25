package check

import (
	"ergo.local/prototype/internal/sketch/stdlib"
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

// unionLike reports a type that a machine takes as its state or signal: a
// named union, or a named record, which is a union of one variant (fu-mb2
// F10). match already reads a record subject as one constructor, so a
// match on a record state is exhaustive with one arm. As asUnion does for
// a union, it reads a defined type by its underlying type.
func unionLike(t Type) bool {
	_, named := t.(*Named)
	return asUnion(t) != nil || named && asStruct(t) != nil
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
		if !containsInvalid(part.t) && !unionLike(part.t) {
			c.errorf(part.x, CodeType, "a machine's %s type must be a union or a record, not %s%s", part.w, part.t, tupleRecordHint(tn.Name, part.w, part.t))
		}
	}
	valid := !isInvalid(state) && !isInvalid(signal) && unionLike(state) && unionLike(signal)
	if spec.Initial == nil || spec.Step == nil {
		c.errorf(tn.Decl.Name, CodeType, "machine %s declares both initial and step (runtime.md, Machines and actors 1)", tn.Name)
	}
	start := c.machineEntry(tn, entryStart)
	if start != nil {
		c.checkMachineStart(tn, start, state, valid)
	}
	if spec.Step == nil {
		return
	}
	c.checkMachineStep(tn, state, signal, valid)
	if spec.After != nil {
		c.checkMachineAfter(tn, state, signal)
	}
	if spec.Work != nil {
		c.checkMachineWork(tn, state, signal)
	}
	if spec.Exit != nil {
		c.checkMachineExit(tn, state)
	}
	if view, step, sig := c.machineValue(n); view != nil {
		if conv, why := c.machineConversion(n, view, step, sig, start != nil); why == "" {
			c.pkg.Info.MachineEntries[tn].Conv = &conv
		}
	}
}

// checkMachineStep checks the machine's step entry: an expression of the
// type func(S, E) Decision[S, O], with any error and capability rows, which
// are the Machine's F and R (MACHINE-EXPR; runtime.md, Machines and actors
// 1 and 8).
func (c *checker) checkMachineStep(tn *TypeName, state, signal Type, valid bool) {
	e := c.machineEntry(tn, entryStep)
	if e == nil || !valid {
		return
	}
	if !c.stepShape(e.Type, state, signal) {
		want := "func(" + state.String() + ", " + signal.String() + ") Decision[" + state.String() + ", O]"
		c.errorf(e.Expr, CodeType, "the step %sof machine %s must have the type %s, with any error and capability rows; it has the type %s (runtime.md, Machines and actors 1)", entryLabel(e.Expr), tn.Name, want, e.Type)
	}
}

// stepShape reports whether sig is a step of the state and signal:
// func(S, E) Decision[S, O], no type parameters, any rows.
func (c *checker) stepShape(sig *Signature, state, signal Type) bool {
	decision := preludeType("Decision")
	if sig == nil || decision == nil || len(sig.TParams) > 0 || len(sig.Params) != 2 || len(sig.Results) != 1 {
		return false
	}
	if !c.identical(sig.Params[0].Type, state) || !c.identical(sig.Params[1].Type, signal) {
		return false
	}
	r, ok := sig.Results[0].(*Named)
	return ok && r.origin() == decision && len(r.TArgs) == 2 && c.identical(r.TArgs[0], state)
}

// entryLabel is the name an entry's message gives it, with a trailing
// space: the name it is written as, or nothing for a computed entry.
func entryLabel(x syntax.Expr) string {
	if id, ok := unparen(x).(*syntax.Ident); ok {
		return id.Name + " "
	}
	return ""
}

// checkMachineStart checks a machine's initial entry when it is a function
// of an input: func(i I) S, its parameters the machine's input, no type
// parameters and no rows, so that a run computes the first state from the
// input it is given (runtime.md, Machines and actors 10).
func (c *checker) checkMachineStart(tn *TypeName, e *MachineEntry, state Type, valid bool) {
	if !valid {
		return
	}
	sig := e.Type
	if machineInput(sig, state, c) == nil {
		c.errorf(e.Expr, CodeType, "the start %sof machine %s must have the type func(…) %s: its parameters are the machine's input, and it has no type parameters and no error or capability row (runtime.md, Machines and actors 10)", entryLabel(e.Expr), tn.Name, state)
	}
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

// checkMachineAfter checks a machine's after entry: a function from the
// state to Option[Timeout[E]] (or Option[machines.Every[E]]) with no rows,
// which the runner calls after each commit that opens an entry (runtime.md,
// Machines and actors 9).
func (c *checker) checkMachineAfter(tn *TypeName, state, signal Type) {
	e := c.machineEntry(tn, entryAfter)
	if e == nil {
		return
	}
	if !c.machineAfterShape(e.Type, state, signal) {
		want := "func(" + state.String() + ") Option[Timeout[" + signal.String() + "]]"
		c.errorf(e.Expr, CodeType, "the after %sof machine %s must have the type %s, or Option[machines.Every[%s]], with no error or capability row: the runner calls it after a commit (runtime.md, Machines and actors 9)", entryLabel(e.Expr), tn.Name, want, signal)
	}
}

// machineAfter is the after entry of the machine type t, or nil when it
// has none or it is refused (checkMachineAfter reports that). Its type is
// at t's type arguments.
func (c *checker) machineAfter(t Type) (*MachineEntry, *Signature) {
	n, tn, m := machineDeclOf(t)
	if n == nil {
		return nil, nil
	}
	e := c.machineEntry(tn, entryAfter)
	sig := c.entrySig(n, e)
	if sig == nil || !c.machineAfterShape(sig, m.State, m.Signal) {
		return nil, nil
	}
	return e, sig
}

// machineAfterShape reports whether sig is func(S) Option[Timeout[E]] or
// func(S) Option[machines.Every[E]] (FKG1) with no type parameters; its
// rows are checked where it is assigned.
func (c *checker) machineAfterShape(sig *Signature, state, signal Type) bool {
	option, timeout := preludeType("Option"), preludeType("Timeout")
	if option == nil || timeout == nil || len(sig.TParams) > 0 || len(sig.Params) != 1 || len(sig.Results) != 1 {
		return false
	}
	if !c.identical(sig.Params[0].Type, state) {
		return false
	}
	want := c.instantiate(option, []Type{c.instantiate(timeout, []Type{signal})})
	return c.identical(sig.Results[0], want) || c.machineAfterEvery(sig, signal)
}

// machineAfterEvery reports whether the after function's sig gives
// Option[machines.Every[E]] for the signal type E (FKG1): a timer whose
// signal is made from the clock's reading when it fires.
func (c *checker) machineAfterEvery(sig *Signature, signal Type) bool {
	option := preludeType("Option")
	if option == nil || len(sig.Results) != 1 {
		return false
	}
	o, ok := sig.Results[0].(*Named)
	if !ok || o.origin() != option || len(o.TArgs) != 1 {
		return false
	}
	ev, ok := o.TArgs[0].(*Named)
	return ok && IsEvery(ev) && len(ev.TArgs) == 1 && c.identical(ev.TArgs[0], signal)
}

// IsEvery reports whether t is an Every timer (FKG1): an instance of a
// record of one type parameter E whose fields are In duration and Signal
// func(at T) E, where T's package declares the Clock its runner reads
// (EveryClock). std machines.Every is one. The rule reads the record's
// shape, never the package that declares it, so a copy of machines, or a
// user package, declares one the same way (STD-COPY-SAME: std has no
// privileges).
func IsEvery(t Type) bool {
	n, ok := t.(*Named)
	if !ok || len(n.TArgs) != 1 {
		return false
	}
	o := n.origin()
	if o == nil || len(o.TParams) != 1 {
		return false
	}
	st, ok := o.Underlying().(*Struct)
	if !ok || len(st.Fields) != 2 {
		return false
	}
	in, _ := st.field("In")
	signal, _ := st.field("Signal")
	if in == nil || signal == nil || !isDuration(in.Type) {
		return false
	}
	fn, ok := signal.Type.(*Signature)
	if !ok || len(fn.TParams) > 0 || len(fn.Params) != 1 || len(fn.Results) != 1 || !fn.Errors.Empty() || !fn.Uses.Empty() {
		return false
	}
	if e, ok := fn.Results[0].(*TypeParam); !ok || e != o.TParams[0] {
		return false
	}
	at, ok := fn.Params[0].Type.(*Named)
	if !ok || at.Obj == nil || at.Obj.Pkg == nil {
		return false
	}
	_, ok = at.Obj.Pkg.Scope.names["Clock"].(*TypeName)
	return ok
}

// EveryClock is std time.Clock, the capability that an Every timer's
// runner reads (FKG1), found from the type of the timer's Signal
// parameter, time.Time; nil when it cannot be found.
func EveryClock(sig *Signature) Type {
	o, ok := sig.Results[0].(*Named)
	if !ok || len(o.TArgs) != 1 {
		return nil
	}
	ev, ok := o.TArgs[0].(*Named)
	if !ok {
		return nil
	}
	st, ok := ev.Underlying().(*Struct)
	if !ok {
		return nil
	}
	field, _ := st.field("Signal")
	if field == nil {
		return nil
	}
	fn, ok := field.Type.(*Signature)
	if !ok || len(fn.Params) != 1 {
		return nil
	}
	at, ok := fn.Params[0].Type.(*Named)
	if !ok || at.Obj == nil || at.Obj.Pkg == nil {
		return nil
	}
	clock, ok := at.Obj.Pkg.Scope.names["Clock"].(*TypeName)
	if !ok {
		return nil
	}
	return clock.Type
}

// isPreludeMachine reports whether t is an instance of the prelude's
// Machine.
func isPreludeMachine(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	// By its declaration, not preludeType: the prelude's own check
	// unifies its generic types here, before prelude() returns (a
	// re-entrant prelude() waits on itself).
	o := n.origin().Obj
	return o != nil && o.Name == "Machine" && o.Pkg != nil && o.Pkg.Path == stdlib.PreludePath
}

// machineValue is the prelude Machine[S, E, O, F, R, W] that a value of
// the machine declaration t converts to (README Gap 46), with the
// machine's step entry and its type at t's type arguments: O is the
// step's output, F its error row and R its capability row; W is its
// work's and its exit action's capability row. It is nil when t is not a
// machine declaration, or when an entry is refused (checkMachine reports
// that).
func (c *checker) machineValue(t Type) (*Named, *MachineEntry, *Signature) {
	n, tn, m := machineDeclOf(t)
	if n == nil {
		return nil, nil, nil
	}
	machine := preludeType("Machine")
	step := c.machineEntry(tn, entryStep)
	sig := c.entrySig(n, step)
	if machine == nil || sig == nil || !c.stepShape(sig, m.State, m.Signal) {
		return nil, nil, nil
	}
	o := sig.Results[0].(*Named).TArgs[1]
	errs, uses := sig.Errors, sig.Uses
	if errs == nil {
		errs = newRow()
	}
	if uses == nil {
		uses = newRow()
	}
	// The work's capability row is the machine's W, apart from the step's
	// R: a fold (machines.Run) runs no work, so it needs R only, and the
	// runner (actor.StartMachine) needs both (fumb-e, lane MB F1).
	works := newRow()
	if _, wr := c.machineWork(t); wr != nil {
		works = wr
	}
	// The exit action's row is W too (FK9-g1): the runner runs it, a fold
	// does not.
	if _, xr := c.machineExit(t); xr != nil {
		works = works.union(c.types, xr)
	}
	if after, asig := c.machineAfter(t); after != nil && c.machineAfterEvery(asig, m.Signal) {
		// An Every timer's runner reads time.Clock when it fires (FKG1):
		// the runner's row W carries it, and a fold, which fires no
		// timer, needs no clock.
		if clock := EveryClock(asig); clock != nil {
			works = works.with(c.types, clock)
		}
	}
	if start := c.machineEntry(tn, entryStart); start != nil {
		// A machine with an input is an InputMachine (V4); a start of the
		// wrong shape was reported by checkMachineStart.
		im := preludeType("InputMachine")
		input := machineInput(c.entrySig(n, start), m.State, c)
		if im == nil || input == nil {
			return nil, nil, nil
		}
		return c.instantiate(im, []Type{input, m.State, m.Signal, o, &RowArg{Row: errs}, &RowArg{Row: uses}, &RowArg{Row: works}}), step, sig
	}
	return c.instantiate(machine, []Type{m.State, m.Signal, o, &RowArg{Row: errs}, &RowArg{Row: uses}, &RowArg{Row: works}}), step, sig
}

// isPreludeMachineValue reports whether t is an instance of the prelude's
// Machine or InputMachine: the types a machine declaration's value
// converts to.
func isPreludeMachineValue(t Type) bool {
	n, ok := t.(*Named)
	if !ok {
		return false
	}
	o := n.origin().Obj // by declaration, as isPreludeMachine
	return o != nil && (o.Name == "Machine" || o.Name == "InputMachine") && o.Pkg != nil && o.Pkg.Path == stdlib.PreludePath
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
	if d.Type.Defects {
		c.errorf(d.Type, CodeEntry, "main cannot declare defects: run deliberate defects inside a child task (task.Go) and observe via task.ExitOf")
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
