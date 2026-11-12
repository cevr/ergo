package lower

import (
	"regexp"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// sealedOperations lowers the operations conversion of a sealed std
// protocol's value (check.SealedStd) into another operation type of the
// prelude, std or the program. The source is the runtime's own Go type
// (std.go runtimeType), whose methods have the runtime's Go shapes and
// hidden arguments, not those of the target's Go interface (a Seq result
// is Go's iter.Seq, an Option is comma-ok), so no Go conversion makes the
// one the other. The adapter holds the runtime value; each of its methods
// makes the std operation's call, as a call in the program does (opCall),
// on an environment of the caller's task alone, and returns the target's
// Go form. Every pair the checker's member match admits lowers here
// (TestSealedStdConvertsToItsOperationTypes).
//
// Prior art: Go's own interface conversion needs identical method
// signatures; Gleam and ReScript keep host values behind a wrapper record
// of functions when the host's shapes differ from the language's.
func (fc *funcCtx) sealedOperations(conv check.Conversion, from *check.Named, val string, site syntax.Node) string {
	l := fc.l
	to, ok := conv.To.(*check.Named)
	if !ok {
		ice(site, "a sealed std value converted to %s, which is not a named operation type", conv.To)
	}
	toDecl := l.typeDecl(to.Obj, site)
	if toDecl.kind != kindOps {
		ice(site, "an operations conversion to %s, which is not an operation type", conv.To)
	}
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	free := convParams(conv)
	adapterScope := newNamer(l.names)
	var tpDecl, tpArgs, own []string
	for _, tp := range free {
		n := adapterScope.alloc(tp.Name)
		tc.params[tp] = n
		tpDecl = append(tpDecl, n+" "+l.constraint(tp, site))
		tpArgs = append(tpArgs, fc.goType(tp))
		own = append(own, n)
	}
	declParams, instArgs, recvArgs := "", "", ""
	if len(free) > 0 {
		declParams = "[" + strings.Join(tpDecl, ", ") + "]"
		instArgs = "[" + strings.Join(tpArgs, ", ") + "]"
		recvArgs = "[" + strings.Join(own, ", ") + "]"
	}
	fromGo := tc.goType(conv.From)
	tc.goType(conv.To) // the adapter implements it: the type is emitted
	if to.Orig != nil {
		for i, tp := range to.Orig.TParams {
			if i < len(to.TArgs) && tp.Kind == check.KindType {
				tc.params[tp] = tc.goType(to.TArgs[i])
			}
		}
	}
	key, base, _ := l.adapterName(conv)
	name, done := l.adapters[key]
	if !done {
		name = l.pkgName(pkgKey{"adapter", key}, base)
		l.adapters[key] = name
		var b strings.Builder
		b.WriteString("// " + name + " provides " + conv.To.String() + " through the std operations of " + conv.From.String() + " (an operations conversion of a sealed std protocol).\n")
		b.WriteString("type " + name + declParams + " struct{ v " + fromGo + " }\n\n")
		for _, op := range toDecl.ops {
			b.WriteString(fc.sealedMethod(tc, name+recvArgs, from, conv.From, op, site))
		}
		l.gen.WriteString(b.String())
	}
	return "&" + name + instArgs + "{v: " + val + "}"
}

// sealedMethod is the Go method of the adapter recv for the target
// operation op: the std operation of that name on the runtime value,
// called through opCall in a function context of its own.
func (fc *funcCtx) sealedMethod(tc *typeCtx, recv string, from *check.Named, fromType check.Type, op *check.Method, site syntax.Node) string {
	mc := newFuncCtx(fc.l, newNamer(fc.l.names), false)
	mc.typeCtx = *tc
	self, task, env := mc.scope.alloc("a"), mc.scope.alloc("t"), mc.scope.alloc("e")
	params := []string{task + " *" + fc.l.taskPkg() + ".Task"}
	vals := []string{self + ".v"}
	for _, p := range op.Sig.Params {
		n := mc.scope.alloc("p")
		params = append(params, n+" "+tc.goType(p.Type))
		vals = append(vals, n)
	}
	var result check.Type
	if len(op.Sig.Results) == 1 {
		result = op.Sig.Results[0]
	}
	opFallible := len(rowElems(op.Sig.Errors)) > 0
	mc.depth = 1
	mc.env = env
	// The std operation's call, as opCallExpr makes it for a call in the
	// program: a function argument takes the runtime's Go shape (adaptFunc).
	std := opBody(from, op.Name, len(op.Sig.Params), site)
	for i, p := range std.Params {
		if p.Func == nil {
			continue
		}
		pt := op.Sig.Params[i].Type
		if n, ok := pt.(*check.Named); ok {
			pt = n.Underlying()
		}
		sig, ok := pt.(*check.Signature)
		if !ok {
			ice(site, "the std operation %s.%s takes a function where %s takes %s", from.Obj.Name, op.Name, fromType, op.Sig.Params[i].Type)
		}
		vals[i+1] = mc.adaptFunc(vals[i+1], p.Func, sig, site)
	}
	mc.bindDeferred(std, vals)
	call := mc.stdResult(std, result, env, site, func(env string) string {
		return mc.runtimeCall(from.Obj.Name+"."+op.Name, std, "", vals[0], vals[1:], env, nil, nil, site)
	})
	goVal, goErr := goValue(std), goFallible(std)
	v, err := "_", "_"
	var rets []string
	if result != nil {
		if !goVal {
			ice(site, "the std operation %s.%s has no Go value for its result %s", from.Obj.Name, op.Name, result)
		}
		v = mc.scope.alloc("v")
		rets = append(rets, v)
	}
	if opFallible {
		if goErr {
			err = mc.scope.alloc("err")
			rets = append(rets, err)
		} else {
			rets = append(rets, "nil")
		}
	}
	var lhs []string
	if goVal {
		lhs = append(lhs, v)
	}
	if goErr {
		lhs = append(lhs, err)
	}
	switch {
	case len(lhs) == 0:
		mc.line(call)
	case strings.Trim(strings.Join(lhs, ""), "_") == "":
		mc.line(strings.Join(lhs, ", ") + " = " + call)
	default:
		mc.line(strings.Join(lhs, ", ") + " := " + call)
	}
	if len(rets) > 0 {
		mc.line("return " + strings.Join(rets, ", "))
	}
	body := mc.out.String()
	if regexp.MustCompile(`\b` + env + `\b`).MatchString(body) {
		// The environment of the caller's task, where the call reads it.
		body = "\t" + env + " := &" + fc.l.env() + "{" + envTask + ": " + task + "}\n" + body
	}
	return "func (" + self + " *" + recv + ") " + memberName(op.Name) + "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, opFallible) + " {\n" + body + "}\n\n"
}
