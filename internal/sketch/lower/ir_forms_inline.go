package lower

// IR builder: Ergo-side inlining on the IR (RED17). See inline.go.
//
// The text path writes a small Ergo body at its call site itself (OPT4a,
// inline.go): Go's inliner refuses a body past 80 of its cost units, and
// a generic shape body pays for its dictionary at every call it makes.
// lru's Cache.Get calls nodeAt (Go cost 102), touch (261) and, through
// touch, unlink (274); the text path writes all three into Get, and the
// IR, which called them, ran lru at 1.26x the text path (RED17,
// IR2-FLIP/RED17/inline-ir1.md).
//
// The IR does the same with the same decision, after every body is built:
//   - the builder marks a call (inlineMark) that inline.go's rule admits
//     (inlineBody, pays, the same budgets), of a function with an Ergo
//     body whose own IR body is built: one result at most, no callback
//     parameter (IRF-3b places a known function argument), the site in
//     the program's sources (its call is in the call table); a callee
//     with a failure row only where its call binds a value and an error
//     (def2) or is returned as it is;
//   - the pass (inlineBodies) replaces each marked call that is a whole
//     statement (`x := f(…)`, `x = f(…)`, `x, err := f(…)`, `f(…)`) with
//     a copy of the callee's IR body in a block: each parameter bound to
//     its operand (a local the body only reads, or a pointer it stores
//     through, stands for itself), the callee's locals and labels renamed
//     apart, its type parameters replaced by the call's type arguments,
//     each return an assignment and a goto past the block (the block's
//     last return falls through), each failure an assignment of the
//     site's error and the same goto (a failure that is a local or a
//     parameter of the callee, a try's error passed on or a failure held
//     in a let, by its Go type: an error as it is, a failure type
//     converted to error, INLINE-FAIL-LOCAL; a parameter used only as a
//     failure whose argument is a variant literal of constants, locals
//     and their fields stands for that literal, so the failure is built
//     in place and jumps to its arm, when nothing between the call and
//     the failure can write what the literal reads: no later argument
//     and nothing in the body calls, stores or takes an address,
//     INLINE-FAILARG; a call of a generic function's copy for its type
//     arguments, mono.go, is copied too, ir_call.go); a return of a fallible call's
//     value and error as they are (`return f(…)`, marked Pair) binds
//     both to temporaries the site's value and error then take. A call
//     that is itself returned (`return f(…)`, outside a function
//     literal) is replaced the same way, but the copy's returns and
//     failures stay the frame's own: no assignment, no goto
//     (INLINE-RETURN). Copied into a def2 site, such a copy's failures
//     become the site's and so still jump to its catch arm, through a
//     chain of tail calls. The copy's own marked calls are
//     inlined in it, at most inlineDepth deep and inlineGrowth body nodes
//     per function, as the text path's growth rule allows;
//   - a value lands in place, as the text path yields a call's value to
//     its destination (a record of five words or more is a memory copy
//     in Go): at `x := f(…)` the local f returns is x itself
//     (irRenames), and a copy `a := b` or `a = b` of a body the pass
//     changed, b read nowhere after, is dropped for b written as a
//     (coalesce). The builder's catch whose arms all leave yields the
//     call's own local, with no caught copy (ir_fail.go).
//
// Defects stay exact (runtime.md, Exit causes 6): the copy's positions
// name the callee's file with the inline site's tag (path#ergo-inline-N),
// and the site joins the program's inline table (v1.RegisterInlines), as
// the text path's //line directives tag them, so the report writes the
// frames the calls would have had. A scope's open and close in the copy
// take the site's position (irScopesIncrease: Go's DWARF scope marks
// order positions by file name, and a tagged name sorts after the
// caller's).
//
// The reference is irInlineLowering "off"
// (-ldflags=-X=ergo.local/prototype/internal/sketch/lower.irInlineLowering=off):
// every call stays a call. irInlineReturnLowering "off" keeps only the
// INLINE-RETURN cases calls: a call in return position, and a fallible
// body that returns a fallible call's pair. irFailLocalLowering "off"
// keeps a fallible body whose failure is a local or a parameter a call.
// irFailArgLowering "off" binds every failure argument at the copy's head
// and keeps a try's check after a copy (failThrough).
//
// Prior art: OCaml's flambda and GHC inline by size on their own IR before
// the backend; ReScript inlines small functions across modules before it
// writes JavaScript. Gleam and Elixir leave inlining to the host (the BEAM
// JIT, a JavaScript engine), as the IR did before this pass.

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

var irInlineLowering string

// IRInlineOn reports that the IR inlines (the property's reach check).
func IRInlineOn() bool { return irInlineLowering != "off" }

// irInlineReturnLowering is "off" for the reference (INLINE-RETURN): a
// call in return position stays a call, and a fallible body that returns
// a fallible call's value and error as they are is not copied.
var irInlineReturnLowering string

// IRInlineReturnOn reports that calls in return position are inlined
// (the property's reach check).
func IRInlineReturnOn() bool { return irInlineReturnLowering != "off" }

// SetInlineReturnLoweringForTest sets irInlineReturnLowering ("" or
// "off") and returns the previous mode, for a property that lowers the
// same program both ways in one process.
func SetInlineReturnLoweringForTest(mode string) string {
	prev := irInlineReturnLowering
	irInlineReturnLowering = mode
	return prev
}

// irFailLocalLowering is "off" for the reference (INLINE-FAIL-LOCAL): a
// fallible callee one of whose failures is a local or a parameter (a
// try's error passed on, a failure held in a let) is not copied into a
// site that binds a value and an error.
var irFailLocalLowering string

// IRFailLocalOn reports that such callees are copied (the property's
// reach check).
func IRFailLocalOn() bool { return irFailLocalLowering != "off" }

// SetFailLocalLoweringForTest sets irFailLocalLowering ("" or "off") and
// returns the previous mode.
func SetFailLocalLoweringForTest(mode string) string {
	prev := irFailLocalLowering
	irFailLocalLowering = mode
	return prev
}

// irFailArgLowering is "off" for the reference (INLINE-FAILARG): a
// parameter used only as a failure is bound to its argument at the
// copy's head, as any other, and its failure is set from that local.
var irFailArgLowering string

// IRFailArgOn reports that failure parameters stand for their literal
// arguments (the property's reach check).
func IRFailArgOn() bool { return irFailArgLowering != "off" }

// SetFailArgLoweringForTest sets irFailArgLowering ("" or "off") and
// returns the previous mode.
func SetFailArgLoweringForTest(mode string) string {
	prev := irFailArgLowering
	irFailArgLowering = mode
	return prev
}

// irInline is the pass's state: the marked calls and every built body's
// parameters.
type irInline struct {
	sites  map[*irExpr]*irInlSite
	bodies map[string]*irInlBody
	anon   int // the anon copies made, for unique names
}

// irInlSite is a marked call.
type irInlSite struct {
	f           *check.Func
	key         string // the callee's IR function name
	src, offset int    // the call's identity in the call table
	tsub        map[*check.TypeParam]string
	// conc holds the Slot rows the call binds to a struct (FAILJUMP-TYPED).
	conc map[*check.TypeParam]bool
	// slotOf maps a Slot row the call passes the caller's own Slot row
	// to onto that row.
	slotOf map[*check.TypeParam]*check.TypeParam
	size   int
	loop   bool // on a loop path at its site
	leaf   bool // a Go leaf with a boxed result (pays)
	// sourced is a callee in the program's sources; a std body's copy
	// takes the site's position, as the text path writes it with no line
	// directive of its own (inline.go closeInline).
	sourced bool
	// fallible is a callee with a failure row that fails at the site: its
	// call binds a value and an error (def2).
	fallible bool
	// result is the call's Go type, "" when the builder does not spell
	// it: a stored call's temporary (storedCall).
	result string
	// anon is a call in a library body: its copy adds no inline site.
	anon bool
	// known is a call of a known variant, its callbacks known pure
	// functions bound at the copy's head (KNOWN-FILTER-INLINE).
	known bool
	// failLeaf is a fallible leaf inlined for its failures' jumps
	// (FAIL-INLINE): it pays wherever it is.
	failLeaf bool
	// slot is a callee with a Slot row, bound at the call (INLINE-SLOT).
	slot bool
}

// irStdInlLowering is "off" for the reference: a call in a library body
// is left to Go's inliner (IR-STDINL).
var irStdInlLowering string

// IRStdInlOn reports that calls in library bodies are inlined (the
// property's reach check).
func IRStdInlOn() bool { return irStdInlLowering != "off" }

// irInlBody is a built body's frame: its parameter slots, each with its
// Go type ("" when unknown: the slot is only ever passed a local), and
// its type parameters' spellings.
type irInlBody struct {
	f      *check.Func
	params []string
	tps    map[*check.TypeParam]string
	env    int // the environment's slot, or -1: a pointer no body writes
}

// inlState makes st's inliner state once.
func (st *irState) inlState() {
	if st.inl == nil {
		st.inl = &irInline{sites: map[*irExpr]*irInlSite{}, bodies: map[string]*irInlBody{}}
	}
}

// inlineCallee records the frame of b's body, built for fd (ir.go body).
func (st *irState) inlineCallee(b *irBuilder, fd *funcDecl, params []*check.Var) {
	if st.census != nil || irInlineLowering == "off" {
		return
	}
	st.inlState()
	// Every Go parameter slot: the parameters, the environment and the
	// witnesses (ir_witness.go), whose type stays "" (no copy binds one).
	n := b.slots
	body := &irInlBody{f: fd.fn, params: make([]string, n), tps: map[*check.TypeParam]string{}, env: b.env}
	for tp, name := range b.fc.params {
		body.tps[tp] = b.spelling(name)
	}
	for i, v := range params {
		l, ok := b.locals[v]
		if v == nil || !ok || l >= n {
			continue
		}
		body.params[l] = irTypeOrNone(func() string {
			if b.fc.plain[v] {
				// A known variant's callback is a plain Go function
				// (ir_kcb.go plainSigType), not the declared signature.
				return b.plainSigType(v.Type.(*check.Signature), fd.fn.Decl)
			}
			t := b.goType(v.Type, fd.fn.Decl)
			if i == 0 && fd.method != nil && fd.ptr && !strings.HasPrefix(t, "*") {
				t = "*" + t
			}
			return t
		})
	}
	st.inl.bodies[b.fn.Name] = body
}

// knownInline reports whether a call of f's known variant may be copied
// at its site (KNOWN-FILTER-INLINE): a body within the inline budget that
// opens no scope of its own (bodyNeedsFrame, as inlineRule refuses). A
// variant takes no owned parameter and is never recursive (knownBody
// refuses both: a call of itself is decided while it is deciding). Its
// callbacks are known pure at the site and its other calls are those
// knownBody admits, so nothing in the copy re-enters the frame; splice's
// irInlinable then checks the built body's shape.
func (l *lowerer) knownInline(f *check.Func) bool {
	d := f.Decl
	if d == nil || d.Body == nil || f.Pkg == nil || f.Pkg.Info == nil || inlineSize(d.Body) > inlineBudget {
		return false
	}
	saved := l.info
	l.info = f.Pkg.Info
	frame := l.bodyNeedsFrame(d.Body.List)
	l.info = saved
	return !frame
}

// irTypeOrNone is f's type, or "" where the builder refuses it.
func irTypeOrNone(f func() string) (t string) {
	defer func() {
		if r := recover(); r != nil {
			if _, no := r.(irUnsupported); !no {
				panic(r)
			}
			t = ""
		}
	}()
	return f()
}

// inlineMark marks e, the call x of f (fd), for the pass when the rule
// admits it; it returns e.
func (b *irBuilder) inlineMark(x *syntax.CallExpr, c *check.Call, fd *funcDecl, e *irExpr) *irExpr {
	st := b.st
	if st.census != nil || irInlineLowering == "off" || e == nil || inlineLowering == "call" {
		return e
	}
	st.inlState()
	f := c.Func
	// A call of the known variant (ir_kcb.go knownCall): its callbacks are
	// known pure functions, built plain at the site, so its copy binds
	// them as it binds any operand (KNOWN-FILTER-INLINE). A call of the
	// function itself passes callbacks the copy cannot know.
	known := fd.known != "" && e.Name == fd.known
	if f == nil || x.Trailing != nil || c.StartsTask || fd.fallible && (b.dropsErr[e] || len(f.Sig.Results) != 1) || len(f.Sig.Results) > 1 || len(callbackParams(f)) > 0 && !known {
		return e
	}
	// A call in a library body has no identity (calls.go identify): its
	// copy is no inline site of the table, as the text path's site0 adds
	// none, and the callee must be a library body too, whose copy takes
	// the position of the call (IR-STDINL).
	anon := !b.fc.directives
	if f.Pkg == nil || !x.Pos().IsValid() || anon && (irStdInlLowering == "off" || b.l.isSourced(f.Pkg)) {
		return e
	}
	// A fallible callee Go would inline itself (a leaf) is the IR's to
	// copy too: its failures then jump to the catch arm that takes them,
	// with no error value (FAIL-INLINE, ir_forms_failjump.go).
	failLeaf := false
	if known {
		// The text path's rule (inlineRule) asks whether a literal can be
		// substituted into the uniform body; the variant's callbacks are
		// plain Go values of the instance's types, bound as operands.
		if !b.l.knownInline(f) {
			return e
		}
	} else if !b.l.inlineSlotBody(f) {
		if !fd.fallible || irFailJumpLowering == "off" || !b.l.inlineWithin(f, -inlineBudget) {
			return e
		}
		failLeaf = true
	}
	// The variant's footprint is its callbacks' rows, empty at a known
	// site (knownBody admits no other effect).
	if fp := f.Pkg.Info.Footprints[f.Decl]; !fp.Empty() && !b.l.knownClosed(f) && !known {
		return e
	}
	var conc map[*check.TypeParam]bool
	var slotOf map[*check.TypeParam]*check.TypeParam
	tsub := map[*check.TypeParam]string{}
	for i, tp := range f.Sig.TParams {
		if !b.l.goParam(tp) {
			continue
		}
		if i >= len(c.TypeArgs) || tp.Kind != check.KindType && (!tp.Slot || irInlineSlotLowering == "off") {
			return e
		}
		if tp.Kind == check.KindType {
			tsub[tp] = b.goArg(tp, c.TypeArgs[i], x)
			continue
		}
		// A Slot row whose Go type is not known at the call (a row
		// parameter of the caller) keeps the call (INLINE-SLOT).
		a := c.TypeArgs[i]
		t := irTypeOrNone(func() string { return b.goArg(tp, a, x) })
		if t == "" {
			return e
		}
		tsub[tp] = t
		if irStructFailure(a) {
			if conc == nil {
				conc = map[*check.TypeParam]bool{}
			}
			conc[tp] = true
		} else if otp := irSlotParam(a); otp != nil {
			if slotOf == nil {
				slotOf = map[*check.TypeParam]*check.TypeParam{}
			}
			slotOf[tp] = otp
		}
	}
	// The body's name: a method's is its type's and its own; a
	// monomorphic copy of a method is a plain function (ir.go body).
	key := e.Name
	if fd.method != nil && e.K == "mcall" {
		key = fd.method.name + "." + e.Name
	}
	src := -1 // a call in a library body has no identity
	if !anon {
		src, _, _, _ = b.l.at(x, x.Pos())
	}
	site := &irInlSite{
		f: f, key: key, src: src, offset: int(x.Pos()), tsub: tsub, conc: conc, slotOf: slotOf,
		size: inlineSize(f.Decl.Body), loop: b.loops > 0 || b.fc.hot,
		leaf: goLeaf(f), sourced: b.l.isSourced(f.Pkg), fallible: fd.fallible, anon: anon, failLeaf: failLeaf,
		slot: b.l.hasGoRow(f.Sig), known: known,
	}
	if t := b.l.info.Types[x]; t != nil && len(f.Sig.Results) == 1 {
		site.result = irTypeOrNone(func() string { return b.goType(t, x) })
	}
	st.inl.sites[e] = site
	return e
}

// pays is inline.go's pays for a site, on a loop path or not.
func (s *irInlSite) pays(loop bool) bool {
	// A known variant Go declines to inline costs a call and an indirect
	// call of its callback per use; its copy costs neither.
	if inlineCostLowering == "budget" || s.failLeaf || s.known {
		return true
	}
	big := s.size > goSizeBudget
	if s.leaf {
		return big && loop
	}
	return !big || loop
}

// inlineBodies inlines the marked calls of every IR body (program, before
// the call table, which writes the inline table).
func (st *irState) inlineBodies(l *lowerer) {
	if st.inl == nil || st.census != nil || len(st.inl.sites) == 0 {
		return
	}
	orig := map[string]*irFunc{}
	for i := range st.out.Funcs {
		orig[st.out.Funcs[i].Name] = &irFunc{Name: st.out.Funcs[i].Name, Pos: st.out.Funcs[i].Pos, End: st.out.Funcs[i].End, Locals: st.out.Funcs[i].Locals, Body: st.out.Funcs[i].Body}
	}
	for i := range st.out.Funcs {
		fn := &st.out.Funcs[i]
		frame := st.inl.bodies[fn.Name]
		if frame == nil {
			continue
		}
		in := &irInliner{st: st, l: l, orig: orig, fn: fn, np: len(frame.params), frame: frame, names: map[string]bool{}, tagged: map[int]bool{}}
		for _, lc := range fn.Locals {
			in.names[lc.Name] = true
		}
		body := in.stmts(in.clone(fn.Body, nil), irPath{})
		if in.count == 0 {
			continue
		}
		body = in.coalesce(body)
		// After coalesce: a copy's result local is then the match's own
		// subject, which the copy's returns set (ir_forms_variant.go).
		body = in.knownVariants(body)
		body = in.failJumps(body)   // ir_forms_failjump.go
		body = in.storeFields(body) // ir_forms_store.go
		body = in.deadZeros(body)   // ir_forms_zero.go
		fn.Locals = in.locals(fn.Locals)
		fn.Body = irScopesIncreaseTagged(irUseLocals(body), in.tagged, fn.Pos)
		st.report.Inlined += in.count
	}
}

// irInliner inlines the marked calls of one body, fn.
type irInliner struct {
	st     *irState
	l      *lowerer
	orig   map[string]*irFunc
	fn     *irFunc
	np     int        // fn's parameter slots
	frame  *irInlBody // fn's parameters' types (slotType)
	extra  []irLocal
	names  map[string]bool
	tagged map[int]bool
	count  int
	known  int // the known-variant sites rewritten, for unique labels (ir_forms_variant.go)
	jumps  int // the checks failJumps rewrote, for unique labels (ir_forms_failjump.go)
}

// slotType is the Go type of fn's slot i, a parameter's or a local's: ""
// for the environment's slot or one the builder did not spell.
func (in *irInliner) slotType(i int) string {
	if i < in.np {
		if in.frame == nil || i == in.frame.env || i >= len(in.frame.params) {
			return ""
		}
		return in.frame.params[i]
	}
	return in.localType(i)
}

func (in *irInliner) locals(base []irLocal) []irLocal {
	return append(append([]irLocal(nil), base...), in.extra...)
}

// newLocal adds a local to fn and returns its index.
func (in *irInliner) newLocal(name, t string, p irPos) int {
	for in.names[name] {
		name += "_"
	}
	in.names[name] = true
	in.extra = append(in.extra, irLocal{Name: name, Type: t, Pos: p})
	return in.np + len(in.fn.Locals) + len(in.extra) - 1
}

// irPath is where a statement list is (growth.go's rule on the IR): the
// inline site (plus one) it is in and that site's depth, whether a loop
// runs it, the growth its path has taken, and whether it is cold.
type irPath struct {
	parent, depth int
	loop, cold    bool
	spent         int
	lit           bool // in a function literal's body: its return is the literal's
}

// stmts inlines the marked whole-statement calls of list, on the path p
// opens for it: a list that ends in a defect or a failure is cold, and a
// site there is left to Go, as growth.go leaves it.
func (in *irInliner) stmts(list []*irStmt, p irPath) []*irStmt {
	if n := len(list); n > 0 && (list[n-1].K == "panic" || list[n-1].K == "fail") {
		p.cold = true
	}
	var out []*irStmt
	for _, s := range list {
		in.nested(s, p)
		if spliced := in.splice(s, &p); spliced != nil {
			out = append(out, spliced...)
			continue
		}
		if spliced := in.storedCall(s, &p); spliced != nil {
			out = append(out, spliced...)
			continue
		}
		out = append(out, s)
	}
	return out
}

// nested inlines inside s's own statement lists and literals.
func (in *irInliner) nested(s *irStmt, p irPath) {
	inner := p
	inner.loop = p.loop || s.K == "while" || s.K == "count" || s.K == "range"
	s.Then = in.stmts(s.Then, p)
	s.Else = in.stmts(s.Else, p)
	s.Body = in.stmts(s.Body, inner)
	for _, c := range s.Cases {
		c.Body = in.stmts(c.Body, p)
	}
	lit := p
	lit.lit = true
	irLiterals(s, func(e *irExpr) {
		e.Body = in.stmts(e.Body, lit)
	})
}

// splice is s with its marked call inlined, or nil; it spends the body's
// size on p.
func (in *irInliner) splice(s *irStmt, p *irPath) []*irStmt {
	if s.X == nil || s.Op != "" || p.depth >= inlineDepth || p.cold {
		return nil
	}
	mode := s.K
	// A call in return position (INLINE-RETURN): the copy's returns and
	// failures are the frame's own, so they stay as they are; not in a
	// function literal, whose frame has its own return convention.
	ret := mode == "ret" && irInlineReturnLowering != "off" && !p.lit
	if mode != "def" && mode != "set" && mode != "expr" && (mode != "def2" || s.Val == nil) && !ret {
		return nil
	}
	site := in.st.inl.sites[s.X]
	if site != nil && !ret && site.fallible != (mode == "def2") {
		return nil
	}
	if site == nil || !site.pays(site.loop || p.loop) || p.spent+site.size > inlineGrowth || ret && site.fallible != s.Pair {
		return nil
	}
	callee, frame := in.orig[site.key], in.st.inl.bodies[site.key]
	if callee == nil || frame == nil || frame.f != site.f {
		return nil
	}
	args := s.X.Args
	if s.X.K == "mcall" {
		args = append([]*irExpr{s.X.X}, args...)
	} else if s.X.K != "call" {
		return nil
	}
	if len(args) != len(frame.params) || !irInlinable(callee.Body, len(site.f.Sig.Results) == 1, site.fallible) {
		return nil
	}
	if mode != "expr" && len(site.f.Sig.Results) != 1 {
		return nil
	}
	if mode == "def2" && irInlineReturnLowering != "off" && in.localType(s.Local) == "" {
		return nil // a returned pair's temporary takes the site's type (irRets)
	}
	n := len(in.l.inlineSites)
	id, parent := strconv.Itoa(n), n+1 // the copy's names; the site its lines are in
	if site.anon {
		// No site of the table: its lines stay in the enclosing site.
		id, parent = "s"+strconv.Itoa(in.st.inl.anon), p.parent
		in.st.inl.anon++
	}
	tag := inlineTag + id
	files := map[int]int{}
	file := func(k int) int {
		if j, ok := files[k]; ok {
			return j
		}
		path := in.st.out.Files[k] + tag
		j, ok := in.st.files[path]
		if !ok {
			j = len(in.st.out.Files)
			in.st.files[path] = j
			in.st.out.Files = append(in.st.out.Files, path)
		}
		files[k] = j
		in.tagged[j] = true
		return j
	}
	at := s.Pos
	// The parameters: a local the body only reads stands for itself.
	np := len(frame.params)
	subst := map[int]*irExpr{}
	failArgs := 0
	var binds []*irStmt
	for i, a := range args {
		t := frame.params[i]
		if i == frame.env {
			t = "*"
		}
		if a.K == "local" && irReadOnly(callee.Body, i, t) {
			subst[i] = a
			continue
		}
		// A parameter used only as a failure, whose argument is a variant
		// literal of constants, locals and their fields, stands for that
		// argument: the failure is built in place, so its arm takes it
		// with no error value (failJumps). The argument is then evaluated
		// where the copy fails, not at the call, so nothing run in between
		// may write a slot it reads: the later arguments, bound at the
		// copy's head, call nothing (irNoCalls), and the body calls
		// nothing, stores nothing and takes no address (irFrameOnly), so
		// it writes only its own renamed locals and the temporaries of
		// parameters it writes (INLINE-FAILARG).
		if irFailArgLowering != "off" && irFailOnly(callee.Body, i, t) && in.irPureFailArg(a) && irNoCalls(args[i+1:]) && irFrameOnly(callee.Body) {
			subst[i] = a
			failArgs++
			continue
		}
		if frame.params[i] == "" {
			return nil
		}
		subst[i] = nil // bound below, once the copy's locals are known
	}
	if !site.anon {
		in.l.inlineSites = append(in.l.inlineSites, inlineSite{src: site.src, offset: site.offset, parent: p.parent})
	} else {
		in.st.report.StdInlined++
	}
	if site.failLeaf {
		in.st.report.FailLeaves++
	}
	p.spent += site.size
	in.count++
	in.st.report.FailArgs += failArgs
	if site.slot {
		in.st.report.InlinedSlots++
	}
	if ret {
		in.st.report.InlinedReturns++
	}
	if site.known {
		in.st.report.KnownInlined++
	}
	ren := map[int]int{}
	for i, a := range args {
		if subst[i] != nil {
			continue
		}
		l := in.newLocal("p"+strconv.Itoa(i)+"_inl"+id, irSubstTypes(frame.params[i], frame.tps, site.tsub), at)
		ren[i] = l
		binds = append(binds, &irStmt{K: "def", Pos: a.Pos, Local: l, X: a})
	}
	// The result: a local of the callee's that its returns name becomes
	// the site's own, so its value lands in place (the text path's
	// yield to its destination; a record of five words or more is a
	// copy in Go).
	into := -1
	if mode == "def" { // a def2 site may read its value after a failure: zero
		into = irResultLocal(callee.Body, np, np+len(callee.Locals), subst, s.Local)
		if into >= 0 && irSubstTypes(callee.Locals[into-np].Type, frame.tps, site.tsub) != in.localType(s.Local) {
			into = -1
		}
	}
	for j, lc := range callee.Locals {
		ren[np+j] = in.newLocal(lc.Name+"_inl"+id, irSubstTypes(lc.Type, frame.tps, site.tsub), at)
	}
	cp := &irCopy{in: in, id: id, ren: ren, subst: subst, file: file, tps: frame.tps, tsub: site.tsub, conc: site.conc, slotOf: site.slotOf}
	if !site.sourced {
		call := s.X.Pos
		cp.at = &call
	}
	body := in.clone(callee.Body, cp)
	body = in.stmts(body, irPath{parent: parent, depth: p.depth + 1, loop: p.loop || site.loop, spent: p.spent})
	end := "inl" + id
	r := &irRets{in: in, mode: mode, local: s.Local, label: end}
	if s.Val != nil {
		r.err = *s.Val
	}
	if into >= 0 {
		body, r.into = in.irRenames(body, ren[into], s.Local)
	}
	body = r.stmts(body, true)
	var out []*irStmt
	switch mode {
	case "def":
		out = append(out, &irStmt{K: "var", Pos: s.Pos, Local: s.Local})
	case "def2":
		out = append(out, &irStmt{K: "var", Pos: s.Pos, Local: s.Local}, &irStmt{K: "var", Pos: s.Pos, Local: *s.Val})
	}
	out = append(out, &irStmt{K: "block", Pos: at, End: at, Body: append(binds, body...)})
	if r.gotos > 0 {
		out = append(out, &irStmt{K: "block", Pos: at, End: at, Label: end})
	}
	return out
}

// irFailValue is a failure's value x as an error, for the error local of
// the site's def2 (the decoder assigns only identical types): a variant
// built in place (a literal or a zero value) converted to error, or one
// converted already; nil for any other, whose type the IR does not spell.
func irFailValue(x *irExpr) *irExpr {
	switch {
	case x == nil:
		return nil
	case x.K == "zero" || x.K == "lit":
		return &irExpr{K: "toiface", Pos: x.Pos, T: "error", X: x}
	case x.K == "toiface" && x.T == "error":
		return x
	}
	return nil
}

// irResultLocal is the local in [lo, hi) of the callee's own that its
// last top-level return names, when no parameter stands for the site's
// local dest; else -1.
func irResultLocal(body []*irStmt, lo, hi int, subst map[int]*irExpr, dest int) int {
	n := len(body)
	if n == 0 || body[n-1].K != "ret" || body[n-1].X == nil || body[n-1].X.K != "local" {
		return -1
	}
	x := body[n-1].X.I
	if x < lo || x >= hi {
		return -1
	}
	for _, a := range subst {
		if a != nil && a.K == "local" && a.I == dest {
			return -1
		}
	}
	return x
}

// irRenames writes the copy's result local x as the site's local dest,
// declared before the copy, when only def, var and set outside a literal
// name it, and no literal reads it (its own calls inlined by now, a
// def2 of x spliced into var and sets): a def of x becomes a set of
// dest, a var its zero value, and a var at the copy's top level, run once
// with dest still zero from the site's var, nothing. It reports whether it
// did, and the body.
func (in *irInliner) irRenames(body []*irStmt, x, dest int) ([]*irStmt, bool) {
	ok := true
	irWalk(body, false, func(s *irStmt, lit bool) {
		switch {
		case s.Key != nil && *s.Key == x, s.Val != nil && *s.Val == x:
			ok = false
		case s.K == "mapget" || s.K == "count" || s.K == "def2" || lit && (s.K == "def" || s.K == "set" || s.K == "var"):
			ok = ok && s.Local != x
		}
		for _, l := range s.Ls {
			ok = ok && l != x
		}
	}, func(e *irExpr, lit bool) {
		ok = ok && !(lit && e.K == "local" && e.I == x)
	})
	if !ok {
		return body, false
	}
	var kept []*irStmt
	for _, s := range body {
		if s.K != "var" || s.Local != x {
			kept = append(kept, s)
		}
	}
	body = kept
	t := in.localType(dest)
	irWalk(body, false, func(s *irStmt, lit bool) {
		if lit || s.Local != x {
			return
		}
		switch s.K {
		case "var":
			*s = irStmt{K: "set", Pos: s.Pos, Local: dest, X: &irExpr{K: "zero", Pos: s.Pos, T: t}}
		case "def":
			s.K, s.Local = "set", dest
		case "set":
			s.Local = dest
		}
	}, func(e *irExpr, lit bool) {
		if e.K == "local" && e.I == x {
			e.I = dest
		}
	})
	return body, true
}

// coalesce drops the copies `def a = b` and `set a = b` of a body the
// inliner changed, where b is a local of the same type declared earlier in
// the same list and read nowhere after the copy nor in a literal: b's
// declaration declares (or for a set, assigns) a instead, so a record of
// five words or more is not copied in Go (the text path yields a call's
// value to its destination), and a is unread from b's declaration to the
// copy.
func (in *irInliner) coalesce(list []*irStmt) []*irStmt {
	for _, s := range list {
		s.Then, s.Else, s.Body = in.coalesce(s.Then), in.coalesce(s.Else), in.coalesce(s.Body)
		for _, c := range s.Cases {
			c.Body = in.coalesce(c.Body)
		}
	}
	for i := 0; i < len(list); i++ {
		s := list[i]
		if (s.K != "def" && s.K != "set") || s.Op != "" || s.X == nil || s.X.K != "local" {
			continue
		}
		a, b := s.Local, s.X.I
		if a == b || a < in.np || b < in.np || in.localType(a) == "" || in.localType(a) != in.localType(b) {
			continue
		}
		j := -1
		for k := i - 1; k >= 0; k-- {
			if d := list[k]; d.Local == b && (d.K == "var" || d.K == "def" || s.K == "def" && (d.K == "def2" || d.K == "mapget")) {
				j = k
				break
			}
		}
		if j < 0 || irNames(list[i+1:], b, true) || irNamesInLiteral(list, b) || irDeclares(list[j+1:i], b) || irNames(list[j:i], a, true) {
			continue
		}
		d := list[j]
		switch {
		case s.K == "def":
			d.Local = a
		case d.K == "var":
			*d = irStmt{K: "set", Pos: d.Pos, Local: a, X: &irExpr{K: "zero", Pos: d.Pos, T: in.localType(a)}}
		default:
			d.K, d.Local = "set", a
		}
		irRename(list[j:i], b, a)
		list = append(list[:i], list[i+1:]...)
		i--
	}
	return list
}

// irNames reports a reference to local x in list: an expression, or a
// statement's local, key, value or locals (lit: in literals too).
func irNames(list []*irStmt, x int, lit bool) bool {
	found := false
	irWalk(list, false, func(s *irStmt, inLit bool) {
		if inLit && !lit {
			return
		}
		switch s.K {
		case "def", "set", "var", "def2", "mapget", "count":
			found = found || s.Local == x
		}
		found = found || s.Key != nil && *s.Key == x || s.Val != nil && *s.Val == x
		for _, l := range s.Ls {
			found = found || l == x
		}
	}, func(e *irExpr, inLit bool) {
		found = found || (lit || !inLit) && e.K == "local" && e.I == x
	})
	return found
}

// irNamesInLiteral reports a reference to x inside a literal of list.
func irNamesInLiteral(list []*irStmt, x int) bool {
	found := false
	for _, s := range list {
		irWalk([]*irStmt{s}, false, func(*irStmt, bool) {}, func(e *irExpr, _ bool) {
			if e.K == "func" && irNames(e.Body, x, true) {
				found = true
			}
		})
	}
	return found
}

// irDeclares reports a declaration of x in list, or x as a key, value or
// local of a loop or lookup: anything a rename to a set cannot keep.
func irDeclares(list []*irStmt, x int) bool {
	found := false
	irWalk(list, false, func(s *irStmt, _ bool) {
		switch s.K {
		case "def", "var", "def2", "mapget", "count":
			found = found || s.Local == x
		}
		found = found || s.Key != nil && *s.Key == x || s.Val != nil && *s.Val == x
		for _, l := range s.Ls {
			found = found || l == x
		}
	}, func(*irExpr, bool) {})
	return found
}

// irRename writes local b as a in list, outside literals (coalesce
// checked that none names b).
func irRename(list []*irStmt, b, a int) {
	irWalk(list, false, func(s *irStmt, lit bool) {
		if !lit && s.K == "set" && s.Local == b {
			s.Local = a
		}
	}, func(e *irExpr, lit bool) {
		if !lit && e.K == "local" && e.I == b {
			e.I = a
		}
	})
}

// localType is the IR type of the inliner's function's local i, "" for a
// parameter slot.
func (in *irInliner) localType(i int) string {
	j := i - in.np
	switch {
	case j < 0:
		return ""
	case j < len(in.fn.Locals):
		return in.fn.Locals[j].Type
	case j-len(in.fn.Locals) < len(in.extra):
		return in.extra[j-len(in.fn.Locals)].Type
	}
	return ""
}

// localTypeOf is the IR type of b's local i.
func (b *irBuilder) localTypeOf(i int) string {
	if j := i - (b.next - len(b.fn.Locals)); j >= 0 && j < len(b.fn.Locals) {
		return b.fn.Locals[j].Type
	}
	return ""
}

// irArmsLeave reports a catch's type switch sw whose arms and default all
// leave (return, fail, panic or jump) and never name result, the local a
// yielding arm would set (ir_fail.go, catch).
func irArmsLeave(sw *irStmt, result int) bool {
	if !irListTerminates(sw.Else) {
		return false
	}
	for _, c := range sw.Cases {
		if !irListTerminates(c.Body) || irBreaks(c.Body, "") {
			return false
		}
	}
	named := false
	irWalk([]*irStmt{sw}, false, func(s *irStmt, _ bool) {
		named = named || (s.Local == result && (s.K == "def" || s.K == "set" || s.K == "var" || s.K == "def2"))
	}, func(e *irExpr, _ bool) {
		named = named || (e.K == "local" && e.I == result)
	})
	return !named
}

// irFailField reports a failure that is a field of a local, at any depth
// (`return e.Error` of a variant whose field holds a Slot row's value):
// read where the copy fails, as the callee reads it, and converted to
// error. INLINE-SLOT; off with it.
func irFailField(x *irExpr) bool {
	if irInlineSlotLowering == "off" || x == nil || x.K != "field" {
		return false
	}
	for x != nil && x.K == "field" {
		x = x.X
	}
	return x != nil && x.K == "local"
}

// irInlinable reports a callee body the pass may copy: no statement that
// belongs to its own frame (a deferred call, a named result, a failure,
// a recover), and, with a result, a return that gives it.
func irInlinable(body []*irStmt, result, fallible bool) bool {
	ok, ret := true, false
	irWalk(body, false, func(s *irStmt, lit bool) {
		switch {
		case s.K == "defer", s.K == "fail" && !lit && !fallible:
			ok = false
		case s.K == "fail" && !lit && irFailValue(s.X) == nil && (irFailLocalLowering == "off" || s.X == nil || s.X.K != "local") && !irFailField(s.X):
			// A failure of a type the IR does not spell; a local's or a
			// parameter's is spelled by its slot (irRets.failValue,
			// INLINE-FAIL-LOCAL); a field's converts to error whatever its
			// type (INLINE-SLOT: a Slot row's value in a variant's field).
			ok = false
		case s.K == "ret" && !lit:
			ret = true
			if fallible && s.X != nil && (s.X.K == "call" || s.X.K == "mcall" || s.X.K == "pcall" || s.X.K == "vcall") && (!s.Pair || irInlineReturnLowering == "off") {
				// return f(…): a pair or an error, by a type the IR does
				// not spell; but a fallible Ergo call's value and error
				// (Pair) bind at the site (irRets, INLINE-RETURN).
				ok = false
			}
		}
	}, func(e *irExpr, _ bool) {
		switch e.K {
		case "result", "recover":
			ok = false
		}
	})
	return ok && (ret || !result)
}

// irReadOnly reports whether the body only reads its parameter slot i: no
// statement writes it, no address of it or of a part of it is taken, no
// store's place starts at it, and no method is called on it unless it is
// a pointer (Go would take its address).
func irReadOnly(body []*irStmt, i int, t string) bool {
	ok := true
	// A pointer is the same pointer in the caller: a store or an address
	// through it changes what it points to, as the call would; only the
	// pointer variable itself must stay unwritten.
	ptr := strings.HasPrefix(t, "*")
	uses := func(e *irExpr) bool {
		found := false
		irExprWalk(e, false, func(x *irExpr, _ bool) {
			found = found || x.K == "local" && x.I == i
		})
		return found
	}
	irWalk(body, false, func(s *irStmt, _ bool) {
		switch s.K {
		case "def", "set", "var", "def2", "mapget", "count":
			if s.Local == i {
				ok = false
			}
		case "store":
			if !ptr && uses(s.X) {
				ok = false
			}
		}
		if s.Key != nil && *s.Key == i || s.Val != nil && *s.Val == i {
			ok = false
		}
		for _, l := range s.Ls {
			ok = ok && l != i
		}
	}, func(e *irExpr, _ bool) {
		switch e.K {
		case "addr":
			if ptr && e.X != nil && e.X.K == "local" && e.X.I == i || !ptr && uses(e.X) {
				ok = false
			}
		case "mcall":
			if !strings.HasPrefix(t, "*") && uses(e.X) {
				ok = false
			}
		case "func":
			for _, p := range e.Params {
				ok = ok && p != i
			}
		}
	})
	return ok
}

// irFailOnly reports that body reads parameter i only as the whole failure
// of a `fail`, outside any function literal, and never writes it.
func irFailOnly(body []*irStmt, i int, t string) bool {
	if !irReadOnly(body, i, t) {
		return false
	}
	uses, fails := 0, 0
	irWalk(body, false, func(s *irStmt, lit bool) {
		if !lit && s.K == "fail" && s.X != nil && s.X.K == "local" && s.X.I == i {
			fails++
		}
	}, func(e *irExpr, _ bool) {
		if e.K == "local" && e.I == i {
			uses++
		}
	})
	return fails > 0 && uses == fails
}

// irPureFailArg reports that a is a variant literal whose evaluation
// cannot panic, call or write: constants, zero values, the caller's
// locals and fields of those of a spelled value type (no pointer to
// dereference), literals of them, and operators that cannot panic.
func (in *irInliner) irPureFailArg(a *irExpr) bool {
	// A variant passed as an error parameter is converted at the call
	// (an error row lowered as error): the conversion is pure too.
	if a != nil && a.K == "toiface" && a.T == "error" {
		a = a.X
	}
	if a == nil || a.K != "lit" {
		return false
	}
	var pure func(e *irExpr) bool
	pure = func(e *irExpr) bool {
		switch e.K {
		case "const", "zero":
			return true
		case "local":
			return e.I != in.frameEnv()
		case "lit":
			for _, x := range e.Elems {
				if !pure(x) {
					return false
				}
			}
			return true
		case "field":
			if e.X == nil || e.X.K != "local" {
				return false
			}
			t := in.slotType(e.X.I)
			return t != "" && !strings.HasPrefix(t, "*") && pure(e.X)
		case "bin":
			switch e.Op {
			case "+", "-", "*", "&", "|", "^", "&^", "==", "!=", "<", "<=", ">", ">=":
				return pure(e.X) && pure(e.Y)
			}
		case "un":
			switch e.Op {
			case "-", "!", "^", "+":
				return pure(e.X)
			}
		}
		return false
	}
	return pure(a)
}

// irCloneLit is a deep copy of an operand irPureFailArg admits (its
// operators' operands and its literals' elements).
func irCloneLit(e *irExpr) *irExpr {
	if e == nil {
		return nil
	}
	n := *e
	n.X, n.Y = irCloneLit(e.X), irCloneLit(e.Y)
	if e.Elems != nil {
		n.Elems = make([]*irExpr, len(e.Elems))
		for i, x := range e.Elems {
			n.Elems[i] = irCloneLit(x)
		}
	}
	n.Keys = append([]string(nil), e.Keys...)
	n.FT = append([]string(nil), e.FT...)
	return &n
}

// frameEnv is fn's environment slot, or -1.
func (in *irInliner) frameEnv() int {
	if in.frame == nil {
		return -1
	}
	return in.frame.env
}

// irNoCalls reports that no expression of list calls a function, recovers
// or builds a function literal: evaluating them writes nothing.
func irNoCalls(list []*irExpr) bool {
	ok := true
	for _, a := range list {
		irExprWalk(a, false, func(e *irExpr, _ bool) {
			ok = ok && !irCallsOrWrites(e)
		})
	}
	return ok
}

// irCallsOrWrites reports an expression that runs code or makes a write
// possible: a call of any kind, recover, append (it can write a shared
// backing array), a function literal or an address.
func irCallsOrWrites(e *irExpr) bool {
	switch e.K {
	case "call", "pcall", "mcall", "vcall", "builtin", "recover", "append", "func", "addr":
		return true
	}
	return false
}

// irFrameOnly reports that body writes nothing outside its own frame: no
// call, no store, no defer, no function literal, no address.
func irFrameOnly(body []*irStmt) bool {
	ok := true
	irWalk(body, false, func(s *irStmt, _ bool) {
		switch s.K {
		case "store", "defer":
			ok = false
		}
	}, func(e *irExpr, _ bool) {
		ok = ok && !irCallsOrWrites(e)
	})
	return ok
}

// irWalk calls fs on every statement of list and fe on every expression,
// lit set inside a function literal.
func irWalk(list []*irStmt, lit bool, fs func(*irStmt, bool), fe func(*irExpr, bool)) {
	for _, s := range list {
		fs(s, lit)
		for _, e := range []*irExpr{s.X, s.Y} {
			irExprWalkStmts(e, lit, fs, fe)
		}
		for _, c := range s.Cases {
			for _, v := range c.Vals {
				irExprWalkStmts(v, lit, fs, fe)
			}
			irWalk(c.Body, lit, fs, fe)
		}
		irWalk(s.Then, lit, fs, fe)
		irWalk(s.Else, lit, fs, fe)
		irWalk(s.Body, lit, fs, fe)
	}
}

func irExprWalkStmts(e *irExpr, lit bool, fs func(*irStmt, bool), fe func(*irExpr, bool)) {
	if e == nil {
		return
	}
	fe(e, lit)
	irExprWalkStmts(e.X, lit, fs, fe)
	irExprWalkStmts(e.Y, lit, fs, fe)
	for _, a := range e.Args {
		irExprWalkStmts(a, lit, fs, fe)
	}
	for _, a := range e.Elems {
		irExprWalkStmts(a, lit, fs, fe)
	}
	if e.K == "func" {
		irWalk(e.Body, true, fs, fe)
	}
}

// irExprWalk calls f on e and every expression inside it, statements of a
// literal's body included.
func irExprWalk(e *irExpr, lit bool, f func(*irExpr, bool)) {
	irExprWalkStmts(e, lit, func(*irStmt, bool) {}, f)
}

// irCopy is how the pass copies a callee's body to a site: its locals
// renamed (ren) or replaced by the operand (subst), its labels tagged with
// the site, its files tagged, its type parameters replaced.
type irCopy struct {
	in     *irInliner
	id     string
	ren    map[int]int
	subst  map[int]*irExpr
	file   func(int) int
	tps    map[*check.TypeParam]string
	tsub   map[*check.TypeParam]string
	conc   map[*check.TypeParam]bool
	slotOf map[*check.TypeParam]*check.TypeParam
	at     *irPos // every position of a std body's copy
}

func (c *irCopy) local(i int) int {
	if j, ok := c.ren[i]; ok {
		return j
	}
	panic(irUnsupported{"inlined local out of range"})
}

func (c *irCopy) pos(p irPos) irPos {
	if p == (irPos{}) {
		return p
	}
	if c.at != nil {
		return *c.at
	}
	return irPos{c.file(p[0]), p[1], p[2]}
}

func (c *irCopy) label(l string) string {
	if l == "" {
		return ""
	}
	return l + "_inl" + c.id
}

// clone is a deep copy of list, through cp when it is a callee's copy; a
// marked call's copy stays marked. It is the one tree copy (ir_treecopy.go)
// with the inliner's hooks.
func (in *irInliner) clone(list []*irStmt, cp *irCopy) []*irStmt {
	return in.copier(cp).stmts(list)
}

// copier is the tree copy of a body (cp nil) or of a callee's copy cp:
// a parameter's local becomes its operand, each local, position and
// label the copy's, each type spelled in the callee's type parameters
// the site's, and each marked call's site the copy's call's.
func (in *irInliner) copier(cp *irCopy) *irTreeCopy {
	c := &irTreeCopy{node: func(e, n *irExpr) { in.cloneExpr(e, n, cp) }}
	if cp == nil {
		return c
	}
	c.local = cp.local
	c.expr = func(e *irExpr) *irExpr {
		if e.K == "local" {
			if a := cp.subst[e.I]; a != nil {
				// A literal (a failure argument) is copied whole: each
				// failure of the copy gets its own tree.
				n := *irCloneLit(a)
				n.Pos = cp.pos(e.Pos)
				return &n
			}
		}
		return nil
	}
	c.cas = func(k, n *irCase) {
		n.Pos = cp.pos(k.Pos)
		for j, t := range k.Types {
			n.Types[j] = irSubstTypes(t, cp.tps, cp.tsub)
		}
	}
	c.stmt = func(s, n *irStmt) {
		n.Pos, n.End, n.ElsePos, n.ElseEnd = cp.pos(s.Pos), cp.pos(s.End), cp.pos(s.ElsePos), cp.pos(s.ElseEnd)
		n.Label = cp.label(s.Label)
		// A failure's Go type, the site's; a Slot row's a struct where
		// the call binds it to one (FAILJUMP-TYPED).
		n.FailT, n.FailTP = irSubstTypes(s.FailT, cp.tps, cp.tsub), nil
		if s.FailTP != nil && cp.conc[s.FailTP] && irFailTypedLowering != "off" {
			n.FailT = cp.tsub[s.FailTP]
		} else if s.FailTP != nil {
			n.FailTP = cp.slotOf[s.FailTP] // the caller's own Slot row, bound by its copy
		}
	}
	return c
}

// cloneExpr finishes n, the tree copy of e: a marked call's site, and
// through cp its position and types.
func (in *irInliner) cloneExpr(e, n *irExpr, cp *irCopy) {
	if site := in.st.inl.sites[e]; site != nil {
		if cp != nil {
			// A call inside the copy: its type arguments, spelled in the
			// callee's type parameters, become the site's.
			s := *site
			s.tsub = map[*check.TypeParam]string{}
			s.conc = map[*check.TypeParam]bool{}
			for tp, a := range site.tsub {
				s.tsub[tp] = irSubstTypes(a, cp.tps, cp.tsub)
				// A Slot row passed on from the callee's own, which this
				// copy binds to a struct, is one too (FAILJUMP-TYPED).
				s.conc[tp] = site.conc[tp]
				for otp, name := range cp.tps {
					if name == a && cp.conc[otp] {
						s.conc[tp] = true
					}
				}
			}
			s.result = irSubstTypes(site.result, cp.tps, cp.tsub)
			site = &s
		}
		in.st.inl.sites[n] = site
	}
	if cp == nil {
		return
	}
	n.Pos = cp.pos(e.Pos)
	if n.End != nil {
		*n.End = cp.pos(*n.End)
	}
	if e.FT != nil {
		for i, t := range e.FT {
			n.FT[i] = irSubstTypes(t, cp.tps, cp.tsub)
		}
	}
	if e.K != "pcall" {
		n.T = irSubstTypes(e.T, cp.tps, cp.tsub)
	} else {
		n.V = irSubstTypes(e.V, cp.tps, cp.tsub) // a package call's type arguments (T is its import path)
	}
}

// irSubstTypes is the Go type text t with each type parameter's spelling
// (tps) replaced by its type argument (tsub): a whole identifier, not one
// a dot qualifies.
func irSubstTypes(t string, tps, tsub map[*check.TypeParam]string) string {
	if t == "" || len(tsub) == 0 {
		return t
	}
	to := map[string]string{}
	for tp, name := range tps {
		if a, ok := tsub[tp]; ok && a != name {
			to[name] = a
		}
	}
	if len(to) == 0 {
		return t
	}
	var b strings.Builder
	for i := 0; i < len(t); {
		c := t[i]
		if !irIdentStart(c) {
			b.WriteByte(c)
			i++
			continue
		}
		j := i
		for j < len(t) && (irIdentStart(t[j]) || t[j] >= '0' && t[j] <= '9') {
			j++
		}
		id := t[i:j]
		if a, ok := to[id]; ok && (i == 0 || t[i-1] != '.') {
			b.WriteString(a)
		} else {
			b.WriteString(id)
		}
		i = j
	}
	return b.String()
}

func irIdentStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= 0x80
}

// irRets rewrites a copy's returns: a result into the site's local (def,
// set) or dropped (expr), then a goto past the block; the copy's last
// top-level return falls through.
type irRets struct {
	in    *irInliner
	mode  string
	local int
	err   int // the error local of a def2
	label string
	gotos int
	into  bool // the copy's result local is local: a return of it sets nothing
}

// failValue is a copy's failure x as the site's error: a variant built in
// place (irFailValue), or a local of the copy's frame (a parameter
// substituted by the site's local, a try's error, a failure in a let) by
// its Go type: an error as it is, any other type converted to error as
// the call's own return converts it (INLINE-FAIL-LOCAL).
func (r *irRets) failValue(x *irExpr) *irExpr {
	if v := irFailValue(x); v != nil {
		return v
	}
	if irFailField(x) {
		// error(x): the conversion the call's own return makes, valid
		// for an error and a variant alike (INLINE-SLOT).
		r.in.st.report.FailFields++
		return &irExpr{K: "toiface", Pos: x.Pos, T: "error", X: x}
	}
	if r.in.slotType(x.I) == "error" {
		r.in.st.report.FailLocalErrors++
		return x
	}
	r.in.st.report.FailLocalValues++
	return &irExpr{K: "toiface", Pos: x.Pos, T: "error", X: x}
}

func (r *irRets) stmts(list []*irStmt, top bool) []*irStmt {
	if r.mode == "ret" {
		// A call in return position: the copy's returns and failures
		// are the frame's own (INLINE-RETURN).
		return list
	}
	var out []*irStmt
	for i, s := range list {
		if s.K == "ret" && s.Pair && r.mode == "def2" {
			// `return f(…)` of a fallible call: its value and error,
			// bound to temporaries of the site's types, are the site's
			// (INLINE-RETURN). A failure set this way is no variant
			// built in place, so failJumps leaves the site's check.
			p := s.Pos
			v := r.in.newLocal("tv"+strconv.Itoa(len(r.in.extra)), r.in.localType(r.local), p)
			e := r.in.newLocal("te"+strconv.Itoa(len(r.in.extra)), "error", p)
			out = append(out,
				&irStmt{K: "def2", Pos: p, Local: v, Val: &e, X: s.X},
				&irStmt{K: "set", Pos: p, Local: r.local, X: &irExpr{K: "local", Pos: p, I: v}},
				&irStmt{K: "set", Pos: p, Local: r.err, X: &irExpr{K: "local", Pos: p, I: e}})
			r.in.st.report.TailPairs++
			if !top || i != len(list)-1 {
				r.gotos++
				out = append(out, &irStmt{K: "goto", Pos: p, Label: r.label})
			}
			continue
		}
		if s.K == "fail" && r.mode == "def2" {
			out = append(out, &irStmt{K: "set", Pos: s.Pos, Local: r.err, X: r.failValue(s.X), FailT: s.FailT, FailTP: s.FailTP})
			if !top || i != len(list)-1 {
				r.gotos++
				out = append(out, &irStmt{K: "goto", Pos: s.Pos, Label: r.label})
			}
			continue
		}
		if s.K != "ret" {
			s.Then = r.stmts(s.Then, false)
			s.Else = r.stmts(s.Else, false)
			s.Body = r.stmts(s.Body, false)
			for _, c := range s.Cases {
				c.Body = r.stmts(c.Body, false)
			}
			out = append(out, s)
			continue
		}
		if s.X != nil && !(r.into && s.X.K == "local" && s.X.I == r.local) {
			switch r.mode {
			case "def", "set", "def2":
				out = append(out, &irStmt{K: "set", Pos: s.Pos, Local: r.local, X: s.X})
			default:
				out = append(out, &irStmt{K: "discard", Pos: s.Pos, X: s.X})
			}
		}
		if top && i == len(list)-1 {
			continue
		}
		r.gotos++
		out = append(out, &irStmt{K: "goto", Pos: s.Pos, Label: r.label})
	}
	return out
}
