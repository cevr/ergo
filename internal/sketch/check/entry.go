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

// checkErrorMethods reports a function Error on an error type: the emitter
// generates it from the fields (types.md, Methods 5). A row of any owner
// counts: the test build lowers the function beside the tests' error
// types, so a test's row would give the type a second Error method.
func (c *checker) checkErrorMethods() {
	for _, f := range c.funcs {
		owner := f.owner // Error(e E) string, reached as e.Error() (196m)
		if owner == nil || f.Name != "Error" {
			continue
		}
		if !c.errorTypeAnywhere(owner) {
			continue
		}
		c.cur = f.file
		c.errorf(f.Decl.Name, CodeType, "%s is an error type, so it declares no Error method: its text comes from its fields (types.md, Methods 5)", owner.Obj.Name)
	}
}
