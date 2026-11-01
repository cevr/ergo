package lower

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Calls (Info.Calls). A declared function is called by its Go name, or as a
// Go method on its first argument, with the environment first when its
// capability row is not empty and its error last when its error row is not
// (spec D3); generic type arguments are written out from the call's
// TypeArgs, never left to Go's inference. A capability's operation is called
// on the provider the environment holds; an operation type's operation on
// the value. A function value has the uniform shape (typeCtx.funcType).

// call lowers a call in a value position: a result is bound to a
// temporary, so it is evaluated exactly here, in order.
func (fc *funcCtx) call(x *syntax.CallExpr) operand {
	c := fc.l.info.Calls[x]
	if c == nil {
		ice(x, "a call with no Calls entry")
	}
	switch c.Kind {
	case check.CallWith:
		return fc.with(x)
	case check.CallConversion:
		if len(x.Args) != 1 {
			ice(x, "a conversion with %d arguments", len(x.Args))
		}
		arg := fc.value(x.Args[0])
		target := fc.goType(c.Type)
		if isString(c.Type) && isInteger(fc.typeOf(x.Args[0])) {
			arg = "rune(" + arg + ")" // Go's string(int) is a rune conversion; vet asks for it spelled out
		}
		return operand{val: target + "(" + arg + ")"}
	case check.CallLen:
		if len(x.Args) != 1 {
			ice(x, "len with %d arguments", len(x.Args))
		}
		return operand{val: "len(" + fc.value(x.Args[0]) + ")"}
	}
	text, result, fallible := fc.callText(x, c)
	defer fc.identify(x, c)()
	if result && fc.noValue(x) {
		// The Go form returns struct{}, a no-value type argument's
		// (types.md, Inference): Wait on a Task whose T is no value, or a
		// generic function whose result T a block with no value bound.
		switch {
		case fallible && failsHere(c):
			e := fc.temp("err")
			fc.line("_, " + e + " := " + text)
			return operand{err: e}
		case fallible:
			fc.line("_, _ = " + text)
		default:
			fc.line("_ = " + text)
		}
		return operand{}
	}
	switch {
	case result && fallible:
		v, e := fc.temp("v"), fc.temp("err")
		fc.line(v + ", " + e + " := " + text)
		if !failsHere(c) {
			fc.line("_ = " + e)
			return operand{val: v}
		}
		return operand{val: v, err: e}
	case fallible:
		e := fc.temp("err")
		fc.line(e + " := " + text)
		if !failsHere(c) {
			fc.line("_ = " + e)
			return operand{}
		}
		return operand{err: e}
	case result:
		v := fc.temp("v")
		fc.line(v + " := " + text)
		return operand{val: v}
	}
	fc.line(text)
	return operand{}
}

// callStmt is a call whose results are discarded, as one Go statement. A
// call that cannot fail at this site drops its error, which is nil.
func (fc *funcCtx) callStmt(x *syntax.CallExpr) string {
	c := fc.l.info.Calls[x]
	text, _, fallible := fc.callText(x, c)
	if fallible && failsHere(c) {
		ice(x, "an untried fallible call in a statement")
	}
	fc.identify(x, c) // the caller writes text next, the statement's last line
	return text
}

// failsHere reports whether the call c can fail at its site: its error row
// there (Info's Fails, after substitution) is not empty. A call whose Go
// form returns an error where the row is empty drops it, which is nil: a
// declared row such as Task's E may be empty at the site (stage D3b round
// 1 review m4: one owner of the empty-row drop).
func failsHere(c *check.Call) bool { return len(rowElems(c.Fails)) > 0 }

// callText is a call's Go text, whether it returns a value, and whether its
// Go form returns an error.
func (fc *funcCtx) callText(x *syntax.CallExpr, c *check.Call) (string, bool, bool) {
	args := x.Args
	if x.Trailing != nil {
		args = append(append([]syntax.Expr{}, args...), nil) // the trailing block, lowered below
	}
	switch c.Kind {
	case check.CallFunc:
		fd := fc.l.useFunc(c.Func, x)
		if body, name, ok := fc.l.stdBody(fd); ok {
			if c.Recv != nil {
				ice(x, "a dot call to the std function %s", c.Func.Name)
			}
			return fc.stdCall(x, c, args, body, name)
		}
		list := args
		if c.Recv != nil {
			list = append([]syntax.Expr{c.Recv}, args...)
		}
		// A *T function called on an addressable T takes the receiver's
		// address where the receiver is evaluated, so a later argument's
		// spill keeps the place, never a copy (types.md, Methods 1 and 5).
		addr := false
		if c.Recv != nil && c.Func.Ptr {
			_, isPtr := fc.typeOf(c.Recv).(*check.Pointer)
			addr = !isPtr
		}
		vals := fc.callArgsAddr(list, x, addr)
		fc.noValueArgs(x, c, list, vals)
		var head string
		var rest []string
		if fd.method != nil {
			if len(vals) == 0 {
				ice(x, "a method call with no receiver")
			}
			head, rest = "("+vals[0]+")."+fd.name, vals[1:]
		} else {
			if len(c.TypeArgs) < len(c.Func.Sig.TParams) {
				// Info names fewer type arguments than the function has
				// (stage C round 4 review T1): an instance in parentheses,
				// (Fn[int])(1), which is a value. A dot call has them all,
				// its receiver's included (D-D8).
				if c.Recv != nil {
					ice(x, "a dot call to the generic function %s with %d of its %d type arguments", c.Func.Name, len(c.TypeArgs), len(c.Func.Sig.TParams))
				}
				if _, ok := unparen(x.Fun).(*syntax.IndexExpr); ok {
					refuse(x.Fun, "an explicit instantiation used as a value is lowered after check-v1 stage E")
				}
			}
			head, rest = fd.name+fc.typeArgs(c.Func.Sig, c.TypeArgs, x), vals
		}
		rest = append(fc.taskDestArgs(x, c, c.Func.Sig), rest...)
		return head + "(" + strings.Join(rest, ", ") + ")", len(c.Func.Sig.Results) > 0, fd.fallible
	case check.CallOp:
		if c.Recv == nil || c.Op == nil {
			ice(x, "an operation call with no receiver or operation")
		}
		return fc.opCallExpr(x, c, args)
	case check.CallProviderOp:
		sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
		if !ok || c.Op == nil {
			ice(x, "a capability call that is not Cap.op(…)")
		}
		cap, ok := fc.typeOf(sel.X).(*check.Named)
		if !ok {
			ice(sel.X, "a capability with no named type")
		}
		vals := fc.callArgs(args, x)
		// The interruption point (runtime.md, I1): after the operands, each
		// evaluated once and in order, and before the operation runs. A
		// capability call writes statements (emits), so no operand after it
		// runs before the poll.
		if fc.frame.polls {
			fc.poll(x)
			if fc.lastPos == x.Pos() {
				// The call's own Go line follows its directive (identify),
				// so the call table and a defect name it, not the poll.
				fc.lastPos = syntax.NoPos
			}
		}
		env := fc.envExpr(x)
		// A capability's operation takes the running task first (rows.md,
		// Rows 7).
		vals = append([]string{env + "." + envTask}, vals...)
		return env + "." + fc.l.envField(cap.Obj) + "." + memberName(c.Op.Name) + "(" + strings.Join(vals, ", ") + ")", len(c.Op.Sig.Results) > 0, len(rowElems(c.Op.Sig.Errors)) > 0
	case check.CallFuncValue:
		// A value of a declared function type (Seq[T]) calls through its
		// underlying signature.
		ft := fc.typeOf(x.Fun)
		if n, isNamed := ft.(*check.Named); isNamed {
			ft = n.Underlying()
		}
		sig, ok := ft.(*check.Signature)
		if !ok {
			ice(x.Fun, "a called value that is not a function")
		}
		vals := fc.callArgs(append([]syntax.Expr{x.Fun}, args...), x)
		return vals[0] + "(" + strings.Join(append(fc.taskDestArgs(x, c, sig), vals[1:]...), ", ") + ")", len(sig.Results) > 0, true
	}
	ice(x, "an unknown call kind %d", c.Kind)
	return "", false, false
}

// taskDestArgs are the leading arguments of a call of a declared function
// or a function value of signature sig: the environment, then, when the
// result holds a Task (Info's TaskResult), the frame scope as the
// destination the callee's return moves the Task to (stage D3b, Rule 2).
// A result that holds a Task only through a type argument or inside
// another type is stage E's.
func (fc *funcCtx) taskDestArgs(x *syntax.CallExpr, c *check.Call, sig *check.Signature) []string {
	out := []string{fc.envExpr(x)}
	if !c.TaskResult {
		return out
	}
	if !sig.ResultIsTask() {
		refuse(x, "a call whose result holds a Task through a type argument or inside another type is lowered after check-v1 stage E")
	}
	return append(out, fc.frameScope(x))
}

// callArgs lowers a call's operands left to right, with each argument's
// implicit conversion. A nil operand is the call's trailing block.
func (fc *funcCtx) callArgs(list []syntax.Expr, call *syntax.CallExpr) []string {
	return fc.callArgsAddr(list, call, false)
}

// callArgsAddr is callArgs; with addr, the first operand is a receiver
// place whose address is the operand.
func (fc *funcCtx) callArgsAddr(list []syntax.Expr, call *syntax.CallExpr, addr bool) []string {
	later := make([]bool, len(list)+1)
	for i := len(list) - 1; i >= 0; i-- {
		later[i] = later[i+1] || (list[i] != nil && fc.emits(list[i]))
	}
	out := make([]string, len(list))
	for i, x := range list {
		if x == nil {
			out[i] = fc.funcLit(nil, call.Trailing.List, call.Trailing)
			continue
		}
		switch {
		case i == 0 && addr:
			out[i] = fc.place(x, later[1])
		case i == 0 && x == call.Fun:
			out[i] = fc.value(x)
		default:
			out[i] = fc.assigned(x)
		}
		if later[i+1] && !fc.isConst(x) {
			out[i] = fc.spill(out[i])
		}
	}
	return out
}

// typeArgs writes a generic call's type-kinded arguments, from the call's
// Info: the row arguments are erased.
func (fc *funcCtx) typeArgs(sig *check.Signature, args []check.Type, site syntax.Node) string {
	var out []string
	for i, tp := range sig.TParams {
		if tp.Kind != check.KindType {
			continue
		}
		if i >= len(args) {
			ice(site, "a generic call with %d type arguments for %d parameters", len(args), len(sig.TParams))
		}
		out = append(out, fc.goType(args[i]))
	}
	if len(out) == 0 {
		return ""
	}
	return "[" + strings.Join(out, ", ") + "]"
}

// envExpr is the current environment, which every call receives: every Go
// function the lowering writes binds one (stage D3b, Rule 1).
func (fc *funcCtx) envExpr(site syntax.Node) string {
	if fc.env == "" {
		ice(site, "a call where no environment is bound")
	}
	return fc.env
}

// funcValue lowers a declared function used as a value: a closure of the
// uniform shape that calls it. A generic function's type arguments come
// from Go's inference on the closure's typed parameters, so every
// type-kinded parameter must appear in a parameter type.
func (fc *funcCtx) funcValue(f *check.Func, t check.Type, site syntax.Node) string {
	fd := fc.l.useFunc(f, site)
	sig, ok := t.(*check.Signature)
	if !ok {
		ice(site, "the function %s used as a value has no function type", f.Name)
	}
	for _, tp := range f.Sig.TParams {
		if tp.Kind == check.KindType && !paramsMention(f.Sig, tp) {
			refuse(site, "the generic function %s used as a value has a type parameter %s that no parameter names; its instantiation needs a check-v1 Info fact (lower-v1 request)", f.Name, tp.Name)
		}
	}
	return fc.envClosure(callerEnv, "env", site, func(env string) (params []string, results, body string) {
		lead := []string{env}
		if sig.ResultIsTask() {
			// The destination passes through to the function, whose return
			// moves the Task (stage D3b, Rule 2).
			if !f.Pkg.Info.TaskResults[f] || !f.Sig.ResultIsTask() {
				refuse(site, "the function %s used as a value returns a Task through a type argument, which is lowered after check-v1 stage E", f.Name)
			}
			dest := fc.scope.alloc("dest")
			params = append(params, dest+" *"+fc.l.rt()+".Scope")
			lead = append(lead, dest)
		} else if f.Pkg.Info.TaskResults[f] {
			refuse(site, "the function %s used as a value returns a Task inside another type, which is lowered after check-v1 stage E", f.Name)
		}
		var args []string
		for _, p := range sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			args = append(args, name)
		}
		var head string
		if fd.method != nil {
			head, args = args[0]+"."+fd.name, args[1:]
		} else {
			head = fd.name
		}
		args = append(lead, args...)
		call := head + "(" + strings.Join(args, ", ") + ")"
		var result check.Type
		if len(sig.Results) == 1 {
			result = sig.Results[0]
		}
		ind := strings.Repeat("\t", fc.depth+1)
		body = uniformBody(ind, call, len(f.Sig.Results) > 0, result != nil, fd.fallible)
		return params, fc.resultList(result, true), body
	})
}

// paramsMention reports whether a parameter type of sig names tp.
func paramsMention(sig *check.Signature, tp *check.TypeParam) bool {
	for _, p := range sig.Params {
		if walkType(p.Type, func(t check.Type, _ bool) bool { return t == tp }) {
			return true
		}
	}
	return false
}

// walkType reports whether visit holds for t or a type inside it; visit's
// second argument says the type is a function type's result.
func walkType(t check.Type, visit func(t check.Type, result bool) bool) bool {
	var walk func(t check.Type, result bool) bool
	walk = func(t check.Type, result bool) bool {
		if visit(t, result) {
			return true
		}
		switch t := t.(type) {
		case *check.Named:
			for _, a := range t.TArgs {
				if walk(a, false) {
					return true
				}
			}
		case *check.Slice:
			return walk(t.Elem, false)
		case *check.Map:
			return walk(t.Key, false) || walk(t.Elem, false)
		case *check.Pointer:
			return walk(t.Elem, false)
		case *check.Tuple:
			for _, e := range t.Types {
				if walk(e, false) {
					return true
				}
			}
		case *check.Signature:
			for _, p := range t.Params {
				if walk(p.Type, false) {
					return true
				}
			}
			for _, r := range t.Results {
				if walk(r, true) {
					return true
				}
			}
		}
		return false
	}
	return walk(t, false)
}

// noValueArgs adapts the arguments vals of the call x of a declared generic
// function whose type argument is no value: a type parameter T that a block
// with no value bound (types.md, Inference). Go instantiates T with
// struct{}, so a parameter func(…) T is a Go function that returns
// struct{}, while the argument, of the checked type with no result, returns
// only its error: the argument is bound once, here, and wrapped in a
// function that returns struct{}{} with its error. A result T is struct{},
// which the call site discards (funcCtx.call). T in any other function
// type's result, a parameter's or the result's, is refused: the checked
// type and Go's instance differ there, and adapting them is stage E's.
func (fc *funcCtx) noValueArgs(x *syntax.CallExpr, c *check.Call, list []syntax.Expr, vals []string) {
	sig := c.Func.Sig
	bound := map[*check.TypeParam]bool{}
	for i, tp := range sig.TParams {
		if i < len(c.TypeArgs) && tp.Kind == check.KindType && c.TypeArgs[i] == check.NoValue {
			bound[tp] = true
		}
	}
	if len(bound) == 0 {
		return
	}
	isBound := func(t check.Type) bool {
		tp, ok := t.(*check.TypeParam)
		return ok && bound[tp]
	}
	inResult := func(t check.Type, result bool) bool { return result && isBound(t) }
	stray := func(t check.Type) bool { return walkType(t, inResult) }
	refuseAt := func() {
		refuse(x, "a type parameter of %s that a block with no value binds, inside a function type other than a parameter's own (func(…) T), is lowered after check-v1 stage E", c.Func.Name)
	}
	for _, r := range sig.Results {
		if !isBound(r) && stray(r) {
			refuseAt()
		}
	}
	if len(vals) != len(sig.Params) {
		ice(x, "a call of %s with %d operands for %d parameters", c.Func.Name, len(vals), len(sig.Params))
	}
	for i, p := range sig.Params {
		ps, ok := p.Type.(*check.Signature)
		if !ok || len(ps.Results) != 1 || !isBound(ps.Results[0]) {
			if stray(p.Type) {
				refuseAt()
			}
			continue
		}
		for _, q := range ps.Params {
			if walkType(q.Type, func(t check.Type, _ bool) bool { return isBound(t) }) {
				refuseAt()
			}
		}
		// The wrapper's parameters are the argument's checked ones, which
		// are the instance's: T is not in them (stray above).
		as := fc.argSig(list[i], x)
		if len(as.Params) != len(ps.Params) || len(as.Results) != 0 {
			ice(x, "an argument for a parameter func(…) T with T no value has the type %s", as)
		}
		fv := fc.bindFunc(vals[i])
		ind := strings.Repeat("\t", fc.depth+1)
		vals[i] = fc.envClosure(callerEnv, "e", x, func(e string) (params []string, results, body string) {
			args := []string{e}
			for _, q := range as.Params {
				name := fc.scope.alloc("p")
				params = append(params, name+" "+fc.goType(q.Type))
				args = append(args, name)
			}
			return params, " (struct{}, error)", ind + "return struct{}{}, " + fv + "(" + strings.Join(args, ", ") + ")\n"
		})
	}
}

// with lowers with W { block } (rows.md, With): a Go block that copies the
// environment, builds the wire's providers into the copy and runs the block
// against it, with no closure (With 8). The block's tail is the with's
// value, and its untried failure the with's.
func (fc *funcCtx) with(x *syntax.CallExpr) operand {
	w := fc.l.info.Withs[x]
	if w == nil {
		ice(x, "a with with no Withs entry")
	}
	if len(x.Args) != 1 || x.Trailing == nil {
		ice(x, "a with without one wire and a block")
	}
	if len(w.Wires) == 0 {
		if _, inline := unparen(x.Args[0]).(*syntax.WireLit); inline {
			refuse(x.Args[0], "an inline wire value is lowered after check-v1 stage E (196x)")
		}
		refuse(x.Args[0], "a with of an imported wire is lowered after check-v1 stage B supplies its wire (check-v1 r5b m2)")
	}
	list := x.Trailing.List
	var res operand
	if !fc.noValue(x) {
		res.val = fc.temp("v")
		fc.line("var " + res.val + " " + fc.goType(fc.typeOf(x)))
	}
	if fc.fallibleExpr(x) {
		res.err = fc.temp("err")
		fc.line("var " + res.err + " error")
	}
	build := fc.l.wireBuilder(w.Wires[0], x)
	env := fc.temp("env")
	fc.line("{")
	fc.depth++
	fc.line(env + " := *" + fc.envExpr(x))
	fc.line(build + "(&" + env + ")")
	saved := fc.env
	fc.env = "(&" + env + ")"
	// The block yields by the one tail rule (D-C1, D-C2): its last
	// statement, through a final if, match or block, assigns the with's
	// value and passes its failure on.
	if res.val == "" && res.err == "" {
		fc.yieldList(list, dest{kind: destStmt})
	} else {
		fc.yieldList(list, dest{kind: destAssign, name: res.val, err: res.err})
	}
	fc.env = saved
	fc.depth--
	fc.line("}")
	return res
}

// wireBuilder is the Go function that builds a named wire's providers into
// an environment (rows.md, Wire 4: its own function, so its providers do not
// see the caller's locals). It builds each provider entry that still binds a
// capability once replace and embedding are resolved (Wire.Provider), once
// per build, so a shared entry is one value under each of its keys and an
// overridden provider never runs (Wire 7). An entry whose provider uses a
// capability of this wire is built after that capability's entry.
func (l *lowerer) wireBuilder(w *check.Wire, site syntax.Node) string {
	if name, ok := l.wires[w]; ok {
		return name
	}
	name := l.pkgName(pkgKey{"wire", w}, "build"+w.Name)
	l.wires[w] = name
	l.pending = append(l.pending, func() { l.emitWire(w, name, site) })
	return name
}

// wire lowers a named wire declaration: its builder.
func (l *lowerer) wire(w *check.Wire, site syntax.Node) {
	l.wireBuilder(w, site)
}

func (l *lowerer) emitWire(w *check.Wire, name string, site syntax.Node) {
	if w.Decl == nil || l.isImported(w) {
		refuse(site, "a with of an imported wire is lowered after check-v1 stage B supplies its wire (check-v1 r5b m2)")
	}
	if w.Decl.Provides != nil || w.Decl.Needs != nil {
		refuse(w.Decl, "a wire header is checked by #196 stage b")
	}
	// The entries, in source order through embeddings, each once.
	var entries []*check.WireEntry
	seen := map[*check.WireEntry]bool{}
	visited := map[*check.Wire]bool{}
	var walk func(w *check.Wire)
	walk = func(v *check.Wire) {
		if visited[v] {
			return
		}
		visited[v] = true
		if v.Decl == nil || l.isImported(v) {
			refuse(site, "an embedded imported wire is lowered after check-v1 stage B")
		}
		for _, e := range v.Entries {
			if e.Embedded != nil {
				walk(e.Embedded)
				continue
			}
			if !seen[e] {
				seen[e] = true
				entries = append(entries, e)
			}
		}
	}
	walk(w)
	type build struct {
		entry *check.WireEntry
		caps  []int // indexes into entry.Caps it still binds
		needs map[*check.TypeName]bool
	}
	bound := map[*check.TypeName]bool{}
	var builds []*build
	for _, e := range entries {
		b := &build{entry: e}
		for i, cap := range e.Caps {
			if w.Provider(cap) == e {
				b.caps = append(b.caps, i)
				if n, ok := cap.(*check.Named); ok {
					bound[n.Obj] = true
				}
			}
		}
		if len(b.caps) > 0 {
			builds = append(builds, b)
		}
	}
	for _, b := range builds {
		b.needs = l.providerNeeds(b.entry.Provider)
	}
	fc := newFuncCtx(l, newNamer(l.names), true)
	fc.owner = w.Name
	if w.Decl != nil {
		l.recordDecl(w.Name, w.Decl)
	}
	// A builder has no loop and calls no capability (a named wire is
	// closed); its frame reads the wire's footprint all the same.
	fc.frame = &frame{polls: framePolls(l.info, w.Decl)}
	fc.depth = 1
	fc.env = fc.scope.alloc("env")
	fc.entryPoll(l.info, w.Decl)
	built := map[*check.TypeName]bool{}
	done := make([]bool, len(builds))
	for range builds {
		next := -1
		for i, b := range builds {
			if done[i] {
				continue
			}
			ready := true
			for cap := range b.needs {
				if bound[cap] && !built[cap] {
					ready = false
					break
				}
			}
			if ready {
				next = i
				break
			}
		}
		if next < 0 {
			ice(w.Decl, "the wire %s has a provider cycle the checker did not report", w.Name)
		}
		done[next] = true
		b := builds[next]
		fc.at(b.entry.Provider)
		val := fc.value(b.entry.Provider)
		if len(b.caps) > 1 {
			val = fc.spill(val)
		}
		for _, i := range b.caps {
			cap, ok := b.entry.Caps[i].(*check.Named)
			if !ok {
				ice(b.entry.Provider, "a wire key that is not a named capability")
			}
			conv := check.Conversion{}
			if i < len(b.entry.Conversions) {
				conv = b.entry.Conversions[i]
			}
			fc.line(fc.env + "." + l.envField(cap.Obj) + " = " + fc.convert(conv, val, b.entry.Provider))
			built[cap.Obj] = true
		}
	}
	l.user.WriteString(l.directive(w.Decl.Pos()))
	l.user.WriteString("func " + name + "(" + fc.env + " *" + l.env() + ") {\n" + fc.out.String() + "}\n\n")
}

// isImported reports a wire declared outside the user package.
func (l *lowerer) isImported(w *check.Wire) bool {
	for _, d := range l.file.Decls {
		if d == syntax.Decl(w.Decl) {
			return false
		}
	}
	return true
}

// providerNeeds are the capabilities a provider expression charges where it
// is evaluated: the needs of its calls and its capability operations, not
// those inside a function literal, which charge their caller.
func (l *lowerer) providerNeeds(x syntax.Expr) map[*check.TypeName]bool {
	needs := map[*check.TypeName]bool{}
	syntax.Inspect(x, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CallExpr:
			c := l.info.Calls[n]
			if c == nil {
				return true
			}
			for _, t := range rowElems(c.Needs) {
				if named, ok := t.(*check.Named); ok {
					needs[named.Obj] = true
				}
			}
			if c.Kind == check.CallProviderOp {
				if sel, ok := unparen(n.Fun).(*syntax.SelectorExpr); ok {
					if named, ok := l.info.Types[sel.X].(*check.Named); ok {
						needs[named.Obj] = true
					}
				}
			}
		}
		return true
	})
	return needs
}

// operations converts val to the operation type of an operations
// conversion (Info.Implicit, Wire entries): an adapter struct whose methods
// call the table's functions. An operation takes the caller's task first
// (rows.md, Rows 7) and a function takes an environment, so no Go method of
// the source has the operation's Go signature, and the adapter is always
// written (stage D3b).
func (l *lowerer) operations(conv check.Conversion, val string, site syntax.Node) string {
	// A *T source (types.md, Methods 5) holds the pointer: a table function
	// on T reads *v, one on *T takes v (stage C round 4 review T1).
	from, ptrSource, ok := opsSource(conv.From)
	to, ok2 := conv.To.(*check.Named)
	if _, isAny := conv.To.(*check.Interface); isAny {
		// `any` waits for stage E as a type does (types.go): the corpus
		// guard met it (stage C round 7).
		refuse(site, "the type %s is lowered after check-v1 stage E (no any, constraints)", conv.To)
	}
	if !ok || !ok2 {
		refuse(site, "an operations conversion from %s to %s is not lowered yet", conv.From, conv.To)
	}
	if l.runtimeType(to, site) != "" {
		// A std type is the runtime's concrete Go type (stage D3b).
		refuse(site, "a value of the program converted to the std type %s is lowered after check-v1 stage E", conv.To)
	}
	for _, f := range conv.Table {
		if f != nil && f.Pkg != nil && f.Pkg.Info.TaskResults[f] {
			refuse(site, "an operation provided by %s, whose result holds a Task, is lowered after check-v1 stage E", f.Name)
		}
	}
	toDecl := l.typeDecl(to.Obj, site)
	if toDecl.kind != kindOps {
		ice(site, "an operations conversion to %s, which is not an operation type", conv.To)
	}
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	if mentionsTypeParam(conv.From) || mentionsTypeParam(conv.To) {
		refuse(site, "an operations conversion inside a generic function is lowered after check-v1 stage E (D-E1)")
	}
	fromGo := tc.goType(conv.From)
	tc.goType(conv.To) // the adapter implements it: the type is emitted
	// An instance of a generic operation type, A[int]: its operations'
	// signatures name A's type parameters, which stand for the instance's
	// arguments here (stage C round 4 review T1).
	if to.Orig != nil {
		for i, tp := range to.Orig.TParams {
			if i < len(to.TArgs) && tp.Kind == check.KindType {
				tc.params[tp] = tc.goType(to.TArgs[i])
			}
		}
	}
	for _, op := range toDecl.ops {
		f := conv.Table[op.Name]
		if f == nil {
			ice(site, "the operations conversion to %s has no function for %s", conv.To, op.Name)
		}
		// A provider's requirements are met where a wire builds it (rows.md,
		// Wire 4 and 6), never by the caller of its operation, so the
		// adapter's environment holds the task alone. The checker refuses a
		// table function that needs capabilities (S108, with #37).
		if len(rowElems(f.Sig.Uses)) > 0 {
			ice(site, "the operation %s of %s is provided by %s, which needs capabilities", op.Name, conv.To, f.Name)
		}
		l.useFunc(f, site)
	}
	key := adapterKey(conv)
	name, ok := l.adapters[key]
	if !ok {
		name = l.pkgName(pkgKey{"adapter", key}, "ops"+from.Obj.Name+"As"+to.Obj.Name)
		l.adapters[key] = name
		var b strings.Builder
		b.WriteString(fmt.Sprintf("// %s provides %s through the functions of %s (an operations conversion).\n", name, conv.To, conv.From))
		b.WriteString("type " + name + " struct{ v " + fromGo + " }\n\n")
		for _, op := range toDecl.ops {
			f := conv.Table[op.Name]
			fd := l.funcDecl(f)
			locals := newNamer(l.names) // so no local hides the function it calls
			self := locals.alloc("a")
			// The operation takes the caller's task first (rows.md, Rows 7);
			// the table function runs in an environment of that task alone.
			task := locals.alloc("t")
			params := []string{task + " *" + l.taskPkg() + ".Task"}
			args := []string{"&" + l.env() + "{" + envTask + ": " + task + "}"}
			for _, p := range op.Sig.Params {
				n := locals.alloc("p")
				params = append(params, n+" "+tc.goType(p.Type))
				args = append(args, n)
			}
			recv := self + ".v"
			switch {
			case fd.ptr && !ptrSource:
				recv = "&" + recv
			case !fd.ptr && ptrSource:
				recv = "*" + recv
			}
			var call string
			if fd.method != nil {
				call = "(" + recv + ")." + fd.name + "(" + strings.Join(args, ", ") + ")"
			} else {
				call = fd.name + "(" + strings.Join(append([]string{args[0], recv}, args[1:]...), ", ") + ")"
			}
			var result check.Type
			if len(op.Sig.Results) == 1 {
				result = op.Sig.Results[0]
			}
			opFallible := len(rowElems(op.Sig.Errors)) > 0
			b.WriteString("func (" + self + " *" + name + ") " + memberName(op.Name) + "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, opFallible) + " {\n")
			switch {
			case fd.fallible == opFallible && (result != nil || opFallible):
				b.WriteString("\treturn " + call + "\n")
			case result != nil && opFallible:
				b.WriteString("\treturn " + call + ", nil\n")
			case opFallible:
				b.WriteString("\t" + call + "\n\treturn nil\n")
			case fd.fallible:
				ice(site, "the provider function %s fails and the operation %s cannot", f.Name, op.Name)
			default:
				b.WriteString("\t" + call + "\n")
			}
			b.WriteString("}\n\n")
		}
		l.gen.WriteString(b.String())
	}
	return "&" + name + "{v: " + val + "}"
}

// mentionsTypeParam reports a type that names a type parameter.
func mentionsTypeParam(t check.Type) bool {
	switch t := t.(type) {
	case *check.TypeParam:
		return true
	case *check.Named:
		for _, a := range t.TArgs {
			if mentionsTypeParam(a) {
				return true
			}
		}
	case *check.Slice:
		return mentionsTypeParam(t.Elem)
	case *check.Map:
		return mentionsTypeParam(t.Key) || mentionsTypeParam(t.Elem)
	case *check.Pointer:
		return mentionsTypeParam(t.Elem)
	}
	return false
}

func isString(t check.Type) bool {
	b, ok := t.(*check.Basic)
	return ok && b.Kind == check.String
}

// Call identities (#230). Every call of the program's source, to a function,
// an operation or a function value, keeps its Ergo identity: the source
// declaration that makes it, what it calls and its position. The program
// lists them in its call table, ergoCalls, and the Go line that makes the
// call stands directly after the //line directive of its own position, so a
// Go stack and the table name one site: a defect's report reads its Ergo
// call chain from them (v1.RegisterCalls). Only user code has identities:
// library bodies carry no directives.

// identify records x's identity when it has one and writes the directive
// of its position, unless the current directive already names it. The Go
// call line comes next. The function it returns writes the directive that
// was current before, so the rest of the statement maps back to it; call
// it after the call line.
func (fc *funcCtx) identify(x *syntax.CallExpr, c *check.Call) func() {
	none := func() {}
	if !fc.directives || !x.Pos().IsValid() {
		return none
	}
	fc.l.recordCall(x, fc.owner, fc.callee(x, c))
	if x.Pos() == fc.lastPos {
		return none
	}
	prev, site := fc.lastPos, fc.site
	fc.at(x)
	return func() {
		if prev.IsValid() {
			fc.out.WriteString(fc.l.directive(prev))
			fc.lastPos = prev
		}
		fc.site = site
	}
}

// callee is the Ergo name of what x calls: a function, qualified by its
// package outside the user's and the prelude, a same-named function by its
// receiver's type, an operation by its operations or capability type, and
// a function value by the name it is called through.
func (fc *funcCtx) callee(x *syntax.CallExpr, c *check.Call) string {
	typeName := func(t check.Type) string {
		if p, ok := t.(*check.Pointer); ok {
			t = p.Elem
		}
		if n, ok := t.(*check.Named); ok {
			return n.Obj.Name
		}
		return t.String()
	}
	switch c.Kind {
	case check.CallFunc:
		if c.Recv != nil {
			return typeName(fc.typeOf(c.Recv)) + "." + c.Func.Name
		}
		if p := c.Func.Pkg; p != nil && p != fc.l.pkg && p.Path != "prelude" {
			return p.Path + "." + c.Func.Name
		}
		return c.Func.Name
	case check.CallOp:
		return typeName(fc.typeOf(c.Recv)) + "." + c.Op.Name
	case check.CallProviderOp:
		if sel, ok := unparen(x.Fun).(*syntax.SelectorExpr); ok {
			return typeName(fc.typeOf(sel.X)) + "." + c.Op.Name
		}
	case check.CallFuncValue:
		switch f := unparen(x.Fun).(type) {
		case *syntax.Ident:
			return f.Name
		case *syntax.SelectorExpr:
			return f.Sel.Name
		}
		return "a function value"
	}
	return "a call"
}

// callIdent is one entry of the call table.
type callIdent struct {
	offset       int
	fn, callee   string
	line, column int
}

// recordCall adds x's identity to the call table once: a body lowered
// again (an instance) does not repeat it.
func (l *lowerer) recordCall(x *syntax.CallExpr, owner, callee string) {
	if l.callSeen[x] {
		return
	}
	if l.callSeen == nil {
		l.callSeen = map[*syntax.CallExpr]bool{}
	}
	l.callSeen[x] = true
	line, column := l.file.Position(x.Pos())
	l.calls = append(l.calls, callIdent{offset: int(x.Pos()), fn: owner, callee: callee, line: line, column: column})
}

// declIdent is one entry of the declaration table: a source declaration's
// name (the Func of its calls) and its first and last lines.
type declIdent struct {
	offset    int
	name      string
	line, end int
}

// recordDecl adds the source declaration n, named name, to the
// declaration table once.
func (l *lowerer) recordDecl(name string, n syntax.Node) {
	if l.declSeen[n] {
		return
	}
	if l.declSeen == nil {
		l.declSeen = map[syntax.Node]bool{}
	}
	l.declSeen[n] = true
	line, _ := l.file.Position(n.Pos())
	end, _ := l.file.Position(n.End())
	l.decls = append(l.decls, declIdent{offset: int(n.Pos()), name: name, line: line, end: end})
}

// callTable is the Go declaration of the program's source map and the init
// function that registers it with the runtime (v1.RegisterCalls), written
// for every program, its tables empty or not (stage C round 2 review m1):
// the declaration table, each source declaration's name and lines, and the
// call table, in source order. A call's Func is the source declaration that
// makes it, Callee what it calls, and File, Line and Column its position,
// which the //line directive before the call also names. The entry types
// are aliases of the structs v1.RegisterCalls takes; registered, the tables
// name the Ergo frames of a defect in its report and no Go frame
// (runtime.md, Exit causes). It is written after the entry, so it may name
// the runtime package.
func (l *lowerer) callTable() string {
	sort.SliceStable(l.decls, func(i, j int) bool { return l.decls[i].offset < l.decls[j].offset })
	sort.SliceStable(l.calls, func(i, j int) bool { return l.calls[i].offset < l.calls[j].offset })
	declEntry := l.pkgName(pkgKey{"decl", nil}, "ergoDecl")
	declTable := l.pkgName(pkgKey{"decls", nil}, "ergoDecls")
	entry := l.pkgName(pkgKey{"call", nil}, "ergoCall")
	table := l.pkgName(pkgKey{"calls", nil}, "ergoCalls")
	file := strconv.Quote(l.src)
	var b strings.Builder
	b.WriteString("// " + declEntry + " is one source declaration and its lines (#230).\n")
	b.WriteString("type " + declEntry + " = struct {\n\tName      string\n\tFile      string\n\tLine, End int\n}\n\n")
	b.WriteString("// " + declTable + " lists the program's source declarations, in source order.\n")
	b.WriteString("var " + declTable + " = [...]" + declEntry + "{\n")
	for _, d := range l.decls {
		fmt.Fprintf(&b, "\t{Name: %s, File: %s, Line: %d, End: %d},\n", strconv.Quote(d.name), file, d.line, d.end)
	}
	b.WriteString("}\n\n")
	b.WriteString("// " + entry + " is the Ergo identity of one lowered call (#230).\n")
	b.WriteString("type " + entry + " = struct {\n\tFunc, Callee string\n\tFile         string\n\tLine, Column int\n}\n\n")
	b.WriteString("// " + table + " lists the identity of each call of the program, in source order.\n")
	b.WriteString("var " + table + " = [...]" + entry + "{\n")
	for _, c := range l.calls {
		fmt.Fprintf(&b, "\t{Func: %s, Callee: %s, File: %s, Line: %d, Column: %d},\n", strconv.Quote(c.fn), strconv.Quote(c.callee), file, c.line, c.column)
	}
	b.WriteString("}\n\n")
	b.WriteString("func init() {\n\t" + l.rt() + ".RegisterCalls(" + declTable + "[:], " + table + "[:])\n}\n\n")
	return b.String()
}
