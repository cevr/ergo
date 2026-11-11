package lower

import (
	"go/constant"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// A rewrite law (types.md, Laws 14; check.Rewrite) `law L(params) [when
// R() == C] rewrite { lhs } to { rhs }` holds for a provider P when, from
// every state a drawn prefix of the owner's operations reaches (and the
// guard admits), `prefix; lhs; cont` and `prefix; rhs; cont` read the same:
// every result of a drawn continuation cont, then every read operation (no
// parameters, one result). Each side runs on its own fresh provider, which
// P's constructor builds from the case's arguments, so P must be
// deterministic and need not be copyable. A run draws its cases, so it is
// tested, never proved: a prefix is a sequence, an unbounded value (Laws
// 10, one rule). To the drawn cases a run adds rewriteInstanceCases cases
// at each site instance (RewriteInstance): the constants at which a
// module's lowering used the law.

// RewriteInstance is a site instance of a rewrite law: Args holds, by the
// law's parameter index, the constant a site passed for each constant
// parameter (check.Rewrite.Const), nil for a pass-through one.
type RewriteInstance struct {
	Law  *check.Law
	Args []constant.Value
}

// rewriteSteps bounds a case's drawn prefix and continuation: each is zero
// to rewriteSteps-1 operations.
const rewriteSteps = 5

// rewriteInstanceCases is the number of cases a run takes at each site
// instance, each with its own prefix and continuation: one continuation
// may erase the difference the instance makes (a Halve after a write to
// the high bit), so one case would not decide it.
const rewriteInstanceCases = 40

func (l *lowerer) rewriteSide(law *check.Law, right bool) string {
	kind, suffix := "rewriteleft", "Left"
	if right {
		kind, suffix = "rewriteright", "Right"
	}
	return l.pkgName(pkgKey{kind, law}, l.lawFunc(law)+suffix)
}

func (l *lowerer) rewriteGuard(law *check.Law) string {
	return l.pkgName(pkgKey{"rewriteguard", law}, l.lawFunc(law)+"When")
}

// rewriteDecl lowers a rewrite law's sides to Go functions of the test
// build, `func ergoLawTLLeft(env *Env, params...) error` and its Right, and
// its guard, when it has one, to `func ergoLawTLWhen(env *Env) (bool,
// error)`. A side's bare operation calls call the provider the case binds
// into env, as a member law's do.
func (l *lowerer) rewriteDecl(law *check.Law) {
	r := law.Decl.Rewrite
	l.rewriteFunc(law, l.rewriteSide(law, false), r.Lhs.List, nil, true)
	l.rewriteFunc(law, l.rewriteSide(law, true), r.Rhs.List, nil, true)
	if r.Guard != nil {
		l.rewriteFunc(law, l.rewriteGuard(law), law.Decl.Body.List[:1], check.Typ[check.Bool], false)
	}
}

func (l *lowerer) rewriteFunc(law *check.Law, name string, list []syntax.Stmt, result check.Type, params bool) {
	d := law.Decl
	l.tests.law = law
	defer func() { l.tests.law = nil }()
	defer l.leave(l.enter(d.Name, law.Pkg))
	sourced := l.isSourced(law.Pkg)
	if !sourced {
		l.info = law.Pkg.Info
	}
	l.bodies++
	scope := newNamer(l.names)
	fc := newFuncCtx(l, scope, sourced)
	fc.site = d
	fc.owner = "law " + l.lawTitle(law)
	fc.env = scope.alloc("env")
	parts := []string{fc.env + " *" + l.env()}
	fc.frame = newFrame(result, true, framePolls(l.info, d))
	if params {
		for _, v := range law.Params {
			if v.Name == "_" {
				parts = append(parts, scope.alloc("_p")+" "+fc.goType(v.Type))
				continue
			}
			n := scope.alloc(v.Name)
			fc.locals[v] = n
			parts = append(parts, n+" "+fc.goType(v.Type))
		}
	}
	head := "func " + name + "(" + strings.Join(parts, ", ") + ")" + fc.resultList(result, true)
	fc.depth = 1
	fc.entryPoll(l.info, d)
	fc.body(list)
	if sourced {
		l.user.WriteString(l.directive(d))
	}
	l.user.WriteString(head + " {\n")
	l.user.WriteString(fc.out.String())
	l.user.WriteString("}\n\n")
}

// rewriteInstances are the site instances of law the unit gives.
func (l *lowerer) rewriteInstances(law *check.Law) []RewriteInstance {
	var out []RewriteInstance
	for _, in := range l.tests.instances {
		if in.Law == law {
			out = append(out, in)
		}
	}
	return out
}

// constGo is the Go text of the constant v of type t.
func (l *lowerer) constGo(t check.Type, v constant.Value, site syntax.Node) string {
	tc := &typeCtx{l: l, site: site}
	switch v.Kind() {
	case constant.String:
		return tc.goType(t) + "(" + strconv.Quote(constant.StringVal(v)) + ")"
	case constant.Bool:
		return tc.goType(t) + "(" + strconv.FormatBool(constant.BoolVal(v)) + ")"
	case constant.Int, constant.Float:
		return tc.goType(t) + "(" + v.ExactString() + ")"
	}
	ice(site, "a site instance constant of kind %s", v.Kind())
	return ""
}

// rewriteRuns writes the run of a rewrite law against the provider p into
// the runner b (see the top of this file). A case draws the constructor's
// arguments, the law's parameters (a site instance case sets its constant
// ones), a prefix and a continuation, and runs each side on a provider of
// its own: the prefix, the guard (a case it refuses counts as not
// admitted), the side's function, then the continuation and the reads,
// whose results it shows as Ergo literals. Two sides that read differently
// refute the law; a guard that reads differently on the two fresh
// providers says the provider is not deterministic. A run no case of which
// the guard admits is a skip, never a pass.
func (l *lowerer) rewriteRuns(b *strings.Builder, law *check.Law, label string, p check.LawProvider, scope *namer, env, seed, failed, at string) {
	rw := law.Rewrite
	fmtPkg, conv := l.helper("fmt"), l.helper("strconv")
	insts := l.rewriteInstances(law)
	total := testCases + rewriteInstanceCases*len(insts)
	inner := newNamer(scope)
	r, ok, i := inner.alloc("r"), inner.alloc("ok"), inner.alloc("i")
	name := strconv.Quote(label) + ", " + at
	task := taskOf(env)
	b.WriteString("\t{\n")
	b.WriteString("\t\t" + r + " := " + l.helper("NewRand") + "(" + seed + ", " + strconv.Quote(label) + ")\n")
	b.WriteString("\t\t" + r + ".env = " + env + "\n")
	b.WriteString("\t\t" + r + ".rewrite = true\n")
	for _, in := range insts {
		var shown []string
		for k, v := range in.Args {
			if v != nil {
				shown = append(shown, law.Params[k].Name+" = "+v.ExactString())
			}
		}
		b.WriteString("\t\t" + r + ".instances = append(" + r + ".instances, " + strconv.Quote(strings.Join(shown, ", ")) + ")\n")
	}
	b.WriteString("\t\t" + ok + " := true\n")
	b.WriteString("\t\tfor " + i + " := 1; " + ok + " && " + i + " <= " + strconv.Itoa(total) + "; " + i + "++ {\n")
	b.WriteString("\t\t\t" + r + ".start(" + i + ")\n")
	b.WriteString("\t\t\t" + ok + " = " + l.helper("Prop") + "(" + task + ", " + name + ", " + r + ", func() (func() string, func() error) {\n")
	shown := []string{strconv.Quote("case "), conv + ".Itoa(" + i + ")", strconv.Quote(" of " + strconv.Itoa(total) + ", seed "), conv + ".FormatUint(" + seed + ", 10)", strconv.Quote(", size "), conv + ".Itoa(" + i + " / 4)"}
	fc := newFuncCtx(l, inner, false)
	fc.site = law.Decl
	tc := &typeCtx{l: l, site: law.Decl}
	depth := strconv.Itoa(testDepth)
	w := func(s string) { b.WriteString("\t\t\t" + s + "\n") }
	// The constructor's arguments, drawn once: each side calls it with them.
	ctor := p.Ctor
	var cargs, cshown []string
	for _, v := range ctor.Sig.Params {
		a := inner.alloc(v.Name)
		w(a + " := " + l.genExpr(v.Type, r, depth, inner, law.Decl))
		cargs = append(cargs, a)
		cshown = append(cshown, l.litExpr(v.Type, a, law.Decl))
	}
	call := strconv.Quote(": provider = " + ctor.Name + "(")
	if len(cshown) > 0 {
		call += " + " + strings.Join(cshown, ` + ", " + `)
	}
	shown = append(shown, call, strconv.Quote(")"))
	// The law's parameters; a site instance case sets its constants.
	var args []string
	for k, v := range law.Params {
		base := v.Name
		if base == "_" {
			base = "arg"
		}
		a := inner.alloc(base)
		w(a + " := " + l.genExpr(v.Type, r, depth, inner, law.Decl))
		for n, in := range insts {
			if in.Args != nil && k < len(in.Args) && in.Args[k] != nil {
				lo := testCases + rewriteInstanceCases*n
				w("if " + i + " > " + strconv.Itoa(lo) + " && " + i + " <= " + strconv.Itoa(lo+rewriteInstanceCases) + " { " + a + " = " + l.constGo(v.Type, in.Args[k], law.Decl) + " }")
			}
		}
		args = append(args, a)
		shown = append(shown, strconv.Quote(", "+v.Name+" = "), l.litExpr(v.Type, a, law.Decl))
	}
	// The prefix and the continuation: drawn operations, each a closure
	// over its drawn arguments that calls it on a provider and returns
	// what it read.
	ops := tc.goType(p.Slot)
	stepT := "func(" + ops + ") string"
	var steps [2]string
	for side, what := range []string{"prefix", "continuation"} {
		list, shownList, n, j := inner.alloc(what), inner.alloc(what+"Shown"), inner.alloc("n"), inner.alloc("j")
		steps[side] = list
		w("var " + list + " []" + stepT)
		w("var " + shownList + " []string")
		if len(rw.Steps) > 0 {
			w(n + " := " + r + ".intn(" + strconv.Itoa(rewriteSteps) + ")")
			w("for " + j + " := 0; " + j + " < " + n + "; " + j + "++ {")
			w("\tswitch " + r + ".intn(" + strconv.Itoa(len(rw.Steps)) + ") {")
			for k, m := range rw.Steps {
				w("\tcase " + strconv.Itoa(k) + ":")
				var margs, mshown []string
				for _, prm := range m.Sig.Params {
					a := inner.alloc("a")
					w("\t\t" + a + " := " + l.genExpr(prm.Type, r, depth, inner, law.Decl))
					margs = append(margs, a)
					mshown = append(mshown, l.litExpr(prm.Type, a, law.Decl))
				}
				text := strconv.Quote(m.Name + "(")
				if len(mshown) > 0 {
					text += " + " + strings.Join(mshown, ` + ", " + `)
				}
				w("\t\t" + shownList + " = append(" + shownList + ", " + text + " + \")\")")
				pv := inner.alloc("p")
				w("\t\t" + list + " = append(" + list + ", func(" + pv + " " + ops + ") string { " + l.rewriteObserve(m, pv, task, margs, law.Decl) + " })")
			}
			w("\t}")
			w("}")
		}
		shown = append(shown, strconv.Quote(", "+what+" = ["), l.helper("strings")+".Join("+shownList+", \", \")", strconv.Quote("]"))
	}
	// The side: a fresh provider, the prefix, the guard, the side, the
	// continuation and the reads.
	side, right := inner.alloc("side"), inner.alloc("right")
	obs, admitted, e, pv, err := inner.alloc("obs"), inner.alloc("admitted"), inner.alloc("e"), inner.alloc("pv"), inner.alloc("err")
	field := e + "." + l.envField(law.Owner.Type.(*check.Named))
	w(side + " := func(" + right + " bool) (" + obs + " []string, " + admitted + " bool) {")
	w("\t" + e + " := *" + env)
	w("\t" + pv + ", _ := " + fc.funcValue(ctor, ctor.Sig, law.Decl, nil) + "(" + strings.Join(append([]string{env}, cargs...), ", ") + ")")
	w("\t" + field + " = " + fc.convert(p.Conv, pv, law.Decl))
	s := inner.alloc("s")
	w("\tfor _, " + s + " := range " + steps[0] + " { " + s + "(" + field + ") }")
	if rw.Guard != nil {
		g := inner.alloc("g")
		w("\tif " + g + ", " + err + " := " + l.rewriteGuard(law) + "(&" + e + "); " + err + " != nil || !" + g + " { return nil, false }")
	}
	callArgs := strings.Join(append([]string{"&" + e}, args...), ", ")
	w("\tvar " + err + " error")
	w("\tif " + right + " { " + err + " = " + l.rewriteSide(law, true) + "(" + callArgs + ") } else { " + err + " = " + l.rewriteSide(law, false) + "(" + callArgs + ") }")
	w("\tif " + err + " != nil { return []string{\"fails \" + " + err + ".Error()}, true }")
	w("\tfor _, " + s + " := range " + steps[1] + " { " + obs + " = append(" + obs + ", " + s + "(" + field + ")) }")
	it := law.Owner.Type.(*check.Named).Underlying().(*check.Interface)
	for _, m := range it.Methods {
		if len(m.Sig.Params) == 0 && len(m.Sig.Results) == 1 && l.printable(m.Sig.Results[0], map[*check.Named]bool{}) {
			pr := inner.alloc("p")
			w("\t" + obs + " = append(" + obs + ", func(" + pr + " " + ops + ") string { " + l.rewriteObserve(m, pr, task, nil, law.Decl) + " }(" + field + "))")
		}
	}
	w("\treturn " + obs + ", true")
	w("}")
	left, okL, rgt, okR, k := inner.alloc("left"), inner.alloc("okLeft"), inner.alloc("rgt"), inner.alloc("okRight"), inner.alloc("k")
	body := left + ", " + okL + " := " + side + "(false); " + rgt + ", " + okR + " := " + side + "(true); " +
		"if " + okL + " != " + okR + " { " + l.helper("AssertFail") + "(" + at + ", \"the guard reads differently on two fresh providers: the provider is not deterministic\") }; " +
		"if !" + okL + " { return nil }; " + r + ".admitted++; " +
		"for " + k + " := 0; " + k + " < len(" + left + ") || " + k + " < len(" + rgt + "); " + k + "++ { " +
		"if " + k + " >= len(" + left + ") || " + k + " >= len(" + rgt + ") || " + left + "[" + k + "] != " + rgt + "[" + k + "] { " +
		l.helper("AssertFail") + "(" + at + ", \"the sides read differently: left \" + " + l.helper("strings") + ".Join(" + left + ", \", \") + \"; right \" + " + l.helper("strings") + ".Join(" + rgt + ", \", \")) } }; return nil"
	b.WriteString("\t\t\treturn func() string { return " + strings.Join(shown, " + ") + " }, func() error { " + body + " }\n\t\t\t})\n")
	b.WriteString("\t\t}\n")
	why := "no case reaches a state its guard admits"
	b.WriteString("\t\tif " + ok + " && " + r + ".admitted == 0 {\n\t\t\t" + fmtPkg + ".Println(" + strconv.Quote("skip "+label+": "+why) + ")\n")
	b.WriteString("\t\t\t" + l.helper("Record") + "(" + strconv.Quote(label) + ", nil, " + strconv.Quote(why) + ")\n")
	b.WriteString("\t\t} else {\n")
	b.WriteString("\t\t\tif " + ok + " {\n\t\t\t\t" + fmtPkg + ".Println(" + strconv.Quote("ok "+label+" (") + " + " + conv + ".Itoa(" + r + ".admitted) + " + strconv.Quote(" cases)") + ")\n")
	b.WriteString("\t\t\t} else {\n\t\t\t\t" + failed + "++\n")
	b.WriteString("\t\t\t\t" + l.replayLine(law, strconv.Quote("--seed ")+" + "+conv+".FormatUint("+seed+", 10)") + "\n\t\t\t}\n")
	b.WriteString("\t\t\t" + l.helper("Record") + "(" + strconv.Quote(label) + ", " + r + ", \"\")\n\t\t}\n\t}\n")
}

// rewriteObserve is the Go body of a closure over the provider pv that
// calls the operation m with args on the task and returns what it read:
// its result as an Ergo literal, "" for none, or its failure.
func (l *lowerer) rewriteObserve(m *check.Method, pv, task string, args []string, site syntax.Node) string {
	call := pv + "." + memberName(m.Name) + "(" + strings.Join(append([]string{task}, args...), ", ") + ")"
	fallible := len(rowElems(m.Sig.Errors)) > 0
	var result check.Type
	if len(m.Sig.Results) == 1 {
		result = m.Sig.Results[0]
	}
	shown := result != nil && l.printable(result, map[*check.Named]bool{})
	switch {
	case result != nil && fallible:
		if shown {
			return "v, err := " + call + "; if err != nil { return \"fails \" + err.Error() }; return " + l.litExpr(result, "v", site)
		}
		return "_, err := " + call + "; if err != nil { return \"fails \" + err.Error() }; return \"\""
	case result != nil:
		if shown {
			return "return " + l.litExpr(result, call, site)
		}
		return "_ = " + call + "; return \"\""
	case fallible:
		return "if err := " + call + "; err != nil { return \"fails \" + err.Error() }; return \"\""
	}
	return call + "; return \"\""
}
