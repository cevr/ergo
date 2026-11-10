package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Machine laws (MU6, X1; types.md, Laws 13). A law with a body in a
// machine type runs in the test build as every law does (Listed laws,
// lawcases.go), with one addition:
//
//   - A reachability law (`reaches V`, `never reaches V`, `path [V1, …]`)
//     explores the machine's graph: its states from the start (Initial, or
//     the start of every input), and from each state one edge per signal.
//     Enter, Stay and Emit go to their Next; Ignore and Reject keep the
//     state (a Reject is observable and the state stays, runtime.md
//     Machines 2); Done ends the run, so it is no edge; a step failure keeps
//     the state, as Reject does. When the starts and the signals can be
//     listed and the state has a key (State keys, below), the law is
//     decided on the graph by runtime/explore: a breadth-first search with
//     a visited set of hashed keys, so a counterexample or a witness is a
//     shortest run. The search stops at a budget of states (`ergo test
//     --states N`, 1,000,000 by default): a law is "proved" only when the
//     whole graph was seen. Past the budget, and when the graph cannot be
//     listed or keyed, the law also runs over 100 generated runs
//     ($ExploreRuns), whose walk picks only signals that change the state
//     when the state has a key: `never reaches` is then tested, and
//     `reaches` and `path` hold only when a generated run is a witness. A
//     witness proves an existential law in both modes. A counterexample
//     prints as an Ergo test that fails by itself, and a replay command.
//   - An invariant law (X2) is decided the same way, with its predicate as
//     the target: `always p` as a never law whose target is a state where
//     p is false, `sometimes p` as a reaches law whose target is a state
//     where p holds. A sometimes law's witness, a shortest run when the
//     graph is decided, prints after its verdict as an Ergo test that passes
//     by itself; with no witness in a search the budget stopped and in the
//     generated runs, the law fails as "not found".
//
// Prior art: effect-machine's assertPath, assertReaches and
// assertNeverReaches test hand-picked event lists; here they are laws over
// every run, decided on the graph when it is finite (as a model checker,
// TLC or Quint's, decides an invariant by breadth-first search; TLC hashes
// states to 64-bit fingerprints, this search to 128 bits).

// machineLawRuns writes the run of a reachability law (Machine laws) and
// reports whether law is one. Every other law runs by lawRuns.
func (l *lowerer) machineLawRuns(b *strings.Builder, law *check.Law, scope *namer, env, seed, failed string) bool {
	if law.Machine == nil || law.Decl.Reach == nil {
		return false
	}
	l.reachRuns(b, law, scope, env, seed, failed)
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
	label := "law " + l.lawTitle(law)
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
	// machines is the Ergo text of the machine of each listed start, as a
	// counterexample's test writes it.
	mq, _ := l.importAs("machines")
	machineText := l.ergoType(law.Machine.Type) + "{}"
	machines := "nil"
	if md.start == nil {
		starts = "[]" + state + "{" + td.name + "{}.Initial()}"
		machines = "[]string{" + strconv.Quote(machineText) + "}"
	} else {
		// The inputs are listed by the rule of every law's run (Listed
		// laws): each value of each input, every combination once.
		sfd := l.useFunc(md.start, site)
		var types []check.Type
		for _, p := range md.start.Sig.Params {
			types = append(types, p.Type)
		}
		if lists, _, listed := l.enumRun(types, site); listed {
			combos := [][]string{nil}
			for _, vs := range lists {
				var next [][]string
				for _, c := range combos {
					for _, v := range vs {
						next = append(next, append(c[:len(c):len(c)], v))
					}
				}
				combos = next
			}
			calls := make([]string, len(combos))
			texts := make([]string, len(combos))
			for i, c := range combos {
				calls[i] = sfd.callText(sfd.envLead(env), c)
				parts := []string{strconv.Quote(mq + ".Start(" + machineText)}
				for j, v := range c {
					parts = append(parts, "\", \"", l.litExpr(types[j], v, site))
				}
				texts[i] = strings.Join(append(parts, "\")\""), " + ")
			}
			starts = "[]" + state + "{" + strings.Join(calls, ", ") + "}"
			machines = "[]string{" + strings.Join(texts, ", ") + "}"
		} else {
			var gens []string
			for _, p := range md.start.Sig.Params {
				gens = append(gens, l.genExpr(p.Type, r, d, inner, site))
			}
			start = "func(" + r + " " + rand + ", " + d + " int) " + state + " { return " + sfd.callText(sfd.envLead(env), gens) + " }"
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
	call := fd.callText(fd.envLead(env), []string{s, e})
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
	// An invariant law (X2) is decided as a never or a reaches law whose
	// target is a predicate: always p never reaches a state where p is
	// false, and sometimes p reaches one where p is true.
	kind, target, pred := law.Decl.Reach.Kind(), "", ""
	if law.Pred != nil {
		pf := l.useFunc(law.Pred, site)
		test := pf.callText(pf.envLead(env), []string{s})
		if kind == "always" {
			kind, test = "never", "!"+test
		} else {
			kind = "reaches"
		}
		pred = law.Decl.Reach.Pred.Name
		targets = append(targets, "func("+s+" "+state+") bool { return "+test+" }")
		names = append(names, pred)
	} else {
		target = l.ergoType(law.Targets[0])
	}
	show := "func(" + s + " " + state + ") string { return " + l.litExpr(md.state, s, site) + " }"
	showE := "func(" + e + " " + signal + ") string { return " + l.litExpr(md.signal, e, site) + " }"

	// The key of the state, or why it has none.
	key, keyless := "nil", l.keyWhy(md.state, map[string]bool{})
	if keyless == "" {
		k := inner.alloc("k")
		key = "func(" + k + " *" + l.helper("explore") + ".Key, " + s + " " + state + ") { " + l.keyStmt(md.state, k, s, site) + " }"
	}
	// The counterexample's test: its outcome type and its target's text.
	out := "struct{}"
	if len(decision.TArgs) == 2 {
		out = l.ergoType(decision.TArgs[1])
	}
	file := l.siteText(site)
	if i := strings.Index(file, ":"); i >= 0 {
		file = file[:i]
	}
	needs := ""
	if _, imported := l.importAs("machines"); !imported {
		needs = "\t// needs: import machines\n"
	}

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
	b.WriteString("\t\t\tkey: " + key + ",\n")
	b.WriteString("\t\t\tkeyless: " + strconv.Quote(keyless) + ",\n")
	b.WriteString("\t\t\tmachines: " + machines + ",\n")
	b.WriteString("\t\t\tfrom: " + strconv.Quote(mq+".From(") + ",\n")
	b.WriteString("\t\t\trunning: " + strconv.Quote(mq+".Running["+l.ergoType(md.state)+", "+out+"]") + ",\n")
	b.WriteString("\t\t\ttarget: " + strconv.Quote(target) + ",\n")
	b.WriteString("\t\t\tpred: " + strconv.Quote(pred) + ",\n")
	b.WriteString("\t\t\tneeds: " + strconv.Quote(needs) + ",\n")
	b.WriteString("\t\t\tfile: " + strconv.Quote(file) + ",\n")
	b.WriteString("\t\t}\n")
	b.WriteString("\t\tvar " + how + ", " + where + " string\n")
	b.WriteString("\t\tif " + run + "(" + taskOf(env) + ", " + strconv.Quote(label) + ", " + at + ", func() string { return " + where + " }, func() error { " + how + " = " + l.helper("Explore") + "(" + g + ", " + strconv.Quote(kind) + ", " + seed + ", " + l.helper("States") + "(), " + strconv.Quote(label) + ", " + at + ", &" + where + "); return nil }) {\n")
	b.WriteString("\t\t\t" + fmtPkg + ".Println(" + l.helper("Verdict") + "(" + strconv.Quote("ok "+label) + ", " + how + "))\n")
	b.WriteString("\t\t} else {\n\t\t\t" + failed + "++\n\t\t}\n\t}\n")
}

// importAs is the name the root package's files import path under, and
// whether one does; else path's last element, the name an import would
// take.
func (l *lowerer) importAs(path string) (string, bool) {
	for _, f := range l.srcs {
		if f.pkg != l.pkg {
			continue
		}
		for _, d := range f.file.Decls {
			g, ok := d.(*syntax.ImportDecl)
			if !ok {
				continue
			}
			for _, s := range g.Specs {
				if s.Path == nil {
					continue
				}
				p := s.Path.Value
				if q, err := strconv.Unquote(p); err == nil {
					p = q
				}
				if p != path {
					continue
				}
				if s.Name != nil {
					return s.Name.Name, true
				}
				return path[strings.LastIndex(path, "/")+1:], true
			}
		}
	}
	return path[strings.LastIndex(path, "/")+1:], false
}

// State keys (X1). The search hashes each state by its key, a canonical
// encoding (runtime/explore Key) that a generated Go function per named,
// slice, Map, tuple and row type writes: a record its fields in declaration
// order, a union its variant's index and the variant, a slice its length
// and elements, a Map its entries in the order of their encodings, a float
// its bits. So two states have one key exactly when they are identical:
// Ergo's structural `==` (types.md, Operators 1) with each float compared
// by its bits, so 0 and -0 are apart and a NaN is one state (batch-15 X1
// M1: a merge of states a step can tell apart is a false proof). A type that
// holds a function, an operation type's value, a Go type or a runtime
// handle has no key: its law runs over generated runs and says why.

// keyWhy is why values of t have no key, or "" when they have one.
func (l *lowerer) keyWhy(t check.Type, seen map[string]bool) string {
	switch u := t.(type) {
	case *check.Basic:
		return ""
	case *check.Slice:
		return l.keyWhy(u.Elem, seen)
	case *check.Map:
		if why := l.keyWhy(u.Key, seen); why != "" {
			return why
		}
		return l.keyWhy(u.Elem, seen)
	case *check.Tuple:
		for _, e := range u.Types {
			if why := l.keyWhy(e, seen); why != "" {
				return why
			}
		}
		return ""
	case *check.Union:
		if u.Row == nil {
			for _, v := range u.Variants {
				if why := l.keyWhy(v, seen); why != "" {
					return why
				}
			}
			return ""
		}
	case *check.Named:
		k := l.typeKey(t)
		if seen[k] {
			return ""
		}
		seen[k] = true
		if _, ok := check.GoRefElem(u); ok || u.Obj.Decl == nil || u.Obj.Pkg == nil || u.Obj.Pkg.Host() || check.IsGoError(u) {
			return l.ergoType(t) + " is a Go type"
		}
		if isOps(u) {
			return l.ergoType(t) + " is an operation type"
		}
		if l.isRuntimeType(u, nil) {
			return l.ergoType(t) + " is a runtime handle"
		}
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				if why := l.keyWhy(f.Type, seen); why != "" {
					return why
				}
			}
			return ""
		case *check.Union:
			for _, v := range under.Variants {
				if why := l.keyWhy(v, seen); why != "" {
					return why
				}
			}
			return ""
		case *check.Basic, *check.Slice, *check.Map, *check.Tuple:
			return l.keyWhy(under, seen)
		}
	}
	return l.ergoType(t) + " holds a value with no key"
}

// keyName is the package-scope key of the key function of t and its
// preferred name; ok is false for a basic type, whose key is inline.
func (l *lowerer) keyName(t check.Type) (key pkgKey, base string, ok bool) {
	switch u := t.(type) {
	case *check.Named:
		return pkgKey{"key", l.typeKey(t)}, "ergoKey" + u.Obj.Name, true
	case *check.Slice:
		return pkgKey{"key", l.typeKey(t)}, "ergoKeySlice", true
	case *check.Map:
		return pkgKey{"key", l.typeKey(t)}, "ergoKeyMap", true
	case *check.Tuple:
		return pkgKey{"key", l.typeKey(t)}, "ergoKeyTuple", true
	case *check.Union:
		return pkgKey{"key", l.typeKey(t)}, "ergoKeyRow", true
	}
	return pkgKey{}, "", false
}

// keyTypes calls visit on t and each type t's key reaches, each once: the
// naming pass reserves their functions' names. t has a key (keyWhy).
func (l *lowerer) keyTypes(t check.Type, seen map[string]bool, visit func(check.Type)) {
	if _, _, ok := l.keyName(t); !ok {
		return
	}
	if k := l.typeKey(t); seen[k] {
		return
	} else {
		seen[k] = true
	}
	visit(t)
	switch u := t.(type) {
	case *check.Slice:
		l.keyTypes(u.Elem, seen, visit)
	case *check.Map:
		l.keyTypes(u.Key, seen, visit)
		l.keyTypes(u.Elem, seen, visit)
	case *check.Tuple:
		for _, e := range u.Types {
			l.keyTypes(e, seen, visit)
		}
	case *check.Union:
		for _, v := range u.Variants {
			l.keyTypes(v, seen, visit)
		}
	case *check.Named:
		switch under := u.Underlying().(type) {
		case *check.Struct:
			for _, f := range under.Fields {
				l.keyTypes(f.Type, seen, visit)
			}
		case *check.Union:
			for _, v := range under.Variants {
				l.keyTypes(v, seen, visit)
			}
		default:
			l.keyTypes(under, seen, visit)
		}
	}
}

// keyStmt is the Go statement that writes the key of val, of type t, to
// the *explore.Key k.
func (l *lowerer) keyStmt(t check.Type, k, val string, site syntax.Node) string {
	if b, ok := t.(*check.Basic); ok {
		switch {
		case b.Kind == check.Bool:
			return k + ".Bool(" + val + ")"
		case b.Kind == check.String:
			return k + ".String(" + val + ")"
		case b.Kind == check.Duration, b.Kind >= check.Int && b.Kind <= check.Int64:
			return k + ".Int(int64(" + val + "))"
		case b.Kind >= check.Uint && b.Kind <= check.Uintptr:
			return k + ".Uint(uint64(" + val + "))"
		case b.Kind == check.Float32, b.Kind == check.Float64:
			return k + ".Float(float64(" + val + "))"
		case b.Kind == check.Complex64, b.Kind == check.Complex128:
			return k + ".Complex(complex128(" + val + "))"
		}
		ice(site, "a state of type %s, which has no key", t)
	}
	return l.keyFunc(t, site) + "(" + k + ", " + val + ")"
}

// keyFunc is the key function of t, emitted once.
func (l *lowerer) keyFunc(t check.Type, site syntax.Node) string {
	key, base, ok := l.keyName(t)
	if !ok {
		ice(site, "a state of type %s, which has no key function", t)
	}
	name := l.pkgName(key, base)
	if l.tests.keys[key] {
		return name
	}
	if l.tests.keys == nil {
		l.tests.keys = map[pkgKey]bool{}
	}
	l.tests.keys[key] = true
	tc := &typeCtx{l: l, site: site}
	scope := newNamer(l.names)
	k, v := scope.alloc("k"), scope.alloc("v")
	var body strings.Builder
	line := func(s string) { body.WriteString("\t" + s + "\n") }
	variants := func(vs []check.Type) {
		x := scope.alloc("x")
		line("switch " + x + " := " + v + ".(type) {")
		for i, vt := range vs {
			line("case " + tc.goType(vt) + ":")
			line("\t" + k + ".Tag(" + strconv.Itoa(i) + ")")
			line("\t" + l.keyStmt(vt, k, x, site))
		}
		line("default:")
		line("\tpanic(" + strconv.Quote("a value of "+l.ergoType(t)+" with no variant") + ")")
		line("}")
	}
	switch u := t.(type) {
	case *check.Slice:
		e := scope.alloc("e")
		line(k + ".Tag(len(" + v + "))")
		line("for _, " + e + " := range " + v + " {")
		line("\t" + l.keyStmt(u.Elem, k, e, site))
		line("}")
	case *check.Map:
		k2, a, b := scope.alloc("k"), scope.alloc("a"), scope.alloc("b")
		line(l.helper("explore") + ".Map(" + k + ", " + v + ", func(" + k2 + " *" + l.helper("explore") + ".Key, " + a + " " + tc.goType(u.Key) + ", " + b + " " + tc.goType(u.Elem) + ") {")
		line("\t" + l.keyStmt(u.Key, k2, a, site))
		line("\t" + l.keyStmt(u.Elem, k2, b, site))
		line("})")
	case *check.Tuple:
		for i, e := range u.Types {
			line(l.keyStmt(e, k, v+"."+tupleField(i), site))
		}
	case *check.Union:
		variants(u.Variants)
	case *check.Named:
		switch under := u.Underlying().(type) {
		case *check.Struct:
			td := l.typeDecl(u.Obj, site)
			for _, f := range under.Fields {
				field, ok := td.fieldName[f.Name]
				if !ok {
					ice(site, "%s has no field %s", td.name, f.Name)
				}
				line(l.keyStmt(f.Type, k, v+"."+field, site))
			}
		case *check.Union:
			if fu := l.flatUnionOf(t); fu != nil {
				flatBail(site, fu) // a state keeps its interface (flatunion.go), as its literal does
			}
			variants(under.Variants)
		default:
			line(l.keyStmt(under, k, tc.goType(under)+"("+v+")", site))
		}
	default:
		ice(site, "a state of type %s, which has no key function", t)
	}
	l.gen.WriteString("func " + name + "(" + k + " *" + l.helper("explore") + ".Key, " + v + " " + tc.goType(t) + ") {\n" + body.String() + "}\n\n")
	return name
}

// callText is a call of fd with the leading arguments lead (its
// environment) and the arguments args: a function that the lowering made a
// method of its first parameter's type is called on that argument.
func (fd *funcDecl) callText(lead, args []string) string {
	if fd.method != nil && len(args) > 0 {
		return "(" + args[0] + ")." + fd.name + "(" + strings.Join(append(lead[:len(lead):len(lead)], args[1:]...), ", ") + ")"
	}
	return fd.name + "(" + strings.Join(append(lead[:len(lead):len(lead)], args...), ", ") + ")"
}

func quoteAll(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = strconv.Quote(x)
	}
	return strings.Join(q, ", ")
}

// harnessReachGo is the explorer of reachability laws (Machine laws), a
// part of the test build's fixed Go (harness). The search itself is
// runtime/explore's.
const harnessReachGo = `
// $Graph is a machine as a reachability law explores it (MU6, X1): its
// starts when they can be listed (else start draws one), its signals when
// they can be listed (else signal draws one), its step as an edge (false
// when the step ends the run), its literals, the law's targets in order,
// the key of its state (nil, and keyless says why, when it has none), and
// the texts a counterexample's test is written with: the machine of each
// listed start, machines.From and machines.Running as the file names
// them, the target, the import the test needs, and the source file. An
// invariant law (X2) has pred, its predicate's name, and no target: its
// targets[0] is the predicate's failure for always (decided as never) and
// the predicate for sometimes (decided as reaches).
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
	key        func(*$explore.Key, S)
	keyless    string
	machines   []string
	from       string
	running    string
	target     string
	pred       string
	needs      string
	file       string
}

// $Goal is the text of the law's first target: a variant's name, or the
// states where an invariant law's predicate is false (always) or true
// (sometimes).
func $Goal[S, E any](g $Graph[S, E], kind string) string {
	switch {
	case g.pred == "":
		return g.names[0]
	case kind == "never":
		return "a state where " + g.pred + " is false"
	}
	return "a state where " + g.pred + " holds"
}

// $Broken is what a counterexample's last state breaks: ", where p is
// false" for an always law, else "".
func $Broken[S, E any](g $Graph[S, E]) string {
	if g.pred == "" {
		return ""
	}
	return ", where " + g.pred + " is false"
}

// $Verdict is the line of a law that holds: the head, then how in
// parentheses; the lines after how's first are a witness's program, which
// follow the parentheses.
func $Verdict(head, how string) string {
	if i := $strings.Index(how, "\n"); i >= 0 {
		return head + " (" + how[:i] + ")" + how[i:]
	}
	return head + " (" + how + ")"
}

// $Node is a state a generated run reached: from the start of its run, or
// from the node parent by the signal sig.
type $Node[S, E any] struct {
	s      S
	parent int
	sig    E
}

// $Explore decides the law of kind ("reaches", "never" or "path") on g
// with a budget of states. It returns how the law holds; a law that does
// not hold sets where and panics as a refuted law ($Refuted).
func $Explore[S, E any](g $Graph[S, E], kind string, seed uint64, budget int, name, at string, where *string) string {
	note, bounded := "", false
	if g.starts != nil && g.signals != nil {
		if g.key == nil {
			note = "the state has no key: " + g.keyless
		} else {
			how, decided := $ExploreAll(g, kind, budget, name, at, where)
			if decided {
				return how
			}
			note, bounded = how, true
		}
	}
	how := $ExploreRuns(g, kind, seed, budget, name, at, where, note)
	switch {
	case how != "100 runs" || note == "":
		return how
	case bounded:
		return note + "; 100 runs; not proved"
	}
	return how + "; " + note
}

// $Signals is the Ergo text of the signals of a run.
func $Signals[S, E any](g $Graph[S, E], sigs []E) string {
	parts := make([]string, len(sigs))
	for i, e := range sigs {
		parts[i] = g.showSignal(e)
	}
	return "[]" + g.signalType + "{" + $strings.Join(parts, ", ") + "}"
}

// $Block is a counterexample of a never law as an Ergo test that fails by
// itself (the run from start by sigs, on the machine of the listed start
// m, or none when the start was drawn), then the command that replays the
// search.
func $Block[S, E any](g $Graph[S, E], name string, m int, start S, sigs []E, replay string) string {
	check := []string{"match r.State {", "case " + g.target + ":", "\tassert false", "case _:", "}"}
	if g.pred != "" {
		check = []string{"assert " + g.pred + "(r.State)"}
	}
	return $Program(g, name, "Counterexample", m, start, sigs, check, nil) + "\n\treplay: ergo test " + g.file + " " + replay
}

// $Witness is the witness of a sometimes law (X2) as an Ergo test that
// passes by itself: the run ends running, in a state where the predicate
// holds.
func $Witness[S, E any](g $Graph[S, E], name string, m int, start S, sigs []E) string {
	return $Program(g, name, "Witness", m, start, sigs, []string{"assert " + g.pred + "(r.State)"}, []string{"assert false"})
}

// $Program is the run from start by sigs, on the machine of the listed
// start m, as an Ergo test named for the law name and suffix: running are
// its statements when the run ends running (its last state is r.State),
// other those when it does not. It is "" when the start was drawn.
func $Program[S, E any](g $Graph[S, E], name, suffix string, m int, start S, sigs []E, running, other []string) string {
	if m < 0 || m >= len(g.machines) {
		return ""
	}
	text := "\n" + g.needs +
		"\ttest " + $strings.ReplaceAll($strings.TrimPrefix(name, "law "), ".", "") + suffix + " {\n" +
		"\t\tmatch try " + g.from + g.machines[m] + ", " + g.show(start) + ", " + $Signals(g, sigs) + ") {\n" +
		"\t\tcase " + g.running + " as r:\n"
	for _, line := range running {
		text += "\t\t\t" + line + "\n"
	}
	text += "\t\tcase _:\n"
	for _, line := range other {
		text += "\t\t\t" + line + "\n"
	}
	return text + "\t\t}\n\t}"
}

func $Refute(at, text string) { panic($Assertion{at: at, text: text, what: "law refuted"}) }

// $ExploreAll decides the law on the graph by runtime/explore's
// breadth-first search, within budget states. When the budget stops it
// first, it returns what it checked and false.
func $ExploreAll[S, E any](g $Graph[S, E], kind string, budget int, name, at string, where *string) (string, bool) {
	x := $explore.Graph[S, E]{Starts: g.starts, Signals: g.signals, Step: g.step, Key: g.key}
	itoa := $strconv.Itoa
	run := func(r $explore.Result) (int, []E, string) {
		start, idx := r.Run(r.Found)
		sigs := make([]E, len(idx))
		for i, e := range idx {
			sigs[i] = g.signals[e]
		}
		return start, sigs, "from " + g.show(g.starts[start]) + " with " + $Signals(g, sigs)
	}
	if kind == "path" {
		r := $explore.Path(x, g.targets, budget)
		if r.Found >= 0 {
			_, _, text := run(r)
			return "proved by the run " + text, true
		}
		if r.Complete {
			*where = itoa(r.States) + " states, every run"
			$Refute(at, "no run follows the path ["+$strings.Join(g.names, ", ")+"]")
		}
		return "no run follows the path in " + itoa(r.States) + " states", false
	}
	r := $explore.Search(x, g.targets[0], budget)
	if r.Found >= 0 {
		start, sigs, text := run(r)
		if kind == "reaches" {
			if g.pred != "" {
				text += $Witness(g, name, start, g.starts[start], sigs)
			}
			return "proved by the run " + text, true
		}
		*where = itoa(r.States) + " states, " + itoa(len(sigs)) + " signals"
		end := g.starts[start]
		for _, e := range sigs {
			end, _ = g.step(end, e)
		}
		$Refute(at, "the run "+text+" reaches "+g.show(end)+$Broken(g)+$Block(g, name, start, g.starts[start], sigs, "--states "+itoa(r.States)))
	}
	if r.Complete {
		if kind == "reaches" {
			*where = itoa(r.States) + " states, every run"
			$Refute(at, "no run reaches "+$Goal(g, kind)+" ("+itoa(r.States)+" states)")
		}
		return "proved: " + itoa(r.States) + " states, " + itoa(r.Edges) + " edges, longest shortest run " + itoa(r.Depth), true
	}
	if kind == "reaches" {
		return "no run of at most " + itoa(r.Checked) + " signals reaches " + $Goal(g, kind) + "; " + itoa(r.States) + " states", false
	}
	return "no counterexample in runs of at most " + itoa(r.Checked) + " signals; " + itoa(r.States) + " states", false
}

// $ExploreRuns runs the law over 100 generated runs: each from a start,
// with up to the run's size of signals. When the state has a key and the
// signals are listed, each step picks among the signals that change the
// state (runtime/explore Moves), else each listed signal is as likely as
// another. A never law fails at the first run that reaches its target; a
// reaches or path law holds at the first run that is a witness, and fails
// when no run is; note says why the graph was not decided.
func $ExploreRuns[S, E any](g $Graph[S, E], kind string, seed uint64, budget int, name, at string, where *string, note string) string {
	replay := "--seed " + $strconv.FormatUint(seed, 10)
	if g.key != nil && g.starts != nil && g.signals != nil {
		replay = "--states " + $strconv.Itoa(budget) + " " + replay
	}
	r := $NewRand(seed, name)
	draw := g.signal
	if g.signals != nil {
		draw = func(r *$Rand, _ int) E { return g.signals[r.intn(len(g.signals))] }
	}
	walk := g.key != nil && g.signals != nil
	x := $explore.Graph[S, E]{Signals: g.signals, Step: g.step, Key: g.key}
	for i := 1; i <= 100; i++ {
		r.size = i / 4
		var s S
		m := -1
		if g.starts != nil {
			m = r.intn(len(g.starts))
			s = g.starts[m]
		} else {
			s = g.start(r, $Depth)
		}
		sigs := $GenSlice(r, $Depth, draw)
		nodes := []$Node[S, E]{{s: s, parent: -1}}
		for _, e := range sigs {
			if walk {
				moves := $explore.Moves(x, s)
				if len(moves) == 0 {
					break
				}
				e = g.signals[moves[r.intn(len(moves))]]
			}
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
				var run []E
				for _, n := range nodes[1 : k+1] {
					run = append(run, n.sig)
				}
				$Refute(at, "the run "+$Trace(g, nodes, k)+" reaches "+g.show(nodes[k].s)+$Broken(g)+$Block(g, name, m, nodes[0].s, run, replay))
			case kind == "reaches" && g.targets[0](nodes[k].s):
				text := "proved by the run " + $Trace(g, nodes, k)
				if g.pred != "" {
					var run []E
					for _, n := range nodes[1 : k+1] {
						run = append(run, n.sig)
					}
					text += $Witness(g, name, m, nodes[0].s, run)
				}
				return text
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
	if note != "" {
		*where += "; " + note
	}
	if kind == "path" {
		$Refute(at, "no generated run follows the path ["+$strings.Join(g.names, ", ")+"]")
	}
	if g.pred != "" {
		// A sometimes law with no witness in the runs is not found: the
		// graph was not decided, so no witness is no proof either way.
		$Refute(at, "not found: no generated run reaches "+$Goal(g, kind))
	}
	$Refute(at, "no generated run reaches "+g.names[0])
	return ""
}

// $Trace is the run that reached nodes[i]: its start and its signals.
func $Trace[S, E any](g $Graph[S, E], nodes []$Node[S, E], i int) string {
	var sigs []E
	for ; nodes[i].parent >= 0; i = nodes[i].parent {
		sigs = append(sigs, nodes[i].sig)
	}
	for a, b := 0, len(sigs)-1; a < b; a, b = a+1, b-1 {
		sigs[a], sigs[b] = sigs[b], sigs[a]
	}
	return "from " + g.show(nodes[i].s) + " with " + $Signals(g, sigs)
}

// $States reads --states N or --states=N, N a positive decimal, the budget
// of states of every reachability law's search; 1,000,000 by default.
// $Seed reports any other argument.
func $States() int {
	states := 1000000
	args := $os.Args[1:]
	for i := 0; i < len(args); i++ {
		v, a := "", args[i]
		switch {
		case a == "--states" && i+1 < len(args):
			i++
			v = args[i]
		case $strings.HasPrefix(a, "--states="):
			v = a[len("--states="):]
		case a == "--seed":
			i++
			continue
		default:
			continue
		}
		n, err := $strconv.Atoi(v)
		if err != nil || n <= 0 {
			$fmt.Fprintln($os.Stderr, "usage: [--states N], N a positive number, not "+v)
			$os.Exit(2)
		}
		states = n
	}
	return states
}
`
