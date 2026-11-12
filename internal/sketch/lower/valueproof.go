package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/eval"
	"ergo.local/prototype/internal/sketch/syntax"
)

// lawProvider lends the case's own record. Ordinary operation conversions
// remain unchanged; a law run never makes a user-visible provider cell.
func (fc *funcCtx) lawProvider(p check.LawProvider, val string, site syntax.Node) string {
	if p.Value {
		val = "&" + val
	}
	return fc.convert(p.Conv, val, site)
}

// proofStateText must not print a constructor instead of the reached
// record: NewP() describes only its initial state and hides a counterexample.
func (l *lowerer) proofStateText(t check.Type, val string, site syntax.Node) string {
	if n, ok := t.(*check.Named); ok {
		if record, ok := n.Underlying().(*check.Struct); ok {
			td := l.typeDecl(n.Obj, site)
			parts := []string{strconv.Quote(l.ergoType(n) + "{")}
			for i, f := range record.Fields {
				sep := ""
				if i != 0 {
					sep = ", "
				}
				parts = append(parts, strconv.Quote(sep+f.Name+": "), l.proofStateText(f.Type, val+"."+td.fieldName[f.Name], site))
			}
			return strings.Join(append(parts, strconv.Quote("}")), " + ")
		}
	}
	return l.litExpr(t, val, site)
}

// proofBodyWhy rejects foreign or open behavior transitively, including in
// helpers and guards. A completed native proof runs these exact bodies.
func proofBodyWhy(fn *check.Func, seen map[*check.Func]bool) string {
	if fn == nil {
		return "an indirect call has no checked body"
	}
	if seen[fn] {
		return ""
	}
	seen[fn] = true
	if fn.Decl == nil || fn.Decl.Body == nil || fn.Decl.Extern != nil || fn.Pkg.Host() {
		return fn.Name + " has no Ergo body"
	}
	if len(rowElems(fn.Sig.Uses)) != 0 || len(rowElems(fn.Sig.Errors)) != 0 || fn.Sig.Fallible {
		return fn.Name + " has a needs or fails row"
	}
	why := ""
	syntax.Inspect(fn.Decl.Body, func(n syntax.Node) bool {
		if why != "" {
			return false
		}
		if call, ok := n.(*syntax.CallExpr); ok {
			c := fn.Pkg.Info.Calls[call]
			if c == nil {
				why = fn.Name + " has an unchecked call"
			} else {
				switch c.Kind {
				case check.CallFunc:
					why = proofBodyWhy(c.Func, seen)
				case check.CallConversion, check.CallLen:
				default:
					why = fn.Name + " calls an operation or function value"
				}
			}
		}
		return why == ""
	})
	return why
}

// proofLists lists parameter products without writing a second generator.
func (l *lowerer) proofLists(params []*check.Var, site syntax.Node) ([][]string, bool) {
	var lists [][]string
	n := 1
	for _, p := range params {
		vs, ok := l.enumValues(p.Type, site, nil)
		if !ok || len(vs) == 0 || n > enumCases/len(vs) {
			return nil, false
		}
		n *= len(vs)
		lists = append(lists, vs)
	}
	return lists, true
}

// proofEach writes the cartesian product in the shared finite-domain order.
func proofEach(b *strings.Builder, scope *namer, params []*check.Var, lists [][]string, typ func(check.Type) string, body func([]string)) {
	b.WriteString("{\n")
	var args []string
	for i, p := range params {
		a := scope.alloc("arg")
		b.WriteString("for _, " + a + " := range []" + typ(p.Type) + "{" + strings.Join(lists[i], ", ") + "} {\n")
		args = append(args, a)
	}
	body(args)
	for range params {
		b.WriteString("}\n")
	}
	b.WriteString("}\n")
}

// valueProof emits exhaustive runs on keyed provider records. Search mode
// closes all constructor states under all operations. When an operation has
// an unlistable parameter, all finite field values are a safe superset;
// a counterexample there is not a reachable failure. Unsupported runs fall
// back to the existing tested reporter, never to proof.
func (l *lowerer) valueProof(b *strings.Builder, law *check.Law, label string, p check.LawProvider, scope *namer, env, failed, at string) bool {
	if !p.Value {
		return false
	}
	skip := func(why string) bool {
		b.WriteString(l.helper("fmt") + ".Println(" + strconv.Quote("skip "+label+" (not proved: "+why+")") + ")\n")
		b.WriteString(l.helper("Record") + "(" + strconv.Quote(label) + ", nil, " + strconv.Quote("not proved: "+why) + ")\n")
		return true
	}
	if why := l.keyWhy(p.Type); why != "" {
		if p.Ctor == nil {
			return skip(why)
		}
		return false
	}
	for _, side := range [][]check.RewriteCall{law.Rewrite.Lhs, law.Rewrite.Rhs} {
		for _, call := range side {
			for _, result := range call.Op.Sig.Results {
				if why := l.keyWhy(result); why != "" {
					return skip("side result has no key: " + why)
				}
			}
		}
	}
	for _, r := range law.Where {
		if r != nil {
			return skip("a law parameter has a where restriction")
		}
	}
	// A case the native run cannot list, or a body it cannot run, goes to
	// the check-time evaluator's symbolic run (eval.ProveSymbolic), whose
	// verdict is fixed here.
	symbolic := func() bool {
		pr := eval.ProveSymbolic(law, p)
		if !pr.Proved {
			return skip(pr.Why)
		}
		b.WriteString(l.helper("fmt") + ".Println(" + strconv.Quote("ok "+label+" (proved: "+pr.How()+")") + ")\n")
		b.WriteString(l.helper("Record") + "(" + strconv.Quote(label) + ", &" + l.helper("Rand") + "{cases: " + strconv.Itoa(pr.Cases) + ", proved: true, rewrite: true, admitted: " + strconv.Itoa(pr.Cases) + "}, \"\")\n")
		return true
	}
	params, listable := l.proofLists(law.Params, law.Decl)
	if !listable {
		return symbolic()
	}
	it := p.Slot.Underlying().(*check.Interface)
	// Search runs from the constructor only when every value of the type
	// is one it and the operations reach (construction.go).
	open := constructionWhy(p.Type, p.Ctor, p.Conv, func(o *openConstruction) string {
		if o.node == nil {
			return ""
		}
		return l.siteText(o.node)
	})
	search := p.Ctor != nil && open == ""
	opLists := make([][][]string, len(it.Methods))
	for i, op := range it.Methods {
		var ok bool
		opLists[i], ok = l.proofLists(op.Sig.Params, law.Decl)
		search = search && ok
		if why := proofBodyWhy(p.Conv.Table[op.Name], map[*check.Func]bool{}); why != "" {
			return symbolic()
		}
	}
	var ctorLists [][]string
	if search {
		var ok bool
		ctorLists, ok = l.proofLists(p.Ctor.Sig.Params, law.Decl)
		if !ok {
			search = false
		} else if why := proofBodyWhy(p.Ctor, map[*check.Func]bool{}); why != "" {
			return symbolic()
		}
	}
	// Helpers in computed RHS arguments and the guard are proof inputs too.
	why := ""
	syntax.Inspect(law.Decl.Body, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.CallExpr); ok {
			if call := law.Pkg.Info.Calls[c]; call != nil && call.Kind == check.CallFunc {
				why = proofBodyWhy(call.Func, map[*check.Func]bool{})
			}
		}
		return why == ""
	})
	if why != "" {
		return symbolic()
	}
	var fieldVars []*check.Var
	var fieldLists [][]string
	if !search {
		for _, f := range p.Type.Underlying().(*check.Struct).Fields {
			fieldVars = append(fieldVars, &check.Var{Name: f.Name, Type: f.Type})
		}
		var ok bool
		fieldLists, ok = l.proofLists(fieldVars, law.Decl)
		if !ok {
			return symbolic()
		}
	}
	fc := newFuncCtx(l, newNamer(scope), false)
	fc.site = law.Decl
	tc := &typeCtx{l: l, site: law.Decl}
	// The closure has its own Go scope; fresh names avoid shadowing user code.
	ns := newNamer(scope)
	stateT := tc.goType(p.Type)
	w := func(s string) { b.WriteString(s + "\n") }
	states, seen, key, add := ns.alloc("states"), ns.alloc("seen"), ns.alloc("key"), ns.alloc("add")
	v, k := ns.alloc("v"), ns.alloc("k")
	complete, cases, admitted, held := ns.alloc("complete"), ns.alloc("cases"), ns.alloc("admitted"), ns.alloc("held")
	depths, paths := ns.alloc("depths"), ns.alloc("paths")
	budget := ns.alloc("budget")
	counterexample := ns.alloc("counterexample")
	stop := ns.alloc("stop")
	w("{")
	w("var " + states + " []" + stateT)
	w("var " + depths + " []int; var " + paths + " []string")
	w(seen + " := map[string]bool{}")
	w(complete + ", " + held + " := true, true")
	w(cases + ", " + admitted + " := 0, 0")
	w(counterexample + " := \"\"")
	w(stop + " := \"\"")
	w(budget + " := " + l.helper("States") + "()")
	w(key + " := func(" + v + " " + stateT + ") string { var " + k + " " + l.helper("explore") + ".Key; " + l.keyStmt(p.Type, "&"+k, v, law.Decl) + "; return string(" + k + ".Bytes()) }")
	d, path := ns.alloc("depth"), ns.alloc("path")
	w(add + " := func(" + v + " " + stateT + ", " + d + " int, " + path + " string) {")
	w(k + " := " + key + "(" + v + "); if " + seen + "[" + k + "] { return }; if len(" + states + ") >= " + budget + " { " + complete + " = false; return }")
	w(seen + "[" + k + "] = true; " + states + " = append(" + states + ", " + v + "); " + depths + " = append(" + depths + ", " + d + "); " + paths + " = append(" + paths + ", " + path + ") }")
	if search {
		proofEach(b, ns, p.Ctor.Sig.Params, ctorLists, tc.goType, func(args []string) {
			w(v + " := " + fc.rewriteCall(p.Ctor, "", args, law.Decl))
			w(add + "(" + v + ", 0, " + strconv.Quote(p.Ctor.Name+"()") + ")")
		})
	} else {
		fields := p.Type.Underlying().(*check.Struct).Fields
		proofEach(b, ns, fieldVars, fieldLists, tc.goType, func(args []string) {
			td := l.typeDecl(p.Type.Obj, law.Decl)
			var fs []string
			for i, f := range fields {
				fs = append(fs, td.fieldName[f.Name]+": "+args[i])
			}
			w(add + "(" + stateT + "{" + strings.Join(fs, ", ") + "}, 0, \"all values\")")
		})
	}
	idx, state := ns.alloc("i"), ns.alloc("state")
	w("for " + idx + " := 0; " + idx + " < len(" + states + ") && " + complete + " && " + held + "; " + idx + "++ {")
	w(state + " := " + states + "[" + idx + "]")
	proofEach(b, ns, law.Params, params, tc.goType, func(args []string) {
		a, c, e, err := ns.alloc("left"), ns.alloc("right"), ns.alloc("e"), ns.alloc("err")
		w("if " + held + " {")
		w(a + " := " + tc.cloneValue(p.Type, state, law.Decl) + "; " + c + " := " + tc.cloneValue(p.Type, state, law.Decl))
		w(e + " := *" + env)
		field := e + "." + l.envField(law.Owner.Type.(*check.Named))
		w(field + " = " + fc.lawProvider(p, a, law.Decl))
		g := "true"
		if law.Rewrite.Guard != nil {
			g = ns.alloc("guard")
			w(g + ", " + err + " := " + l.rewriteGuard(law) + "(&" + e + "); if " + err + " != nil { " + held + " = false }")
			w("if " + key + "(" + a + ") != " + key + "(" + state + ") { " + stop + " = \"the guard changes the provider state\"; " + complete + " = false }")
		}
		w(cases + "++")
		w("if " + g + " && " + held + " && " + complete + " {")
		w(admitted + "++")
		callargs := strings.Join(append([]string{"&" + e}, args...), ", ")
		leftResults, rightResults, rightError := ns.alloc("leftResults"), ns.alloc("rightResults"), ns.alloc("rightError")
		w(leftResults + ", " + err + " := " + l.rewriteSide(law, false) + "(" + callargs + ")")
		w(field + " = " + fc.lawProvider(p, c, law.Decl))
		w("if " + err + " != nil { " + held + " = false }")
		w(rightResults + ", " + rightError + " := " + l.rewriteSide(law, true) + "(" + callargs + ")")
		w("if " + rightError + " != nil { " + held + " = false }")
		w("if len(" + leftResults + ") != len(" + rightResults + ") { " + held + " = false }")
		j := ns.alloc("result")
		w("for " + j + " := range " + leftResults + " { if " + j + " >= len(" + rightResults + ") || " + leftResults + "[" + j + "] != " + rightResults + "[" + j + "] { " + held + " = false } }")
		w("if " + key + "(" + a + ") != " + key + "(" + c + ") { " + held + " = false }")
		w("}")
		w("if !" + held + " { " + counterexample + " = \"state = \" + " + l.proofStateText(p.Type, state, law.Decl) + " + \", path = \" + " + paths + "[" + idx + "] + \"; left = \" + " + l.proofStateText(p.Type, a, law.Decl) + " + \"; right = \" + " + l.proofStateText(p.Type, c, law.Decl) + " }")
		w("}")
	})
	if search {
		for i, op := range it.Methods {
			proofEach(b, ns, op.Sig.Params, opLists[i], tc.goType, func(args []string) {
				w(v + " := " + tc.cloneValue(p.Type, state, law.Decl))
				f := p.Conv.Table[op.Name]
				self := v
				if f.Ptr {
					self = "&" + v
				}
				call := fc.rewriteCall(f, self, args, law.Decl)
				if len(f.Sig.Results) > 0 {
					call = "_ = " + call
				}
				w(call)
				w(add + "(" + v + ", " + depths + "[" + idx + "]+1, " + paths + "[" + idx + "]+" + strconv.Quote("; "+op.Name+"()") + ")")
			})
		}
	}
	w("}")
	fmt, conv := l.helper("fmt"), l.helper("strconv")
	w("if " + stop + " != \"\" {")
	w(fmt + ".Println(" + strconv.Quote("skip "+label+" (not proved: ") + "+" + stop + "+\")\")")
	w("} else if !" + held + " {")
	if search {
		w(fmt + ".Println(" + strconv.Quote("FAIL "+label+" (reachable state): sides differ: ") + "+" + counterexample + "); " + failed + "++")
		w(l.replayLine(law, `""`))
	} else {
		why := "false at a state which may be unreachable"
		if open != "" {
			why += "; " + open
		}
		w(fmt + ".Println(" + strconv.Quote("skip "+label+" (not proved: "+why+"): ") + "+" + counterexample + ")")
	}
	w("} else if !" + complete + " {")
	w(fmt + ".Println(" + strconv.Quote("skip "+label+" (not proved: explored ") + "+" + conv + ".Itoa(len(" + states + "))+\" states, depth \"+ " + conv + ".Itoa(" + depths + "[len(" + depths + ")-1])+\")\")")
	w("} else if " + admitted + " == 0 {")
	w(fmt + ".Println(" + strconv.Quote("skip "+label+": no case reaches a state its guard admits") + ")")
	w("} else {")
	w(fmt + ".Println(" + strconv.Quote("ok "+label+" (proved: all ") + "+" + conv + ".Itoa(" + cases + ")+\" cases)\")")
	rec := ns.alloc("rec")
	w(rec + " := &" + l.helper("Rand") + "{cases: " + cases + ", proved: true, rewrite: true, admitted: " + admitted + "}; " + l.helper("Record") + "(" + strconv.Quote(label) + ", " + rec + ", \"\")")
	w("}")
	w("}")
	return true
}
