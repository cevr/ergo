package check

import (
	"fmt"
	"slices"

	"ergo.local/prototype/internal/sketch/syntax"
)

// The predeclared encode and decode (types.md, Codecs; L11 design items 2
// to 5). encode(v, w) writes v's wire form as events on the Writer w;
// decode[T](r) reads a T from the Reader r. Both take a wire type, and
// the lowering derives the walk from the type (lower/wire.go); the
// checker decides what the walk may fail.

// The prelude names the codec reads, each spelled here once.
const (
	WireWriterName  = "Writer"
	WireReaderName  = "Reader"
	WireKindName    = "WireWrongKind"
	WireMissingName = "WireMissingField"
	WireTagName     = "WireUnknownTag"
	wireMethodName  = "Wire"
	// WiredName is the predeclared row former wired[T] (owner q80).
	WiredName = "wired"
)

// wiredElem resolves the row element wired[X] at x (owner q80): the row of
// decode[X], each wire type parameter X reaches standing as wired[T].
// It reports false when x is not a use of the predeclared wired.
func (c *checker) wiredElem(x syntax.Expr, kind Kind, ctx *typeCtx) (Type, bool) {
	ix, ok := x.(*syntax.IndexExpr)
	if !ok {
		return nil, false
	}
	id, ok := ix.X.(*syntax.Ident)
	if !ok {
		return nil, false
	}
	if b, _ := c.lookup(id.Name).(*Builtin); b == nil || b.Name != WiredName {
		return nil, false
	}
	if kind != KindErrors || len(ix.Indices) != 1 {
		c.errorf(x, CodeType, "%s[T] takes one wire type and stands in a fails row: fails %s[T] (types.md, Codecs 2)", WiredName, WiredName)
		return nil, true
	}
	t := c.typExpr(ix.Indices[0], ctx)
	if isInvalid(t) {
		return nil, true
	}
	if r := c.wireReason(t); r != "" {
		c.errorf(ix.Indices[0], CodeType, "%s takes a %s type: %s", WiredName, SetWire, r)
		return nil, true
	}
	return &RowArg{Row: c.wireRow(t)}, true
}

// WirePair is the by-name wire form of a type X (types.md, Codecs 1): its
// package declares ParseX(b B) X and Wire(x X) B for a wire B, so X
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
	parse, _ := n.Obj.Pkg.Scope.names[syntax.ParseName(n.Obj.Name)].(*Func)
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

// PreludeType is the prelude's named type name, or nil: the lowering reads
// the codec's Writer, Reader and failure types through it.
func PreludeType(name string) *Named { return preludeType(name) }

// RefinedParse is the derived ParseN of the refined type t, or nil.
func RefinedParse(t Type) *Func {
	n, ok := t.(*Named)
	if !ok || RefinementOf(t) == nil || n.Obj == nil || n.Obj.Pkg == nil {
		return nil
	}
	f, _ := n.Obj.Pkg.Scope.names[syntax.ParseName(n.Obj.Name)].(*Func)
	return f
}

// WireFieldName is a field's key on the wire (types.md, Codecs 2): its
// explicit key, otherwise its name with its leading run of capitals lowered, Port as port,
// Some's Value as value (Effect Schema.Option's encoded form), ID as id,
// URL as url. A run that a lower-case letter follows keeps its last
// capital, which starts the next word: HTTPStatus as httpStatus, IOError
// as ioError. Every key mapper (the codec, the TS projection, ergo gen
// rpc) calls this one function, so the two ends agree.
func WireFieldName(field *Field) string {
	if key := field.Origin().WireKey; key != "" {
		return key
	}
	name := field.Name
	n := 0
	for n < len(name) && name[n] >= 'A' && name[n] <= 'Z' {
		n++
	}
	if n > 1 && n < len(name) && name[n] >= 'a' && name[n] <= 'z' {
		n-- // the run's last capital starts the next word
	}
	b := []byte(name)
	for i := 0; i < n; i++ {
		b[i] += 'a' - 'A'
	}
	return string(b)
}

// builtinKind is the call kind of a call of b.
func builtinKind(b *Builtin) CallKind {
	switch b.Name {
	case "encode":
		return CallEncode
	case "decode":
		return CallDecode
	case "embedded":
		return CallEmbed
	}
	return CallLen
}

// encodeCall checks encode(v, w): v of a wire type, w a Writer. It has
// no value and fails nothing: a Writer's events do not fail.
func (c *checker) encodeCall(x *syntax.CallExpr) operand {
	if len(x.Args) != 2 {
		c.errorf(x, CodeType, "encode takes a value and a %s: encode(v, w) (types.md, Codecs)", WireWriterName)
		c.silentArgs(x)
		return bad(x)
	}
	v, ok := c.argValue(x.Args[0], nil, x)
	okW := c.argument(x.Args[1], preludeType(WireWriterName), x)
	if !ok || !okW || v.invalid() {
		return bad(x)
	}
	if !c.wireOperand(x.Args[0], v.typ, "encode") {
		return bad(x)
	}
	call := c.callInfo(x)
	call.Kind, call.Type = CallEncode, v.typ
	c.recordWire(v.typ)
	c.chargeWire(x, v.typ, false)
	return operand{mode: modeNoValue, typ: NoValue, errs: v.errs}
}

// decodeCall checks decode[T](r): T wire, r a Reader. Its value is a T,
// and its row is wireRow(T).
func (c *checker) decodeCall(x *syntax.CallExpr, t Type) operand {
	if t == nil || isInvalid(t) {
		c.errorf(x, CodeType, "decode takes the type it builds: decode[T](r) (types.md, Codecs)")
		c.silentArgs(x)
		return bad(x)
	}
	if len(x.Args) != 1 {
		c.errorf(x, CodeType, "decode[%s] takes a %s: decode[%s](r) (types.md, Codecs)", t, WireReaderName, t)
		c.silentArgs(x)
		return bad(x)
	}
	if !c.argument(x.Args[0], preludeType(WireReaderName), x) {
		return bad(x)
	}
	if !c.wireOperand(x, t, "decode") {
		return bad(x)
	}
	row := c.wireRow(t)
	call := c.callInfo(x)
	call.Kind, call.Type, call.Fails = CallDecode, t, row
	c.recordWire(t)
	c.chargeWire(x, t, true)
	return operand{mode: modeValue, typ: t, errs: row}
}

// wireOperand reports whether what (encode or decode) takes t, else
// S108 at n. A type parameter is wire under the wire constraint;
// its part of the row is wired[T] (wireRow).
func (c *checker) wireOperand(n syntax.Node, t Type, what string) bool {
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

// The needs of a codec (types.md, Codecs 8; G3 of the v1 audit 3): encode(v, w)
// calls the Wire of each by-name pair v's type reaches, and decode[T](r)
// each ParseX, so the call is charged their capabilities as a written call
// of them there is (chargeWire); codecs may carry needs, counted (owner,
// 2026-11-12). A ParseX of this package whose needs row is inferred is
// reached by no name in the body that decodes, so the reference graph has
// no edge to it and its row may still grow when the decode is typed: the
// first check records the edge (lateEdges), and the package is checked
// again with it (wireEdges, checkBodies), so the ParseX's row is final
// first, or joins the fixpoint of its component. A package let's
// initializer has no such edge, and a wire type parameter carries no
// capability row (no needs counterpart of wired[T]), so a codec with needs
// reached there is S108 (checkWireNeeds, satisfies).

// wireNeed is a decode in a package let, or an instance, that reached a
// ParseX of this package whose needs row is inferred: checked once the
// rows are final. at is the decode; nil for an instance, reported at the
// ParseX.
type wireNeed struct {
	at    syntax.Node
	file  *fileInfo
	parse *Func
}

// lateUses reports whether f's needs row is still being inferred while
// this package's bodies are checked.
func (c *checker) lateUses(f *Func) bool {
	return f.inferUses && f.owner == nil && f.Pkg == c.pkg
}

// wireFuncs are the codec functions t's wire form reaches: each pair's
// ParseX for decode, its Wire for encode.
func (c *checker) wireFuncs(t Type, decode bool) []*Func {
	var out []*Func
	c.wireEach(t, func(t Type) {
		if p := c.wirePair(t); p != nil {
			if decode {
				out = append(out, p.Parse)
			} else {
				out = append(out, p.Wire)
			}
		}
	})
	return out
}

// chargeWire charges at x the needs of the codec functions an encode
// (decode false) or a decode of t calls.
func (c *checker) chargeWire(x syntax.Node, t Type, decode bool) {
	for _, f := range c.wireFuncs(t, decode) {
		if c.lateUses(f) {
			var caller *Func
			if c.fn != nil {
				caller = c.fn.fn
			}
			switch {
			case caller == nil || caller.Decl == nil:
				if c.silent == 0 {
					c.wireNeeds = append(c.wireNeeds, wireNeed{x, c.cur, f})
				}
				continue
			case !slices.Contains(c.wireEdges[caller.Decl], f.Decl):
				if c.lateEdges == nil {
					c.lateEdges = map[*syntax.FuncDecl][]*syntax.FuncDecl{}
				}
				if !slices.Contains(c.lateEdges[caller.Decl], f.Decl) {
					c.lateEdges[caller.Decl] = append(c.lateEdges[caller.Decl], f.Decl)
				}
			}
		}
		what := "encode"
		if decode {
			what = "decode"
		}
		for _, cap := range c.types.resolve(c.funcSig(f).Uses).Elems() {
			c.chargeAs(x, cap, fmt.Sprintf("%s, which this %s calls,", f.Name, what))
		}
	}
}

// wireInstanceNeeds is why t, bound to a wire type parameter, is refused
// for the needs of its codec, or "".
func (c *checker) wireInstanceNeeds(t Type) string {
	for _, decode := range []bool{true, false} {
		for _, f := range c.wireFuncs(t, decode) {
			if c.lateUses(f) {
				if c.silent == 0 {
					c.wireNeeds = append(c.wireNeeds, wireNeed{nil, f.file, f})
				}
				continue
			}
			if row := c.types.resolve(c.funcSig(f).Uses); !row.Empty() {
				return fmt.Sprintf("%s reaches %s, which needs %s, and a wire type parameter carries no capability row; call encode or decode with %s itself, where the call is charged; types.md, Codecs 8", t, f.Name, rowText(row), t)
			}
		}
	}
	return ""
}

// needEdges are the lateEdges whose ParseX needs a capability once the
// rows are final: the edges a second check must order the bodies by.
func (c *checker) needEdges() map[*syntax.FuncDecl][]*syntax.FuncDecl {
	var out map[*syntax.FuncDecl][]*syntax.FuncDecl
	for caller, parses := range c.lateEdges {
		for _, d := range parses {
			pf, ok := c.pkg.Info.Defs[d.Name].(*Func)
			if !ok || c.types.resolve(c.funcSig(pf).Uses).Empty() {
				continue
			}
			if out == nil {
				out = map[*syntax.FuncDecl][]*syntax.FuncDecl{}
			}
			out[caller] = append(out[caller], d)
		}
	}
	return out
}

// checkWireNeeds reports each decode that reached a ParseX whose inferred
// needs row is not empty.
func (c *checker) checkWireNeeds() {
	done := map[syntax.Node]bool{}
	for _, w := range c.wireNeeds {
		row := c.types.resolve(c.funcSig(w.parse).Uses)
		if row.Empty() {
			continue
		}
		c.cur = w.file
		if w.at == nil {
			if !done[w.parse.Decl.Name] {
				done[w.parse.Decl.Name] = true
				c.errorf(w.parse.Decl.Name, CodeType, "%s needs %s and is the codec of a type bound to a wire type parameter, which carries no capability row; call encode or decode with the type itself, where the call is charged (types.md, Codecs 8)", w.parse.Name, rowText(row))
			}
			continue
		}
		if !done[w.at] {
			done[w.at] = true
			c.errorf(w.at, CodeType, "this decode reaches %s, whose inferred needs row is %s, and its body has no function whose row could take it; write the row of %s (types.md, Codecs 8)", w.parse.Name, rowText(row), w.parse.Name)
		}
	}
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
// by-name ParseX it reaches. A wire type parameter T it reaches adds
// wired[T], which an instance replaces by its argument's wire row.
func (c *checker) wireRow(t Type) *Row {
	row := newRow().with(c.types, preludeType(WireKindName))
	add := func(name string) { row = row.with(c.types, preludeType(name)) }
	c.wireEach(t, func(t Type) {
		if tp, ok := t.(*TypeParam); ok {
			row = row.with(c.types, &Wired{T: tp})
			return
		}
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
				add(WireMissingName)
			}
		case *Union:
			add(WireMissingName)
			add(WireTagName)
		}
	})
	return row
}
