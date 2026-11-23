package lower

// IR builder: a codec decoder function on the prelude's Reader
// (PORT-WIRE-READERS R1). The text emitter writes it (wireDecodeBody); on
// the IR path the same function is built here as an IR body, statement for
// statement, in the forms R1 takes: a basic value, a record whose fields
// are neither omittable nor typed by a type parameter, a list and a map.
// Each read is the Reader interface's operation, the task first, as readOp
// writes it; a failure returns as wireDecodeBody's `return zero, err`
// does, the same value at the same read. Any other form, a direct reader
// (R2) or a type that holds type parameters keeps its text body and its
// census reason "codec reader: <form>" (wireReadForm).
//
// The text form stays selectable as a test oracle only: wireReadLowering
// "text" keeps every decoder's text, and goes with the text path.

import (
	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// wireReadLowering selects the decoders' form on the IR path: "" (an IR
// body where wireRead takes the form) or "text" (the reference, a test
// oracle that goes with the text path).
var wireReadLowering = ""

// wireReadForms are the forms wireRead builds (wireReadForm's names).
func wireReadForms(form string) bool {
	return form == "basic" || form == "record" || form == "list" || form == "map"
}

// wireRead builds the decoder name of t on the Reader interface as an IR
// body and reports whether it did; tc is the decoder's type context.
func (st *irState) wireRead(l *lowerer, name string, t check.Type, tc *typeCtx, params *namer, site syntax.Node) (ok bool) {
	// Under a census it builds too: a decoder it takes is not counted as
	// a text body, one it refuses is, by its reason (codecText).
	if _, own := check.WireReadOf(t); own {
		// A type's own read (wireread.go) first tries its reader's
		// adapter: R4 builds it.
		st.codecText(name, "codec reader: own read")
		return false
	}
	if form := l.wireReadForm(t); wireReadLowering == "text" || tc.readVia != 0 || !wireReadForms(form) {
		st.codecText(name, "codec reader: "+form)
		return false
	}
	scope := newNamer(params)
	fc := newFuncCtx(l, scope, false)
	fc.site = site
	b := st.newBuilder(fc, nil) // a codec decoder: no source statements
	b.env, b.next, b.fn, b.names = 0, 2, &irFunc{Name: name}, scope
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
	d := &wireReader{b: b, tc: tc, site: site, p: b.pos(site, site.Pos())}
	body := d.body(t)
	b.fn.Pos, b.fn.End = d.p, d.p
	b.fn.Body = irScopesIncrease(irUseLocals(body), b.fn.Pos) // ir_end.go
	st.out.Funcs = append(st.out.Funcs, *b.fn)
	st.report.CodecRead = append(st.report.CodecRead, name)
	return true
}

// wireReader builds one decoder's body: locals 0 and 1 are the
// environment and the reader.
type wireReader struct {
	b    *irBuilder
	tc   *typeCtx
	site syntax.Node
	p    irPos
}

func (d *wireReader) local(i int) *irExpr { return &irExpr{K: "local", Pos: d.p, I: i} }

// op is the Reader operation name on r, the task first (readOp).
func (d *wireReader) op(name string, args ...*irExpr) *irExpr {
	task := &irExpr{K: "field", Pos: d.p, Name: envTask, X: d.local(0)}
	return &irExpr{K: "mcall", Pos: d.p, Name: name, X: d.local(1), Args: append([]*irExpr{task}, args...)}
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
	if _, param := t.(*check.TypeParam); param || len(freeTypeParams(t)) > 0 {
		irNo("codec reader: generic")
	}
	name := l.wireFunc("decode", t, d.site)
	pre, v := d.checked(&irExpr{K: "call", Pos: d.p, Name: name, Args: []*irExpr{d.local(0), d.local(1)}}, b.goType(t, d.site))
	return pre, d.local(v)
}

// body is the decoder's statements (wireDecodeBody's forms R1 takes).
func (d *wireReader) body(t check.Type) []*irStmt {
	b, l := d.b, d.b.l
	ret := func(x *irExpr) *irStmt { return &irStmt{K: "ret", Pos: d.p, X: x} }
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
		body := []*irStmt{{K: "def", Pos: d.p, Local: k, X: d.op("Key")}}
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
	}
	irNo("codec reader: %s", l.wireReadForm(t))
	return nil
}

// record reads the fields of s into out, after BeginObject: each key's
// field, an unknown key skipped, then End and a missing field's failure,
// in the field order (wireDecodeBody's record).
func (d *wireReader) record(td *typeDecl, s *check.Struct, out int) []*irStmt {
	b := d.b
	var pre []*irStmt
	seen := make([]int, len(s.Fields))
	for i := range s.Fields {
		seen[i] = b.temp("seen", "bool", d.p)
		pre = append(pre, &irStmt{K: "var", Pos: d.p, Local: seen[i]})
	}
	sw := &irStmt{K: "switch", Pos: d.p, End: d.p, ElsePos: d.p, X: d.op("Key")}
	for i, f := range s.Fields {
		if _, _, param := d.tc.paramField(f.Type); param || check.WireFormOf(f.Type) == check.WireOmittable {
			irNo("codec reader: record")
		}
		vpre, e := d.value(f.Type)
		field := &irExpr{K: "field", Pos: d.p, Name: td.fieldName[f.Name], X: d.local(out)}
		body := append(vpre, &irStmt{K: "store", Pos: d.p, X: field, Y: e}, &irStmt{K: "set", Pos: d.p, Local: seen[i], X: &irExpr{K: "const", Pos: d.p, T: "bool", V: "true"}})
		sw.Cases = append(sw.Cases, &irCase{Pos: d.p, Vals: []*irExpr{irStringConst(d.p, "string", check.WireFieldName(f))}, Body: body})
	}
	sw.Cases = append(sw.Cases, &irCase{Pos: d.p, Def: true, Body: []*irStmt{{K: "expr", Pos: d.p, X: d.op("Skip")}}})
	pre = append(pre, &irStmt{K: "while", Pos: d.p, End: d.p, X: d.op("More"), Body: []*irStmt{sw}}, &irStmt{K: "expr", Pos: d.p, X: d.op("End")})
	for i, f := range s.Fields {
		missing := d.failure(check.WireMissingName, d.op("Path"), irStringConst(d.p, "string", check.WireFieldName(f)))
		pre = append(pre, d.failIf(&irExpr{K: "un", Pos: d.p, Op: "!", X: d.local(seen[i])}, missing))
	}
	return pre
}
