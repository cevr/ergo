package eval

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	"ergo.local/prototype/internal/sketch/testplan"
	"ergo.local/prototype/runtime/explore"
	"ergo.local/prototype/runtime/testgen"
)

// Machine laws at check time (L7 EV4; types.md, Laws 13). A reachability
// law (`reaches V`, `never reaches V`, `path [V1, …]`) runs on the same
// explorer the test build runs (runtime/explore: Search, Path, Moves and
// the replay that rebuilds a frontier state from its run), with the
// machine's step interpreted: Enter, Stay and Emit go to their Next;
// Ignore, Reject and a step's typed failure keep the state; Done ends the
// run, so it is no edge. States are keyed by the encoding the test build's
// key functions write (testplan, encoding.go), so a float keeps its bits.
// When the graph cannot be listed or keyed, or the budget of states stops
// the search first, the law runs over the 100 generated runs the test
// build draws ($ExploreRuns, lower/machinelaws.go), from the same stream
// and in the same order, so the two find the same run.
//
// Work is metered as everywhere in the evaluator: each call of the step
// (a replay's too: the explorer rebuilds each state it expands by stepping
// its run again, so its work is more than its edges), each target test and
// each key component and byte costs steps. A search the step budget stops
// is incomplete, never proved; the budget of states (Budget.States, the
// test build's --states) is the explorer's own bound, and past it a law is
// not proved, as the test build says.
//
// Prior art: TLC and Quint decide an invariant by breadth-first search over
// fingerprints of states; TLC's evaluator and its explorer are one
// program, as here: the interpreted step is the explorer's successor
// function, and no second search is written for check time.

// MachineSearch is what a reachability law's run did: the explorer's
// counters, Steps the calls of the interpreted step (a replayed step
// included), Complete when the whole graph was seen, and Run the witness or
// counterexample run, as the test build prints it.
type MachineSearch struct {
	States, Edges, Depth, Checked int
	Steps                         int64
	Complete                      bool
	Runs                          int
	Run                           string
}

// machine is a machine type's checked parts, as the test build reads them
// (lower/machine.go machineOf).
type machine struct {
	tn            *check.TypeName
	state, signal check.Type
	step, start   *check.Func
}

func machineOf(tn *check.TypeName) machine {
	spec, ok := tn.Decl.Spec.(*syntax.MachineType)
	m, ok2 := tn.Type.(*check.Named).Underlying().(*check.MachineType)
	if !ok || !ok2 {
		ice("the machine %s has no checked declaration", tn.Name)
	}
	id, _ := spec.Step.(*syntax.Ident)
	step, _ := tn.Pkg.Info.Uses[id].(*check.Func)
	if id == nil || step == nil {
		ice("the step of machine %s is not a checked function", tn.Name)
	}
	out := machine{tn: tn, state: m.State, signal: m.Signal, step: step}
	if iid, ok := spec.Initial.(*syntax.Ident); ok {
		out.start, _ = tn.Pkg.Info.Uses[iid].(*check.Func)
	}
	return out
}

// initMachines computes the initial value of each machine of p that has
// one, in declaration order, after every package's lets, as the test
// build's initializer does (lower/decls.go initFunc): a defect in one is
// the run's, even when no law reads it.
func (in *interp) initMachines(p *check.Package) *initFailure {
	for _, f := range p.Files() {
		for _, d := range f.Decls {
			td, ok := d.(*syntax.TypeDecl)
			if !ok {
				continue
			}
			spec, ok := td.Spec.(*syntax.MachineType)
			if !ok || spec.Initial == nil {
				continue
			}
			tn, ok := p.Info.Defs[td.Name].(*check.TypeName)
			if !ok {
				ice("the machine %s has no Defs entry", td.Name.Name)
			}
			if machineOf(tn).start != nil {
				continue
			}
			fr := in.newFrame(p, td)
			v, ab := in.expr(fr, spec.Initial)
			if ab == nil {
				in.initials[tn] = v
				continue
			}
			site := in.site(p, td, spec.Initial.Pos())
			switch ab.kind {
			case ctlDefect:
				return &initFailure{Defect, "a machine's initial value hit a defect: " + ab.msg, site}
			case ctlExhausted:
				return &initFailure{Incomplete, "a machine's initial value: " + ab.msg, site}
			case ctlUnsupported:
				return &initFailure{Skipped, fmt.Sprintf("the initial value of machine %s at %s cannot run at check time: %s", tn.Name, siteText(site), ab.msg), site}
			}
			ice("the control %d left machine %s's initial value", ab.kind, tn.Name)
		}
	}
	return nil
}

// machineLaw runs the reachability law law (Machine laws, above).
func (in *interp) machineLaw(law *check.Law, res Result) (out Result) {
	m := machineOf(law.Machine)
	fr := in.newFrame(lawPkg(law), law.Decl)
	start := in.steps
	ms := &MachineSearch{}
	out = res
	out.Machine = ms
	defer func() {
		if r := recover(); r != nil {
			ab, ok := r.(*abrupt)
			if !ok {
				panic(r)
			}
			out.Status, out.Reason, out.Site = statusOf(ab), ab.msg, in.site(ab.pkg, ab.decl, ab.at)
			out.Evidence = ""
			ms.Complete = false
		}
		out.Steps = in.steps - start
	}()
	skip := func(why string) Result {
		out.Status, out.Reason = Skipped, why
		return out
	}
	pre := preflight{in: in, seen: map[syntax.Node]bool{}}
	if why := pre.function(fr, m.step); why != "" {
		return skip(why)
	}
	std := ""
	keyless := testplan.KeyWhy(m.state, func(n *check.Named) bool {
		if p := n.Obj.Pkg; p != nil && p.Std() && std == "" {
			std = in.argumentType(n)
		}
		return false
	}, in.argumentType)
	if std != "" {
		return skip("the machine's state holds the std type " + std + ", whose key the test build may take from its runtime value")
	}

	// The starts: Initial, or the start of each listed input, or a drawn one.
	var starts []Value
	var startTypes []check.Type
	if m.start == nil {
		v, ok := in.initials[m.tn]
		if !ok {
			ice("machine %s has no initial value", m.tn.Name)
		}
		starts = []Value{v}
	} else {
		if why := pre.function(fr, m.start); why != "" {
			return skip(why)
		}
		for _, p := range m.start.Sig.Params {
			startTypes = append(startTypes, p.Type)
		}
		if lists, ok := listRun(startTypes); ok {
			combos := [][]testplan.Value{nil}
			for _, vs := range lists {
				var next [][]testplan.Value
				for _, c := range combos {
					for _, v := range vs {
						next = append(next, append(c[:len(c):len(c)], v))
					}
				}
				combos = next
			}
			for _, c := range combos {
				args := make([]Value, len(c))
				for i, v := range c {
					args[i] = in.listValue(fr, v)
				}
				starts = append(starts, mustValue(in.call(fr, nil, m.start, nil, args)))
			}
		} else {
			for _, t := range startTypes {
				if why := pre.generator(fr, t, nil, map[check.Type]bool{}); why != "" {
					return skip(why)
				}
			}
		}
	}
	var signals []Value
	if vs, ok := testplan.Domain(m.signal); ok {
		for _, v := range vs {
			signals = append(signals, in.listValue(fr, v))
		}
	} else if why := pre.generator(fr, m.signal, nil, map[check.Type]bool{}); why != "" {
		return skip(why)
	}

	g := explore.Graph[Value, Value]{Starts: starts, Signals: signals}
	g.Step = func(s, e Value) (Value, bool) {
		ms.Steps++
		mustCharge(in, fr, 1)
		return in.machineStep(fr, m, s, e)
	}
	if keyless == "" {
		g.Key = func(k *explore.Key, s Value) {
			in.keyOf(fr, k, m.state, s)
			mustCharge(in, fr, int64(len(k.Bytes()))) // the hash reads each byte
		}
	}
	var targets []func(Value) bool
	var names []string
	for _, t := range law.Targets {
		t := t
		targets = append(targets, func(s Value) bool {
			mustCharge(in, fr, 1)
			return isVariant(t, s)
		})
		names = append(names, t.Obj.Name)
	}
	kind := law.Decl.Reach.Kind()
	l := &machineRun{in: in, fr: fr, m: m, g: g, targets: targets, names: names, ms: ms, res: &out}
	note, bounded := "", false
	if starts != nil && signals != nil {
		if g.Key == nil {
			note = "the state has no key: " + keyless
		} else {
			how, decided := l.all(kind)
			if decided {
				return out
			}
			note, bounded = how, true
		}
	}
	how := l.runs(kind, startTypes, note)
	if out.Status != "" {
		return out
	}
	out.Status = Passed
	switch {
	case how != "100 runs" || note == "":
		out.Reason = how
	case bounded:
		out.Reason = note + "; 100 runs; not proved"
	default:
		out.Reason = how + "; " + note
	}
	return out
}

// machineRun is one reachability law's run.
type machineRun struct {
	in      *interp
	fr      *frame
	m       machine
	g       explore.Graph[Value, Value]
	targets []func(Value) bool
	names   []string
	ms      *MachineSearch
	res     *Result
}

func (l *machineRun) refute(why string) {
	l.res.Status, l.res.Reason, l.res.Evidence = Refuted, why, ""
	l.res.Site = l.in.site(l.fr.pkg, l.fr.decl, l.fr.decl.Pos())
}

func (l *machineRun) proved(how, run string) {
	l.res.Status, l.res.Reason, l.res.Evidence, l.ms.Run = Passed, how, "proved", run
}

// text is the run from start by sigs, as $Trace writes it.
func (l *machineRun) text(start Value, sigs []Value) string {
	parts := make([]string, len(sigs))
	for i, e := range sigs {
		parts[i] = l.in.argument(l.fr, l.m.signal, e)
	}
	return "from " + l.in.argument(l.fr, l.m.state, start) + " with []" + l.in.argumentType(l.m.signal) + "{" + strings.Join(parts, ", ") + "}"
}

func (l *machineRun) counters(r explore.Result) {
	l.ms.States, l.ms.Edges, l.ms.Depth, l.ms.Checked, l.ms.Complete = r.States, r.Edges, r.Depth, r.Checked, r.Complete
}

// all decides the law on the graph ($ExploreAll), or returns what it
// checked and false when the budget of states stops it first.
func (l *machineRun) all(kind string) (string, bool) {
	budget := l.in.budget.States
	itoa := strconv.Itoa
	run := func(r explore.Result) (int, []Value, string) {
		start, idx := r.Run(r.Found)
		sigs := make([]Value, len(idx))
		for i, e := range idx {
			sigs[i] = l.g.Signals[e]
		}
		return start, sigs, l.text(l.g.Starts[start], sigs)
	}
	if kind == "path" {
		r := explore.Path(l.g, l.targets, budget)
		l.counters(r)
		if r.Found >= 0 {
			_, _, text := run(r)
			l.proved("proved by the run "+text, text)
			return "", true
		}
		if r.Complete {
			l.refute("no run follows the path [" + strings.Join(l.names, ", ") + "]")
			return "", true
		}
		return "no run follows the path in " + itoa(r.States) + " states", false
	}
	r := explore.Search(l.g, l.targets[0], budget)
	l.counters(r)
	if r.Found >= 0 {
		start, _, text := run(r)
		if kind == "reaches" {
			l.proved("proved by the run "+text, text)
			return "", true
		}
		l.ms.Run = text
		_, idx := r.Run(r.Found)
		l.refute("the run " + text + " reaches " + l.in.argument(l.fr, l.m.state, explore.Replay(l.g, start, idx)))
		return "", true
	}
	if r.Complete {
		if kind == "reaches" {
			l.refute("no run reaches " + l.names[0] + " (" + itoa(r.States) + " states)")
			return "", true
		}
		l.proved("proved: "+itoa(r.States)+" states, "+itoa(r.Edges)+" edges, longest shortest run "+itoa(r.Depth), "")
		return "", true
	}
	if kind == "reaches" {
		return "no run of at most " + itoa(r.Checked) + " signals reaches " + l.names[0] + "; " + itoa(r.States) + " states", false
	}
	return "no counterexample in runs of at most " + itoa(r.Checked) + " signals; " + itoa(r.States) + " states", false
}

// runs runs the law over 100 generated runs ($ExploreRuns): the same draws
// in the same order from the stream of the law's name. It returns how the
// law held, or sets the result when a run refutes or proves it.
func (l *machineRun) runs(kind string, startTypes []check.Type, note string) string {
	in, fr, g := l.in, l.fr, l.g
	seed := in.budget.Seed
	r := testgen.New(seed, l.res.Name)
	r.Charge = func(n int64) { mustCharge(in, fr, n) }
	walk := g.Key != nil && g.Signals != nil
	type node struct {
		s      Value
		parent int
		sig    Value
	}
	trace := func(nodes []node, i int) (string, []Value) {
		var sigs []Value
		for ; nodes[i].parent >= 0; i = nodes[i].parent {
			sigs = append(sigs, nodes[i].sig)
		}
		for a, b := 0, len(sigs)-1; a < b; a, b = a+1, b-1 {
			sigs[a], sigs[b] = sigs[b], sigs[a]
		}
		return l.text(nodes[i].s, sigs), sigs
	}
	for i := 1; i <= testplan.Cases; i++ {
		l.ms.Runs = i
		r.SetSize(i / 4)
		var s Value
		if g.Starts != nil {
			s = g.Starts[r.Intn(len(g.Starts))]
		} else {
			args := make([]Value, len(startTypes))
			for k, t := range startTypes {
				args[k] = in.generate(fr, r, t, testplan.Depth, nil)
			}
			s = mustValue(in.call(fr, nil, l.m.start, nil, args))
		}
		var sigs []Value
		if d := testplan.Depth; d > 0 {
			n := r.Intn(r.Limit(d) + 1)
			mustCharge(in, fr, int64(n))
			sigs = make([]Value, n)
			for k := range sigs {
				if g.Signals != nil {
					sigs[k] = g.Signals[r.Intn(len(g.Signals))]
				} else {
					sigs[k] = in.generate(fr, r, l.m.signal, d-1, nil)
				}
			}
		}
		nodes := []node{{s: s, parent: -1}}
		for _, e := range sigs {
			if walk {
				moves := explore.Moves(g, s)
				if len(moves) == 0 {
					break
				}
				e = g.Signals[moves[r.Intn(len(moves))]]
			}
			next, ok := g.Step(s, e)
			if !ok {
				break
			}
			s = next
			nodes = append(nodes, node{s: s, parent: len(nodes) - 1, sig: e})
		}
		for k := range nodes {
			switch {
			case kind == "never" && l.targets[0](nodes[k].s):
				text, _ := trace(nodes, k)
				l.ms.Run = text
				l.refute("the run " + text + " reaches " + in.argument(fr, l.m.state, nodes[k].s))
				return ""
			case kind == "reaches" && l.targets[0](nodes[k].s):
				text, _ := trace(nodes, k)
				l.proved("proved by the run "+text, text)
				return ""
			case kind == "path" && k == len(l.targets)-1:
				held := true
				for j := 0; j <= k; j++ {
					held = held && l.targets[j](nodes[j].s)
				}
				if held {
					text, _ := trace(nodes, k)
					l.proved("proved by the run "+text, text)
					return ""
				}
			}
		}
	}
	if kind == "never" {
		l.res.Evidence = "tested"
		return "100 runs"
	}
	if kind == "path" {
		l.refute("no generated run follows the path [" + strings.Join(l.names, ", ") + "]")
		return ""
	}
	l.refute("no generated run reaches " + l.names[0])
	return ""
}

// listRun lists each input's values, every combination at most
// testplan.CaseCap (lower lawcases.go enumRun).
func listRun(types []check.Type) ([][]testplan.Value, bool) {
	n := 1
	var all [][]testplan.Value
	for _, t := range types {
		vs, ok := testplan.Domain(t)
		if !ok || n*len(vs) > testplan.CaseCap {
			return nil, false
		}
		n *= len(vs)
		all = append(all, vs)
	}
	return all, true
}

// machineStep is one edge of the machine: the next state, and false when
// the decision is Done. Another completion than a typed failure (a defect,
// the budget, a form check time does not run) unwinds the search.
func (in *interp) machineStep(fr *frame, m machine, s, e Value) (Value, bool) {
	d, ab := in.call(fr, nil, m.step, nil, []Value{s, e})
	if ab != nil {
		if ab.kind == ctlFail {
			return s, true // a step's failure keeps the state, as Reject does
		}
		panic(ab)
	}
	r, ok := d.(*record)
	if !ok || r.t.Obj.Pkg == nil || r.t.Obj.Pkg.Path != "prelude" {
		ice("the step of machine %s returned %T, not a Decision", m.tn.Name, d)
	}
	switch r.t.Obj.Name {
	case "Enter", "Stay", "Emit":
		for i, f := range structOf(r.t).Fields {
			if f.Name == "Next" {
				return r.fields[i], true
			}
		}
		ice("the prelude's %s has no Next", r.t.Obj.Name)
	case "Done":
		return s, false
	}
	return s, true // Ignore and Reject keep the state
}

// isVariant reports whether the state s is a value of the variant t.
func isVariant(t check.Type, s Value) bool {
	r, ok := s.(*record)
	if !ok {
		return false
	}
	switch u := t.(type) {
	case *check.Named:
		if _, isUnion := u.Underlying().(*check.Union); isUnion {
			return variantIndex(u.Underlying().(*check.Union).Variants, s) >= 0
		}
		return r.t.Obj == u.Obj
	case *check.Union:
		return variantIndex(u.Variants, s) >= 0
	}
	return false
}

// variantIndex is the index of the first variant of vs that s is a value
// of, as a Go type switch picks its first matching case, or -1.
func variantIndex(vs []check.Type, s Value) int {
	for i, v := range vs {
		if isVariant(v, s) {
			return i
		}
	}
	return -1
}

// keyOf writes the key of v, of type t, to k (testplan, encoding.go): the
// bytes the test build's key function writes for the same value. Each
// component costs a step before it is written; a string its bytes; a Map's
// ordering its comparisons.
func (in *interp) keyOf(fr *frame, k *explore.Key, t check.Type, v Value) {
	mustCharge(in, fr, 1)
	switch u := t.(type) {
	case *check.Basic:
		in.keyScalar(fr, k, u, v)
	case *check.Slice:
		xs := v.(sliceV).elems()
		k.Tag(len(xs))
		for _, x := range xs {
			in.keyOf(fr, k, u.Elem, x)
		}
	case *check.Map:
		m := v.(mapV).b.m
		n := int64(len(m))
		mustCharge(in, fr, n*int64(bits.Len64(uint64(n))+1))
		explore.Map(k, m, func(sub *explore.Key, a, b Value) {
			in.keyOf(fr, sub, u.Key, a)
			in.keyOf(fr, sub, u.Elem, b)
		})
	case *check.Tuple:
		for i, x := range v.(tuple).elems {
			in.keyOf(fr, k, u.Types[i], x)
		}
	case *check.Union:
		in.keyVariant(fr, k, t, u.Variants, v)
	case *check.Named:
		switch under := u.Underlying().(type) {
		case *check.Struct:
			r := v.(*record)
			for i, f := range under.Fields {
				in.keyOf(fr, k, f.Type, r.fields[i])
			}
		case *check.Union:
			in.keyVariant(fr, k, t, under.Variants, v)
		default:
			in.keyOf(fr, k, under, v)
		}
	default:
		ice("a state key of %s", t)
	}
}

func (in *interp) keyVariant(fr *frame, k *explore.Key, t check.Type, vs []check.Type, v Value) {
	i := variantIndex(vs, v)
	if i < 0 {
		panic(fr.unsupported(fr.decl, "a state key of a %s value that is not a record variant", t))
	}
	k.Tag(i)
	in.keyOf(fr, k, vs[i], v)
}

func (in *interp) keyScalar(fr *frame, k *explore.Key, b *check.Basic, v Value) {
	switch testplan.ScalarOf(b.Kind) {
	case testplan.ScalarBool:
		k.Bool(v.(bool))
		return
	case testplan.ScalarString:
		s := v.(string)
		mustCharge(in, fr, int64(len(s)))
		k.String(s)
		return
	case testplan.ScalarInt:
		switch x := v.(type) {
		case int8:
			k.Int(int64(x))
		case int16:
			k.Int(int64(x))
		case int32:
			k.Int(int64(x))
		case int64:
			k.Int(x)
		}
		return
	case testplan.ScalarUint:
		switch x := v.(type) {
		case uint8:
			k.Uint(uint64(x))
		case uint16:
			k.Uint(uint64(x))
		case uint32:
			k.Uint(uint64(x))
		case uint64:
			k.Uint(x)
		}
		return
	case testplan.ScalarFloat:
		switch x := v.(type) {
		case float32:
			k.Float(float64(x))
		case float64:
			k.Float(x)
		}
		return
	case testplan.ScalarComplex:
		switch x := v.(type) {
		case complex64:
			k.Complex(complex128(x))
		case complex128:
			k.Complex(x)
		}
		return
	}
	ice("a state key of the basic type %s", b)
}
