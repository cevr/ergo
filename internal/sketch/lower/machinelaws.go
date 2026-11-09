package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Machine laws (MU6; types.md, Laws 13). A law in a machine type runs in
// the test build as other laws do, with two additions:
//
//   - A law with parameters whose types can all be listed (enumValues: bool,
//     records and unions of such, tuples, at most enumCap values each, and
//     at most enumCases cases in all) runs once for every value of its
//     parameters, and a run that holds is reported proved. A law with a
//     parameter that cannot be listed runs over 100 generated cases, as a
//     top-level law does, and is reported tested.
//   - A reachability law (`reaches V`, `never reaches V`, `path [V1, …]`)
//     explores the machine's graph: its states from the start (Initial, or
//     the start of every input), and from each state one edge per signal.
//     Enter, Stay and Emit go to their Next; Ignore and Reject keep the
//     state (a Reject is observable and the state stays, runtime.md
//     Machines 2); Done ends the run, so it is no edge; a step failure keeps
//     the state, as Reject does. When the starts and the signals can be
//     listed and the explored graph has at most reachCap states, the law
//     is decided exactly on the graph ($ExploreAll, a breadth-first search,
//     so a counterexample or a witness is a shortest run). Otherwise it
//     runs over 100 generated runs ($ExploreRuns): `never reaches` is then
//     tested, and `reaches` and `path` hold only when a generated run is a
//     witness. A witness proves an existential law, so a `reaches` or a
//     `path` law with a witness is reported proved in both modes; `never
//     reaches` is proved only on the whole graph.
//
// Prior art: effect-machine's assertPath, assertReaches and
// assertNeverReaches test hand-picked event lists; here they are laws over
// every run, decided on the graph when it is finite (as a model checker,
// TLC or Quint's, decides an invariant by breadth-first search). SmallCheck
// checks a property over every small value, as an enumerable machine law
// is checked over every value.

// enumCap bounds the values of one listed type, and enumCases the cases of
// a listed law.
const (
	enumCap   = 256
	enumCases = 4096
)

// enumValues is every value of t as a Go expression, when t can be listed
// with at most enumCap values: bool, a record of such fields, a union of
// such variants, a type defined over one, and a tuple of such.
func (l *lowerer) enumValues(t check.Type, site syntax.Node, seen map[*check.Named]bool) ([]string, bool) {
	tc := &typeCtx{l: l, site: site}
	product := func(goType string, names []string, types []check.Type) ([]string, bool) {
		out := []string{""}
		for i, ft := range types {
			vs, ok := l.enumValues(ft, site, seen)
			if !ok || len(out)*len(vs) > enumCap {
				return nil, false
			}
			var next []string
			for _, prefix := range out {
				for _, v := range vs {
					sep := ""
					if prefix != "" {
						sep = ", "
					}
					next = append(next, prefix+sep+names[i]+": "+v)
				}
			}
			out = next
		}
		for i := range out {
			out[i] = goType + "{" + out[i] + "}"
		}
		return out, true
	}
	switch u := t.(type) {
	case *check.Basic:
		if u.Kind == check.Bool {
			return []string{"false", "true"}, true
		}
	case *check.Tuple:
		names := make([]string, len(u.Types))
		for i := range u.Types {
			names[i] = tupleField(i)
		}
		return product(tc.goType(u), names, u.Types)
	case *check.Named:
		if seen[u] {
			return nil, false // a recursive type has no finite list
		}
		seen[u] = true
		defer delete(seen, u)
		switch under := u.Underlying().(type) {
		case *check.Struct:
			td := l.typeDecl(u.Obj, site)
			names := make([]string, len(under.Fields))
			types := make([]check.Type, len(under.Fields))
			for i, f := range under.Fields {
				names[i], types[i] = td.fieldName[f.Name], f.Type
			}
			return product(tc.goType(u), names, types)
		case *check.Union:
			var out []string
			for _, v := range under.Variants {
				vs, ok := l.enumValues(v, site, seen)
				if !ok || len(out)+len(vs) > enumCap {
					return nil, false
				}
				out = append(out, vs...)
			}
			return out, true
		case *check.Basic:
			vs, ok := l.enumValues(under, site, seen)
			for i := range vs {
				vs[i] = tc.goType(u) + "(" + vs[i] + ")"
			}
			return vs, ok
		}
	}
	return nil, false
}

// enumParams lists every value of each parameter of law, when each can be
// listed and their cases number at most enumCases.
func (l *lowerer) enumParams(law *check.Law) ([][]string, int, bool) {
	n := 1
	var all [][]string
	for _, v := range law.Params {
		vs, ok := l.enumValues(v.Type, law.Decl, map[*check.Named]bool{})
		if !ok || n*len(vs) > enumCases {
			return nil, 0, false
		}
		n *= len(vs)
		all = append(all, vs)
	}
	return all, n, true
}

// machineLawRuns writes the run of a machine law that runs (Machine laws)
// and reports whether it did: a reachability law, or a law whose
// parameters can all be listed. Another machine law runs as a top-level
// law does (lawRuns).
func (l *lowerer) machineLawRuns(b *strings.Builder, law *check.Law, scope *namer, env, seed, failed string) bool {
	if law.Machine == nil || law.Unrun != "" {
		return false
	}
	if law.Decl.Reach != nil {
		l.reachRuns(b, law, scope, env, seed, failed)
		return true
	}
	values, cases, ok := l.enumParams(law)
	if !ok || len(law.Params) == 0 {
		return false
	}
	fmtPkg, run, conv := l.helper("fmt"), l.helper("Case"), l.helper("strconv")
	label := "law " + lawTitle(law)
	at := strconv.Quote(l.siteText(law.Decl))
	inner := newNamer(scope)
	ok1, k := inner.alloc("ok"), inner.alloc("k")
	b.WriteString("\t{\n\t\t" + ok1 + ", " + k + " := true, 0\n")
	var args, shown []string
	shown = []string{strconv.Quote("case "), conv + ".Itoa(" + k + ")", strconv.Quote(" of " + strconv.Itoa(cases) + ", every value")}
	ind := "\t\t"
	for i, v := range law.Params {
		base := v.Name
		if base == "_" {
			base = "arg"
		}
		list, a := inner.alloc(base+"s"), inner.alloc(base)
		b.WriteString(ind + list + " := []" + (&typeCtx{l: l, site: law.Decl}).goType(v.Type) + "{" + strings.Join(values[i], ", ") + "}\n")
		b.WriteString(ind + "for _, " + a + " := range " + list + " {\n")
		ind += "\t"
		args = append(args, a)
		sep := ", "
		if i == 0 {
			sep = ": "
		}
		shown = append(shown, strconv.Quote(sep+v.Name+" = "), l.litExpr(v.Type, a, law.Decl))
	}
	e, held, err := inner.alloc("e"), inner.alloc("held"), inner.alloc("err")
	body := e + " := *" + env + "; " + held + ", " + err + " := " + l.lawFunc(law) + "(" + strings.Join(append([]string{"&" + e}, args...), ", ") + "); if " + err + " != nil { return " + err + " }; if !" + held + " { " + l.helper("Refuted") + "(" + at + ") }; return nil"
	b.WriteString(ind + "if !" + ok1 + " {\n" + ind + "\tbreak\n" + ind + "}\n")
	b.WriteString(ind + k + "++\n")
	b.WriteString(ind + ok1 + " = " + run + "(" + taskOf(env) + ", " + strconv.Quote(label) + ", " + at + ", func() string { return " + strings.Join(shown, " + ") + " }, func() error { " + body + " })\n")
	for range law.Params {
		ind = ind[:len(ind)-1]
		b.WriteString(ind + "}\n")
	}
	b.WriteString("\t\tif " + ok1 + " {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote("ok "+label+" (proved: all "+strconv.Itoa(cases)+" cases)") + ")\n")
	b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
	return true
}

// reachTypes are the types a reachability law's run prints and draws: the
// machine's state and signal, and its start's inputs.
func (l *lowerer) reachTypes(law *check.Law) []check.Type {
	m := law.Machine.Type.(*check.Named).Underlying().(*check.MachineType)
	types := []check.Type{m.State, m.Signal}
	td := l.typeDecl(law.Machine, law.Decl)
	if td.machine != nil && td.machine.start != nil {
		for _, p := range td.machine.start.Sig.Params {
			types = append(types, p.Type)
		}
	}
	return types
}

// reachRuns writes the run of a reachability law: a $Graph of the machine
// and a call of $Explore inside a case, so a defect of the step is
// reported as a test's is.
func (l *lowerer) reachRuns(b *strings.Builder, law *check.Law, scope *namer, env, seed, failed string) {
	site := law.Decl
	tc := &typeCtx{l: l, site: site}
	fmtPkg, run := l.helper("fmt"), l.helper("Case")
	label := "law " + lawTitle(law)
	at := strconv.Quote(l.siteText(site))
	td := l.typeDecl(law.Machine, site)
	md := td.machine
	if md == nil {
		ice(site, "the machine %s has no checked declaration", law.Machine.Name)
	}
	state, signal := tc.goType(md.state), tc.goType(md.signal)
	inner := newNamer(scope)
	g, how, where := inner.alloc("g"), inner.alloc("how"), inner.alloc("where")
	r, d, s, e := inner.alloc("r"), inner.alloc("d"), inner.alloc("s"), inner.alloc("e")
	rand := "*" + l.helper("Rand")

	// The starts: Initial, or the start of each listed input, or of a
	// generated one.
	starts, start := "nil", "nil"
	if md.start == nil {
		starts = "[]" + state + "{" + td.name + "{}.Initial()}"
	} else {
		sfd := l.useFunc(md.start, site)
		combos := []string{""}
		listed := true
		for _, p := range md.start.Sig.Params {
			vs, ok := l.enumValues(p.Type, site, map[*check.Named]bool{})
			if !ok || len(combos)*len(vs) > enumCap {
				listed = false
				break
			}
			var next []string
			for _, c := range combos {
				for _, v := range vs {
					if c != "" {
						next = append(next, c+", "+v)
					} else {
						next = append(next, v)
					}
				}
			}
			combos = next
		}
		lead := strings.Join(sfd.envLead(env), "")
		call := func(args string) string {
			if lead != "" && args != "" {
				return sfd.name + "(" + lead + ", " + args + ")"
			}
			return sfd.name + "(" + lead + args + ")"
		}
		if listed {
			calls := make([]string, len(combos))
			for i, c := range combos {
				calls[i] = call(c)
			}
			starts = "[]" + state + "{" + strings.Join(calls, ", ") + "}"
		} else {
			var gens []string
			for _, p := range md.start.Sig.Params {
				gens = append(gens, l.genExpr(p.Type, r, d, inner, site))
			}
			start = "func(" + r + " " + rand + ", " + d + " int) " + state + " { return " + call(strings.Join(gens, ", ")) + " }"
		}
	}
	signals := "nil"
	if vs, ok := l.enumValues(md.signal, site, map[*check.Named]bool{}); ok {
		signals = "[]" + signal + "{" + strings.Join(vs, ", ") + "}"
	}
	gen := "func(" + r + " " + rand + ", " + d + " int) " + signal + " { return " + l.genExpr(md.signal, r, d, inner, site) + " }"

	// The step as an edge: the next state, and false when the step ends
	// the run (Done).
	fd := l.useFunc(md.step, site)
	call := fd.name + "(" + strings.Join(append(fd.envLead(env), s, e), ", ") + ")"
	var step strings.Builder
	step.WriteString("func(" + s + " " + state + ", " + e + " " + signal + ") (" + state + ", bool) {\n")
	dv := inner.alloc("dv")
	if fd.fallible {
		fe := inner.alloc("fe")
		step.WriteString("\t\t\t" + dv + ", " + fe + " := " + call + "\n")
		step.WriteString("\t\t\tif " + fe + " != nil {\n\t\t\t\treturn " + s + ", true\n\t\t\t}\n")
	} else {
		step.WriteString("\t\t\t" + dv + " := " + call + "\n")
	}
	decision, ok := md.step.Sig.Results[0].(*check.Named)
	if !ok {
		ice(site, "the step of machine %s returns no Decision", law.Machine.Name)
	}
	du, ok := decision.Underlying().(*check.Union)
	if !ok {
		ice(site, "the prelude's Decision is not a union")
	}
	nv := inner.alloc("nv")
	step.WriteString("\t\t\tswitch " + nv + " := any(" + dv + ").(type) {\n")
	for _, v := range du.Variants {
		vn, _ := v.(*check.Named)
		switch {
		case isPreludeType(v, "Enter"), isPreludeType(v, "Stay"), isPreludeType(v, "Emit"):
			next := l.typeDecl(vn.Obj, site).fieldName["Next"]
			step.WriteString("\t\t\tcase " + tc.goType(v) + ":\n\t\t\t\treturn " + nv + "." + next + ", true\n")
		case isPreludeType(v, "Done"):
			step.WriteString("\t\t\tcase " + tc.goType(v) + ":\n\t\t\t\t_ = " + nv + "\n\t\t\t\treturn " + s + ", false\n")
		}
	}
	step.WriteString("\t\t\t}\n\t\t\treturn " + s + ", true\n\t\t}")

	var targets, names []string
	for _, t := range law.Targets {
		targets = append(targets, "func("+s+" "+state+") bool { _, ok := any("+s+").("+tc.goType(t)+"); return ok }")
		names = append(names, t.Obj.Name)
	}
	show := "func(" + s + " " + state + ") string { return " + l.litExpr(md.state, s, site) + " }"
	showE := "func(" + e + " " + signal + ") string { return " + l.litExpr(md.signal, e, site) + " }"

	b.WriteString("\t{\n")
	b.WriteString("\t\t" + g + " := " + l.helper("Graph") + "[" + state + ", " + signal + "]{\n")
	b.WriteString("\t\t\tstarts: " + starts + ",\n")
	b.WriteString("\t\t\tstart: " + start + ",\n")
	b.WriteString("\t\t\tsignals: " + signals + ",\n")
	b.WriteString("\t\t\tsignal: " + gen + ",\n")
	b.WriteString("\t\t\tstep: " + step.String() + ",\n")
	b.WriteString("\t\t\tshow: " + show + ",\n")
	b.WriteString("\t\t\tshowSignal: " + showE + ",\n")
	b.WriteString("\t\t\tsignalType: " + strconv.Quote(l.ergoType(md.signal)) + ",\n")
	b.WriteString("\t\t\ttargets: []func(" + state + ") bool{" + strings.Join(targets, ", ") + "},\n")
	b.WriteString("\t\t\tnames: []string{" + quoteAll(names) + "},\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\tvar " + how + ", " + where + " string\n")
	b.WriteString("\t\tif " + run + "(" + taskOf(env) + ", " + strconv.Quote(label) + ", " + at + ", func() string { return " + where + " }, func() error { " + how + " = " + l.helper("Explore") + "(" + g + ", " + strconv.Quote(law.Decl.Reach.Kind()) + ", " + seed + ", " + strconv.Quote(label) + ", " + at + ", &" + where + "); return nil }) {\n")
	b.WriteString("\t\t\t" + fmtPkg + ".Println(" + strconv.Quote("ok "+label+" (") + " + " + how + " + \")\")\n")
	b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
}

func quoteAll(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return strings.Join(q, ", ")
}

// harnessReachGo is the explorer of reachability laws (Machine laws), a
// part of the test build's fixed Go (harness).
const harnessReachGo = `
// $Graph is a machine as a reachability law explores it (MU6): its starts
// when they can be listed (else start draws one), its signals when they
// can be listed (else signal draws one), its step as an edge (false when
// the step ends the run), its literals, and the law's targets in order.
type $Graph[S, E any] struct {
	starts     []S
	start      func(*$Rand, int) S
	signals    []E
	signal     func(*$Rand, int) E
	step       func(S, E) (S, bool)
	show       func(S) string
	showSignal func(E) string
	signalType string
	targets    []func(S) bool
	names      []string
}

// $Node is a state the explorer reached: from the start of its run, or
// from the node parent by the signal sig.
type $Node[S, E any] struct {
	s      S
	parent int
	sig    E
}

// $ReachCap bounds the states an exact exploration keeps.
const $ReachCap = 4096

// $Explore decides the law of kind ("reaches", "never" or "path") on g.
// It returns how the law holds; a law that does not hold sets where and
// panics as a refuted law ($Refuted).
func $Explore[S, E any](g $Graph[S, E], kind string, seed uint64, name, at string, where *string) string {
	if g.starts != nil && g.signals != nil {
		if how, decided := $ExploreAll(g, kind, at, where); decided {
			return how
		}
	}
	return $ExploreRuns(g, kind, seed, name, at, where)
}

// $Trace is the run that reached nodes[i]: its start and its signals.
func $Trace[S, E any](g $Graph[S, E], nodes []$Node[S, E], i int) string {
	var sigs []string
	for ; nodes[i].parent >= 0; i = nodes[i].parent {
		sigs = append(sigs, g.showSignal(nodes[i].sig))
	}
	for a, b := 0, len(sigs)-1; a < b; a, b = a+1, b-1 {
		sigs[a], sigs[b] = sigs[b], sigs[a]
	}
	return "from " + g.show(nodes[i].s) + " with []" + g.signalType + "{" + $strings.Join(sigs, ", ") + "}"
}

func $Refute(at, text string) { panic($Assertion{at: at, text: text, what: "law refuted"}) }

// $ExploreAll decides the law on the whole graph by breadth-first search,
// and reports false when the graph has more than $ReachCap states.
func $ExploreAll[S, E any](g $Graph[S, E], kind, at string, where *string) (string, bool) {
	var nodes []$Node[S, E]
	seen := map[string]bool{}
	add := func(n $Node[S, E], keep map[string]bool) {
		k := g.show(n.s)
		if !keep[k] {
			keep[k] = true
			nodes = append(nodes, n)
		}
	}
	if kind == "path" {
		layer := map[string]bool{}
		for _, s := range g.starts {
			if g.targets[0](s) {
				add($Node[S, E]{s: s, parent: -1}, layer)
			}
		}
		from, to := 0, len(nodes)
		for k := 1; k < len(g.targets) && from < to; k++ {
			layer = map[string]bool{}
			for i := from; i < to; i++ {
				for _, e := range g.signals {
					if next, ok := g.step(nodes[i].s, e); ok && g.targets[k](next) {
						add($Node[S, E]{s: next, parent: i, sig: e}, layer)
					}
				}
				if len(nodes) > $ReachCap {
					return "", false
				}
			}
			from, to = to, len(nodes)
		}
		if from < to {
			return "proved by the run " + $Trace(g, nodes, from), true
		}
		*where = "every run"
		$Refute(at, "no run follows the path ["+$strings.Join(g.names, ", ")+"]")
	}
	for _, s := range g.starts {
		add($Node[S, E]{s: s, parent: -1}, seen)
	}
	for i := 0; i < len(nodes); i++ {
		if g.targets[0](nodes[i].s) {
			if kind == "reaches" {
				return "proved by the run " + $Trace(g, nodes, i), true
			}
			*where = "every run"
			$Refute(at, "the run "+$Trace(g, nodes, i)+" reaches "+g.show(nodes[i].s))
		}
		for _, e := range g.signals {
			if next, ok := g.step(nodes[i].s, e); ok {
				add($Node[S, E]{s: next, parent: i, sig: e}, seen)
			}
		}
		if len(nodes) > $ReachCap {
			return "", false
		}
	}
	if kind == "reaches" {
		*where = "every run"
		$Refute(at, "no run reaches "+g.names[0]+" ("+$strconv.Itoa(len(nodes))+" states)")
	}
	return "proved over " + $strconv.Itoa(len(nodes)) + " states", true
}

// $ExploreRuns runs the law over 100 generated runs: each from a start,
// with up to the run's size of signals, each listed signal as likely as
// another. A never law fails at the first run that reaches its target; a
// reaches or path law holds at the first run that is a witness, and fails
// when no run is.
func $ExploreRuns[S, E any](g $Graph[S, E], kind string, seed uint64, name, at string, where *string) string {
	r := $NewRand(seed, name)
	draw := g.signal
	if g.signals != nil {
		draw = func(r *$Rand, _ int) E { return g.signals[r.intn(len(g.signals))] }
	}
	for i := 1; i <= 100; i++ {
		r.size = i / 4
		var s S
		if g.starts != nil {
			s = g.starts[r.intn(len(g.starts))]
		} else {
			s = g.start(r, $Depth)
		}
		sigs := $GenSlice(r, $Depth, draw)
		nodes := []$Node[S, E]{{s: s, parent: -1}}
		for _, e := range sigs {
			next, ok := g.step(s, e)
			if !ok {
				break
			}
			s = next
			nodes = append(nodes, $Node[S, E]{s: s, parent: len(nodes) - 1, sig: e})
		}
		for k := range nodes {
			switch {
			case kind == "never" && g.targets[0](nodes[k].s):
				*where = "run " + $strconv.Itoa(i) + " of 100, seed " + $strconv.FormatUint(seed, 10) + ", size " + $strconv.Itoa(i/4)
				$Refute(at, "the run "+$Trace(g, nodes, k)+" reaches "+g.show(nodes[k].s))
			case kind == "reaches" && g.targets[0](nodes[k].s):
				return "proved by the run " + $Trace(g, nodes, k)
			case kind == "path" && k == len(g.targets)-1:
				held := true
				for j := 0; j <= k; j++ {
					held = held && g.targets[j](nodes[j].s)
				}
				if held {
					return "proved by the run " + $Trace(g, nodes, k)
				}
			}
		}
	}
	if kind == "never" {
		return "100 runs"
	}
	*where = "100 runs, seed " + $strconv.FormatUint(seed, 10)
	if kind == "path" {
		$Refute(at, "no generated run follows the path ["+$strings.Join(g.names, ", ")+"]")
	}
	$Refute(at, "no generated run reaches "+g.names[0])
	return ""
}
`
