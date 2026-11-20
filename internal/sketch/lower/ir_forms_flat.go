package lower

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Flat results (IR-FORMS IRF-2, spec §3.2). A function that fails with a
// row of one record type E returns its failure through Go's error today:
// each failure boxes E into an interface (runtime.convT64 on the probe),
// and a catch asserts it back. Under the flat convention the function has
// a worker that reports whether it failed as its one result and writes its
// value or its failure, E itself, into the caller's ergoFlat[V, E]{Val,
// Err}; the function keeps its Go signature and becomes the worker's
// wrapper, for every caller that is not an IR body of this lowering (a
// text body, a function value). A try or a catch of a direct call calls
// the worker: a try tests its result and leaves with Err; a catch whose
// arm takes E reads Err with no type switch and no assertion. This is
// GHC's worker/wrapper with the constructed-product-result analysis,
// here for a sum (a value or a failure).
//
// Why a flag and an out struct, not the struct as the result: the IR
// reader returns one value or a value and an error, and Go compiles a
// struct result that its inliner merges into a branch it tests twice (a
// hand Go probe of the three forms, IRF-2 state.md: three results 60
// instructions a call, the struct 70, the flag and out struct 64).
//
// The choice reads the callee's signature alone (flatResOf), never its
// body or its name, so a call site knows it before the callee is built.
// The worker is the callee's own IR body with each return rewritten
// (flatWorker); where the callee has no IR body, or a return the rewrite
// does not know, the worker calls the wrapper and converts its error
// (trivialWorker), so every call site that chose the worker has one. The
// reference is flatLowering "off": no worker, every call returns Go's
// error, as before this unit.

// flatLowering selects the flat convention: "" (on) or "off" (the
// reference), set by
// -ldflags=-X=ergo.local/prototype/internal/sketch/lower.flatLowering=off.
var flatLowering string

// FlatOn reports that the flat convention is on (flatLowering), for the
// properties that check its reach.
func FlatOn() bool { return flatLowering != "off" }

// flatRes is the flat convention of one function.
type flatRes struct {
	f      *check.Func
	name   string       // the wrapper's Go name: the function's own
	worker string       // the worker's Go name
	typ    string       // the out struct: ergoFlat[V, E], as the IR spells it
	text   string       // the out struct as Go text spells it (the worker's head)
	val    string       // V: the value's Go type, struct{} for none
	err    string       // E: the failure's Go type
	e      *check.Named // E
	unit   bool         // no value
	params int          // the wrapper's Go parameter slots: the environment, then the parameters
	used   bool         // a call site calls the worker
	built  bool         // the worker is the function's own body (flatWorker)
}

// flatResOf is f's flat convention, or nil: f is a declared function of
// the program or std with a body, not a method, not generic, every
// parameter named and none a function or taken, at most one result, and a
// failure row of exactly one non-generic record type. The Go spellings
// are b's.
func (b *irBuilder) flatResOf(f *check.Func) (fl *flatRes) {
	st := b.st
	if flatLowering == "off" || st == nil || st.census != nil || f == nil {
		return nil
	}
	if got, ok := st.flat[f]; ok {
		return got
	}
	if st.flat == nil {
		st.flat = map[*check.Func]*flatRes{}
	}
	defer func() {
		if r := recover(); r != nil {
			if _, no := r.(irUnsupported); !no {
				panic(r)
			}
			fl = nil // a type the IR does not spell: no worker
		}
		st.flat[f] = fl
	}()
	if f.Decl == nil || f.Decl.Body == nil || f.Decl.Extern != nil || f.Decl.Recv != nil || f.Sig == nil || f == b.l.mainFunc() {
		return nil
	}
	if len(f.Sig.TParams) > 0 || len(f.Sig.Results) > 1 || len(f.Sig.Takes) > 0 || f.Sig.TakesSelf {
		return nil
	}
	for _, p := range f.Sig.Params {
		if p == nil || len(p.Name) == 0 || p.Name == "_" { // unnamed: no Go parameter to pass on
			return nil
		}
		if _, fn := p.Type.(*check.Signature); fn {
			return nil
		}
	}
	elems := rowElems(f.Sig.Errors)
	if len(elems) != 1 {
		return nil
	}
	e, ok := elems[0].(*check.Named)
	if !ok || len(e.TArgs) > 0 || len(e.TParams) > 0 {
		return nil
	}
	if _, record := e.Underlying().(*check.Struct); !record {
		return nil
	}
	fd := b.l.funcDecl(f)
	if fd.method != nil || fd.extern != "" || !fd.fallible {
		return nil
	}
	fl = &flatRes{f: f, name: fd.name, val: "struct{}", unit: len(f.Sig.Results) == 0, params: len(f.Sig.Params)}
	if !fd.envFree {
		fl.params++ // the environment (ENV-FREE-FALLIBLE: a fallible function may take none)
	}
	fl.err, fl.e = b.goType(e, f.Decl), e
	if !fl.unit {
		if isNoValue(f.Sig.Results[0]) || isTuple(f.Sig.Results[0]) {
			return nil // Go returns a tuple as its elements
		}
		fl.val = b.goType(f.Sig.Results[0], f.Decl)
	}
	valText := "struct{}"
	if !fl.unit {
		valText = b.fc.goType(f.Sig.Results[0])
	}
	if st.flatType == "" {
		st.flatType = b.l.names.alloc("ergoFlat")
	}
	fl.typ = st.flatType + "[" + fl.val + ", " + fl.err + "]"
	fl.text = st.flatType + "[" + valText + ", " + b.fc.goType(e) + "]"
	fl.worker = b.l.names.alloc(fd.name + "Flat")
	return fl
}

// flatCall is the flat convention of the callee of call, nil unless call
// calls a declared function directly with no type arguments.
func (b *irBuilder) flatCall(call *syntax.CallExpr) *flatRes {
	c := b.l.info.Calls[call]
	if c == nil || c.Kind != check.CallFunc || c.Func == nil || len(c.TypeArgs) > 0 || call.Trailing != nil {
		return nil
	}
	return b.flatResOf(c.Func)
}

// flatBind writes call as a call of fl's worker into a fresh out struct
// r: failed is the worker's call, true where it failed. ok is false when
// the call's Go form is not a plain call of the function (a variant);
// then pre and x are that call, for bindCall.
func (b *irBuilder) flatBind(call *syntax.CallExpr, fl *flatRes) (pre []*irStmt, x, failed *irExpr, r int, ok bool) {
	pre, x = b.rawCall(call)
	if x.K != "call" || x.Name != fl.name {
		return pre, x, nil, -1, false
	}
	p := b.pos(call, call.Pos())
	fl.used = true
	r = b.temp("res", fl.typ, p)
	w := *x
	w.Name = fl.worker
	w.Args = append(append([]*irExpr(nil), x.Args...), &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: r}})
	return append(pre, &irStmt{K: "var", Pos: p, Local: r}), nil, &w, r, true
}

func flatField(r int, name string, p irPos) *irExpr {
	return &irExpr{K: "field", Pos: p, X: &irExpr{K: "local", Pos: p, I: r}, Name: name}
}

// flatTry is try call through the worker: where the callee failed, the
// frame leaves with its failure, E itself.
func (b *irBuilder) flatTry(call *syntax.CallExpr, fl *flatRes) ([]*irStmt, *irExpr) {
	pre, x, failed, r, ok := b.flatBind(call, fl)
	p := b.pos(call, call.Pos())
	if !ok {
		pre, v, e := b.bindCall(pre, x, call)
		pre = append(pre, b.propagate(e, p))
		if v < 0 {
			return pre, nil
		}
		return pre, &irExpr{K: "local", Pos: p, I: v}
	}
	k := "fail"
	if !b.fc.frame.fallible {
		k = "panic"
	}
	leave := b.formed(&irStmt{K: k, Pos: p, X: flatField(r, "Err", p)}, "fail:"+failKey(fl.e))
	pre = append(pre, &irStmt{K: "if", Pos: p, End: p, X: failed, Then: []*irStmt{leave}})
	if fl.unit {
		return pre, nil
	}
	return pre, flatField(r, "Val", p)
}

// flatCatch is x catch { arm } of a direct call of a flat callee whose
// one arm takes E (`case E` or `case E as e`): the arm runs where the
// worker failed, its binding the failure itself. ok is false for any
// other catch, before anything is built.
func (b *irBuilder) flatCatch(x *syntax.CatchExpr, partial bool, call *syntax.CallExpr, m *check.Match) ([]*irStmt, *irExpr, bool) {
	fl := b.flatCall(call)
	if fl == nil || len(x.Arms) != 1 || len(x.Arms[0].Types) != 1 {
		return nil, nil, false
	}
	a := x.Arms[0]
	pat := b.l.info.Patterns[a.Types[0]]
	if pat == nil {
		return nil, nil, false
	}
	var as *check.Var
	var t check.Type
	switch {
	case pat.Kind == check.PatTypeTest:
		t = pat.Type
	case pat.Kind == check.PatAs && len(pat.Elems) == 1 && pat.Elems[0].Kind == check.PatTypeTest && pat.Var != nil:
		t, as = pat.Elems[0].Type, pat.Var
	default:
		return nil, nil, false
	}
	if n, ok := t.(*check.Named); !ok || !sameNamed(n, rowElems(fl.f.Sig.Errors)[0]) {
		return nil, nil, false
	}
	if m.Result != nil && b.l.info.Types[call] == nil {
		return nil, nil, false
	}
	pre, rx, failed, r, ok := b.flatBind(call, fl)
	if !ok {
		// A variant's Go form: the type switch on its error, as catch
		// writes it (bindCall).
		pre, v := b.catchWith(x, partial, m, call, func() ([]*irStmt, int, int) { return b.bindCall(pre, rx, call) })
		return pre, v, true
	}
	p := b.pos(x, x.Catch)
	result := -1
	if m.Result != nil {
		rt := b.goType(m.Result, x)
		result = b.temp("caught", rt, p)
		pre = append(pre, &irStmt{K: "def", Pos: p, Local: result, X: &irExpr{K: "zero", Pos: p, T: rt}})
	}
	end := b.pos(x, x.Rbrace)
	var then []*irStmt
	ap := b.pos(x, a.Case)
	if as != nil {
		name := b.named(as.Name, fl.err, ap)
		b.locals[as] = name
		then = append(then, &irStmt{K: "def", Pos: ap, Local: name, X: flatField(r, "Err", ap)})
	}
	then = append(then, b.armBody(a, result, m.Result)...)
	ifs := &irStmt{K: "if", Pos: p, End: end, X: failed, Then: then}
	if result >= 0 && !fl.unit {
		ifs.ElsePos, ifs.ElseEnd = end, end
		val := b.variantInto(b.l.info.Types[call], m.Result, flatField(r, "Val", end), call)
		ifs.Else = []*irStmt{{K: "set", Pos: end, Local: result, X: val}}
	}
	pre = append(pre, ifs)
	if result < 0 {
		return pre, nil, true
	}
	return pre, &irExpr{K: "local", Pos: p, I: result}, true
}

// sameNamed reports that n and t name one declared type.
func sameNamed(n *check.Named, t check.Type) bool {
	u, ok := t.(*check.Named)
	return ok && n.Obj == u.Obj
}

// formed is s, a return or a failure of the body being built, with its
// form recorded for the worker's rewrite (flatWorker): "val" (ret X, the
// value), "unit" (ret with no value), "tuple" (ret of a fallible call's
// value and error), "fail:error" (the frame fails with a Go error), or
// "fail:"+failKey(T) (with a value of the declared type T).
func (b *irBuilder) formed(s *irStmt, form string) *irStmt {
	if b.flatRets == nil {
		b.flatRets = map[*irStmt]string{}
	}
	b.flatRets[s] = form
	return s
}

// failKey is a failure's type in a recorded form (formed): its declared
// type's identity, "error" for Go's error. It spells no Go type, so
// recording it changes nothing the lowering builds.
func failKey(t check.Type) string {
	if n, ok := t.(*check.Named); ok && len(n.TArgs) == 0 {
		return fmt.Sprintf("named:%p", n.Obj)
	}
	return "other"
}

// flatSplit makes f's built body, when f has the flat convention, its
// worker's, and f's body the wrapper: the worker's call, a failure where
// it failed, its value else. A body with a return flatWorker does not
// know keeps its own, and flatEnd writes the trivial worker. slots are
// the Go parameters' local slots.
func (b *irBuilder) flatSplit(f *check.Func, name string, slots int) {
	if b.law != nil || b.own != nil || len(b.fc.witness) > 0 {
		return
	}
	fl := b.flatResOf(f)
	if fl == nil || fl.name != name || fl.built || slots != fl.params {
		return
	}
	body, ok := b.flatWorker(fl, b.fn.Body)
	if !ok {
		return
	}
	worker := *b.fn
	worker.Name, worker.Body = fl.worker, body
	b.st.out.Funcs = append(b.st.out.Funcs, worker)
	fl.built = true
	b.st.report.FlatWorkers++
	p, end := b.fn.Pos, b.fn.End
	b.fn.Locals = nil
	b.next = fl.params
	r := b.temp("res", fl.typ, p)
	args := make([]*irExpr, fl.params, fl.params+1)
	for i := range args {
		args[i] = &irExpr{K: "local", Pos: p, I: i}
	}
	args = append(args, &irExpr{K: "addr", Pos: p, X: &irExpr{K: "local", Pos: p, I: r}})
	b.fn.Body = []*irStmt{
		{K: "var", Pos: p, Local: r},
		{K: "if", Pos: p, End: p, X: &irExpr{K: "call", Pos: p, Name: fl.worker, Args: args}, Then: []*irStmt{{K: "fail", Pos: p, X: flatField(r, "Err", p)}}},
	}
	if fl.unit {
		b.fn.Body = append(b.fn.Body, &irStmt{K: "ret", Pos: end})
	} else {
		b.fn.Body = append(b.fn.Body, &irStmt{K: "ret", Pos: end, X: flatField(r, "Val", end)})
	}
}

// flatWorker rewrites each return and failure of body, outside its
// function literals, into the worker's: the out struct written, then the
// flag returned. The out pointer is the worker's last parameter, so every
// local after the parameters moves up one slot. ok is false, and body
// unchanged, when a return's form is not recorded (formed).
func (b *irBuilder) flatWorker(fl *flatRes, body []*irStmt) ([]*irStmt, bool) {
	known := true
	irStmtsEach(body, func(s *irStmt) {
		if s.K != "ret" && s.K != "fail" {
			return
		}
		form, ok := b.flatRets[s]
		switch {
		case !ok:
			known = false
		case form == "unit" && s.X != nil, form != "unit" && s.X == nil:
			known = false
		case strings.HasPrefix(form, "fail:") && form != "fail:error" && form != "fail:"+failKey(fl.e):
			known = false
		}
	})
	// A frame's deferred End(&err) settles the function's named error
	// result ("result", ir_own.go): the worker's one result is its flag,
	// so such a body keeps its own form, as the inliner keeps it
	// (ir_forms_inline.go).
	irWalk(body, false, func(*irStmt, bool) {}, func(e *irExpr, _ bool) {
		if e.K == "result" {
			known = false
		}
	})
	if !known {
		return body, false
	}
	// The worker's body is a copy: a node the builder shares with another
	// body (a hoisted value, a cached read) keeps its slots there.
	body = b.flatClone(body)
	irShiftLocals(body, fl.params)
	out := fl.params // the out pointer's slot
	var walk func(list []*irStmt) []*irStmt
	walk = func(list []*irStmt) []*irStmt {
		var res []*irStmt
		for _, s := range list {
			if s.K == "ret" || s.K == "fail" {
				res = append(res, b.flatReturn(fl, s, out)...)
				continue
			}
			s.Then, s.Else, s.Body = walk(s.Then), walk(s.Else), walk(s.Body)
			for _, c := range s.Cases {
				c.Body = walk(c.Body)
			}
			res = append(res, s)
		}
		return res
	}
	body = walk(body)
	if !irListTerminates(body) {
		body = append(body, flatFlag(false, b.fn.End))
	}
	return body, true
}

// irStmtsEach calls f on each statement of list, outside function
// literals.
func irStmtsEach(list []*irStmt, f func(*irStmt)) {
	for _, s := range list {
		f(s)
		irStmtsEach(s.Then, f)
		irStmtsEach(s.Else, f)
		irStmtsEach(s.Body, f)
		for _, c := range s.Cases {
			irStmtsEach(c.Body, f)
		}
	}
}

// irShiftLocals moves every local slot at or after from up one, in the
// statements and expressions of body, function literals included. The
// builder shares nodes (an environment's read, a hoisted value), so each
// slot moves once, by its address.
func irShiftLocals(body []*irStmt, from int) {
	moved := map[*int]bool{}
	shift := func(i *int) {
		if *i >= from && !moved[i] {
			moved[i] = true
			*i++
		}
	}
	var expr func(x *irExpr)
	var stmts func(list []*irStmt)
	expr = func(x *irExpr) {
		if x == nil {
			return
		}
		if x.K == "local" {
			shift(&x.I)
		}
		for i := range x.Params {
			shift(&x.Params[i])
		}
		expr(x.X)
		expr(x.Y)
		for _, a := range x.Args {
			expr(a)
		}
		for _, e := range x.Elems {
			expr(e)
		}
		stmts(x.Body)
	}
	stmts = func(list []*irStmt) {
		for _, s := range list {
			shift(&s.Local) // a kind with no local ignores it
			if s.Key != nil {
				shift(s.Key)
			}
			if s.Val != nil {
				shift(s.Val)
			}
			for i := range s.Ls {
				if s.Ls[i] >= 0 {
					shift(&s.Ls[i])
				}
			}
			expr(s.X)
			expr(s.Y)
			stmts(s.Then)
			stmts(s.Else)
			stmts(s.Body)
			for _, c := range s.Cases {
				for _, v := range c.Vals {
					expr(v)
				}
				stmts(c.Body)
			}
		}
	}
	stmts(body)
}

// flatClone is a deep copy of list, each copied return keeping its
// recorded form (formed).
func (b *irBuilder) flatClone(list []*irStmt) []*irStmt {
	var stmts func([]*irStmt) []*irStmt
	var expr func(*irExpr) *irExpr
	ints := func(p *int) *int {
		if p == nil {
			return nil
		}
		v := *p
		return &v
	}
	exprs := func(xs []*irExpr) []*irExpr {
		if xs == nil {
			return nil
		}
		out := make([]*irExpr, len(xs))
		for i, x := range xs {
			out[i] = expr(x)
		}
		return out
	}
	expr = func(x *irExpr) *irExpr {
		if x == nil {
			return nil
		}
		c := *x
		c.X, c.Y, c.Args, c.Elems, c.Body = expr(x.X), expr(x.Y), exprs(x.Args), exprs(x.Elems), stmts(x.Body)
		c.Params = append([]int(nil), x.Params...)
		c.Keys = append([]string(nil), x.Keys...)
		if x.Params == nil {
			c.Params = nil
		}
		if x.Keys == nil {
			c.Keys = nil
		}
		if x.End != nil {
			e := *x.End
			c.End = &e
		}
		return &c
	}
	stmts = func(list []*irStmt) []*irStmt {
		if list == nil {
			return nil
		}
		out := make([]*irStmt, len(list))
		for i, s := range list {
			c := *s
			c.Key, c.Val = ints(s.Key), ints(s.Val)
			if s.Ls != nil {
				c.Ls = append([]int(nil), s.Ls...)
			}
			c.X, c.Y = expr(s.X), expr(s.Y)
			c.Then, c.Else, c.Body = stmts(s.Then), stmts(s.Else), stmts(s.Body)
			if s.Cases != nil {
				c.Cases = make([]*irCase, len(s.Cases))
				for j, k := range s.Cases {
					kc := *k
					kc.Vals, kc.Body = exprs(k.Vals), stmts(k.Body)
					kc.Types = append([]string(nil), k.Types...)
					if k.Types == nil {
						kc.Types = nil
					}
					c.Cases[j] = &kc
				}
			}
			if form, ok := b.flatRets[s]; ok {
				b.flatRets[&c] = form
			}
			out[i] = &c
		}
		return out
	}
	return stmts(list)
}

// flatFlag is the worker's return: whether it failed.
func flatFlag(failed bool, p irPos) *irStmt {
	return &irStmt{K: "ret", Pos: p, X: &irExpr{K: "const", Pos: p, T: "bool", V: strconv.FormatBool(failed)}}
}

// flatStore is out.name = x, out the worker's out pointer.
func flatStore(out int, name string, x *irExpr, p irPos) *irStmt {
	return &irStmt{K: "store", Pos: p, X: flatField(out, name, p), Y: x}
}

// flatReturn is s, a recorded return or failure, in the worker.
func (b *irBuilder) flatReturn(fl *flatRes, s *irStmt, out int) []*irStmt {
	p := s.Pos
	asE := func(x *irExpr) *irExpr { return &irExpr{K: "assert", Pos: p, T: fl.err, X: x} }
	switch form := b.flatRets[s]; form {
	case "val":
		return []*irStmt{flatStore(out, "Val", s.X, p), flatFlag(false, p)}
	case "unit":
		return []*irStmt{flatFlag(false, p)}
	case "fail:error":
		return []*irStmt{flatStore(out, "Err", asE(s.X), p), flatFlag(true, p)}
	case "fail:" + failKey(fl.e):
		return []*irStmt{flatStore(out, "Err", s.X, p), flatFlag(true, p)}
	}
	// "tuple": the fallible call's value and error, then the worker's.
	// Its new locals take the next slots, after the out pointer's.
	b.next++
	e := b.temp("err", "error", p)
	errLocal := &irExpr{K: "local", Pos: p, I: e}
	failed := &irStmt{K: "if", Pos: p, End: p, X: b.notNil(e, p), Then: []*irStmt{flatStore(out, "Err", asE(errLocal), p), flatFlag(true, p)}}
	var res []*irStmt
	if fl.unit {
		res = []*irStmt{{K: "def", Pos: p, Local: e, X: s.X}, failed, flatFlag(false, p)}
	} else {
		v := b.temp("v", fl.val, p)
		res = []*irStmt{{K: "def2", Pos: p, Local: v, Val: &e, X: s.X}, failed, flatStore(out, "Val", &irExpr{K: "local", Pos: p, I: v}, p), flatFlag(false, p)}
	}
	b.next--
	return res
}

// flatEnd ends the flat convention of a lowering: the trivial worker of
// each function a call site chose that has no worker of its own body
// (flatSplit), each worker's Go head beside its wrapper's, and the out
// struct's type, appended to main.go's code.
func (st *irState) flatEnd(l *lowerer, code []byte) []byte {
	var fls []*flatRes
	for _, fl := range st.flat {
		if fl != nil && (fl.used || fl.built) {
			fls = append(fls, fl)
		}
	}
	if len(fls) == 0 {
		return code
	}
	sort.Slice(fls, func(i, j int) bool { return fls[i].worker < fls[j].worker })
	src := string(code)
	for _, fl := range fls {
		// The out parameter's name is none of the worker body's locals:
		// a library's Go text (LibraryText) declares them by name.
		var locals []string
		for _, fn := range st.out.Funcs {
			if fn.Name == fl.worker {
				for _, lc := range fn.Locals {
					locals = append(locals, lc.Name)
				}
			}
		}
		head, params, ok := flatHead(src, fl, locals)
		if !ok {
			ice(fl.f.Decl, "the flat function %s has no Go head %s in main.go", fl.f.Name, fl.name)
		}
		if !fl.built {
			st.trivialWorker(l, fl, params)
		}
		code = append(code, []byte("\n"+head+"\n")...)
	}
	code = append(code, []byte("\n// "+st.flatType+" is a flat function's value or failure, written by its worker (IR-FORMS IRF-2).\ntype "+st.flatType+"[V, E any] struct {\n\tVal V\n\tErr E\n}\n")...)
	return code
}

// flatHead is the worker's Go head, written from the wrapper's in src,
// and the names taken: the wrapper's parameters, the worker's locals and
// the out parameter.
func flatHead(src string, fl *flatRes, locals []string) (string, map[string]bool, bool) {
	re := regexp.MustCompile(`(?m)^(/\*line [^\n]*?\*/)?func ` + regexp.QuoteMeta(fl.name) + `\(.*$`)
	line := re.FindString(src)
	if line == "" {
		return "", nil, false
	}
	at := strings.Index(line, "func "+fl.name+"(")
	text := strings.TrimSuffix(strings.TrimSpace(line[at:]), "{")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", "package p\n"+text+" {}\n", 0)
	if err != nil || len(file.Decls) != 1 {
		return "", nil, false
	}
	fd, ok := file.Decls[0].(*ast.FuncDecl)
	if !ok {
		return "", nil, false
	}
	off := len("package p\n")
	from, to := fset.Position(fd.Type.Params.Opening).Offset-off, fset.Position(fd.Type.Params.Closing).Offset-off
	names := map[string]bool{}
	for _, f := range fd.Type.Params.List {
		for _, n := range f.Names {
			names[n.Name] = true
		}
	}
	for _, n := range locals {
		names[n] = true
	}
	out := freeName("out", names)
	params := strings.TrimSpace(text[from+1 : to])
	if params != "" {
		params += ", "
	}
	return line[:at] + "func " + fl.worker + "(" + params + out + " *" + fl.text + ") bool", names, true
}

// freeName is base, or base_N, not in taken; it takes it.
func freeName(base string, taken map[string]bool) string {
	n := base
	for i := 1; taken[n]; i++ {
		n = base + "_" + strconv.Itoa(i)
	}
	taken[n] = true
	return n
}

// trivialWorker writes fl's worker as a call of its wrapper: its error,
// when it has one, is the failure E.
func (st *irState) trivialWorker(l *lowerer, fl *flatRes, params map[string]bool) {
	b := &irBuilder{l: l, st: st, env: -1}
	d := fl.f.Decl
	p, end := b.pos(d, d.Pos()), b.pos(d, d.Body.Rbrace)
	fn := &irFunc{Name: fl.worker, Pos: p, End: end}
	args := make([]*irExpr, fl.params)
	for i := range args {
		args[i] = &irExpr{K: "local", Pos: p, I: i}
	}
	call := &irExpr{K: "call", Pos: p, Name: fl.name, Args: args}
	out := fl.params
	v, e := out+1, out+1
	if !fl.unit {
		fn.Locals = append(fn.Locals, irLocal{Name: freeName("v", params), Type: fl.val, Pos: p})
		e++
	}
	fn.Locals = append(fn.Locals, irLocal{Name: freeName("err", params), Type: "error", Pos: p})
	errLocal := &irExpr{K: "local", Pos: p, I: e}
	failed := &irStmt{K: "if", Pos: p, End: p, X: &irExpr{K: "bin", Pos: p, Op: "!=", X: errLocal, Y: &irExpr{K: "nil", Pos: p, T: "error"}},
		Then: []*irStmt{flatStore(out, "Err", &irExpr{K: "assert", Pos: p, T: fl.err, X: errLocal}, p), flatFlag(true, p)}}
	if fl.unit {
		fn.Body = []*irStmt{{K: "def", Pos: p, Local: e, X: call}, failed, flatFlag(false, end)}
	} else {
		fn.Body = []*irStmt{{K: "def2", Pos: p, Local: v, Val: &e, X: call}, failed, flatStore(out, "Val", &irExpr{K: "local", Pos: end, I: v}, end), flatFlag(false, end)}
	}
	st.out.Funcs = append(st.out.Funcs, *fn)
}
