package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Scripts (stdlib.md, Testing 7 to 12; check/script.go). A
// testing.Script[S] instance is a Go struct of its own, one field per
// operation of S, each the runtime's *v1.Op[A, F] (scriptType); its literal
// makes each Op with v1.NewOp (scriptLit); its conversion to an operation
// type is an adapter whose methods record the call and call the answer
// (scriptOperations). Hand Go control: a fake struct with a mutex, a slice
// of calls and a func field per method, as mockgen writes; the same work
// per call (one lock, one append, one call). A held call (Op.Hold) waits
// inside Op.Call on the caller's task, which the adapter passes.

// scriptBase is the preferred Go name of the struct of the Script n.
func scriptBase(n *check.Named) string { return "script" + check.ScriptOf(n).Obj.Name }

// scriptType is the Go struct of the Script instance n, emitted at its
// first use.
func (l *lowerer) scriptType(n *check.Named, site syntax.Node) string {
	key := l.typeKey(n)
	name := l.pkgName(pkgKey{"script", key}, scriptBase(n))
	if l.scripts[key] {
		return name
	}
	l.scripts[key] = true
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	var b strings.Builder
	b.WriteString("// " + name + " is " + n.String() + ", one operation of " + check.ScriptOf(n).String() + " per field (a derived Script).\n")
	b.WriteString("type " + name + " struct {\n")
	if innerCap := check.WrappingProviderOf(n); innerCap != nil {
		b.WriteString("\tinner " + tc.goType(innerCap) + "\n")
	}
	for _, f := range scriptFields(n, site) {
		b.WriteString("\t" + memberName(f.Name) + " " + tc.goType(f.Type) + "\n")
	}
	b.WriteString("}\n\n")
	l.gen.WriteString(b.String())
	return name
}

// reserveScripts reserves the struct name of every Script instance the
// program's facts name: the types of its expressions and variables, at any
// depth of a type (naming.go, step 4).
func (l *lowerer) reserveScripts(pkgs []*check.Package, reserve func(key pkgKey, base string)) {
	walked := map[check.Type]bool{}
	var walk func(t check.Type)
	walk = func(t check.Type) {
		if t == nil || walked[t] {
			return
		}
		walked[t] = true
		switch u := t.(type) {
		case *check.Slice:
			walk(u.Elem)
		case *check.Array:
			walk(u.Elem)
		case *check.Map:
			walk(u.Key)
			walk(u.Elem)
		case *check.Pointer:
			walk(u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		case *check.Signature:
			for _, p := range u.Params {
				walk(p.Type)
			}
			for _, r := range u.Results {
				walk(r)
			}
		case *check.Named:
			if check.ScriptOf(u) != nil {
				key := l.typeKey(u)
				reserve(pkgKey{"script", key}, scriptBase(u))
			}
			for _, a := range u.TArgs {
				walk(a)
			}
			if s, ok := u.Underlying().(*check.Struct); ok {
				for _, f := range s.Fields {
					walk(f.Type)
				}
			}
		}
	}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for _, tn := range l.canon.types(p) {
			walk(tn.Type)
		}
		for _, f := range l.canon.funcs(p) {
			if f.Sig != nil {
				walk(f.Sig)
			}
		}
		for id, o := range p.Info.Defs {
			if v, ok := o.(*check.Var); ok && l.readsFact(p, id) {
				walk(v.Type)
			}
		}
		for x, t := range p.Info.Types {
			if l.readsFact(p, x) {
				walk(t)
			}
		}
	}
}

// scriptFields are the fields of the Script n, one per operation.
func scriptFields(n *check.Named, site syntax.Node) []*check.Field {
	s, ok := n.Underlying().(*check.Struct)
	if !ok {
		ice(site, "the Script %s has no record", n)
	}
	return s.Fields
}

// scriptLit is the Go value of the literal of the Script n: each field a
// new Op with no answer and no calls, named by the Script and the
// operation for its defect and its end-of-test report.
func (fc *funcCtx) scriptLit(n *check.Named, site syntax.Node) string {
	var parts []string
	scriptText := strconv.Quote(n.String())
	for _, f := range scriptFields(n, site) {
		op, ok := f.Type.(*check.Named)
		if !ok || len(op.TArgs) != 2 {
			ice(site, "the field %s of %s is not a provider field", f.Name, n)
		}
		if pc := f.ProviderCalls(); pc != nil {
			// An Ergo field type: its NewF (check/provider.go).
			parts = append(parts, memberName(f.Name)+": "+fc.providerCall(pc.New, pc, fc.envExpr(site), nil, site))
			continue
		}
		// The field type names its runtime Go type G, whose constructor
		// NewG the checker requires (check/body.go providerFieldRefusal).
		g, ok := check.GoTypeOf(op)
		if !ok {
			ice(site, "the field %s of %s names no runtime Go type", f.Name, n)
		}
		parts = append(parts, memberName(f.Name)+": "+fc.l.rt()+"."+check.ProviderFieldNew(g)+"["+fc.goType(op.TArgs[0])+", "+fc.goType(op.TArgs[1])+"]("+scriptText+", "+strconv.Quote(f.Name)+")")
	}
	return fc.goType(n) + "{" + strings.Join(parts, ", ") + "}"
}

// scriptOperations converts val, a Script, to the operation type of conv:
// an adapter whose method for each operation records the arguments with
// Op.Call and calls the answer with an environment of the caller's task,
// as a table function's adapter does (operations). An operation with no
// answer is the NoAnswer defect, except one with no result and no failure,
// whose call is only recorded.
func (fc *funcCtx) scriptOperations(conv check.Conversion, val string, site syntax.Node) string {
	l := fc.l
	key, base, ok := l.adapterName(conv)
	if !ok {
		unreachable(site, ruleAccepted, "an operations conversion from %s to %s with no adapter (adapterName)", conv.From, conv.To)
	}
	from := conv.From.(*check.Named)
	to, ok := conv.To.(*check.Named)
	if !ok {
		ice(site, "a Script converted to %s, which is not a named operation type", conv.To)
	}
	toDecl := l.typeDecl(to.Obj, site)
	if toDecl.kind != kindOps {
		ice(site, "a Script converted to %s, which is not an operation type", conv.To)
	}
	name, ok := l.adapters[key]
	if ok {
		return "&" + name + "{v: " + val + "}"
	}
	name = l.pkgName(pkgKey{"adapter", key}, base)
	l.adapters[key] = name
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	fromGo := tc.goType(from)
	tc.goType(to) // the adapter implements it: the type is emitted
	if to.Orig != nil {
		for i, tp := range to.Orig.TParams {
			if i < len(to.TArgs) && tp.Kind == check.KindType {
				tc.params[tp] = tc.goType(to.TArgs[i])
			}
		}
	}
	fields := map[string]*check.Field{}
	for _, f := range scriptFields(from, site) {
		fields[f.Name] = f
	}
	scriptText := strconv.Quote(from.String())
	var b strings.Builder
	b.WriteString("// " + name + " provides " + conv.To.String() + " through the Script " + from.String() + " (an operations conversion).\n")
	b.WriteString("type " + name + " struct{ v " + fromGo + " }\n\n")
	for _, op := range toDecl.ops {
		f := fields[op.Name]
		if f == nil {
			ice(site, "the Script %s has no operation %s of %s", from, op.Name, conv.To)
		}
		sig := scriptSig(f, site)
		locals := newNamer(l.names)
		self := locals.alloc("a")
		task := locals.alloc("t")
		params := []string{task + " *" + l.taskPkg() + ".Task"}
		args := []string{"&" + l.env() + "{" + envTask + ": " + task + "}"}
		var names []string
		for _, p := range sig.Params {
			n := locals.alloc("p")
			params = append(params, n+" "+tc.goType(p.Type))
			args = append(args, n)
			names = append(names, n)
		}
		// The arguments the Op records: the one parameter, a tuple of
		// several, NoArgs for none (check/script.go, rule 2).
		argsType := f.Type.(*check.Named).TArgs[0]
		var record string
		switch len(names) {
		case 0:
			record = tc.goType(argsType) + "{}"
		case 1:
			record = names[0]
		default:
			parts := make([]string, len(names))
			for i, n := range names {
				parts[i] = tupleField(i) + ": " + n
			}
			record = tc.goType(argsType) + "{" + strings.Join(parts, ", ") + "}"
		}
		var result check.Type
		if len(sig.Results) == 1 {
			result = sig.Results[0]
		}
		fallible := len(rowElems(sig.Errors)) > 0
		answer, has := locals.alloc("f"), locals.alloc("has")
		call := answer + "(" + strings.Join(args, ", ") + ")"
		b.WriteString("func (" + self + " *" + name + ") " + memberName(op.Name) + "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, fallible) + " {\n")
		if pc := f.ProviderCalls(); pc != nil && pc.Wrapping {
			errTypes := check.ErrorRowTypes(sig.Errors)
			innerCallArgs := append([]string{task}, names...)
			if len(errTypes) == 0 {
				if result != nil {
					b.WriteString("\treturn " + self + ".v.inner." + memberName(op.Name) + "(" + strings.Join(innerCallArgs, ", ") + ")\n")
				} else {
					b.WriteString("\t" + self + ".v.inner." + memberName(op.Name) + "(" + strings.Join(innerCallArgs, ", ") + ")\n")
				}
				b.WriteString("}\n\n")
				continue
			}
			afc := newFuncCtx(l, locals, false)
			afc.typeCtx = *tc
			env := "&" + l.env() + "{" + envTask + ": " + task + "}"
			var wrapResT check.Type = check.Typ[check.Bool]
			if result != nil {
				wrapResT = result
			}
			goWrapRes := tc.goType(wrapResT)
			innerEnv := locals.alloc("ie")
			innerFwdArgs := append([]string{innerEnv + "." + envTask}, names...)
			innerFn := locals.alloc("innerFn")
			if result != nil {
				b.WriteString("\t" + innerFn + " := func(" + innerEnv + " *" + l.env() + ") (" + goWrapRes + ", error) {\n")
				b.WriteString("\t\treturn " + self + ".v.inner." + memberName(op.Name) + "(" + strings.Join(innerFwdArgs, ", ") + ")\n")
				b.WriteString("\t}\n")
			} else {
				b.WriteString("\t" + innerFn + " := func(" + innerEnv + " *" + l.env() + ") (bool, error) {\n")
				b.WriteString("\t\treturn true, " + self + ".v.inner." + memberName(op.Name) + "(" + strings.Join(innerFwdArgs, ", ") + ")\n")
				b.WriteString("\t}\n")
			}
			failEnv := locals.alloc("fe")
			failIdx := locals.alloc("idx")
			failZero := locals.alloc("zero")
			failFn := locals.alloc("failFn")
			b.WriteString("\t" + failFn + " := func(" + failEnv + " *" + l.env() + ", " + failIdx + " int) (" + goWrapRes + ", error) {\n")
			b.WriteString("\t\tvar " + failZero + " " + goWrapRes + "\n")
			b.WriteString("\t\tswitch " + failIdx + " {\n")
			drawCall := func(n uint64) string {
				return afc.providerCall(pc.Draw, pc, failEnv, []string{self + ".v." + memberName(op.Name), "uint64(" + strconv.FormatUint(n, 10) + ")"}, site)
			}
			for ei, et := range errTypes {
				b.WriteString("\t\tcase " + strconv.Itoa(ei) + ":\n")
				expr := afc.goFaultExpr(et, drawCall, site, 0)
				b.WriteString("\t\t\treturn " + failZero + ", " + expr + "\n")
			}
			b.WriteString("\t\tdefault:\n\t\t\treturn " + failZero + ", nil\n")
			b.WriteString("\t\t}\n\t}\n")
			wrapCall := afc.providerWrapCall(pc, env, self+".v."+memberName(op.Name), wrapResT, sig.Errors, innerFn, failFn, site)
			if result != nil {
				b.WriteString("\treturn " + wrapCall + "\n")
			} else {
				e := locals.alloc("err")
				b.WriteString("\t_, " + e + " := " + wrapCall + "\n\treturn " + e + "\n")
			}
			b.WriteString("}\n\n")
			continue
		}
		if pc := f.ProviderCalls(); pc != nil {
			// An Ergo field type: its Call records the arguments and
			// returns the answer as an Option (check/provider.go), on the
			// caller's task.
			afc := newFuncCtx(l, locals, false)
			afc.typeCtx = *tc
			env := "&" + l.env() + "{" + envTask + ": " + task + "}"
			o, w := locals.alloc("o"), locals.alloc("w")
			b.WriteString("\t" + o + " := " + afc.providerCall(pc.Call, pc, env, []string{self + ".v." + memberName(op.Name), record}, site) + "\n")
			b.WriteString("\tvar " + answer + " " + tc.goType(f.Type.(*check.Named).TArgs[1]) + "\n\t" + has + " := false\n")
			b.WriteString("\tif " + afc.optionSomeIf(pc.CallSig.Results[0], o, w, site) + " {\n\t\t" + answer + ", " + has + " = " + w + "." + memberName("Value") + ", true\n\t}\n")
		} else {
			b.WriteString("\t" + answer + ", " + has + " := " + self + ".v." + memberName(op.Name) + ".Call(" + task + ", " + record + ")\n")
		}
		noAnswer := l.rt() + ".NoAnswer[struct{}](" + scriptText + ", " + strconv.Quote(op.Name) + ")"
		switch {
		case result == nil && !fallible:
			// Only recorded when the test set no answer.
			b.WriteString("\tif " + has + " {\n\t\t_, _ = " + call + "\n\t}\n")
		case result == nil:
			b.WriteString("\tif !" + has + " {\n\t\t" + noAnswer + "\n\t}\n")
			e := locals.alloc("err")
			b.WriteString("\t_, " + e + " := " + call + "\n\treturn " + e + "\n")
		default:
			b.WriteString("\tif !" + has + " {\n\t\t" + noAnswer + "\n\t}\n")
			if fallible {
				b.WriteString("\treturn " + call + "\n")
			} else {
				// A function value of an empty error row returns a nil error.
				r := locals.alloc("r")
				b.WriteString("\t" + r + ", _ := " + call + "\n\treturn " + r + "\n")
			}
		}
		b.WriteString("}\n\n")
	}
	l.gen.WriteString(b.String())
	return "&" + name + "{v: " + val + "}"
}

// scriptSig is F of the field f, a testing.Op[A, F]: the operation's
// signature.
func scriptSig(f *check.Field, site syntax.Node) *check.Signature {
	op, ok := f.Type.(*check.Named)
	if !ok || len(op.TArgs) != 2 {
		ice(site, "the Script field %s is not a testing.Op", f.Name)
	}
	sig, ok := op.TArgs[1].(*check.Signature)
	if !ok {
		ice(site, "the Script field %s answers with %s, not a function", f.Name, op.TArgs[1])
	}
	return sig
}

// providerCall is the Go call of f, the NewF or Call of a provider field
// declared in Ergo, at the field's type arguments (pc.TypeArgs): env is the
// environment it runs in and args its Go arguments. It is the direct call
// funcValue's closure makes; the pair has no row (check/provider.go), so
// the call returns its one result.
func (fc *funcCtx) providerCall(f *check.Func, pc *check.ProviderCalls, env string, args []string, site syntax.Node) string {
	fd := fc.l.useFunc(f, site)
	if fd.fallible {
		ice(site, "the provider field function %s has an error row", f.Name)
	}
	inst := &check.Instance{Func: f, TypeArgs: pc.TypeArgs}
	lead := append(fd.envLead(env), fc.witnessArgs(f, inst, site)...)
	var head string
	switch {
	case fd.method != nil:
		head, args = args[0]+"."+fd.name, args[1:]
	default:
		head = fd.name + fc.typeArgs(f.Sig, inst.TypeArgs, site)
	}
	return head + "(" + strings.Join(append(lead, args...), ", ") + ")"
}

// providerWrapCall emits a call to a wrapping provider field's Wrap[A, G, T, E].
func (fc *funcCtx) providerWrapCall(pc *check.ProviderCalls, env, fieldExpr string, resT check.Type, errRow *check.Row, innerFn, failFn string, site syntax.Node) string {
	f := pc.Wrap
	fd := fc.l.useFunc(f, site)
	targs := []check.Type{pc.TypeArgs[0], pc.TypeArgs[1], resT, &check.RowArg{Row: errRow}}
	inst := &check.Instance{Func: f, TypeArgs: targs}
	lead := append(fd.envLead(env), fc.witnessArgs(f, inst, site)...)
	head := fd.name + fc.typeArgs(f.Sig, targs, site)
	args := []string{fieldExpr, innerFn, failFn}
	return head + "(" + strings.Join(append(lead, args...), ", ") + ")"
}

// goFaultExpr emits a Go expression that constructs a value of failure type t
// using draw(n) in the exact same draw sequence as eval's drawFaultValue.
func (fc *funcCtx) goFaultExpr(t check.Type, draw func(uint64) string, site syntax.Node, depth int) string {
	goT := fc.goType(t)
	switch u := underEqual(t).(type) {
	case *check.Basic:
		switch u.Kind {
		case check.Bool:
			return goT + "(" + draw(2) + " == 1)"
		case check.String:
			return goT + "(([]string{\"\", \"a\", \"err\"})[" + draw(3) + "])"
		case check.Float32, check.Float64:
			return goT + "(" + draw(3) + ")"
		case check.Int, check.Int8, check.Int16, check.Int32, check.Int64, check.Duration,
			check.Uint, check.Uint8, check.Uint16, check.Uint32, check.Uint64, check.Uintptr:
			return goT + "(" + draw(5) + ")"
		}
	case *check.Struct:
		nn := t.(*check.Named)
		td := fc.l.typeDecl(nn.Obj, site)
		if len(u.Fields) == 0 || depth >= 3 {
			return goT + "{}"
		}
		var parts []string
		for _, fld := range u.Fields {
			parts = append(parts, td.fieldName[fld.Name]+": "+fc.goFaultExpr(fld.Type, draw, site, depth+1))
		}
		return goT + "{" + strings.Join(parts, ", ") + "}"
	case *check.Union:
		if len(u.Variants) == 0 {
			return "func() (z " + goT + ") { return z }()"
		}
		if depth >= 3 || len(u.Variants) == 1 {
			v := fc.goFaultExpr(u.Variants[0], draw, site, depth+1)
			return fc.wrapUnionFault(t, u, 0, v)
		}
		var b strings.Builder
		b.WriteString("func() " + goT + " {\n\t\t\t\tswitch " + draw(uint64(len(u.Variants))) + " {\n")
		for i, vt := range u.Variants {
			v := fc.goFaultExpr(vt, draw, site, depth+1)
			b.WriteString("\t\t\t\tcase " + strconv.Itoa(i) + ":\n\t\t\t\t\treturn " + fc.wrapUnionFault(t, u, i, v) + "\n")
		}
		b.WriteString("\t\t\t\tdefault:\n\t\t\t\t\tvar z " + goT + "\n\t\t\t\t\treturn z\n\t\t\t\t}\n\t\t\t}()")
		return b.String()
	case *check.Slice:
		return goT + "{}"
	case *check.Map:
		return goT + "{}"
	case *check.Tuple:
		if len(u.Types) == 0 {
			return goT + "{}"
		}
		var parts []string
		for i, et := range u.Types {
			parts = append(parts, tupleField(i)+": "+fc.goFaultExpr(et, draw, site, depth+1))
		}
		return goT + "{" + strings.Join(parts, ", ") + "}"
	}
	return "func() (z " + goT + ") { return z }()"
}

func (fc *funcCtx) wrapUnionFault(t check.Type, u *check.Union, idx int, val string) string {
	if n, ok := t.(*check.Named); ok {
		if fu := fc.l.flatUnionOf(n); fu != nil {
			return fu.flatValue(&fc.typeCtx, fc.goType(t), idx, val)
		}
	}
	return val
}
