package check

import "ergo.local/prototype/internal/sketch/syntax"

// The predeclared encode and decode (types.md, Codecs; L11 design items 2
// to 5). encode(v, w) writes v's wire form as events on the Writer w;
// decode[T](r) reads a T from the Reader r. Both take a codable type, and
// the lowering derives the walk from the type (lower/wire.go); the
// checker decides what the walk may fail.

// The prelude names the codec reads, each spelled here once.
const (
	wireWriterName  = "Writer"
	wireReaderName  = "Reader"
	wireKindName    = "WireWrongKind"
	wireMissingName = "WireMissingField"
	wireTagName     = "WireUnknownTag"
	wireParsePrefix = "Parse"
	wireMethodName  = "Wire"
)

// WirePair is the by-name wire form of a type X (types.md, Codecs 1): its
// package declares ParseX(b B) X and Wire(x X) B for a codable B, so X
// encodes as Wire(x) and decodes through ParseX. Coherence: the pair is
// looked up in X's own package only, as construction authority is.
type WirePair struct {
	Parse, Wire *Func
	Base        Type // B
}

// wirePair is t's by-name pair, or nil. A refined type has none: its
// derived ParseN decodes it (wireWalk reads the refinement first).
func (c *checker) wirePair(t Type) *WirePair {
	n, ok := t.(*Named)
	if !ok || n.Obj == nil || n.Obj.Pkg == nil || len(n.TArgs) > 0 || len(n.origin().TParams) > 0 {
		return nil
	}
	parse, _ := n.Obj.Pkg.Scope.names[wireParsePrefix+n.Obj.Name].(*Func)
	wire := n.method(wireMethodName)
	if parse == nil || wire == nil {
		return nil
	}
	ps, ws := c.funcSig(parse), c.funcSig(wire)
	if ps == nil || ws == nil || len(ps.TParams) > 0 || len(ws.TParams) > 0 {
		return nil
	}
	if len(ps.Params) != 1 || len(ps.Results) != 1 || len(ws.Params) != 1 || len(ws.Results) != 1 {
		return nil
	}
	base := ws.Results[0]
	if !c.identical(ps.Results[0], t) || !c.identical(ws.Params[0].Type, t) || !c.identical(ps.Params[0].Type, base) || !ws.Errors.Empty() {
		return nil
	}
	return &WirePair{Parse: parse, Wire: wire, Base: base}
}

// WirePairOf is the by-name pair the check found for t, or nil; the
// lowering reads it (Info.WirePairs).
func (i *Info) WirePairOf(t Type) *WirePair {
	if n, ok := t.(*Named); ok {
		return i.WirePairs[n.Obj]
	}
	return nil
}

// builtinKind is the call kind of a call of b.
func builtinKind(b *Builtin) CallKind {
	switch b.Name {
	case "encode":
		return CallEncode
	case "decode":
		return CallDecode
	}
	return CallLen
}

// encodeCall checks encode(v, w): v of a codable type, w a Writer. It has
// no value and fails nothing: a Writer's events do not fail.
func (c *checker) encodeCall(x *syntax.CallExpr) operand {
	if len(x.Args) != 2 {
		c.errorf(x, CodeType, "encode takes a value and a %s: encode(v, w) (types.md, Codecs)", wireWriterName)
		c.silentArgs(x)
		return bad(x)
	}
	v, ok := c.argValue(x.Args[0], nil, x)
	okW := c.argument(x.Args[1], preludeType(wireWriterName), x)
	if !ok || !okW || v.invalid() {
		return bad(x)
	}
	if !c.codableOperand(x.Args[0], v.typ, "encode") {
		return bad(x)
	}
	call := c.callInfo(x)
	call.Kind, call.Type = CallEncode, v.typ
	c.recordWire(v.typ)
	return operand{mode: modeNoValue, typ: NoValue, errs: v.errs}
}

// decodeCall checks decode[T](r): T codable, r a Reader. Its value is a T,
// and its row is wireRow(T).
func (c *checker) decodeCall(x *syntax.CallExpr, t Type) operand {
	if t == nil || isInvalid(t) {
		c.errorf(x, CodeType, "decode takes the type it builds: decode[T](r) (types.md, Codecs)")
		c.silentArgs(x)
		return bad(x)
	}
	if len(x.Args) != 1 {
		c.errorf(x, CodeType, "decode[%s] takes a %s: decode[%s](r) (types.md, Codecs)", t, wireReaderName, t)
		c.silentArgs(x)
		return bad(x)
	}
	if !c.argument(x.Args[0], preludeType(wireReaderName), x) {
		return bad(x)
	}
	if !c.codableOperand(x, t, "decode") {
		return bad(x)
	}
	row := c.wireRow(t)
	call := c.callInfo(x)
	call.Kind, call.Type, call.Fails = CallDecode, t, row
	c.recordWire(t)
	return operand{mode: modeValue, typ: t, errs: row}
}

// codableOperand reports whether what (encode or decode) takes t, else
// S108 at n. A type parameter waits for generic codecs: its walk is not
// known until the instance, and its row has no spelling yet (Q7).
func (c *checker) codableOperand(n syntax.Node, t Type, what string) bool {
	if tp, ok := t.(*TypeParam); ok {
		c.errorf(n, CodeType, "%s of the type parameter %s is not supported yet: write the concrete type (L11, generic codecs wait for the row former, Q7)", what, tp.Name)
		return false
	}
	if r := c.wireReason(t); r != "" {
		c.errorf(n, CodeType, "%s takes a %s type: %s", what, SetWire, r)
		return false
	}
	if p := c.firstInferredParse(t); p != nil {
		c.errorf(n, CodeType, "%s reaches %s, whose fails row is inferred: write it, so the codec's row is known before its body is checked (types.md, Codecs 2)", what, p.Name)
		return false
	}
	return true
}

// recordWire notes the by-name pairs t reaches, for the lowering.
func (c *checker) recordWire(t Type) {
	c.wireEach(t, func(t Type) {
		if p := c.wirePair(t); p != nil {
			n := t.(*Named)
			if c.pkg.Info.WirePairs == nil {
				c.pkg.Info.WirePairs = map[*TypeName]*WirePair{}
			}
			c.pkg.Info.WirePairs[n.Obj] = p
		}
	})
}

// wireEach calls f on t and each type its wire form reaches, once each:
// the parts of the structure and the base of a by-name pair.
func (c *checker) wireEach(t Type, f func(Type)) {
	seen := map[Type]bool{}
	var walk func(Type)
	walk = func(t Type) {
		if seen[t] {
			return
		}
		seen[t] = true
		f(t)
		if RefinementOf(t) != nil {
			return
		}
		if p := c.wirePair(t); p != nil {
			walk(p.Base)
			return
		}
		switch u := under(t).(type) {
		case *Struct:
			for _, fl := range u.Fields {
				walk(fl.Type)
			}
		case *Union:
			for _, v := range u.Variants {
				walk(v)
			}
		case *Slice:
			walk(u.Elem)
		case *Map:
			walk(u.Elem)
		case *Tuple:
			for _, e := range u.Types {
				walk(e)
			}
		}
	}
	walk(t)
}

// firstInferredParse is a by-name ParseX t reaches whose row is inferred.
func (c *checker) firstInferredParse(t Type) *Func {
	var out *Func
	c.wireEach(t, func(t Type) {
		if p := c.wirePair(t); p != nil && out == nil && p.Parse.inferErrors {
			out = p.Parse
		}
	})
	return out
}

// wireRow is the row of decode[T] (types.md, Codecs 2): WireWrongKind for
// any value read, WireMissingField when T reaches a record with fields or
// a union (whose tag may be missing), WireUnknownTag when it reaches a
// union, Unsatisfied when it reaches a refined type, and the row of each
// by-name ParseX it reaches.
func (c *checker) wireRow(t Type) *Row {
	row := newRow().with(c.types, preludeType(wireKindName))
	add := func(name string) { row = row.with(c.types, preludeType(name)) }
	c.wireEach(t, func(t Type) {
		if RefinementOf(t) != nil {
			add("Unsatisfied")
			return
		}
		if p := c.wirePair(t); p != nil {
			row = row.union(c.types, c.types.resolve(c.funcSig(p.Parse).Errors))
			return
		}
		switch u := under(t).(type) {
		case *Struct:
			if len(u.Fields) > 0 {
				add(wireMissingName)
			}
		case *Union:
			add(wireMissingName)
			add(wireTagName)
		}
	})
	return row
}
