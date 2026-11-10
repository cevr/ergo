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
// per call (one lock, one append, one call).

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
func (l *lowerer) reserveScripts(reserve func(key pkgKey, base string)) {
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
	for _, p := range l.sourcedPkgs() {
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
// new Op with no answer and no calls.
func (fc *funcCtx) scriptLit(n *check.Named, site syntax.Node) string {
	var parts []string
	for _, f := range scriptFields(n, site) {
		op, ok := f.Type.(*check.Named)
		if !ok || len(op.TArgs) != 2 {
			ice(site, "the field %s of %s is not a testing.Op", f.Name, n)
		}
		parts = append(parts, memberName(f.Name)+": "+fc.l.rt()+".NewOp["+fc.goType(op.TArgs[0])+", "+fc.goType(op.TArgs[1])+"]()")
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
		answer, has := locals.alloc("f"), locals.alloc("ok")
		call := answer + "(" + strings.Join(args, ", ") + ")"
		b.WriteString("func (" + self + " *" + name + ") " + memberName(op.Name) + "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, fallible) + " {\n")
		b.WriteString("\t" + answer + ", " + has + " := " + self + ".v." + memberName(op.Name) + ".Call(" + record + ")\n")
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
