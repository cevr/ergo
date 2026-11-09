// Package machinegraph reads a machine declaration's graph from its
// checked step (docs/spec/runtime.md, Machines and actors 4 and 15; MU15):
// the step's arms, each with the state and signal variants it takes and
// the decisions it can give (states x signals -> decision kinds and the
// state each one moves to), the timer of each state (the after function)
// and its work. `ergo inspect machine` draws it; the lowering registers
// its arms with the program, so a traced actor names the arm that stepped
// each signal (runtime/v1 actor_trace.go); Reachable lists the arms a run
// can reach.
//
// It is effect-machine's Machine.definition, read from the code instead of
// from a registration table: effect-machine needs a handler's or a guard's
// name to show it, and Ergo knows the position of the arm.
//
// The reading is static and keeps to what the source shows. An arm is a
// case of the step's match on its state and signal parameters (one match
// on the tuple, or a match on one parameter whose arm matches the other);
// a step of another shape is one arm that takes every pair. A decision is
// a Decision literal in a value position of the arm; a call of a package
// function of this package with an empty footprint is read through (rows.md,
// Footprints: a helper is static only when it does nothing); any other
// value gives an edge of unknown kind ("?").
package machinegraph

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Any is the variant name of an edge whose target the source does not
// fix: any state.
const Any = "*"

// Unknown is the decision kind of a value the reading cannot follow.
const Unknown = "?"

// Variant is one variant of a machine's state or signal type: its Ergo
// name and its checked type.
type Variant struct {
	Name string
	Type check.Type `json:"-"`
}

// Site is a position in a .ergo file.
type Site struct {
	File string
	Line int
}

func (s Site) String() string { return fmt.Sprintf("%s:%d", s.File, s.Line) }

// Edge is one decision an arm can give: its kind (Enter, Stay, Emit,
// Ignore, Reject, Done, or Unknown), the state variant it moves to for
// Enter, Stay and Emit (Any when the source does not fix it), and where
// the decision is written.
type Edge struct {
	Decision string
	To       string
	Site     Site
}

// Arm is one arm of the step: where it is, the state and signal variants
// it takes (nil for any), whether a pattern of it tests more than the
// variant (a field or a literal, so a pair it takes may still pass to a
// later arm), and its edges.
type Arm struct {
	Site    Site
	States  []string
	Signals []string
	Partial bool
	Edges   []Edge
}

// Timer is the timer of a state's entry: the signal variant it gives
// (Any when the source does not fix it) and where the after function
// arms it.
type Timer struct {
	Signal string
	Site   Site
}

// Graph is one machine's graph.
type Graph struct {
	Machine string
	States  []Variant
	Signals []Variant
	// Initial is the variant of the first state, Any when the source does
	// not fix it (an input machine's start, a call).
	Initial string
	Arms    []Arm
	// Timers and Work hold, by state variant, the timer and the work of
	// the state's entries; a state the source gives none is absent, and a
	// state the reading cannot decide is in Unsure.
	Timers map[string]Timer
	Work   map[string]Site
	Unsure []string
}

// Locate gives the file and line of pos in the file that holds the
// declaration decl.
type Locate func(decl syntax.Node, pos syntax.Pos) Site

// Machines reads the graph of each machine declaration of pkg, in source
// order.
func Machines(pkg *check.Package, at Locate) []*Graph {
	var out []*Graph
	for _, tn := range pkg.Types {
		spec, ok := tn.Decl.Spec.(*syntax.MachineType)
		if !ok {
			continue
		}
		if g := Of(pkg, tn, spec, at); g != nil {
			out = append(out, g)
		}
	}
	return out
}

// Of reads the graph of the machine declaration tn, whose spec is spec.
// It is nil when the declaration did not check.
func Of(pkg *check.Package, tn *check.TypeName, spec *syntax.MachineType, at Locate) *Graph {
	named, ok := tn.Type.(*check.Named)
	if !ok {
		return nil
	}
	mt, ok := named.Underlying().(*check.MachineType)
	if !ok {
		return nil
	}
	r := &reader{pkg: pkg, info: pkg.Info, at: at}
	g := &Graph{Machine: tn.Name, States: variants(mt.State), Signals: variants(mt.Signal),
		Timers: map[string]Timer{}, Work: map[string]Site{}}
	r.g = g
	g.Initial = r.initial(spec.Initial)
	if step := r.fn(spec.Step); step != nil {
		g.Arms = r.arms(step)
	}
	if after := r.fn(spec.After); after != nil {
		r.perState(after, func(state string, v syntax.Expr, decl syntax.Node) {
			sig, some := r.some(v, "Signal")
			if some {
				g.Timers[state] = Timer{Signal: sig, Site: at(decl, v.Pos())}
			}
		})
	}
	if work := r.fn(spec.Work); work != nil {
		r.perState(work, func(state string, v syntax.Expr, decl syntax.Node) {
			if _, some := r.some(v, ""); some {
				g.Work[state] = at(decl, v.Pos())
			}
		})
	}
	return g
}

// variants lists t's variants: a union's, or t itself.
func variants(t check.Type) []Variant {
	if u, ok := under(t).(*check.Union); ok {
		out := make([]Variant, 0, len(u.Variants))
		for _, v := range u.Variants {
			out = append(out, Variant{Name: Name(v), Type: v})
		}
		return out
	}
	return []Variant{{Name: Name(t), Type: t}}
}

func under(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return n.Underlying()
	}
	return t
}

// Name is the variant name of t: its declared name, or its text.
func Name(t check.Type) string {
	if n, ok := t.(*check.Named); ok && n.Obj != nil {
		return n.Obj.Name
	}
	return t.String()
}

// reader reads one machine.
type reader struct {
	pkg  *check.Package
	info *check.Info
	at   Locate
	g    *Graph
	// seen guards the helpers read through against recursion.
	seen map[*check.Func]bool
}

// fn is the package function an entry names, nil for none.
func (r *reader) fn(x syntax.Expr) *check.Func {
	id, ok := x.(*syntax.Ident)
	if !ok {
		return nil
	}
	f, ok := r.info.Uses[id].(*check.Func)
	if !ok || f.Decl == nil || f.Decl.Body == nil {
		return nil
	}
	return f
}

// initial is the variant of the initial value x.
func (r *reader) initial(x syntax.Expr) string {
	if x == nil {
		return Any
	}
	return r.stateOf(r.info.Types[x])
}

// stateOf is the state variant of the type t, Any when t is not one
// variant.
func (r *reader) stateOf(t check.Type) string {
	if t == nil {
		return Any
	}
	name := Name(t)
	for _, v := range r.g.States {
		if v.Name == name {
			return name
		}
	}
	return Any
}

// role is what a match subject is: the step's state parameter, its
// signal parameter, or neither.
type role int

const (
	other role = iota
	stateRole
	signalRole
)

// roleOf is the role of the subject x in a function whose parameters are
// state and signal (nil when absent).
func (r *reader) roleOf(x syntax.Expr, state, signal check.Object) role {
	id, ok := x.(*syntax.Ident)
	if !ok {
		return other
	}
	switch o := r.info.Uses[id]; {
	case o == nil:
		return other
	case o == state:
		return stateRole
	case o == signal:
		return signalRole
	}
	return other
}

// params are f's parameter objects, by the identifiers of its declaration.
func (r *reader) params(f *check.Func) []check.Object {
	var out []check.Object
	for _, fl := range f.Decl.Type.Params.List {
		for _, n := range fl.Names {
			out = append(out, r.info.Defs[n])
		}
	}
	return out
}

// arms reads the step's arms.
func (r *reader) arms(step *check.Func) []Arm {
	ps := r.params(step)
	if len(ps) != 2 {
		return nil
	}
	decl := syntax.Node(step.Decl)
	whole := func() []Arm {
		return []Arm{{Site: r.at(decl, step.Decl.Pos()), Edges: r.edgesOfBlock(step.Decl.Body.List, decl)}}
	}
	m, ok := tail(step.Decl.Body.List).(*syntax.TypeSwitchStmt)
	if !ok {
		return whole()
	}
	roles := make([]role, len(m.Subjects))
	for i, s := range m.Subjects {
		roles[i] = r.roleOf(s.X, ps[0], ps[1])
		if roles[i] == other {
			return whole()
		}
	}
	var out []Arm
	for _, cl := range m.Clauses {
		states, signals, partial := r.clauseVariants(cl, roles)
		if len(roles) == 1 {
			// A match on one parameter: an arm whose value is a match on
			// the other splits into that match's arms.
			if inner, ok := tail(cl.Body).(*syntax.TypeSwitchStmt); ok && len(inner.Subjects) == 1 {
				ir := r.roleOf(inner.Subjects[0].X, ps[0], ps[1])
				if ir != other && ir != roles[0] {
					for _, icl := range inner.Clauses {
						is, isg, ip := r.clauseVariants(icl, []role{ir})
						out = append(out, Arm{Site: r.at(decl, icl.Pos()), States: merge(states, is), Signals: merge(signals, isg),
							Partial: partial || ip, Edges: r.edgesOfBlock(icl.Body, decl)})
					}
					continue
				}
			}
		}
		out = append(out, Arm{Site: r.at(decl, cl.Pos()), States: states, Signals: signals, Partial: partial,
			Edges: r.edgesOfBlock(cl.Body, decl)})
	}
	return out
}

// merge is the variants both a and b allow (nil for any).
func merge(a, b []string) []string {
	if a == nil {
		return b
	}
	return a
}

// clauseVariants is the state and signal variants a clause takes, for a
// match whose subjects have the roles given, and whether a pattern tests
// more than a variant.
func (r *reader) clauseVariants(cl *syntax.CaseClause, roles []role) (states, signals []string, partial bool) {
	anyState, anySignal := false, false
	for _, item := range cl.List {
		p := r.info.Patterns[item]
		if p == nil {
			anyState, anySignal = true, true
			continue
		}
		elems := []*check.Pattern{p}
		if len(roles) > 1 {
			elems = p.Elems
		}
		st, sg := []string(nil), []string(nil)
		stAny, sgAny := true, true
		for i, e := range elems {
			if i >= len(roles) {
				break
			}
			vs, part := patternVariants(e)
			partial = partial || part
			switch roles[i] {
			case stateRole:
				st, stAny = vs, vs == nil
			case signalRole:
				sg, sgAny = vs, vs == nil
			}
		}
		if stAny {
			anyState = true
		} else {
			states = append(states, st...)
		}
		if sgAny {
			anySignal = true
		} else {
			signals = append(signals, sg...)
		}
	}
	if anyState {
		states = nil
	}
	if anySignal {
		signals = nil
	}
	return states, signals, partial
}

// patternVariants is the variants a pattern takes, nil for any, and
// whether it tests more than the variant.
func patternVariants(p *check.Pattern) ([]string, bool) {
	switch p.Kind {
	case check.PatWildcard, check.PatBind:
		return nil, false
	case check.PatTypeTest:
		return []string{Name(p.Type)}, false
	case check.PatRecord:
		partial := false
		for _, f := range p.Fields {
			if f.Pattern != nil && f.Pattern.Kind != check.PatWildcard && f.Pattern.Kind != check.PatBind {
				partial = true
			}
		}
		return []string{Name(p.Type)}, partial
	case check.PatAs:
		if len(p.Elems) > 0 {
			return patternVariants(p.Elems[0])
		}
	case check.PatConst:
		return []string{Name(p.Type)}, true
	}
	return nil, true
}

// tail is the last statement of a block, nil for an empty one.
func tail(list []syntax.Stmt) syntax.Stmt {
	if len(list) == 0 {
		return nil
	}
	return list[len(list)-1]
}

// edgesOfBlock is the decisions a block's value can be: its returns and
// its tail.
func (r *reader) edgesOfBlock(list []syntax.Stmt, decl syntax.Node) []Edge {
	var out []Edge
	for _, s := range list[:max(len(list)-1, 0)] {
		out = append(out, r.returns(s, decl)...)
	}
	if t := tail(list); t != nil {
		out = append(out, r.edgesOfStmt(t, decl)...)
	}
	return dedupe(out)
}

// returns is the decisions of the return statements inside s, a statement
// that is not a block's value.
func (r *reader) returns(s syntax.Stmt, decl syntax.Node) []Edge {
	var out []Edge
	syntax.Inspect(s, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.ReturnStmt:
			if len(n.Results) == 1 {
				out = append(out, r.edgesOf(n.Results[0], decl)...)
			}
		}
		return true
	})
	return out
}

// edgesOfStmt is the decisions of a block's tail statement.
func (r *reader) edgesOfStmt(s syntax.Stmt, decl syntax.Node) []Edge {
	switch s := s.(type) {
	case *syntax.ExprStmt:
		return r.edgesOf(s.X, decl)
	case *syntax.ReturnStmt:
		if len(s.Results) == 1 {
			return r.edgesOf(s.Results[0], decl)
		}
	case *syntax.BlockStmt:
		return r.edgesOfBlock(s.List, decl)
	case *syntax.IfStmt:
		out := r.edgesOfBlock(s.Body.List, decl)
		if s.Else != nil {
			out = append(out, r.edgesOfStmt(s.Else, decl)...)
		}
		return out
	case *syntax.TypeSwitchStmt:
		var out []Edge
		for _, cl := range s.Clauses {
			out = append(out, r.edgesOfBlock(cl.Body, decl)...)
		}
		return out
	case *syntax.SwitchStmt:
		var out []Edge
		for _, cl := range s.Clauses {
			out = append(out, r.edgesOfBlock(cl.Body, decl)...)
		}
		return out
	}
	return nil
}

// decisionKinds are the prelude Decision's variants.
var decisionKinds = []string{"Enter", "Stay", "Emit", "Ignore", "Reject", "Done"}

// edgesOf is the decisions the value x can be.
func (r *reader) edgesOf(x syntax.Expr, decl syntax.Node) []Edge {
	site := r.at(decl, x.Pos())
	switch v := x.(type) {
	case *syntax.ParenExpr:
		return r.edgesOf(v.X, decl)
	case *syntax.TryExpr:
		return r.edgesOf(v.X, decl)
	case *syntax.CallExpr:
		if f := r.helper(v); f != nil {
			r.seen[f] = true
			out := r.edgesOfBlock(f.Decl.Body.List, f.Decl)
			delete(r.seen, f)
			return out
		}
		return []Edge{{Decision: Unknown, Site: site}}
	}
	kind := r.kindOf(r.info.Types[x])
	switch kind {
	case "":
		return []Edge{{Decision: Unknown, Site: site}}
	case "Enter", "Stay", "Emit":
		to := Any
		if lit, ok := x.(*syntax.CompositeLit); ok {
			for _, el := range lit.Elts {
				if kv, ok := el.(*syntax.KeyValueExpr); ok {
					if id, ok := kv.Key.(*syntax.Ident); ok && id.Name == "Next" {
						to = r.stateOf(r.info.Types[kv.Value])
					}
				}
			}
		}
		return []Edge{{Decision: kind, To: to, Site: site}}
	}
	return []Edge{{Decision: kind, Site: site}}
}

// kindOf is the Decision variant t is, "" when it is none (a Decision
// value whose variant the type does not fix, or another type).
func (r *reader) kindOf(t check.Type) string {
	n, ok := t.(*check.Named)
	// A program cannot declare a prelude name, so the name is the variant.
	if !ok || n.Obj == nil {
		return ""
	}
	if slices.Contains(decisionKinds, n.Obj.Name) {
		return n.Obj.Name
	}
	return ""
}

// helper is the function a call reads through: a package function of this
// package with a body and an empty footprint, not already being read.
func (r *reader) helper(call *syntax.CallExpr) *check.Func {
	id, ok := call.Fun.(*syntax.Ident)
	if !ok {
		return nil
	}
	f, ok := r.info.Uses[id].(*check.Func)
	if !ok || f.Pkg != r.pkg || f.Decl == nil || f.Decl.Body == nil || r.seen[f] {
		return nil
	}
	if fp, ok := r.info.Footprints[f.Decl]; !ok || !fp.Empty() {
		return nil
	}
	if r.seen == nil {
		r.seen = map[*check.Func]bool{}
	}
	return f
}

// perState calls each with the value f gives for each state variant: f is
// a function of the state (after, work) whose body is a match on it, or
// any other body, which every state shares.
func (r *reader) perState(f *check.Func, each func(state string, v syntax.Expr, decl syntax.Node)) {
	ps := r.params(f)
	decl := syntax.Node(f.Decl)
	values := func(list []syntax.Stmt) []syntax.Expr {
		var out []syntax.Expr
		var walk func(s syntax.Stmt)
		walk = func(s syntax.Stmt) {
			switch s := s.(type) {
			case *syntax.ExprStmt:
				out = append(out, s.X)
			case *syntax.ReturnStmt:
				if len(s.Results) == 1 {
					out = append(out, s.Results[0])
				}
			case *syntax.IfStmt:
				walk(tail(s.Body.List))
				if s.Else != nil {
					walk(s.Else)
				}
			case *syntax.BlockStmt:
				walk(tail(s.List))
			}
		}
		walk(tail(list))
		return out
	}
	all := func(list []syntax.Stmt, states []string) {
		vs := values(list)
		for _, st := range states {
			sure := len(vs) > 0
			for _, v := range vs {
				if !r.someOrNone(v) {
					sure = false
				}
				each(st, v, decl)
			}
			if !sure && !slices.Contains(r.g.Unsure, st) {
				r.g.Unsure = append(r.g.Unsure, st)
			}
		}
	}
	names := make([]string, len(r.g.States))
	for i, v := range r.g.States {
		names[i] = v.Name
	}
	m, ok := tail(f.Decl.Body.List).(*syntax.TypeSwitchStmt)
	if !ok || len(ps) != 1 || len(m.Subjects) != 1 || r.roleOf(m.Subjects[0].X, ps[0], nil) != stateRole {
		all(f.Decl.Body.List, names)
		return
	}
	taken := map[string]bool{}
	for _, cl := range m.Clauses {
		states, _, _ := r.clauseVariants(cl, []role{stateRole})
		if states == nil {
			states = names
		}
		var open []string
		for _, s := range states {
			if !taken[s] {
				open = append(open, s)
				taken[s] = true
			}
		}
		all(cl.Body, open)
	}
}

// some reports whether v is a Some literal, and the variant of its value's
// field (Any when not fixed, or "" when field is "").
func (r *reader) some(v syntax.Expr, field string) (string, bool) {
	lit, ok := v.(*syntax.CompositeLit)
	if !ok {
		return "", false
	}
	n, ok := r.info.Types[v].(*check.Named)
	if !ok || n.Obj == nil || n.Obj.Name != "Some" {
		return "", false
	}
	if field == "" {
		return "", true
	}
	sig := Any
	syntax.Inspect(lit, func(nd syntax.Node) bool {
		if kv, ok := nd.(*syntax.KeyValueExpr); ok {
			if id, ok := kv.Key.(*syntax.Ident); ok && id.Name == field {
				if t := r.info.Types[kv.Value]; t != nil {
					sig = Name(t)
					if !slices.ContainsFunc(r.g.Signals, func(s Variant) bool { return s.Name == sig }) {
						sig = Any
					}
				}
			}
		}
		return true
	})
	return sig, true
}

// someOrNone reports whether v is a Some or a None literal: a value the
// reading is sure of.
func (r *reader) someOrNone(v syntax.Expr) bool {
	if _, some := r.some(v, ""); some {
		return true
	}
	if id, ok := v.(*syntax.Ident); ok && id.Name == "None" {
		return true
	}
	n, ok := r.info.Types[v].(*check.Named)
	return ok && n.Obj != nil && n.Obj.Name == "None"
}

// dedupe drops repeated edges, keeping the first.
func dedupe(es []Edge) []Edge {
	var out []Edge
	for _, e := range es {
		if !slices.Contains(out, e) {
			out = append(out, e)
		}
	}
	return out
}

// ArmOf is the first arm of g that takes the pair (state, signal), and
// whether one does. A partial arm takes it only maybe: ArmOf gives the
// first arm, and Reachable lists each arm that may.
func (g *Graph) ArmOf(state, signal string) (Arm, bool) {
	for _, a := range g.Arms {
		if takes(a.States, state) && takes(a.Signals, signal) {
			return a, true
		}
	}
	return Arm{}, false
}

func takes(vs []string, v string) bool { return vs == nil || slices.Contains(vs, v) }

// Pair is one (state, signal) pair a run can step, with the sites of the
// arms that may step it: one, or, after a partial arm, each arm up to the
// first that takes the pair whole.
type Pair struct {
	State, Signal string
	Arms          []Site
}

// Reachable is the coverage helper: every pair of a state the machine can
// reach from its initial state (by the arms' Enter, Stay and Emit edges; an
// edge of unknown target or kind reaches every state) and any signal, in
// state then signal order, with the arms that may step it. A pair no arm
// takes is listed with none.
func (g *Graph) Reachable() []Pair {
	names := func(vs []Variant) []string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.Name
		}
		return out
	}
	states, signals := names(g.States), names(g.Signals)
	reached := map[string]bool{}
	var queue []string
	visit := func(s string) {
		if s == Any {
			for _, x := range states {
				if !reached[x] {
					reached[x] = true
					queue = append(queue, x)
				}
			}
			return
		}
		if !reached[s] {
			reached[s] = true
			queue = append(queue, s)
		}
	}
	visit(g.Initial)
	for len(queue) > 0 {
		s := queue[0]
		queue = queue[1:]
		for _, sg := range signals {
			for _, a := range g.arms(s, sg) {
				for _, e := range a.Edges {
					switch e.Decision {
					case "Enter", "Stay", "Emit":
						visit(e.To)
					case Unknown:
						visit(Any)
					}
				}
			}
		}
	}
	var out []Pair
	for _, s := range states {
		if !reached[s] {
			continue
		}
		for _, sg := range signals {
			p := Pair{State: s, Signal: sg}
			for _, a := range g.arms(s, sg) {
				p.Arms = append(p.Arms, a.Site)
			}
			out = append(out, p)
		}
	}
	return out
}

// arms is the arms that may step (state, signal), in match order: up to
// and including the first one that takes it whole.
func (g *Graph) arms(state, signal string) []Arm {
	var out []Arm
	for _, a := range g.Arms {
		if takes(a.States, state) && takes(a.Signals, signal) {
			out = append(out, a)
			if !a.Partial {
				break
			}
		}
	}
	return out
}

// Text draws g, one fact per line: the machine, its states and signals,
// each arm with its edges, then the timers and the work by state.
func (g *Graph) Text() string {
	var b strings.Builder
	list := func(vs []string) string {
		if vs == nil {
			return Any
		}
		return strings.Join(vs, "|")
	}
	names := func(vs []Variant) []string {
		out := make([]string, len(vs))
		for i, v := range vs {
			out[i] = v.Name
		}
		return out
	}
	fmt.Fprintf(&b, "machine %s\n  states %s\n  signals %s\n  initial %s\n", g.Machine,
		strings.Join(names(g.States), " "), strings.Join(names(g.Signals), " "), g.Initial)
	for _, a := range g.Arms {
		partial := ""
		if a.Partial {
			partial = " (partial)"
		}
		fmt.Fprintf(&b, "  arm %s (%s, %s)%s\n", a.Site, list(a.States), list(a.Signals), partial)
		for _, e := range a.Edges {
			if e.To != "" {
				fmt.Fprintf(&b, "    %s -> %s at %s\n", e.Decision, e.To, e.Site)
			} else {
				fmt.Fprintf(&b, "    %s at %s\n", e.Decision, e.Site)
			}
		}
	}
	for _, k := range sortedKeys(g.Timers) {
		fmt.Fprintf(&b, "  timer %s -> %s at %s\n", k, g.Timers[k].Signal, g.Timers[k].Site)
	}
	for _, k := range sortedKeys(g.Work) {
		fmt.Fprintf(&b, "  work %s at %s\n", k, g.Work[k])
	}
	for _, s := range g.Unsure {
		fmt.Fprintf(&b, "  unsure %s: its timer or work is not a literal\n", s)
	}
	return b.String()
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
