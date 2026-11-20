package lower

// IR builder: a cell writer's codec function (CW-IR; wiretext.go, Cell
// writers). The text emitter writes the function's Go text and rewrites it
// line by line (cellEventsFrom). On the IR path the same function is built
// here as an IR body, in two steps:
//
//  1. wirePlan builds the function's statements as a tree of steps from
//     t's structure (wireEncodeBody's forms): an event (a Writer operation,
//     a call of the provider's function), a call of another codec
//     function, a write of the type's own (WireWrite), a loop, a switch on
//     a union. A form the IR does not take refuses the function here, and
//     the function keeps its text body.
//  2. wireWalk reads the steps as cellEventsFrom reads the lines: the
//     events of one straight run are a window of the provider's laws
//     (rewriter.run, through cellRewriter), the state a run ends in is
//     carried into the next run, into a branch's arms, and into a call of
//     another codec function (its entry variant, cellVariant). The rules
//     are cellEventsFrom's, step for step, so both forms make one decision.
//     A form the walk refuses (irNo) restores the rewriter's counters,
//     and the text form runs the same windows, whose evidence records
//     each site once by its key. A rewritten constant that is not UTF-8
//     (CBOR's bytes) is carried quoted (irStringConst).
//
// The text form stays selectable: wireIRLowering "text" keeps every codec
// function's text body on the IR path.
//
// Not taken (the function keeps its text, IRReport.CodecText names it):
// a type that holds type parameters (witnesses), a nullable or bare
// flat union, a tuple, a flat union by its pointer niche.

import (
	"go/constant"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
	v1 "ergo.local/prototype/runtime/v1"
)

// codecText records that the codec function name keeps its text body,
// and why (IRReport.CodecText).
func (st *irState) codecText(name, reason string) {
	if st.report.CodecText == nil {
		st.report.CodecText = map[string]int{}
	}
	st.report.CodecText[name+"\t"+reason]++
	if st.census != nil {
		// A census counts the function as a body that is not lowered
		// (PORT-WIRE-READERS step 0), keyed by its Go name: the same
		// function in two targets is one function (IRCensusFunc.Codec).
		st.census.Funcs = append(st.census.Funcs, IRCensusFunc{Pkg: "codec", Name: name, Codec: true, Reasons: []string{reason}})
	}
}

// wireIRLowering selects the codec writers' form on the IR path: "" (an
// IR body where wirePlan takes the type) or "text" (the reference: the
// text emitter's body).
var wireIRLowering string

// wireStep is one step of a codec function's plan: an event (ev, its
// arguments' IR, nil for a constant), a call of another codec function
// (call: callee by its Go name), a statement that names the writer (own:
// a WireWrite call), a loop (loop, its body), a switch (sw, one arm per
// case, entry is each arm's leading statements), a statement that does
// not name the writer (pre: an omittable field's local or the count of
// the fields written, which leaves the walk's state as it is).
type wireStep struct {
	pre    *irStmt
	ev     *rwCall
	args   []*irExpr
	call   *irStmt
	callee string
	own    *irStmt
	loop   *irStmt
	body   []wireStep
	sw     *irStmt
	arms   [][]wireStep
	heads  [][]*irStmt
}

// wireGen builds one codec function.
type wireGen struct {
	b    *irBuilder
	cw   cellWriter
	em   wireEvents
	site syntax.Node
	p    irPos
	env  int // locals: the environment, the cell's value, the value
	wv   int
	// decls declare the function's key arrays (WireKeysIn), at its entry.
	decls []*irStmt
}

// wireKeysLowering selects how a codec writer orders a Map's keys: ""
// (WireKeysIn, in an array of the frame) or "heap" (the reference:
// WireKeys, a fresh slice per Map, as the text form does).
var wireKeysLowering string

// wireCell builds the IR body of the cell codec function name of t,
// whose first run starts in start's state (nil when unknown), and reports
// whether it did. params is the namer that named the function's Go
// parameters (env, wv, v, in that order: locals 0, 1, 2).
func (st *irState) wireCell(l *lowerer, name string, t check.Type, em wireEvents, cw cellWriter, start *rwCall, params *namer, site syntax.Node) (ok bool) {
	if wireIRLowering == "text" || st.census != nil {
		return false
	}
	if len(freeTypeParams(t)) > 0 {
		st.codecText(name, "codec writer of a type that holds type parameters")
		return false
	}
	scope := newNamer(params)
	fc := newFuncCtx(l, scope, false)
	fc.site = site
	b := st.newBuilder(fc, nil) // a codec cell: no source statements
	b.env, b.next, b.fn, b.names = 0, 3, &irFunc{Name: name}, scope
	keep := st.keep
	files := len(st.out.Files)
	g := &wireGen{b: b, cw: cw, em: em, site: site, env: 0, wv: 1}
	// The rewriter's counters, restored when the walk refuses (a constant
	// the IR cannot carry): the text form then runs the same windows, and
	// the evidence records each site once by its key.
	rw := l.cellRewriter(cw.prov)
	var counts rewriter
	if rw != nil {
		counts = *rw
	}
	defer func() {
		if r := recover(); r != nil {
			why, no := r.(irUnsupported)
			if !no {
				panic(r)
			}
			st.codecText(name, why.reason)
			// A refused function records nothing: no import, no file.
			st.keep = keep
			st.out.Files = st.out.Files[:files]
			for path, i := range st.files {
				if i >= files {
					delete(st.files, path)
				}
			}
			if rw != nil {
				rw.work, rw.applied, rw.refused = counts.work, counts.applied, counts.refused
			}
			ok = false
		}
	}()
	if keep != nil {
		st.keep = map[string]bool{}
		for k := range keep {
			st.keep[k] = true
		}
	}
	g.p = b.pos(site, site.Pos())
	steps := g.plan(t, &irExpr{K: "local", Pos: g.p, I: 2})
	w := &wireWalk{g: g, tail: start}
	body := append(g.decls, w.steps(steps)...)
	body = append(w.flush(body), &irStmt{K: "ret", Pos: g.p, X: g.local(g.wv)})
	b.fn.Pos, b.fn.End = g.p, g.p
	b.fn.Body = irScopesIncrease(irUseLocals(body), b.fn.Pos) // ir_end.go
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	return true
}

func (g *wireGen) local(i int) *irExpr { return &irExpr{K: "local", Pos: g.p, I: i} }

// plan is the steps that write v, of type t (wireEncodeBody's forms).
func (g *wireGen) plan(t check.Type, v *irExpr) []wireStep {
	l, b := g.b.l, g.b
	if hook, ok := check.WireWriteOf(t); ok && hook.Provider.Obj == g.cw.prov.Obj {
		// P's own write of t (wireOwnWrite): w is *P.
		return []wireStep{{own: &irStmt{K: "expr", Pos: g.p, X: g.funcCall(hook.Func, []*irExpr{v, g.cell(true)})}}}
	}
	switch check.WireFormOf(t) {
	case check.WireNullable:
		return g.nullable(t, v)
	case check.WireBare:
		return g.bare(t, v)
	case check.WireNumberText:
		// The spelling Wire gives, as a Number (wireNumEncode).
		p := l.info.WirePairOf(t)
		return []wireStep{g.event("Number", &irExpr{K: "conv", Pos: g.p, T: "string", X: g.funcCall(p.Wire, []*irExpr{v})})}
	case check.WireMembers:
		return g.members(t, v)
	}
	if p := l.info.WirePairOf(t); p != nil {
		if p.Append {
			// The append form (check.WirePair): Wire appends v's text to
			// an array of this frame (WireTextIn, as the text form's
			// `var buf [64]byte`), and StringBytes writes those bytes.
			buf := b.temp("buf", "["+strconv.Itoa(v1.WireTextRoom)+"]byte", g.p)
			g.decls = append(g.decls, &irStmt{K: "var", Pos: g.p, Local: buf})
			room := &irExpr{K: "pcall", Pos: g.p, T: runtimeImport, Name: "WireTextIn", Args: []*irExpr{{K: "addr", Pos: g.p, X: g.local(buf)}}}
			return []wireStep{g.event("StringBytes", g.funcCall(p.Wire, []*irExpr{v, room}))}
		}
		// The pair's base, written from Wire(v) (wireEncodeWith).
		return g.value(p.Base, g.funcCall(p.Wire, []*irExpr{v}))
	}
	if check.RefinementOf(t) != nil {
		// A refined basic type writes its base (wireEncodeBody); any
		// other is written as its underlying type, below.
		if base, ok := wireUnder(t).(*check.Basic); ok {
			return g.value(base, v)
		}
	}
	switch u := wireUnder(t).(type) {
	case *check.Slice:
		e := b.temp("e", b.goType(u.Elem, g.site), g.p)
		out := []wireStep{g.event("BeginList", &irExpr{K: "len", Pos: g.p, X: v})}
		loop := &irStmt{K: "range", Pos: g.p, End: g.p, X: v, Val: &e}
		out = append(out, wireStep{loop: loop, body: g.value(u.Elem, g.local(e))})
		return append(out, g.event("EndList"))
	case *check.Map:
		// Each entry as v[k], in WireKeys' order, as the text reads it.
		// The keys are sorted in an array of this frame, one per Map
		// step (WireKeysIn): the loop over them ends before the step
		// runs again, and a Map it holds has its own.
		kt := b.goType(u.Key, g.site)
		k := b.temp("k", kt, g.p)
		keys := &irExpr{K: "pcall", Pos: g.p, T: runtimeImport, Name: "WireKeys", V: kt + ", " + b.goType(u.Elem, g.site), Args: []*irExpr{v}}
		if wireKeysLowering == "" {
			room := b.temp("keys", "["+strconv.Itoa(v1.WireKeyRoom)+"]"+kt, g.p)
			g.decls = append(g.decls, &irStmt{K: "var", Pos: g.p, Local: room})
			keys.Name = "WireKeysIn"
			keys.Args = []*irExpr{{K: "addr", Pos: g.p, X: g.local(room)}, v}
		}
		out := []wireStep{g.event("BeginObject", &irExpr{K: "len", Pos: g.p, X: v})}
		body := []wireStep{g.event("Key", &irExpr{K: "conv", Pos: g.p, T: "string", X: g.local(k)})}
		body = append(body, g.value(u.Elem, &irExpr{K: "index", Pos: g.p, X: v, Y: g.local(k)})...)
		out = append(out, wireStep{loop: &irStmt{K: "range", Pos: g.p, End: g.p, X: keys, Val: &k}, body: body})
		return append(out, g.event("EndObject"))
	case *check.Struct:
		n, ok := t.(*check.Named)
		if !ok {
			irNo("record type with no name")
		}
		return g.record(n, u, v, "")
	case *check.Union:
		return g.union(t, u, v)
	}
	irNo("codec writer of %s", t)
	return nil
}

// record is the steps of a record (or a union's variant, tag its name):
// BeginObject(n), the tag, each field's key and value, EndObject.
func (g *wireGen) record(n *check.Named, s *check.Struct, v *irExpr, tag string) []wireStep {
	b := g.b
	// An omittable field (a union whose WireForm is WireOmittable) is
	// written only when its value is the present variant, and the count
	// BeginObject takes leaves out each absent one (wireFieldCount,
	// wireFields): each such field is read into a local once, before.
	var out []wireStep
	held := map[string]int{}
	count := -1
	for _, f := range s.Fields {
		if _, _, param := b.fc.paramField(f.Type); param {
			irNo("codec writer of a field typed by a type parameter")
		}
		if check.WireFormOf(f.Type) != check.WireOmittable {
			continue
		}
		if b.flatOf(f.Type) != nil {
			irNo("codec writer of an omittable field of a flat union")
		}
		if count < 0 {
			count = b.temp("fields", "int", g.p)
			out = append(out, wireStep{pre: &irStmt{K: "def", Pos: g.p, Local: count, X: &irExpr{K: "const", Pos: g.p, T: "int", V: strconv.Itoa(len(s.Fields))}}})
		}
		fl := b.temp("field", b.goType(f.Type, g.site), g.p)
		held[f.Name] = fl
		out = append(out, wireStep{pre: &irStmt{K: "def", Pos: g.p, Local: fl, X: b.recordField(n, v, f.Name, g.site)}})
		empty, _, _ := b.fc.wrapperParts(f.Type)
		less := &irStmt{K: "set", Pos: g.p, Local: count, X: &irExpr{K: "bin", Pos: g.p, Op: "-", X: g.local(count), Y: &irExpr{K: "const", Pos: g.p, T: "int", V: "1"}}}
		sw := &irStmt{K: "tswitch", Pos: g.p, End: g.p, X: g.local(fl), Cases: []*irCase{{Pos: g.p, Types: []string{b.goType(empty, g.site)}}}}
		out = append(out, wireStep{sw: sw, arms: [][]wireStep{{{pre: less}}}, heads: [][]*irStmt{nil}})
	}
	if count < 0 {
		out = append(out, g.constEvent("BeginObject", constant.MakeInt64(int64(len(s.Fields)))))
	} else {
		out = append(out, g.event("BeginObject", g.local(count)))
	}
	if tag != "" {
		out = append(out, g.constEvent("Tag", constant.MakeString(tag)))
	}
	for _, f := range s.Fields {
		key := g.constEvent("Key", constant.MakeString(check.WireFieldName(f)))
		fl, omittable := held[f.Name]
		if !omittable {
			out = append(out, key)
			out = append(out, g.value(f.Type, b.recordField(n, v, f.Name, g.site))...)
			continue
		}
		// switch x := field.(type) { case Present: Key; x's payload }
		_, present, pf := b.fc.wrapperParts(f.Type)
		pn, ok := present.(*check.Named)
		if !ok {
			irNo("omittable field whose present variant has no name")
		}
		gp := b.goType(present, g.site)
		x := b.temp("present", gp, g.p)
		head := []*irStmt{{K: "def", Pos: g.p, Local: x, X: &irExpr{K: "assert", Pos: g.p, T: gp, X: g.local(fl)}}}
		arm := append([]wireStep{key}, g.value(pf.Type, b.recordField(pn, g.local(x), pf.Name, g.site))...)
		sw := &irStmt{K: "tswitch", Pos: g.p, End: g.p, X: g.local(fl), Cases: []*irCase{{Pos: g.p, Types: []string{gp}}}}
		out = append(out, wireStep{sw: sw, arms: [][]wireStep{arm}, heads: [][]*irStmt{head}})
	}
	return append(out, g.event("EndObject"))
}

// nullable is a switch on v's variant (wireNullableEncode): the empty
// variant writes Null, the present one its payload.
func (g *wireGen) nullable(t check.Type, v *irExpr) []wireStep {
	b := g.b
	if v.K != "local" {
		irNo("union value not in a local")
	}
	if b.flatOf(t) != nil {
		irNo("codec writer of a nullable flat union")
	}
	empty, present, field := b.fc.wrapperParts(t)
	pn, ok := present.(*check.Named)
	if !ok {
		irNo("nullable whose present variant has no name")
	}
	gp := b.goType(present, g.site)
	x := b.temp("value", gp, g.p)
	head := []*irStmt{{K: "def", Pos: g.p, Local: x, X: &irExpr{K: "assert", Pos: g.p, T: gp, X: v}}}
	sw := &irStmt{K: "tswitch", Pos: g.p, End: g.p, ElsePos: g.p, X: v, Cases: []*irCase{
		{Pos: g.p, Types: []string{b.goType(empty, g.site)}},
		{Pos: g.p, Types: []string{gp}},
	}}
	arms := [][]wireStep{{g.event("Null")}, g.value(field.Type, b.recordField(pn, g.local(x), field.Name, g.site))}
	return []wireStep{{sw: sw, arms: arms, heads: [][]*irStmt{nil, head}}}
}

// bare is a switch on v's variant with no tag (wireValueEncode): the
// null variant writes Null, a variant of one field (barePart) that
// field, any other variant itself.
func (g *wireGen) bare(t check.Type, v *irExpr) []wireStep {
	b := g.b
	if v.K != "local" {
		irNo("union value not in a local")
	}
	if b.flatOf(t) != nil {
		irNo("codec writer of a bare flat union")
	}
	sw := &irStmt{K: "tswitch", Pos: g.p, End: g.p, ElsePos: g.p, X: v} // a bare union: no tag
	var arms [][]wireStep
	var heads [][]*irStmt
	for _, vt := range wireUnder(t).(*check.Union).Variants {
		gs := b.goType(vt, g.site)
		sw.Cases = append(sw.Cases, &irCase{Pos: g.p, Types: []string{gs}})
		if check.WireVariantKind(vt) == "WireNull" {
			arms, heads = append(arms, []wireStep{g.event("Null")}), append(heads, nil)
			continue
		}
		x := b.temp("value", gs, g.p)
		heads = append(heads, []*irStmt{{K: "def", Pos: g.p, Local: x, X: &irExpr{K: "assert", Pos: g.p, T: gs, X: v}}})
		if f := barePart(vt); f != nil {
			vn, ok := vt.(*check.Named)
			if !ok {
				irNo("variant with no name")
			}
			arms = append(arms, g.value(f.Type, b.recordField(vn, g.local(x), f.Name, g.site)))
			continue
		}
		arms = append(arms, g.value(vt, g.local(x)))
	}
	return []wireStep{{sw: sw, arms: arms, heads: heads}}
}

// members is an object of v's one list of key-value entries, in their
// order (wireMembersEncode).
func (g *wireGen) members(t check.Type, v *irExpr) []wireStep {
	b := g.b
	n, ok := t.(*check.Named)
	if !ok {
		irNo("members form with no name")
	}
	outer := wireUnder(t).(*check.Struct).Fields[0]
	list := wireUnder(outer.Type).(*check.Slice)
	en, ok := list.Elem.(*check.Named)
	if !ok {
		irNo("members entry with no name")
	}
	entry := wireUnder(en).(*check.Struct)
	m := b.temp("member", b.goType(en, g.site), g.p)
	out := []wireStep{g.event("BeginObject", &irExpr{K: "len", Pos: g.p, X: b.recordField(n, v, outer.Name, g.site)})}
	body := []wireStep{g.event("Key", &irExpr{K: "conv", Pos: g.p, T: "string", X: b.recordField(en, g.local(m), entry.Fields[0].Name, g.site)})}
	body = append(body, g.value(entry.Fields[1].Type, b.recordField(en, g.local(m), entry.Fields[1].Name, g.site))...)
	loop := &irStmt{K: "range", Pos: g.p, End: g.p, X: b.recordField(n, v, outer.Name, g.site), Val: &m}
	out = append(out, wireStep{loop: loop, body: body})
	return append(out, g.event("EndObject"))
}

// union is a switch on v's variant, each arm its variant's record.
func (g *wireGen) union(t check.Type, u *check.Union, v *irExpr) []wireStep {
	b := g.b
	if v.K != "local" {
		irNo("union value not in a local")
	}
	fu := b.flatOf(t)
	if fu != nil && fu.niche {
		irNo("codec writer of a union by its pointer niche")
	}
	// A type switch's default clause, empty here, takes ElsePos: the
	// decoder's scope for it needs a known position (JOBS-ICE).
	sw := &irStmt{K: "tswitch", Pos: g.p, End: g.p, ElsePos: g.p, X: v}
	if fu != nil {
		sw = &irStmt{K: "switch", Pos: g.p, End: g.p, X: b.tagOf(v.I, g.p)}
	}
	var arms [][]wireStep
	var heads [][]*irStmt
	for k, vt := range u.Variants {
		vn, ok := vt.(*check.Named)
		if !ok {
			irNo("variant with no name")
		}
		vs, _ := vn.Underlying().(*check.Struct)
		if vs == nil {
			irNo("variant that is not a record")
		}
		gs := b.goType(vt, g.site)
		var head []*irStmt
		x := v
		if len(vs.Fields) > 0 {
			xl := b.temp("x", gs, g.p)
			bind := &irExpr{K: "assert", Pos: g.p, T: gs, X: v}
			if fu != nil {
				bind = b.narrow(fu, v.I, k, g.p, g.site)
			}
			head = []*irStmt{{K: "def", Pos: g.p, Local: xl, X: bind}}
			x = g.local(xl)
		}
		c := &irCase{Pos: g.p, Types: []string{gs}}
		if fu != nil {
			c = &irCase{Pos: g.p, Vals: []*irExpr{{K: "const", Pos: g.p, T: "uint8", V: strconv.Itoa(k)}}}
		}
		sw.Cases = append(sw.Cases, c)
		arms = append(arms, g.record(vn, vs, x, vn.Obj.Name))
		heads = append(heads, head)
	}
	return []wireStep{{sw: sw, arms: arms, heads: heads}}
}

// value is the steps that write v, of type t, in its place: a basic
// value's event inline, any other type's codec function's call
// (wireEncodeWith).
func (g *wireGen) value(t check.Type, v *irExpr) []wireStep {
	l := g.b.l
	if l.wireInline(t) {
		bt := wireUnder(t).(*check.Basic)
		op, conv := "Int", "int64"
		switch {
		case bt.Kind == check.Bool:
			op, conv = "Bool", "bool"
		case bt.Kind == check.String:
			op, conv = "String", "string"
		case bt.Kind == check.Float32 || bt.Kind == check.Float64:
			op, conv = "Float", "float64"
		case bt.Kind >= check.Uint && bt.Kind <= check.Uintptr:
			op, conv = "Uint", "uint64"
		}
		return []wireStep{g.event(op, &irExpr{K: "conv", Pos: g.p, T: conv, X: v})}
	}
	if _, ok := t.(*check.TypeParam); ok {
		irNo("codec writer of a type parameter")
	}
	if len(freeTypeParams(t)) > 0 {
		irNo("codec call that passes witnesses")
	}
	name := l.wireFunc(g.em.dir(), t, g.site)
	call := &irExpr{K: "call", Pos: g.p, Name: name, Args: []*irExpr{g.local(g.env), g.local(g.wv), v}}
	return []wireStep{{call: &irStmt{K: "set", Pos: g.p, Local: g.wv, X: call}, callee: name}}
}

// event is the Writer event op with its non-constant arguments args, as
// a call of the provider's function.
func (g *wireGen) event(op string, args ...*irExpr) wireStep {
	fn := g.cw.conv.Table[op]
	if fn == nil {
		ice(g.site, "the event %s has no function of %s", op, g.cw.prov)
	}
	c := rwCall{fn: fn}
	for range args {
		c.args = append(c.args, rwArg{})
	}
	return wireStep{ev: &c, args: args}
}

// constEvent is the event op of one constant argument.
func (g *wireGen) constEvent(op string, v constant.Value) wireStep {
	s := g.event(op, nil)
	s.ev.args[0] = rwArg{c: v}
	return s
}

// cell is the cell's value as a call's receiver: a pointer to it for a
// pointer function, else its value.
func (g *wireGen) cell(ptr bool) *irExpr {
	if ptr {
		return &irExpr{K: "addr", Pos: g.p, X: g.local(g.wv)}
	}
	return g.local(g.wv)
}

// funcCall is the call of the declared function f on args, the
// environment after a method's receiver or first (wireCallFunc).
func (g *wireGen) funcCall(f *check.Func, args []*irExpr) *irExpr {
	fd := g.b.l.useFunc(f, g.site)
	if fd.extern != "" || len(witnessParams(f.Sig)) > 0 || len(f.Sig.TParams) > 0 {
		irNo("codec writer's call of %s", f.Name)
	}
	if !fd.envFree {
		at := 0
		if fd.method != nil {
			at = 1
		}
		args = append(args[:at:at], append([]*irExpr{g.local(g.env)}, args[at:]...)...)
	}
	if fd.method != nil {
		return &irExpr{K: "mcall", Pos: g.p, Name: fd.name, X: args[0], Args: args[1:]}
	}
	return &irExpr{K: "call", Pos: g.p, Name: fd.name, Args: args}
}

// wireWalk reads a plan as cellEventsFrom reads its lines: run is the
// straight run of events so far (its arguments in args), lead 1 when it
// starts with the carried state's call, tail the state where the walk
// stands (nil when unknown).
type wireWalk struct {
	g    *wireGen
	run  []rwCall
	args [][]*irExpr
	lead int
	tail *rwCall
}

// steps is the statements of the steps, in tail's state.
func (w *wireWalk) steps(steps []wireStep) []*irStmt {
	var out []*irStmt
	for _, s := range steps {
		switch {
		case s.pre != nil:
			// It reads the value only: the run before it ends first, in
			// the order the text writes them, and its state goes on.
			out = append(w.flush(out), s.pre)
		case s.ev != nil:
			if len(w.run) == 0 && w.tail != nil {
				// The run starts in tail's state.
				w.run, w.args, w.lead = []rwCall{*w.tail}, [][]*irExpr{make([]*irExpr, len(w.tail.args))}, 1
			}
			w.run = append(w.run, *s.ev)
			w.args = append(w.args, s.args)
		case s.call != nil:
			out = w.flush(out)
			call := *s.call
			if w.tail != nil && w.g.b.l.cellFuncs[s.callee] != nil {
				// The callee's variant for the state (cellEntryCall).
				if v, ok := w.g.b.l.cellVariant(s.callee, *w.tail); ok {
					x := *call.X
					x.Name = v
					call.X = &x
				}
			}
			out = append(out, &call)
			w.tail = nil // the call names the writer
		case s.own != nil:
			out = w.flush(out)
			out = append(out, s.own)
			w.tail = nil
		case s.loop != nil:
			out = w.flush(out)
			w.tail = nil
			loop := *s.loop
			loop.Body = w.flush(w.steps(s.body))
			out = append(out, &loop)
			w.tail = nil
		case s.sw != nil:
			out = w.flush(out)
			entry := w.tail
			sw := *s.sw
			sw.Cases = nil
			for k, c := range s.sw.Cases {
				arm := *c
				w.tail = entry
				arm.Body = append(append([]*irStmt(nil), s.heads[k]...), w.flush(w.steps(s.arms[k]))...)
				sw.Cases = append(sw.Cases, &arm)
			}
			out = append(out, &sw)
			w.tail = nil // a switch's arms end in states of their own
		}
	}
	return out
}

// flush ends the run: its statements after out, and tail the state its
// last call leaves when the laws make that call idempotent (cellEvents).
func (w *wireWalk) flush(out []*irStmt) []*irStmt {
	if len(w.run) > 0 {
		stmts, last := w.g.runStmts(w.run, w.args, w.lead)
		out = append(out, stmts...)
		w.tail = w.g.b.l.idempotent(w.g.cw.prov, last)
	}
	w.run, w.args, w.lead = nil, nil, 0
	return out
}

// runStmts is cellRun on the IR: the rewritten calls when the provider's
// laws rewrite the run, else its own calls, without its lead carried
// calls; last is the run's last call when its arguments are constants.
func (g *wireGen) runStmts(run []rwCall, args [][]*irExpr, lead int) (out []*irStmt, last *rwCall) {
	l := g.b.l
	calls, rewritten := run[lead:], false
	if rw := l.cellRewriter(g.cw.prov); rw != nil {
		if got, changed := rw.run(g.cw.prov, run); changed {
			calls, rewritten = got, true
		}
	}
	if !rewritten {
		args = args[lead:]
	}
	if n := len(calls); n > 0 {
		c := calls[n-1]
		last = &c
		for _, a := range c.args {
			if a.c == nil {
				last = nil
			}
		}
	}
	used := map[[2]int]bool{}
	for k, c := range calls {
		var as []*irExpr
		varies := false
		for j, a := range c.args {
			switch {
			case a.c != nil:
				as = append(as, g.b.constant(c.fn.Sig.Params[j+1].Type, a.c, nil, g.p))
			case !rewritten:
				as, varies = append(as, args[k][j]), true
			default:
				pos, arg := rwPlace(a.text)
				used[[2]int{pos, arg}] = true
				as, varies = append(as, args[pos][arg]), true
			}
		}
		if varies && rewriteLowering != "reference" {
			if s, ok := g.branch(c, as); ok {
				out = append(out, s)
				continue
			}
		}
		out = append(out, g.emit(c, as))
	}
	if rewritten {
		// An argument the rewritten run no longer passes is still read
		// (cellRun): its value is dropped, never its evaluation.
		for k := lead; k < len(run); k++ {
			for j, a := range run[k].args {
				if a.c == nil && !used[[2]int{k, j}] {
					out = append(out, &irStmt{K: "discard", Pos: g.p, X: args[k][j]})
				}
			}
		}
	}
	return out, last
}

// emit is the statement that calls the provider's function c.fn on the
// cell with the arguments as.
func (g *wireGen) emit(c rwCall, as []*irExpr) *irStmt {
	args := append([]*irExpr{g.cell(c.fn.Ptr)}, as...)
	return &irStmt{K: "expr", Pos: g.p, X: g.funcCall(c.fn, args)}
}

// branch is cellBranch on the IR: c, a call of a provider function whose
// body is an if of a pure condition (branchDef), as that if.
func (g *wireGen) branch(c rwCall, as []*irExpr) (*irStmt, bool) {
	tagged := rwCall{fn: c.fn}
	for j, a := range c.args {
		if a.c == nil {
			a = rwArg{text: "#" + strconv.Itoa(j)}
		}
		tagged.args = append(tagged.args, a)
	}
	cond, condArgs, then, els, ok := branchDef(tagged)
	if !ok {
		return nil, false
	}
	exprs := func(fn *check.Func, rs []rwArg, skip int) []*irExpr {
		var out []*irExpr
		for i, a := range rs {
			if a.c != nil {
				out = append(out, g.b.constant(fn.Sig.Params[i+skip].Type, a.c, nil, g.p))
				continue
			}
			j, _ := strconv.Atoi(a.text[1:])
			out = append(out, as[j])
		}
		return out
	}
	arm := func(calls []rwCall) []*irStmt {
		var out []*irStmt
		for _, ac := range calls {
			out = append(out, g.emit(ac, exprs(ac.fn, ac.args, 1)))
		}
		return out
	}
	return &irStmt{K: "if", Pos: g.p, End: g.p, X: g.funcCall(cond, exprs(cond, condArgs, 0)), Then: arm(then), Else: arm(els)}, true
}
