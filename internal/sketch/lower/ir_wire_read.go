package lower

// IR builder for codec decoder functions. Each read uses the Reader
// interface's method, the direct reader's function, or a type parameter's
// codec witness. A failure returns the same value at the same read as
// wireDecodeBody's `return zero, err`.
//
// The text form stays selectable as a test oracle only: wireReadLowering
// "text" keeps every decoder's text, and goes with the text path.

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// wireReadLowering selects the decoders' form on the IR path: "" (an IR
// body where wireRead takes the form) or "text" (the reference, a test
// oracle that goes with the text path).
var wireReadLowering = ""

// wireReadForms are the forms wireRead builds (wireReadForm's names).
func wireReadForms(form string) bool {
	switch form {
	case "basic", "record", "list", "map", "union", "pair", "refined", "nullable", "tuple",
		"append pair", "bare", "members", "number text", "generic":
		return true
	default:
		return false
	}
}

// wireRead builds t's decoder as an IR body and reports whether it did;
// tc is the decoder's type context and selects the reader path.
func (st *irState) wireRead(l *lowerer, name string, t check.Type, tc *typeCtx, params *namer, site syntax.Node) (ok bool) {
	// Under a census it builds too: a decoder it takes is not counted as
	// a text body, one it refuses is, by its reason (codecText).
	if form := l.wireReadForm(t); wireReadLowering == "text" || !wireReadForms(form) {
		if _, own := check.WireReadOf(t); own {
			form = "own read"
		}
		st.codecText(name, "codec reader: "+form)
		return false
	}
	scope := newNamer(params)
	fc := newFuncCtx(l, scope, false)
	fc.site = site
	fc.params = tc.params
	b := st.newBuilder(fc, nil) // a codec decoder: no source statements
	free := freeTypeParams(t)
	b.env, b.next, b.fn, b.names = 0, len(free)+2, &irFunc{Name: name}, scope
	files := len(st.out.Files)
	defer func() {
		if r := recover(); r != nil {
			why, no := r.(irUnsupported)
			if !no {
				panic(r)
			}
			st.codecText(name, why.reason)
			// A refused function records nothing: no import, no file.
			st.out.Files = st.out.Files[:files]
			for path, i := range st.files {
				if i >= files {
					delete(st.files, path)
				}
			}
			ok = false
		}
	}()
	d := &wireReader{b: b, tc: tc, site: site, p: b.pos(site, site.Pos()), free: free}
	body, complete := d.ownRead(t)
	if !complete {
		body = append(body, d.body(t)...)
	}
	b.fn.Pos, b.fn.End = d.p, d.p
	b.fn.Body = irScopesIncrease(irUseLocals(body), b.fn.Pos) // ir_end.go
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.CodecRead = append(st.report.CodecRead, name)
	return true
}

// wireReader builds one decoder's body: the environment is local 0,
// followed by the codec witnesses, then the reader.
type wireReader struct {
	b    *irBuilder
	tc   *typeCtx
	site syntax.Node
	p    irPos
	free []*check.TypeParam
}

func (d *wireReader) reader() *irExpr { return d.local(1 + len(d.free)) }

func (d *wireReader) witness(tp *check.TypeParam) *irExpr {
	for i, p := range d.free {
		if p == tp {
			return d.local(i + 1)
		}
	}
	ice(d.site, "the wire type parameter %s has no codec witness here", tp.Name)
	return nil
}

// ownRead selects a format's own decoder statically for concrete readers
// and by the same operations adapter test as wireOwnRead for the interface.
func (d *wireReader) ownRead(t check.Type) ([]*irStmt, bool) {
	hook, ok := check.WireReadOf(t)
	if !ok {
		return nil, false
	}
	b, l := d.b, d.b.l
	read := func(r *irExpr) []*irStmt {
		out := b.temp("out", b.goType(t, d.site), d.p)
		ptr := &irExpr{K: "addr", Pos: d.p, X: d.local(out)}
		call := b.machineCall(hook.Func, 0, []*irExpr{ptr, r}, d.p, d.site)
		body := []*irStmt{{K: "var", Pos: d.p, Local: out}}
		if l.useFunc(hook.Func, d.site).fallible {
			pre, _ := d.checked(call, "")
			body = append(body, pre...)
		} else {
			body = append(body, &irStmt{K: "expr", Pos: d.p, X: call})
		}
		return append(body, &irStmt{K: "ret", Pos: d.p, X: d.local(out)})
	}
	if d.tc.readVia > 0 {
		if !sameDecl(l.directReaders[d.tc.readVia-1].t, hook.Reader) {
			return nil, false
		}
		return read(d.reader()), true
	}
	key := l.typeKey(hook.Reader) + " => " + l.typeKey(check.PreludeType(check.WireReaderName))
	name, ok := l.pkgNames[pkgKey{"adapter", key}]
	if !ok {
		return nil, false
	}
	if _, emitted := l.adapters[key]; !emitted {
		conv, ok := l.adapterConvs[key]
		if !ok {
			return nil, false
		}
		newFuncCtx(l, newNamer(l.names), false).operations(conv, "nil", d.site)
	}
	typ := "*" + name
	a := &irExpr{K: "assert", Pos: d.p, T: typ, X: d.reader()}
	reader := &irExpr{K: "field", Pos: d.p, Name: "v", X: a}
	sw := &irStmt{K: "tswitch", Pos: d.p, End: d.p, X: d.reader(),
		Cases: []*irCase{{Pos: d.p, Types: []string{typ}, Body: read(reader)}}}
	return []*irStmt{sw}, false
}

func (d *wireReader) local(i int) *irExpr { return &irExpr{K: "local", Pos: d.p, I: i} }

// op is the Reader operation name on r (readOp): on the Reader interface
// its method, the task first; on a direct reader (R2a) the call of the
// reader type's own function for the operation, r first, as
// wireCallFunc writes it.
func (d *wireReader) op(name string, args ...*irExpr) *irExpr {
	if d.tc.readVia > 0 {
		f := d.b.l.directReaders[d.tc.readVia-1].conv.Table[name]
		return d.b.machineCall(f, 0, append([]*irExpr{d.reader()}, args...), d.p, d.site)
	}
	task := &irExpr{K: "field", Pos: d.p, Name: envTask, X: d.local(0)}
	return &irExpr{K: "mcall", Pos: d.p, Name: name, X: d.reader(), Args: append([]*irExpr{task}, args...)}
}

// failIf is `if cond { return zero, failure }`.
func (d *wireReader) failIf(cond, failure *irExpr) *irStmt {
	return &irStmt{K: "if", Pos: d.p, End: d.p, X: cond, Then: []*irStmt{{K: "fail", Pos: d.p, X: failure}}}
}

// checked is `v, err := call; if err != nil { return zero, err }` (v < 0:
// a call with only an error).
func (d *wireReader) checked(call *irExpr, typ string) ([]*irStmt, int) {
	b := d.b
	e := b.temp("err", "error", d.p)
	if typ == "" {
		return []*irStmt{{K: "def", Pos: d.p, Local: e, X: call}, d.failIf(b.notNil(e, d.p), d.local(e))}, -1
	}
	v := b.temp("v", typ, d.p)
	return []*irStmt{{K: "def2", Pos: d.p, Local: v, Val: &e, X: call}, d.failIf(b.notNil(e, d.p), d.local(e))}, v
}

// failure is the prelude failure name with the given field values
// (wireFailure).
func (d *wireReader) failure(name string, vals ...*irExpr) *irExpr {
	l := d.b.l
	n := check.PreludeType(name)
	if n == nil {
		ice(d.site, "the prelude has no %s", name)
	}
	td := l.useType(l.typeDecl(n.Obj, d.site))
	s, ok := n.Underlying().(*check.Struct)
	if !ok || len(s.Fields) != len(vals) {
		ice(d.site, "the prelude's %s is not a record of %d fields", name, len(vals))
	}
	lit := &irExpr{K: "lit", Pos: d.p, T: d.b.goType(n, d.site), Elems: vals}
	for _, f := range s.Fields {
		lit.Keys = append(lit.Keys, td.fieldName[f.Name])
	}
	return lit
}

// wrongKind is the WireWrongKind of r's place, wanting want (wireKind).
func (d *wireReader) wrongKind(want string) *irExpr {
	return d.failure(check.WireKindName, d.op("Path"), irStringConst(d.p, "string", want))
}

// value reads a value of type t (wireDecode): a basic one by its Reader
// operation, any other by its decoder function.
func (d *wireReader) value(t check.Type) ([]*irStmt, *irExpr) {
	b, l := d.b, d.b.l
	if l.wireInline(t) {
		k := wireUnder(t).(*check.Basic)
		read, raw := "Int", "int64"
		switch {
		case k.Kind == check.Bool:
			read, raw = "Bool", "bool"
		case k.Kind == check.String:
			read, raw = "String", "string"
		case k.Kind == check.Float32 || k.Kind == check.Float64:
			read, raw = "Float", "float64"
		case k.Kind >= check.Uint && k.Kind <= check.Uintptr:
			read, raw = "Uint", "uint64"
		}
		pre, v := d.checked(d.op(read), raw)
		bin := func(op string, x *irExpr, lit string) *irExpr {
			return &irExpr{K: "bin", Pos: d.p, Op: op, X: x, Y: &irExpr{K: "const", Pos: d.p, T: raw, V: lit}}
		}
		if lo, hi, ok := intRange(k.Kind); ok {
			cond := &irExpr{K: "bin", Pos: d.p, Op: "||", X: bin("<", d.local(v), lo), Y: bin(">", d.local(v), hi)}
			pre = append(pre, d.failIf(cond, d.wrongKind(k.Name)))
		} else if hi, ok := uintMax(k.Kind); ok {
			pre = append(pre, d.failIf(bin(">", d.local(v), hi), d.wrongKind(k.Name)))
		}
		return pre, &irExpr{K: "conv", Pos: d.p, T: b.goType(t, d.site), X: d.local(v)}
	}
	if tp, param := t.(*check.TypeParam); param {
		return d.readWitness(tp, d.witness(tp))
	}
	name := l.wireFunc(d.tc.decodeDir(), t, d.site)
	args := []*irExpr{d.local(0)}
	var types []string
	for _, tp := range freeTypeParams(t) {
		args = append(args, d.witness(tp))
		types = append(types, b.goType(tp, d.site))
	}
	args = append(args, d.reader())
	pre, v := d.checked(&irExpr{K: "call", Pos: d.p, Name: name, T: strings.Join(types, ", "), Args: args}, b.goType(t, d.site))
	return pre, d.local(v)
}

// readWitness calls the selected reader path through the parameter's
// codec or, for its omittable record-field form, through its Field.
func (d *wireReader) readWitness(tp *check.TypeParam, witness *irExpr) ([]*irStmt, *irExpr) {
	dec := &irExpr{K: "field", Pos: d.p, Name: d.tc.decodeField(), X: witness}
	pre, x := d.checked(&irExpr{K: "vcall", Pos: d.p, X: dec, Args: []*irExpr{d.local(0), d.reader()}}, d.b.goType(tp, d.site))
	return pre, d.local(x)
}

// body is the decoder's statements (wireDecodeBody).
func (d *wireReader) body(t check.Type) []*irStmt {
	b, l := d.b, d.b.l
	ret := func(x *irExpr) *irStmt { return &irStmt{K: "ret", Pos: d.p, X: x} }
	switch check.WireFormOf(t) {
	case check.WireNullable:
		return d.nullable(t)
	case check.WireBare:
		return d.bare(t)
	case check.WireMembers:
		return d.members(t)
	case check.WireNumberText:
		pair := l.info.WirePairOf(t)
		pre, x := d.checked(d.op("Number"), "string")
		v := &irExpr{K: "conv", Pos: d.p, T: b.goType(pair.Base, d.site), X: d.local(x)}
		return append(pre, ret(b.machineCall(pair.Parse, 0, []*irExpr{v}, d.p, d.site)))
	}
	if pair := l.info.WirePairOf(t); pair != nil {
		if pair.Append {
			buf := b.temp("buf", "[64]byte", d.p)
			slice := &irExpr{K: "slice", Pos: d.p, X: d.local(buf), Args: []*irExpr{nil, {K: "const", Pos: d.p, T: "int", V: "0"}}}
			pre, x := d.checked(d.op("StringBytes", slice), b.goType(pair.Parse.Sig.Params[0].Type, d.site))
			body := append([]*irStmt{{K: "var", Pos: d.p, Local: buf}}, pre...)
			return append(body, ret(b.machineCall(pair.Parse, 0, []*irExpr{d.local(x)}, d.p, d.site)))
		}
		return d.parsed(pair.Base, pair.Parse)
	}
	if parse := check.RefinedParse(t); parse != nil {
		return d.parsed(parse.Sig.Params[0].Type, parse)
	}
	if l.wireInline(t) {
		pre, x := d.value(t)
		return append(pre, ret(x))
	}
	gt := b.goType(t, d.site)
	begin := func(what string) []*irStmt {
		pre, _ := d.checked(d.op("Begin"+what), "")
		return pre
	}
	end := &irStmt{K: "expr", Pos: d.p, X: d.op("End")}
	switch u := wireUnder(t).(type) {
	case *check.Slice:
		out := b.temp("out", gt, d.p)
		pre := append(begin("List"), &irStmt{K: "def", Pos: d.p, Local: out, X: &irExpr{K: "lit", Pos: d.p, T: gt}})
		vpre, e := d.value(u.Elem)
		loop := &irStmt{K: "while", Pos: d.p, End: d.p, X: d.op("More"), Body: append(vpre, &irStmt{K: "set", Pos: d.p, Local: out, X: &irExpr{K: "append", Pos: d.p, Args: []*irExpr{d.local(out), e}}})}
		return append(pre, loop, end, ret(d.local(out)))
	case *check.Map:
		l.intMapConflict(u) // intmap.go: the codec makes Go's map
		out := b.temp("out", gt, d.p)
		k := b.temp("k", "string", d.p)
		pre := append(begin("Object"), &irStmt{K: "def", Pos: d.p, Local: out, X: &irExpr{K: "lit", Pos: d.p, T: gt}})
		// The key a Map keeps is a copy (json 2; wireDecodeBody's Map).
		kept := &irExpr{K: "pcall", Pos: d.p, T: runtimeImport, Name: "WireKeyKept", Args: []*irExpr{d.op("Key")}}
		body := []*irStmt{{K: "def", Pos: d.p, Local: k, X: kept}}
		vpre, e := d.value(u.Elem)
		body = append(body, vpre...)
		key := &irExpr{K: "conv", Pos: d.p, T: b.goType(u.Key, d.site), X: d.local(k)}
		body = append(body, &irStmt{K: "store", Pos: d.p, X: &irExpr{K: "mapAt", Pos: d.p, X: d.local(out), Y: key}, Y: e}) // ergoir_mapstore.go
		return append(pre, &irStmt{K: "while", Pos: d.p, End: d.p, X: d.op("More"), Body: body}, end, ret(d.local(out)))
	case *check.Struct:
		n, ok := t.(*check.Named)
		if !ok {
			irNo("codec reader: record")
		}
		out := b.temp("out", gt, d.p)
		pre := append(begin("Object"), &irStmt{K: "var", Pos: d.p, Local: out})
		pre = append(pre, d.record(l.typeDecl(n.Obj, d.site), u, out)...)
		return append(pre, ret(d.local(out)))
	case *check.Union:
		return append(begin("Object"), d.union(t, u)...)
	case *check.Tuple:
		return append(begin("List"), d.tuple(u, gt)...)
	}
	irNo("codec reader: %s", l.wireReadForm(t))
	return nil
}

// bare selects variants by the stream event's kind, without consuming
// it until the matching decoder (or TakeNull) runs.
func (d *wireReader) bare(t check.Type) []*irStmt {
	b := d.b
	kind := check.PreludeType("WireKind")
	peek := b.temp("kind", b.goType(kind, d.site), d.p)
	pre := []*irStmt{{K: "def", Pos: d.p, Local: peek, X: d.op("Peek")}}
	sw := &irStmt{K: "tswitch", Pos: d.p, End: d.p, X: d.local(peek)}
	flat := b.flatOf(kind)
	if flat != nil {
		sw.K, sw.X = "switch", b.flatTag(flat, peek, d.p, d.site)
	}
	for _, v := range wireUnder(t).(*check.Union).Variants {
		name := check.WireVariantKind(v)
		vt := check.PreludeType(name)
		c := &irCase{Pos: d.p}
		if flat == nil {
			c.Types = []string{b.goType(vt, d.site)}
		} else {
			c.Vals = []*irExpr{{K: "const", Pos: d.p, T: "uint8", V: strconv.Itoa(flat.tagOf(vt))}}
		}
		var value *irExpr
		if name == "WireNull" {
			c.Body = []*irStmt{{K: "discard", Pos: d.p, X: d.op("TakeNull")}}
			value = d.wrap(t, v, nil, nil)
		} else if f := barePart(v); f != nil {
			body, x := d.value(f.Type)
			c.Body, value = body, d.wrap(t, v, f, x)
		} else {
			body, x := d.value(v)
			c.Body, value = body, b.variantInto(v, t, x, d.site)
		}
		c.Body = append(c.Body, &irStmt{K: "ret", Pos: d.p, X: value})
		sw.Cases = append(sw.Cases, c)
	}
	sw.Cases = append(sw.Cases, &irCase{Pos: d.p, Def: true, Body: []*irStmt{
		{K: "fail", Pos: d.p, X: d.wrongKind("a value of the bare union")},
	}})
	return append(pre, sw)
}

// members reads an ordered object, retaining every occurrence of a key.
func (d *wireReader) members(t check.Type) []*irStmt {
	b := d.b
	outer, list, entry, td := d.tc.memberParts(t)
	gt := b.goType(outer.Type, d.site)
	out := b.temp("members", gt, d.p)
	key := b.temp("key", "string", d.p)
	pre, _ := d.checked(d.op("BeginObject"), "")
	pre = append(pre, &irStmt{K: "def", Pos: d.p, Local: out, X: &irExpr{K: "lit", Pos: d.p, T: gt}})
	keyRead := d.op("Key")
	kept := &irExpr{K: "pcall", Pos: d.p, T: runtimeImport, Name: "WireKeyKept", Args: []*irExpr{keyRead}}
	body := []*irStmt{{K: "def", Pos: d.p, Local: key, X: kept}}
	vpre, x := d.value(entry.Fields[1].Type)
	body = append(body, vpre...)
	e := &irExpr{K: "lit", Pos: d.p, T: b.goType(list.Elem, d.site),
		Keys:  []string{td.fieldName[entry.Fields[0].Name], td.fieldName[entry.Fields[1].Name]},
		Elems: []*irExpr{{K: "conv", Pos: d.p, T: b.goType(entry.Fields[0].Type, d.site), X: d.local(key)}, x}}
	body = append(body, &irStmt{K: "set", Pos: d.p, Local: out, X: &irExpr{K: "append", Pos: d.p, Args: []*irExpr{d.local(out), e}}})
	value := &irExpr{K: "lit", Pos: d.p, T: b.goType(t, d.site), Keys: []string{b.goField(t, outer.Name, d.site)}, Elems: []*irExpr{d.local(out)}}
	return append(pre, &irStmt{K: "while", Pos: d.p, End: d.p, X: d.op("More"), Body: body},
		&irStmt{K: "expr", Pos: d.p, X: d.op("End")}, &irStmt{K: "ret", Pos: d.p, X: value})
}

// nullable reads null itself, or reads the present variant's payload.
func (d *wireReader) nullable(t check.Type) []*irStmt {
	empty, present, payload := d.tc.wrapperParts(t)
	null := &irStmt{K: "ret", Pos: d.p, X: d.wrap(t, empty, nil, nil)}
	body := []*irStmt{{K: "if", Pos: d.p, End: d.p, X: d.op("TakeNull"), Then: []*irStmt{null}}}
	pre, x := d.value(payload.Type)
	return append(append(body, pre...), &irStmt{K: "ret", Pos: d.p, X: d.wrap(t, present, payload, x)})
}

// tuple reads exactly the arity of t, testing More before each element
// and again before End. A wrong arity fails at the cursor's current path.
func (d *wireReader) tuple(t *check.Tuple, gt string) []*irStmt {
	want := "a list of " + strconv.Itoa(len(t.Types))
	result := &irExpr{K: "lit", Pos: d.p, T: gt}
	var body []*irStmt
	for i, typ := range t.Types {
		noMore := &irExpr{K: "un", Pos: d.p, Op: "!", X: d.op("More")}
		body = append(body, d.failIf(noMore, d.wrongKind(want)))
		pre, x := d.value(typ)
		body = append(body, pre...)
		result.Keys = append(result.Keys, tupleField(i))
		result.Elems = append(result.Elems, x)
	}
	return append(body, d.failIf(d.op("More"), d.wrongKind(want)), &irStmt{K: "expr", Pos: d.p, X: d.op("End")}, &irStmt{K: "ret", Pos: d.p, X: result})
}

// parsed reads the base and returns the parser's result. Returning the
// call passes through its failure ABI, with a nil error for a pure value.
func (d *wireReader) parsed(base check.Type, parse *check.Func) []*irStmt {
	pre, x := d.value(base)
	call := d.b.machineCall(parse, 0, []*irExpr{x}, d.p, d.site)
	return append(pre, &irStmt{K: "ret", Pos: d.p, X: call})
}

// union dispatches by the wire tag before reading the selected record.
// A failed Tag and an unrecognised tag are distinct failures.
func (d *wireReader) union(t check.Type, u *check.Union) []*irStmt {
	b := d.b
	pre, tag := d.checked(d.op("Tag"), "string")
	sw := &irStmt{K: "switch", Pos: d.p, End: d.p, X: d.local(tag)}
	for _, v := range u.Variants {
		n := v.(*check.Named)
		s := n.Underlying().(*check.Struct)
		out := b.temp("out", b.goType(v, d.site), d.p)
		body := []*irStmt{{K: "var", Pos: d.p, Local: out}}
		body = append(body, d.record(b.l.typeDecl(n.Obj, d.site), s, out)...)
		result := b.variantInto(v, t, d.local(out), d.site)
		body = append(body, &irStmt{K: "ret", Pos: d.p, X: result})
		sw.Cases = append(sw.Cases, &irCase{Pos: d.p, Vals: []*irExpr{irStringConst(d.p, "string", n.Obj.Name)}, Body: body})
	}
	unknown := d.failure(check.WireTagName, d.op("Path"), d.local(tag))
	sw.Cases = append(sw.Cases, &irCase{Pos: d.p, Def: true, Body: []*irStmt{{K: "fail", Pos: d.p, X: unknown}}})
	return append(pre, sw)
}

// wrap constructs an empty or a single-payload alternative of a wire union.
func (d *wireReader) wrap(t, variant check.Type, payload *check.Field, value *irExpr) *irExpr {
	b := d.b
	e := &irExpr{K: "zero", Pos: d.p, T: b.goType(variant, d.site)}
	if payload != nil {
		e.K = "lit"
		e.Keys = []string{b.goField(variant, payload.Name, d.site)}
		e.Elems = []*irExpr{value}
	}
	return b.variantInto(variant, t, e, d.site)
}

// record reads fields into out after BeginObject. Omittable fields start
// absent and take their present alternative on each occurrence; only
// required fields need a seen flag and a missing-field check after End.
func (d *wireReader) record(td *typeDecl, s *check.Struct, out int) []*irStmt {
	b := d.b
	var pre []*irStmt
	var after []*irStmt
	sw := &irStmt{K: "switch", Pos: d.p, End: d.p, ElsePos: d.p, X: d.op("Key")}
	for _, f := range s.Fields {
		field := &irExpr{K: "field", Pos: d.p, Name: td.fieldName[f.Name], X: d.local(out)}
		store := func(e *irExpr) *irStmt {
			return &irStmt{K: "store", Pos: d.p, X: field, Y: e}
		}
		var body []*irStmt
		if check.WireFormOf(f.Type) == check.WireOmittable {
			empty, present, payload := d.tc.wrapperParts(f.Type)
			// The empty alternative is not the interface's zero value.
			pre = append(pre, store(d.wrap(f.Type, empty, nil, nil)))
			vpre, e := d.value(payload.Type)
			body = append(vpre, store(d.wrap(f.Type, present, payload, e)))
		} else {
			seen := b.temp("seen", "bool", d.p)
			pre = append(pre, &irStmt{K: "var", Pos: d.p, Local: seen})
			noKey := &irExpr{K: "un", Pos: d.p, Op: "!", X: d.local(seen)}
			missing := d.failure(check.WireMissingName, d.op("Path"), irStringConst(d.p, "string", check.WireFieldName(f)))
			if tp, param := f.Type.(*check.TypeParam); param {
				read, absent := d.recordParam(tp, field, noKey, missing)
				body = []*irStmt{read}
				after = append(after, absent)
			} else {
				vpre, e := d.value(f.Type)
				body = append(vpre, store(e))
				after = append(after, d.failIf(noKey, missing))
			}
			body = append(body, &irStmt{K: "set", Pos: d.p, Local: seen, X: &irExpr{K: "const", Pos: d.p, T: "bool", V: "true"}})
		}
		sw.Cases = append(sw.Cases, &irCase{Pos: d.p, Vals: []*irExpr{irStringConst(d.p, "string", check.WireFieldName(f))}, Body: body})
	}
	sw.Cases = append(sw.Cases, &irCase{Pos: d.p, Def: true, Body: []*irStmt{{K: "expr", Pos: d.p, X: d.op("Skip")}}})
	pre = append(pre, &irStmt{K: "while", Pos: d.p, End: d.p, X: d.op("More"), Body: []*irStmt{sw}}, &irStmt{K: "expr", Pos: d.p, X: d.op("End")})
	return append(pre, after...)
}

// recordParam handles a type parameter instantiated with either a
// required or an omittable field. The witness decides which reader to
// call and whether a missing key fails or supplies the empty value.
func (d *wireReader) recordParam(tp *check.TypeParam, field, noKey, missing *irExpr) (read, absent *irStmt) {
	store := func(x *irExpr) *irStmt { return &irStmt{K: "store", Pos: d.p, X: field, Y: x} }
	wit := d.witness(tp)
	fieldWit := &irExpr{K: "field", Pos: d.p, Name: "Field", X: wit}
	zero := &irExpr{K: "zero", Pos: d.p, T: "*" + d.b.spelling(d.tc.wireFieldType(tp))}
	withField := &irExpr{K: "bin", Pos: d.p, Op: "!=", X: fieldWit, Y: zero}
	fp, fv := d.readWitness(tp, fieldWit)
	wp, wv := d.readWitness(tp, wit)
	read = &irStmt{K: "if", Pos: d.p, End: d.p, X: withField, Then: append(fp, store(fv)), Else: append(wp, store(wv))}
	noField := &irExpr{K: "un", Pos: d.p, Op: "!", X: withField}
	empty := &irExpr{K: "field", Pos: d.p, Name: "Empty", X: fieldWit}
	absent = &irStmt{K: "if", Pos: d.p, End: d.p, X: noKey, Then: []*irStmt{d.failIf(noField, missing), store(empty)}}
	return read, absent
}
