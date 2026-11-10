package lower

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
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
		return fc.withExpr(x)
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
	case check.CallGoError:
		return fc.goErrorCall(x, c) // gocall.go
	case check.CallEncode, check.CallDecode:
		return fc.wireCall(x, c)
	}
	if check.IsUnreachable(c) {
		return fc.unreachableValue(x)
	}
	if goImportCall(c, fc.typeOf) {
		return fc.goCall(x, c) // gocall.go
	}
	if goExternCall(c) {
		return fc.goExtern(x, c) // gocall.go, Level 2 adapters
	}
	text, result, fallible := fc.callText(x, c)
	defer fc.identify(x, c)()
	if hostCalled(c) {
		v := ""
		if result {
			v = fc.temp("v")
		}
		if fallible {
			e := fc.temp("err")
			fc.hostFallible(x, c, v, e, text)
			if !failsHere(c) {
				ice(x, "a user extern that fails go.Error with an empty row at its call")
			}
			return operand{val: v, err: e}
		}
		fc.line(fc.hostStmt(x, c, v, text))
		return operand{val: v}
	}
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
	if goImportCall(c, fc.typeOf) || goExternCall(c) {
		// A Go call writes its own statements (goCall, goExtern); a
		// discarded value is used once so Go accepts it.
		op := operand{}
		if goExternCall(c) {
			op = fc.goExtern(x, c)
		} else {
			op = fc.goCall(x, c)
		}
		if op.err != "" {
			ice(x, "an untried fallible call in a statement")
		} else if op.val != "" {
			return "_ = " + op.val
		}
		return ""
	}
	if c.Kind == check.CallEncode || c.Kind == check.CallDecode {
		// A codec call writes its own statements (wire.go).
		if op := fc.wireCall(x, c); op.err != "" {
			ice(x, "an untried fallible call in a statement")
		} else if op.val != "" {
			return "_ = " + op.val
		}
		return ""
	}
	fc.stmtCall = x // a fused call's value is a name, no statement (update.go)
	text, _, fallible := fc.callText(x, c)
	fc.stmtCall = nil
	if fallible && failsHere(c) {
		ice(x, "an untried fallible call in a statement")
	}
	fc.identify(x, c) // the caller writes text next, the statement's last line
	if hostCalled(c) {
		return fc.hostStmt(x, c, "_", text)
	}
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
		if text, ok := fc.updateFused(x, c); ok {
			return text, true, false
		}
		if text, ok := fc.machineRun(x, c); ok {
			// A run of a machine declaration's value: its flat run
			// (machinerun.go), which returns what std's Run returns.
			return text, true, true
		}
		fd := fc.l.useFunc(c.Func, x)
		if userExtern(c.Func) {
			return fc.hostCall(x, c, args), len(c.Func.Sig.Results) > 0, c.Func.Sig.Fallible
		}
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
		// An argument for a parameter func(…) T whose T a block with no
		// value binds needs no adapter: a function value with no result
		// and the instance's func(…) struct{} are one Go shape (C11).
		vals := fc.callArgsAddr(list, x, addr)
		vals = fc.valueSource(x, c, vals)
		var head string
		var rest []string
		if fd.method != nil {
			if len(vals) == 0 {
				ice(x, "a method call with no receiver")
			}
			head, rest = "("+vals[0]+")."+fd.name, vals[1:]
		} else {
			// Info names every type argument of a generic call: written,
			// inferred, bound by a dot call's receiver (D-D8) or by an
			// instance in parentheses, (Fn[int])(1) (D-E1, Info.Instances).
			name := fd.name
			if fc.l.vals != nil && fc.l.vals.ownCall[x] {
				// The body that takes its owned arguments as they are
				// passed: this call passes them unshared (values.go, V5).
				name = fc.l.pkgName(ownedKey(c.Func), c.Func.Name+"Owned")
			}
			head, rest = name+fc.typeArgs(c.Func.Sig, c.TypeArgs, x), vals
		}
		// A pure function takes no environment (pure.go): no Task result
		// and no witness either, so its call passes its operands alone.
		lead := fc.witnessArgs(c.Func, fc.l.info.Instances[x], x)
		if !fd.pure {
			lead = append(fc.taskDestArgs(x, c, c.Func.Sig), lead...)
		}
		rest = append(lead, rest...)
		return head + "(" + strings.Join(rest, ", ") + ")", len(c.Func.Sig.Results) > 0, fd.fallible
	case check.CallOp:
		if c.Recv == nil || c.Op == nil {
			ice(x, "an operation call with no receiver or operation")
		}
		return fc.opCallExpr(x, c, args)
	case check.CallProviderOp, check.CallLawOp:
		var cap *check.Named
		if c.Kind == check.CallLawOp {
			// A bare operation call in a member law calls the provider
			// under test, which the law's run binds to its own operation
			// type's capability (tests.go, Laws).
			if fc.l.tests == nil || fc.l.tests.law == nil || fc.l.tests.law.Owner == nil || c.Op == nil {
				ice(x, "an operation call of a law outside the law's test-build body")
			}
			own, ok := fc.l.tests.law.Owner.Type.(*check.Named)
			if !ok {
				ice(x, "a law whose operation type is not named")
			}
			cap = own
		} else {
			sel, ok := unparen(x.Fun).(*syntax.SelectorExpr)
			if !ok || c.Op == nil {
				ice(x, "a capability call that is not Cap.op(…)")
			}
			named, ok := fc.typeOf(sel.X).(*check.Named)
			if !ok {
				ice(sel.X, "a capability with no named type")
			}
			cap = named
		}
		// The interruption point (runtime.md, I1): before the operands, so
		// no poll separates an operand's delivered value (a Pop's, a Wait's,
		// any call's result) from the operation that receives it. The
		// operands then run once and in order. A checked Close starts a
		// release instead (entersRelease).
		if fc.frame.polls && !fc.entersRelease(x) {
			fc.poll(x)
			if fc.lastPos == x.Pos() {
				// The call's own Go line follows its directive (identify),
				// so the call table and a defect name it, not the poll.
				fc.lastPos = syntax.NoPos
			}
		}
		vals := fc.callArgs(args, x)
		env := fc.envExpr(x)
		if recv := fc.l.tests.lawOwnOf(c); recv != "" {
			// A law of a generic operation type calls the provider it
			// takes (lawDecl), as a call on a value of the type does.
			return fc.opCall(cap, c.Op, fc.typeOf(x), append([]string{recv}, vals...), env, fc.opDest(x, c), x, c, x)
		}
		// A capability's operation takes the running task first (rows.md,
		// Rows 7), then a returned Task's destination (methodSig).
		lead := []string{taskOf(env)}
		if dest := fc.opDest(x, c); dest != "" {
			lead = append(lead, dest)
		}
		vals = append(lead, vals...)
		return env + "." + fc.l.envField(cap) + "." + memberName(c.Op.Name) + "(" + strings.Join(vals, ", ") + ")", len(c.Op.Sig.Results) > 0, len(rowElems(c.Op.Sig.Errors)) > 0
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
		// The uniform shape returns a Go value even with no result: struct{}
		// (typeCtx.funcType), which call discards.
		return vals[0] + "(" + strings.Join(append(fc.taskDestArgs(x, c, sig), vals[1:]...), ", ") + ")", true, true
	}
	ice(x, "an unknown call kind %d", c.Kind)
	return "", false, false
}

// userExtern reports an extern declared outside std (C26, 196x): the
// checker admits one only with a signature of basic types, no needs row,
// no fails row but go.Error (G4), no type parameters and at most one
// result, and with a target in
// Go's standard library (check/extern.go).
func userExtern(f *check.Func) bool {
	return f != nil && f.Decl != nil && f.Decl.Extern != nil && f.Decl.Extern.Target != nil && !f.Pkg.Std() && !f.Pkg.Host()
}

// hostCall is the Go text of the call x of the user extern c.Func: a direct
// call of its Go target, with no environment, no wrapper and no poll (a
// host call is not an interruption point; runtime.md). The extern is never
// a value (P3), so this is its only lowering. Go checks the call against
// the Ergo signature: every argument is already typed (a literal lowers
// as T(lit), 196u: no untyped constants), and hostStmt binds the result
// at its Ergo result's Go type. A target whose signature is not assignable
// is go build's error, at the call's //line (D-E5).
func (fc *funcCtx) hostCall(x *syntax.CallExpr, c *check.Call, args []syntax.Expr) string {
	f := c.Func
	if c.Recv != nil || len(f.Sig.TParams) > 0 || len(args) != len(f.Sig.Params) || len(f.Sig.Results) > 1 {
		ice(x, "a call of the user extern %s outside its checked domain", f.Name)
	}
	vals := fc.callArgs(args, x)
	for i, p := range f.Sig.Params {
		if cb, ok := p.Type.(*check.Signature); ok {
			// The adapter is bound once, so hostStmt's check and the call
			// share it rather than each writing the literal.
			vals[i] = fc.spill(fc.hostCallback(fc.bindFunc(vals[i]), cb, x))
		}
	}
	return fc.hostTarget(x, f) + "(" + strings.Join(vals, ", ") + ")"
}

// hostTarget is the Go expression that names the target of the user
// extern f: the imported package's name and the target's name.
func (fc *funcCtx) hostTarget(site syntax.Node, f *check.Func) string {
	path, name := f.Decl.Extern.Split()
	return fc.l.hostPkg(path, site) + "." + name
}

// hostCallback adapts fv, an Ergo function value of the uniform shape
// (typeCtx.funcType), to the plain Go function a user extern's callback
// parameter takes (G1; check.externCallback): func(params) result, with
// basic types only. The adapter calls fv with the environment of the code
// that calls the extern, captured here, so the callback runs on its
// caller's task: its interruption points poll that task, and a stop
// unwinds through the Go target as a panic does. The callback type has no
// rows, so fv returns no failure, and the adapter drops its error, which
// is nil. The caller binds fv once (bindFunc, or a parameter's name), so
// the adapter calls the value as it was where the call was made.
func (fc *funcCtx) hostCallback(fv string, sig *check.Signature, site syntax.Node) string {
	if len(sig.Results) > 1 || len(rowElems(sig.Errors)) != 0 || len(rowElems(sig.Uses)) != 0 {
		ice(site, "a user extern callback outside its checked domain")
	}
	return fc.envClosure(capturedEnv, "e", site, func(env string) (params []string, results, body string) {
		args := []string{env}
		for _, p := range sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			args = append(args, name)
		}
		ind := strings.Repeat("\t", fc.depth+1)
		call := fv + "(" + strings.Join(args, ", ") + ")"
		if len(sig.Results) == 0 {
			return params, "", ind + "_, _ = " + call + "\n"
		}
		r := fc.scope.alloc("r")
		return params, " " + fc.goType(sig.Results[0]), ind + r + ", _ := " + call + "\n" + ind + "return " + r + "\n"
	})
}

// hostValue lowers the user extern f used as a value (G1): a literal of the
// uniform shape (typeCtx.funcType) that calls the Go target, as hostCall
// and hostStmt write a direct call: the `if false { defer … }` check that
// the target is called, and the result bound at its Ergo type. A callback
// parameter is adapted with the environment the value is called with.
func (fc *funcCtx) hostValue(f *check.Func, sig *check.Signature, site syntax.Node) string {
	if len(sig.TParams) > 0 || len(sig.Results) > 1 || sig.ResultIsTask() {
		ice(site, "the user extern %s used as a value outside its checked domain", f.Name)
	}
	callbacks := false
	for _, p := range sig.Params {
		if _, ok := p.Type.(*check.Signature); ok {
			callbacks = true
		}
	}
	envParam := "_"
	if callbacks {
		envParam = "e"
	}
	return fc.envClosure(callerEnv, envParam, site, func(env string) (params []string, results, body string) {
		fc.depth++
		defer func() { fc.depth-- }()
		var args []string
		for _, p := range sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			if cb, ok := p.Type.(*check.Signature); ok {
				name = fc.hostCallback(name, cb, site)
			}
			args = append(args, name)
		}
		ind := strings.Repeat("\t", fc.depth)
		text := fc.hostTarget(site, f) + "(" + strings.Join(args, ", ") + ")"
		body = ind + "if false { defer " + text + " }\n"
		if sig.Fallible {
			// A target that fails go.Error (G4): hostFallible's binding,
			// with the error returned as the value's failure.
			e := fc.scope.alloc("err")
			body += ind + "var " + e + " error\n"
			if len(sig.Results) == 0 {
				return params, fc.valueResults(nil), body + ind + e + " = " + text + "\n" + ind + "return struct{}{}, " + fc.l.goErrorOf() + "(" + e + ")\n"
			}
			r := fc.scope.alloc("r")
			body += ind + "var " + r + " " + fc.goType(sig.Results[0]) + "\n" + ind + r + ", " + e + " = " + text + "\n"
			return params, fc.valueResults(sig.Results[0]), body + ind + "return " + r + ", " + fc.l.goErrorOf() + "(" + e + ")\n"
		}
		if len(sig.Results) == 0 {
			return params, fc.valueResults(nil), body + ind + text + "\n" + ind + "return struct{}{}, nil\n"
		}
		r := fc.scope.alloc("r")
		return params, fc.valueResults(sig.Results[0]), body + ind + "var " + r + " " + fc.goType(sig.Results[0]) + " = " + text + "\n" + ind + "return " + r + ", nil\n"
	})
}

// stdValue lowers the std extern f, whose runtime/v1 body is body, used as
// a value (G1): a literal of the uniform shape that calls the body. The
// checker admits only a body with no hidden argument, no function
// parameter, no type parameter and no deferred Seq (check.externValue), so
// the call reads no environment and is made exactly as a direct call's.
func (fc *funcCtx) stdValue(f *check.Func, body v1.Body, name string, sig *check.Signature, site syntax.Node) string {
	if len(body.Hidden) > 0 || len(body.TypeParams) > 0 || defersCall(body) || len(sig.Results) > 1 || len(body.Params) != len(sig.Params) {
		ice(site, "the std extern %s used as a value, whose body the checker does not admit as one", f.Name)
	}
	for _, p := range body.Params {
		if p.Func != nil {
			ice(site, "the std extern %s used as a value takes a function", f.Name)
		}
	}
	envParam := "_"
	return fc.envClosure(callerEnv, envParam, site, func(_ string) (params []string, results, text string) {
		fc.depth++
		defer func() { fc.depth-- }()
		var vals []string
		for _, p := range sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			vals = append(vals, name)
		}
		var result check.Type
		if len(sig.Results) == 1 {
			result = sig.Results[0]
		}
		call := fc.stdResult(body, result, "", site, func(env string) string {
			return fc.runtimeCall(name, body, "", "", vals, env, nil, nil, site)
		})
		ind := strings.Repeat("\t", fc.depth)
		return params, fc.valueResults(result), uniformBody(ind, call, goValue(body), result != nil, goFallible(body))
	})
}

// hostCalled reports a call of a user extern (hostCall).
func hostCalled(c *check.Call) bool { return c.Kind == check.CallFunc && userExtern(c.Func) }

// hostStmt is the Go statement of the call x of a user extern, whose Go
// text is text (hostCall), in every use: call's value, and callStmt's
// statement or discard. Both run after the call's //line directive, so
// go build reports either check at the Ergo call's line.
//
// First it writes `if false { defer pkg.F(args) }`, the same call text in
// a statement that admits only a function call, so Go proves the call a
// call. Without it, the call time.Duration(x) of `extern go time.Duration
// func F(x float64) duration` would be a conversion that truncates x; under
// defer it is "defer requires function call, not conversion". A constant
// fails ("cannot call"). A variadic target (fmt.Sprint) passes, and so does
// a generic Go function (cmp.Compare): Go infers its type arguments from
// the typed arguments, as it does at the call itself. The block is never
// run, and the Go compiler drops it. Go's qualified built-ins (unsafe.Sizeof)
// would fail under defer; the checker refuses a target in unsafe (S108), so
// none reaches here (hostPkg).
//
// Then it binds the result to v at its Ergo result's Go type, the blank
// identifier when the result is discarded, so a target's float64 result is
// go build's error wherever the Ergo int is used or dropped. The call is
// made once.
func (fc *funcCtx) hostStmt(x *syntax.CallExpr, c *check.Call, v, text string) string {
	fc.line("if false { defer " + text + " }")
	switch {
	case len(c.Func.Sig.Results) == 0:
		return text
	case v == "":
		ice(x, "a result of the user extern %s with no variable", c.Func.Name)
	}
	return "var " + v + " " + fc.goType(c.Func.Sig.Results[0]) + " = " + text
}

// hostFallible writes the call x of a user extern that fails go.Error (G4;
// decision O14), whose Go target's last result is error: hostStmt's call
// check, then the call, its result bound to v at the Ergo result's Go type
// and its error to e, typed error, so a target whose results differ is go
// build's error at the call's line. A non-nil error becomes
// the go.Error that keeps it (v1.GoErrorOf, IG5). The partial value a Go
// function returns with an error (strconv's limit on a range error) is
// dropped: the caller reads v only when e is nil, as an Ergo failure has
// no value.
func (fc *funcCtx) hostFallible(x *syntax.CallExpr, c *check.Call, v, e, text string) {
	fc.line("if false { defer " + text + " }")
	fc.line("var " + e + " error")
	if v == "" {
		fc.line(e + " = " + text)
	} else {
		fc.line("var " + v + " " + fc.goType(c.Func.Sig.Results[0]))
		fc.line(v + ", " + e + " = " + text)
	}
	fc.goErrorOf(x, e)
}

// hostPkg is the name in main.go of the Go package path that a user
// extern's target names, imported under that name (module.go). The naming
// pass gave it (hostImportNames), so it never collides with a generated
// or a declared name, and two packages with one last element (math/rand/v2
// and crypto/rand) get two names.
func (l *lowerer) hostPkg(path string, site syntax.Node) string {
	if path == "time" {
		return l.timePkg()
	}
	if first, _, _ := strings.Cut(path, "/"); (first == "" || strings.Contains(first, ".")) && !l.goModule.covers(path) {
		ice(site, "the Go package %q is outside Go's standard library and in no module of the go.mod beside ergo.mod; the loader refuses it (IG7)", path)
	}
	if path == "unsafe" {
		// Go's unsafe holds the qualified built-ins, whose calls the
		// hostStmt check (defer) does not admit.
		ice(site, "a user extern of Go's unsafe, which the checker refuses (S108: Ergo has no unsafe)")
	}
	if l.hostImports == nil {
		l.hostImports = map[string]bool{}
	}
	l.hostImports[path] = true
	return l.pkgName(pkgKey{"import", path}, hostBase(path))
}

// hostImportNames reserves, in the naming pass, the import name of every Go
// package a user extern of the program names as its target, in path order.
func (l *lowerer) hostImportNames(pkgs []*check.Package) {
	seen := map[string]bool{}
	var paths []string
	for _, p := range pkgs {
		for _, f := range l.canon.funcs(p) {
			if !userExtern(f) {
				continue
			}
			if path, _ := f.Decl.Extern.Split(); path != "time" && !seen[path] {
				seen[path] = true
				paths = append(paths, path)
			}
		}
	}
	// Every Go package the program reaches through import go (libraryPackages).
	l.libraries()
	for _, p := range l.goPkgs {
		if p.Path != "time" && !seen[p.Path] {
			seen[p.Path] = true
			paths = append(paths, p.Path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		l.pkgName(pkgKey{"import", path}, hostBase(path))
	}
}

// hostBase is the preferred Go name of an import of path: its last element,
// or the one before a major version (math/rand/v2 is rand), as an
// identifier.
func hostBase(path string) string {
	elems := strings.Split(path, "/")
	base := elems[len(elems)-1]
	if len(elems) > 1 && len(base) > 1 && base[0] == 'v' && strings.Trim(base[1:], "0123456789") == "" {
		base = elems[len(elems)-2]
	}
	return strings.NewReplacer("-", "_", ".", "_").Replace(base)
}

// taskDestArgs are the leading arguments of a call of a declared function
// or a function value of signature sig: the environment, then, when the
// result holds a Task (Info's TaskResult), the frame scope as the
// destination the callee's return moves the Task to (stage D3b, Rule 2).
// Since stage E2 (C13) a result holds a Task only by being one: no type
// argument holds a Task, and a type that holds one otherwise is S108.
func (fc *funcCtx) taskDestArgs(x *syntax.CallExpr, c *check.Call, sig *check.Signature) []string {
	out := []string{fc.envExpr(x)}
	if c.TaskResult != sig.ResultIsTask() {
		ice(x, "a call whose result holds a Task %v, of a signature that returns a Task %v; the checker refuses a Task held otherwise (S108)", c.TaskResult, sig.ResultIsTask())
	}
	if !c.TaskResult {
		return out
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
	if c := fc.l.info.Calls[call]; c != nil && c.Site != nil {
		out = append(out, fc.siteValue(call, c.Site))
	}
	return out
}

// siteValue is the CallSite the call x passes for the final parameter it
// leaves out (types.md, Call sites): where x starts, as a defect names it.
// A constant, so it runs after the written operands with no spill.
func (fc *funcCtx) siteValue(x *syntax.CallExpr, site *check.Named) string {
	file, line, column := fc.l.locate(x, x.Pos())
	td := fc.l.typeDecl(site.Obj, x)
	return fmt.Sprintf("%s{%s: %q, %s: %d, %s: %d}", fc.goType(site), td.fieldName["File"], file, td.fieldName["Line"], line, td.fieldName["Column"], column)
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
// uniform shape that calls it. An explicit instance, Fn[int], is inst
// (Info.Instances) and passes its type arguments; a bare generic
// function's come from Go's inference on the closure's typed parameters,
// so every type-kinded parameter must appear in a parameter type.
func (fc *funcCtx) funcValue(f *check.Func, t check.Type, site syntax.Node, inst *check.Instance) string {
	fd := fc.l.useFunc(f, site)
	sig, ok := t.(*check.Signature)
	if !ok {
		ice(site, "the function %s used as a value has no function type", f.Name)
	}
	// An extern with no Go function of its own (a user extern's target, a
	// std body of runtime/v1) is a value through a literal of the uniform
	// shape that calls it (G1; the checker admits only externs whose call
	// needs no hidden argument, check.externValue). A prelude intrinsic is
	// a Go function, which an operation table reaches when the extern
	// satisfies an operation (196m: Sleep makes LiveScheduler a Scheduler).
	if userExtern(f) {
		return fc.hostValue(f, sig, site)
	}
	if body, name, std := fc.l.stdBody(fd); std {
		return fc.stdValue(f, body, name, sig, site)
	}
	if inst == nil {
		for _, tp := range f.Sig.TParams {
			if tp.Kind == check.KindType && !paramsMention(f.Sig, tp) {
				unreachable(site, ruleGenericValue, "the generic function %s used as a value with no instance, and its type parameter %s in no parameter", f.Name, tp.Name)
			}
		}
	}
	// The value has the one Go shape of every function value; for a pure
	// function the closure discards its environment (pure.go).
	envParam := "env"
	if fd.pure {
		envParam = "_"
	}
	return fc.envClosure(callerEnv, envParam, site, func(env string) (params []string, results, body string) {
		lead := fd.envLead(env)
		// Since stage E2 (C13) a result holds a Task only by being one, and
		// an instance's type argument is never a Task, so the value, the
		// declaration and Info agree.
		if sig.ResultIsTask() != f.Sig.ResultIsTask() || sig.ResultIsTask() != f.Pkg.Info.TaskResults[f] {
			ice(site, "the function %s used as a value returns a Task %v, its declaration %v, Info %v", f.Name, sig.ResultIsTask(), f.Sig.ResultIsTask(), f.Pkg.Info.TaskResults[f])
		}
		if sig.ResultIsTask() {
			// The destination passes through to the function, whose return
			// moves the Task (stage D3b, Rule 2).
			dest := fc.scope.alloc("dest")
			params = append(params, dest+" *"+fc.l.rt()+".Scope")
			lead = append(lead, dest)
		}
		var args []string
		for _, p := range sig.Params {
			name := fc.scope.alloc("p")
			params = append(params, name+" "+fc.goType(p.Type))
			args = append(args, name)
		}
		// An instance's witnesses, from its operations (witness.go): a
		// struct value passed on each call of the value.
		lead = append(lead, fc.witnessArgs(f, inst, site)...)
		var head string
		switch {
		case fd.method != nil:
			head, args = args[0]+"."+fd.name, args[1:]
		case inst != nil:
			head = fd.name + fc.typeArgs(f.Sig, inst.TypeArgs, site)
		default:
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
		return params, fc.valueResults(result), body
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
		case *check.Array:
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

// withExpr lowers with A, …, Z { block } (rows.md, With) as its nesting,
// with A { … with Z { block } … } (With 7): one Go block per operand, the
// first operand outermost, each read from its own With fact (With.Next).
// The block's tail is the with's value, and its untried failure the with's.
func (fc *funcCtx) withExpr(x *syntax.CallExpr) operand {
	w := fc.l.info.Withs[x]
	if w == nil {
		ice(x, "a with with no Withs entry")
	}
	if len(x.Args) == 0 || x.Trailing == nil {
		ice(x, "a with without a context and a block")
	}
	var res operand
	if !fc.noValue(x) {
		res.val = fc.temp("v")
		fc.line("var " + res.val + " " + fc.goType(fc.typeOf(x)))
	}
	if fc.fallibleExpr(x) {
		res.err = fc.temp("err")
		fc.line("var " + res.err + " error")
	}
	// The block yields by the one tail rule (D-C1, D-C2): its last
	// statement, through a final if, match or block, assigns the with's
	// value and passes its failure on.
	d := dest{kind: destStmt}
	if res.val != "" || res.err != "" {
		d = dest{kind: destAssign, name: res.val, err: res.err}
	}
	fc.with(x, 0, w, d)
	return res
}

// with lowers operand i of the with x, whose fact is w, around the
// operands after it and the block, which yields to d. A context operand
// is a Go block that copies the environment, builds the context's
// providers into the copy and runs what it holds against it, with no
// closure (With 8). A named context's providers are built by its builder;
// an inline or a local context's in place, so a local context is its
// literal at each with that names it, and its recipe has no value at run
// time (rows.md, Context 9).
func (fc *funcCtx) with(x *syntax.CallExpr, i int, w *check.With, d dest) {
	arg := x.Args[i]
	if (w.Next == nil) != (i == len(x.Args)-1) {
		ice(arg, "a with of %d operands whose With.Next chain ends at operand %d", len(x.Args), i+1)
	}
	inner := func() {
		if w.Next != nil {
			fc.with(x, i+1, w.Next, d)
		} else {
			fc.yieldList(x.Trailing.List, d)
		}
	}
	if w.Value != nil {
		fc.contextWith(arg, w.Value, inner) // the context owner (context.go)
		return
	}
	if w.Installs != nil {
		fc.installWith(arg, inner) // a Context value (contextvalues.go)
		return
	}
	if len(w.Contexts) == 0 {
		ice(arg, "a with operand with neither a context nor a context binding (With.Contexts, With.Value)")
	}
	// A named context of an imported user package is built as the root's
	// are (196b U4): the unit sources it. One of a std package is built as
	// a std body is (stdContext). One of neither is a compiler defect.
	if w.Contexts[0].Lit == nil && !fc.l.declaredContext(w.Contexts[0]) {
		ice(arg, "a with of the context %s, which no source file of the program declares", w.Contexts[0].Name)
	}
	// The with lifetime owner (resources.md, Context ownership; runtime.md,
	// With cleanup): a with that acquires a resource opens its own scope on
	// the scope of the code around it. The builder registers each
	// provider's release in it as the provider is built; the block's tasks
	// belong to it (frameScope); the normal end of the block ends it here,
	// and the frame's End ends it on every other exit (v1.Scope.close).
	owning := ownsResources(w)
	if owning && (fc.frame == nil || fc.frame.scope == "") {
		ice(arg, "a with that owns a provider with a Close outside a function body; the checker refuses one (S108, check/frames.go)")
	}
	lit := w.Contexts[0].Lit != nil // an inline or a local context
	build := ""
	if !lit {
		build = fc.l.contextBuilder(w, arg)
	}
	env := fc.temp("env")
	fc.line("{")
	fc.depth++
	outer := fc.envExpr(arg)
	fc.line(env + " := *" + outer)
	if lit {
		// An empty inline context and a pure body may read no capability.
		fc.line("_ = " + env)
	}
	savedWith := ""
	ws := ""
	if owning {
		ws = fc.temp("ws")
		fc.line(ws + " := " + fc.scopeCall("Scope.OpenWith", fc.frameScope(arg), nil, arg))
		savedWith, fc.frame.with = fc.frame.with, ws
	}
	if lit {
		fc.l.localContext(w.Contexts[0], arg)
		fc.buildProviders(w, "(&"+env+")", outer, ws)
	} else if owning {
		fc.line(build + "(&" + env + ", " + outer + ", " + ws + ")")
	} else {
		fc.line(build + "(&" + env + ")")
	}
	saved := fc.env
	fc.env = "(&" + env + ")"
	inner()
	fc.env = saved
	if owning {
		fc.frame.with = savedWith
		errArg := "nil"
		if d.err != "" {
			errArg = "&" + d.err
		}
		fc.line(fc.scopeCall("Scope.EndWith", ws, []string{errArg}, arg))
	}
	fc.depth--
	fc.line("}")
}

// scopeCall writes a call of the with scope's method name on recv through
// the runtime binding emitter.
func (fc *funcCtx) scopeCall(name, recv string, vals []string, site syntax.Node) string {
	body, err := v1.BodyOf(name)
	if err != nil {
		ice(site, "the with scope binding %s has no runtime/v1 body: %v", name, err)
	}
	return fc.runtimeCall(name, body, "", recv, vals, "", nil, nil, site)
}

// ownsResources reports a with that acquires a provider with a checked
// Close (With.Acquired, the checker's ownership fact).
func ownsResources(w *check.With) bool {
	for _, a := range w.Acquired {
		if a.Close != nil {
			return true
		}
	}
	return false
}

// contextSet names the builder of a with: its context and the providers it
// acquires, by node identity in acquisition order (nodeID). Two withs that
// acquire the same entries share one builder. A position is no identity:
// two files' providers can start at one offset (196b).
type contextSet struct {
	w   *check.Context
	set string
}

func (l *lowerer) acquiredSet(w *check.With) contextSet {
	var b strings.Builder
	for _, a := range w.Acquired {
		b.WriteString(strconv.Itoa(l.nodeID(a.Entry.Provider)))
		b.WriteByte(' ')
	}
	return contextSet{w.Contexts[0], b.String()}
}

// contextBuilder is the Go function that builds the providers a with acquires
// into an environment (rows.md, Context 4: its own function, so its providers
// do not see the caller's locals). It builds the with's acquisitions
// (With.Acquired), each once, in the checker's acquisition order, so a
// shared entry is one value under each of its keys, an overridden or
// unreached provider never runs (Context 7), and an entry whose provider uses
// a capability of this context is built after that capability's entry.
func (l *lowerer) contextBuilder(w *check.With, site syntax.Node) string {
	key := l.acquiredSet(w)
	if name, ok := l.contexts[key]; ok {
		return name
	}
	name := l.pkgName(pkgKey{"context", key}, "build"+key.w.Name)
	l.contexts[key] = name
	l.queue(key.w.Decl, func() { l.emitContext(w, name, site) })
	return name
}

// context checks a named context declaration. Its builders are written at the
// withs that use it.
func (l *lowerer) context(w *check.Context, site syntax.Node) {
	if w.Decl.Provides != nil || w.Decl.Needs != nil {
		refuse(w.Decl, "a context header with provides or needs is checked by 196b")
	}
}

// emitContext writes the builder name for the with w. A with that owns a
// resource passes its builder the environment outside the with and its
// scope: right after the builder builds a provider with a checked Close,
// it registers that provider's release in the scope, so a later
// provider's defect still closes it. The release runs in the outer
// environment, where the checker charged its needs (With.Acquired).
func (l *lowerer) emitContext(with *check.With, name string, site syntax.Node) {
	w := with.Contexts[0]
	if !l.declaredContext(w) {
		ice(site, "a builder of the context %s, which no source file of the program declares", w.Name)
	}
	l.localContext(w, site)
	// A std context's builder is a std body (lower-gaps G2, batch 11): it
	// goes into the generated part with no //line directive and no row in
	// the declaration table, and it reads its own package's Info.
	std := l.stdContext(w)
	fc := newFuncCtx(l, newNamer(l.names), !std)
	fc.owner = w.Name
	if std {
		saved := l.info
		l.info = w.Package().Info
		defer func() { l.info = saved }()
	} else {
		l.recordDecl(w.Name, w.Decl)
	}
	// A builder has no loop and calls no capability (a named context is
	// closed); its frame reads the context's footprint all the same.
	fc.frame = newFrame(nil, false, framePolls(l.info, w.Decl))
	fc.depth = 1
	fc.env = fc.scope.alloc("env")
	params := fc.env + " *" + l.env()
	outer, ws := "", ""
	if ownsResources(with) {
		outer, ws = fc.scope.alloc("outer"), fc.scope.alloc("ws")
		params += ", " + outer + " *" + l.env() + ", " + ws + " *" + l.rt() + ".Scope"
	}
	fc.entryPoll(l.info, w.Decl)
	l.analyzeValues(w.Decl, l.info)
	fc.buildProviders(with, fc.env, outer, ws)
	decl := "func " + name + "(" + params + ") {\n" + fc.out.String() + "}\n\n"
	if std {
		l.gen.WriteString(decl)
		return
	}
	l.user.WriteString(l.directive(w.Decl) + decl)
}

// localContext checks that every named context w embeds, at any depth, has
// a source in the program, equally for named and inline contexts: an
// embedded context of an imported user package is built as the root's
// are (196b U4), and one with no source is a compiler defect.
func (l *lowerer) localContext(w *check.Context, site syntax.Node) {
	visited := map[*check.Context]bool{}
	var walk func(*check.Context)
	walk = func(v *check.Context) {
		if visited[v] {
			return
		}
		visited[v] = true
		if v.Lit == nil && !l.declaredContext(v) {
			ice(site, "an embedded context %s, which no source file of the program declares", v.Name)
		}
		for _, e := range v.Entries {
			if e.Embedded != nil {
				walk(e.Embedded)
			}
		}
	}
	walk(w)
}

// buildProviders is the acquisition owner shared by named builders and
// lexical inline and local contexts. All consume exactly the checker's
// Acquired fact. The entries of an inline or a local context, and of each
// local context it embeds, see the environment outside the with, where the
// checker charged what they use (rows.md, Context 3 and 9); embedded named
// entries see the partially built context, as their checked Uses require.
func (fc *funcCtx) buildProviders(with *check.With, target, outer, ws string) {
	local := map[*check.ContextEntry]bool{}
	var lexical func(*check.Context)
	lexical = func(w *check.Context) {
		if w.Lit == nil {
			return
		}
		for _, e := range w.Entries {
			if e.Embedded != nil {
				lexical(e.Embedded)
			} else {
				local[e] = true
			}
		}
	}
	lexical(with.Contexts[0])
	// The package of each entry, by the context that holds it: an entry of
	// a std context is lowered as a std body is (buildProvider).
	pkgs := map[*check.ContextEntry]*check.Package{}
	var owners func(*check.Context)
	owners = func(w *check.Context) {
		for _, e := range w.Entries {
			if _, ok := pkgs[e]; ok {
				continue
			}
			pkgs[e] = w.Package()
			if e.Embedded != nil {
				owners(e.Embedded)
			}
		}
	}
	owners(with.Contexts[0])
	for _, a := range with.Acquired {
		fc.buildProvider(a, target, outer, ws, local[a.Entry], pkgs[a.Entry])
	}
}

// buildProvider builds the provider of one acquisition into target. Its
// expression is lowered within its own file and its package's Info: an
// entry of an imported context (196b U4) reads the facts of the package
// that declares it, as every declaration of that package does (sources.go).
// An entry of a std context, pkg std, reads its std package's Info and
// writes no //line directive and no call identity, as a std body does
// (lower-gaps G2, batch 11).
func (fc *funcCtx) buildProvider(a *check.Acquisition, target, outer, ws string, local bool, pkg *check.Package) {
	e := a.Entry
	defer fc.l.leave(fc.l.enter(e.Provider, nil))
	if pkg != nil && pkg.Std() {
		fc.l.info = pkg.Info // leave restores it
		saved := fc.directives
		fc.directives = false
		defer func() { fc.directives = saved }()
	}
	fc.at(e.Provider)
	saved := fc.env
	fc.env = target
	if local {
		fc.env = outer
	}
	if ws != "" {
		fc.withClose(a, target, outer, ws, e.Provider)
	} else {
		fc.publishProvider(a, target)
	}
	fc.env = saved
}

func (fc *funcCtx) publishProvider(a *check.Acquisition, target string) string {
	e := a.Entry
	val := fc.value(e.Provider)
	if len(a.Caps) > 1 || a.Close != nil {
		val = fc.spill(val)
	}
	for _, i := range a.Caps {
		cap, ok := e.Caps[i].(*check.Named)
		if !ok {
			ice(e.Provider, "a context entry key that is not a named capability")
		}
		conv := check.Conversion{}
		if i < len(e.Conversions) {
			conv = e.Conversions[i]
		}
		fc.line(target + "." + fc.l.envField(cap) + " = " + fc.convert(conv, val, e.Provider))
	}
	return val
}

// declaredContext reports a named context the lowering can build: one that
// a source file of the program declares, or one a std package declares
// (stdContext). Any other is a compiler defect at its with.
func (l *lowerer) declaredContext(w *check.Context) bool {
	return w.Decl != nil && (l.stdContext(w) || !l.unsourced(w))
}

// stdContext reports a named context of a std package. Its builder is a
// std body: no source file of the program holds it, so it has no //line
// directive and reads its package's Info (emitContext).
func (l *lowerer) stdContext(w *check.Context) bool {
	return w.Lit == nil && w.Package().Std() && l.unsourced(w)
}

// unsourced reports a named context that no source file of the program
// declares: neither the root package's nor an imported user package's.
func (l *lowerer) unsourced(w *check.Context) bool {
	for _, s := range l.srcs {
		for _, d := range s.file.Decls {
			if d == syntax.Decl(w.Decl) {
				return false
			}
		}
	}
	return true
}

// operations converts val to the operation type of an operations
// conversion (Info.Implicit, Context entries): an adapter struct whose methods
// call the table's functions. An operation takes the caller's task first
// (rows.md, Rows 7) and a function takes an environment, so no Go method of
// the source has the operation's Go signature, and the adapter is always
// written (stage D3b).
//
// Inside a generic function (D-E1, C8) the adapter is a generic Go type over
// the function's type parameters the conversion names; a source that is a
// type parameter T (or *T) carries T's witness beside the value, and its
// methods call the witness (witness.go).
func (fc *funcCtx) operations(conv check.Conversion, val string, site syntax.Node) string {
	l := fc.l
	if check.ScriptOf(conv.From) != nil {
		return fc.scriptOperations(conv, val, site) // a Script's derived methods (script.go)
	}
	// A *T source (types.md, Methods 5) holds the pointer: a table function
	// on T reads *v, one on *T takes v (stage C round 4 review T1).
	key, base, ok := l.adapterName(conv)
	if !ok {
		unreachable(site, ruleAccepted, "an operations conversion from %s to %s with no adapter (adapterName)", conv.From, conv.To)
	}
	to := conv.To.(*check.Named)
	_, ptrSource, _ := opsSource(conv.From)
	param, ptrParam := paramSource(conv.From)
	if param != nil {
		ptrSource = ptrParam
		if _, ok := fc.witness[param]; !ok {
			ice(site, "an operations conversion from %s, whose witness is not bound here", conv.From)
		}
	}
	if l.runtimeType(to, site) != "" {
		// A std type is the runtime's concrete Go type (stage D3b), a sealed
		// std protocol the checker refuses a conversion to (C28, #206).
		ice(site, "a value of the program converted to the sealed std type %s, which the checker refuses (#206)", conv.To)
	}
	toDecl := l.typeDecl(to.Obj, site)
	if toDecl.kind != kindOps {
		ice(site, "an operations conversion to %s, which is not an operation type", conv.To)
	}
	tc := &typeCtx{l: l, params: map[*check.TypeParam]string{}, site: site}
	// The adapter's own type parameters stand for the enclosing function's
	// that the conversion names; the site instantiates it with them.
	free := convParams(conv)
	adapterScope := newNamer(l.names)
	var tpDecl, tpArgs []string
	for _, tp := range free {
		n := adapterScope.alloc(tp.Name)
		tc.params[tp] = n
		tpDecl = append(tpDecl, n+" "+l.constraint(tp, site))
		tpArgs = append(tpArgs, fc.goType(tp))
	}
	declParams, instArgs, recvArgs := "", "", ""
	if len(free) > 0 {
		declParams = "[" + strings.Join(tpDecl, ", ") + "]"
		instArgs = "[" + strings.Join(tpArgs, ", ") + "]"
		var own []string
		for _, tp := range free {
			own = append(own, tc.params[tp])
		}
		recvArgs = "[" + strings.Join(own, ", ") + "]"
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
		if param != nil {
			continue // the witness supplies every operation
		}
		if f == nil {
			ice(site, "the operations conversion to %s has no function for %s", conv.To, op.Name)
		}
		if len(witnessParams(f.Sig)) > 0 {
			if inst := conv.Instances[op.Name]; inst == nil || inst.Func != f {
				ice(site, "the operations conversion to %s has no instance for %s", conv.To, op.Name)
			}
		}
		// A provider's requirements are met where a context builds it (rows.md,
		// Context 4 and 6), never by the caller of its operation, so the
		// adapter's environment holds the task alone. The checker refuses a
		// table function that needs capabilities (S108, with #37).
		if len(rowElems(f.Sig.Uses)) > 0 {
			ice(site, "the operation %s of %s is provided by %s, which needs capabilities", op.Name, conv.To, f.Name)
		}
		l.useFunc(f, site)
	}
	// Store the table functions' witnesses in the adapter. Each method
	// forwards them before its receiver, preserving the function's rows.
	var fields, values []string
	witnessFields := map[string][]string{}
	if param == nil {
		for _, op := range toDecl.ops {
			f := conv.Table[op.Name]
			inst := conv.Instances[op.Name]
			args := fc.witnessArgs(f, inst, site)
			for i, arg := range args {
				w := &inst.Witnesses[i]
				var ops []string
				for _, wo := range w.Ops {
					ops = append(ops, memberName(wo.Name)+" "+tc.goType(wo.Sig))
				}
				field := fmt.Sprintf("w%d", len(fields))
				fields = append(fields, field+" struct{ "+strings.Join(ops, "; ")+" }")
				values = append(values, field+": "+arg)
				witnessFields[op.Name] = append(witnessFields[op.Name], field)
			}
		}
	}
	name, ok := l.adapters[key]
	if !ok {
		name = l.pkgName(pkgKey{"adapter", key}, base)
		l.adapters[key] = name
		var b strings.Builder
		if param != nil {
			b.WriteString(fmt.Sprintf("// %s provides %s through the witness of %s (an operations conversion).\n", name, conv.To, param.Name))
			b.WriteString("type " + name + declParams + " struct {\n\tv " + fromGo + "\n\tw " + tc.witnessType(param, param) + "\n}\n\n")
		} else {
			b.WriteString(fmt.Sprintf("// %s provides %s through the functions of %s (an operations conversion).\n", name, conv.To, conv.From))
			b.WriteString("type " + name + declParams + " struct{ v " + fromGo)
			for _, field := range fields {
				b.WriteString("; " + field)
			}
			b.WriteString(" }\n\n")
		}
		for _, op := range toDecl.ops {
			locals := newNamer(l.names) // so no local hides the function it calls
			if param != nil {
				b.WriteString(fc.witnessMethod(tc, locals, name+recvArgs, op, ptrSource))
				continue
			}
			f := conv.Table[op.Name]
			fd := l.funcDecl(f)
			self := locals.alloc("a")
			// The operation takes the caller's task first (rows.md, Rows 7);
			// the table function runs in an environment of that task alone.
			task := locals.alloc("t")
			params := []string{task + " *" + l.taskPkg() + ".Task"}
			if fd.pure {
				// A pure table function takes no environment (pure.go).
				params[0] = "_ *" + l.taskPkg() + ".Task"
			}
			args := fd.envLead("&" + l.env() + "{" + envTask + ": " + task + "}")
			// An operation that returns a Task takes the destination after
			// the task (methodSig) and passes it to the function, whose
			// return moves the Task there (stage D3b, Rule 2; stage E2, C15).
			if op.Sig.ResultIsTask() != f.Sig.ResultIsTask() {
				ice(site, "the operation %s of %s and its function %s disagree on a Task result", op.Name, conv.To, f.Name)
			}
			if op.Sig.ResultIsTask() {
				dest := locals.alloc("d")
				params = append(params, dest+" *"+l.rt()+".Scope")
				args = append(args, dest)
			}
			hidden := len(args) // the environment and the destination
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
				lead := slices.Clone(args[:hidden])
				for _, field := range witnessFields[op.Name] {
					lead = append(lead, self+"."+field)
				}
				lead = append(lead, recv)
				call = fd.name + "(" + strings.Join(append(lead, args[hidden:]...), ", ") + ")"
			}
			var result check.Type
			if len(op.Sig.Results) == 1 {
				result = op.Sig.Results[0]
			}
			opFallible := len(rowElems(op.Sig.Errors)) > 0
			b.WriteString("func (" + self + " *" + name + recvArgs + ") " + memberName(op.Name) + "(" + strings.Join(params, ", ") + ")" + tc.resultList(result, opFallible) + " {\n")
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
	if param != nil {
		return "&" + name + instArgs + "{v: " + val + ", w: " + fc.witness[param] + "}"
	}
	if len(values) > 0 {
		val += ", " + strings.Join(values, ", ")
	}
	return "&" + name + instArgs + "{v: " + val + "}"
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
	case *check.Array:
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
	prev, prevNode, site := fc.lastPos, fc.lastNode, fc.site
	if prevNode == nil {
		prevNode = x // no directive yet: prev is an offset in x's file
	}
	fc.at(x)
	return func() {
		if prev.IsValid() {
			fc.out.WriteString(fc.l.directiveAt(prevNode, prev))
			fc.lastPos, fc.lastNode = prev, prevNode
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
	case check.CallLawOp:
		if fc.l.tests != nil && fc.l.tests.law != nil && fc.l.tests.law.Owner != nil {
			return fc.l.tests.law.Owner.Name + "." + c.Op.Name
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
	src, offset  int // the source file (sources.go) and the offset in it
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
	src, _, line, column := l.at(x, x.Pos())
	l.calls = append(l.calls, callIdent{src: src, offset: int(x.Pos()), fn: owner, callee: callee, line: line, column: column})
}

// declIdent is one entry of the declaration table: a source declaration's
// name (the Func of its calls) and its first and last lines.
type declIdent struct {
	src, offset int // the source file (sources.go) and the offset in it
	name        string
	line, end   int
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
	src, _, line, _ := l.at(n, n.Pos())
	_, _, end, _ := l.at(n, n.End())
	l.decls = append(l.decls, declIdent{src: src, offset: int(n.Pos()), name: name, line: line, end: end})
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
	// Source order: by source file (sources.go), then by offset.
	sort.SliceStable(l.decls, func(i, j int) bool {
		a, b := l.decls[i], l.decls[j]
		return a.src < b.src || a.src == b.src && a.offset < b.offset
	})
	sort.SliceStable(l.calls, func(i, j int) bool {
		a, b := l.calls[i], l.calls[j]
		return a.src < b.src || a.src == b.src && a.offset < b.offset
	})
	declEntry := l.pkgName(pkgKey{"decl", nil}, "ergoDecl")
	declTable := l.pkgName(pkgKey{"decls", nil}, "ergoDecls")
	entry := l.pkgName(pkgKey{"call", nil}, "ergoCall")
	table := l.pkgName(pkgKey{"calls", nil}, "ergoCalls")
	file := func(src int) string { return strconv.Quote(l.srcs[src].path) }
	var b strings.Builder
	b.WriteString("// " + declEntry + " is one source declaration and its lines (#230).\n")
	b.WriteString("type " + declEntry + " = struct {\n\tName      string\n\tFile      string\n\tLine, End int\n}\n\n")
	b.WriteString("// " + declTable + " lists the program's source declarations, in source order.\n")
	b.WriteString("var " + declTable + " = [...]" + declEntry + "{\n")
	for _, d := range l.decls {
		fmt.Fprintf(&b, "\t{Name: %s, File: %s, Line: %d, End: %d},\n", strconv.Quote(d.name), file(d.src), d.line, d.end)
	}
	b.WriteString("}\n\n")
	b.WriteString("// " + entry + " is the Ergo identity of one lowered call (#230).\n")
	b.WriteString("type " + entry + " = struct {\n\tFunc, Callee string\n\tFile         string\n\tLine, Column int\n}\n\n")
	b.WriteString("// " + table + " lists the identity of each call of the program, in source order.\n")
	b.WriteString("var " + table + " = [...]" + entry + "{\n")
	for _, c := range l.calls {
		fmt.Fprintf(&b, "\t{Func: %s, Callee: %s, File: %s, Line: %d, Column: %d},\n", strconv.Quote(c.fn), strconv.Quote(c.callee), file(c.src), c.line, c.column)
	}
	b.WriteString("}\n\n")
	b.WriteString("func init() {\n\t" + l.rt() + ".RegisterCalls(" + declTable + "[:], " + table + "[:])\n}\n\n")
	return b.String()
}
