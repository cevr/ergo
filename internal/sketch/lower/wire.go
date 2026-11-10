package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// Derived codecs (types.md, Codecs; L11 design item 8). encode(v, w) and
// decode[T](r) lower to calls of one generated Go function per type and
// direction, which walks the type's structure as the checker's wireReason
// admitted it: no reflection, no any, no interface boxing beyond the
// Writer and Reader values themselves. A basic value is one event, inline;
// every other type has its function, named in the naming pass
// (reserveWires) and emitted once, at first use, as equality functions are
// (equal.go). A recursive type's function calls itself.
//
//   - a record: BeginObject(n), then Key and the field for each field, in
//     declaration order, then EndObject; decode reads the keys in any
//     order, skips unknown ones, and fails WireMissingField for a field
//     the object lacks;
//   - a union: an object that starts with Tag(variant), then the
//     variant's fields; decode reads the tag first and fails
//     WireUnknownTag for a name no variant has;
//   - a slice and a tuple: BeginList(n), the elements, EndList;
//   - a Map: an object, its keys in order (v1.WireKeys);
//   - a refined type: its base; decode then calls its ParseN;
//   - a by-name pair: Wire(x)'s value; decode then calls ParseX.
//
// Prior art: serde's derive and ReScript's @spice emit one plain function
// per type over a format interface; Go's encoding/json walks by reflection.
// The Writer is an interface here, the reference form; a format-specialised
// form (std json writing bytes directly) is the measured fast path that
// waits for S-json.

// Generic codecs (owner q80). A type parameter under wire takes its
// codec as a witness (witness.go): a struct of the encode and decode
// functions of its argument, of wireCodecType's shape. A codec function of
// a type that holds type parameters is a Go generic function that takes
// one such witness per parameter after the environment, so Go infers the
// type arguments from the witnesses, and a type parameter's events are a
// call of its witness. Prior art: Haskell's and Rust's trait dictionaries,
// Go's own GC-shape dictionaries; Roc's Encoding ability passes the same.

// wireCodecType is the Go type of the codec witness of self: two function
// fields of the uniform shape (funcType), Enc of (Writer, self) and Dec of
// (Reader) with a self result.
func (tc *typeCtx) wireCodecType(self check.Type) string {
	return "struct{ " + tc.wireCodecFuncs(self) + "; Field *" + tc.wireFieldType(self) + " }"
}

// wireCodecFuncs are the Enc, Dec and JSON fields of a codec witness.
func (tc *typeCtx) wireCodecFuncs(self check.Type) string {
	enc, dec := wireCodecSigs(self)
	return "Enc " + tc.funcType(enc) + "; Dec " + tc.funcType(dec) + "; JSON " + tc.wireJSONType(self)
}

// wireFieldType is the Go type of a witness's Field: the record-field form
// of a json.Optional argument (wirefields.go), nil for every other type.
// Its Enc, Dec and JSON write and read Present's bare payload, Absent
// reports the empty state and Empty is the value of an absent key.
func (tc *typeCtx) wireFieldType(self check.Type) string {
	gt := tc.goType(self)
	return "struct{ " + tc.wireCodecFuncs(self) + "; Absent func(" + gt + ") bool; Empty " + gt + " }"
}

// wireJSONType is the Go type of a codec witness's JSON field: the fast
// path's function of self (wirejson.go), nil when the build takes the
// reference path.
func (tc *typeCtx) wireJSONType(self check.Type) string {
	return "func(*" + tc.l.pkgName(pkgKey{"env", nil}, "Env") + ", *" + tc.l.rt() + ".Text, " + tc.goType(self) + ")"
}

// wireCodecSigs are the signatures of a codec witness's Enc and Dec.
func wireCodecSigs(self check.Type) (enc, dec *check.Signature) {
	w := check.PreludeType(check.WireWriterName)
	r := check.PreludeType(check.WireReaderName)
	enc = &check.Signature{Params: []*check.Var{{Name: "w", Type: w}, {Name: "v", Type: self}}}
	dec = &check.Signature{Params: []*check.Var{{Name: "r", Type: r}}, Results: []check.Type{self}}
	return enc, dec
}

// instArg is the type argument inst binds to f's type parameter tp.
func instArg(f *check.Func, inst *check.Instance, tp *check.TypeParam, site syntax.Node) check.Type {
	for i, p := range f.Sig.TParams {
		if p == tp && i < len(inst.TypeArgs) {
			return inst.TypeArgs[i]
		}
	}
	ice(site, "the instance of %s has no argument for %s", f.Name, tp.Name)
	return nil
}

// wireWitness is the codec witness of t at a call: the caller's own
// witness when t is the caller's type parameter, else a literal whose
// functions write and read t as encode and decode do.
func (fc *funcCtx) wireWitness(t check.Type, site syntax.Node) string {
	tc := fc.wireCtx(site)
	if tp, ok := t.(*check.TypeParam); ok {
		return tc.wireOf(tp)
	}
	gt := tc.goType(t)
	encSig, decSig := wireCodecSigs(t)
	enc := fc.envClosure(callerEnv, "e", site, func(e string) ([]string, string, string) {
		w, v := fc.scope.alloc("w"), fc.scope.alloc("v")
		params := []string{w + " " + tc.goType(encSig.Params[0].Type), v + " " + gt}
		return params, tc.valueResults(nil), tc.wireEncode(t, e, w, v) + "\nreturn struct{}{}, nil\n"
	})
	dec := fc.envClosure(callerEnv, "e", site, func(e string) ([]string, string, string) {
		r, x := fc.scope.alloc("r"), fc.scope.alloc("x")
		params := []string{r + " " + tc.goType(decSig.Params[0].Type)}
		return params, tc.valueResults(t), tc.wireDecode(t, e, r, x, "*new("+gt+")", fc.scope) + "return " + x + ", nil\n"
	})
	fast := "nil"
	if wireFast() {
		e, b, v := fc.scope.alloc("e"), fc.scope.alloc("b"), fc.scope.alloc("v")
		fast = "func(" + e + " *" + fc.l.pkgName(pkgKey{"env", nil}, "Env") + ", " + b + " *" + fc.l.rt() + ".Text, " + v + " " + gt + ") {\n" + tc.wireEncodeWith(jsonEvents{}, t, e, b, v) + "\n}"
	}
	return tc.wireCodecType(t) + "{Enc: " + enc + ", Dec: " + dec + ", JSON: " + fast + ", Field: " + fc.wireFieldWitness(tc, t, site) + "}"
}

// wireCtx is fc's type context with its codec witnesses, for a codec call.
func (fc *funcCtx) wireCtx(site syntax.Node) *typeCtx {
	tc := fc.typeCtx
	tc.wires = fc.witness
	tc.site = site
	return &tc
}

// wireOf is the codec witness in scope for tp.
func (tc *typeCtx) wireOf(tp *check.TypeParam) string {
	w, ok := tc.wires[tp]
	if !ok {
		ice(tc.site, "the wire type parameter %s has no codec witness here", tp.Name)
	}
	return w
}

// wireWits are the witnesses a call of t's codec function passes, one for
// each type parameter t holds, in freeTypeParams order.
func (tc *typeCtx) wireWits(t check.Type) string {
	s := ""
	for _, tp := range freeTypeParams(t) {
		s += ", " + tc.wireOf(tp)
	}
	return s
}

// wireKey is the package-scope key of t's codec function in direction dir
// ("encode" or "decode").
func (l *lowerer) wireKey(dir string, t check.Type) pkgKey {
	return pkgKey{"wire " + dir, l.typeKey(t)}
}

// wireBase is the preferred Go name of t's codec function.
func wireBase(dir string, t check.Type) string {
	head := "ergoEncode"
	switch dir {
	case "decode":
		head = "ergoDecode"
	case "json":
		head = "ergoJSON"
	}
	switch u := t.(type) {
	case *check.Named:
		return head + u.Obj.Name
	case *check.Slice:
		return head + "Slice"
	case *check.Map:
		return head + "Map"
	case *check.Tuple:
		return head + "Tuple"
	}
	return head
}

// wireInline reports a type whose events the call site writes inline: a
// basic type, or a defined type over one that is neither refined (decode
// parses it) nor a by-name pair.
func (l *lowerer) wireInline(t check.Type) bool {
	if l.info.WirePairOf(t) != nil {
		return false
	}
	if _, ok := t.(*check.Basic); ok {
		return true
	}
	if check.RefinementOf(t) != nil {
		return false
	}
	_, ok := wireUnder(t).(*check.Basic)
	return ok
}

// wireUnder is t's structure: a defined type's or instance's underlying
// type, t itself otherwise.
func wireUnder(t check.Type) check.Type {
	if n, ok := t.(*check.Named); ok {
		return n.Underlying()
	}
	return t
}

// reserveWires names, in the naming pass, every codec function the
// program's encode and decode calls can reach.
func (l *lowerer) reserveWires(pkgs []*check.Package, reserve func(key pkgKey, base string, sort string)) {
	walked := map[string]bool{}
	var walk func(dir string, info *check.Info, t check.Type)
	walk = func(dir string, info *check.Info, t check.Type) {
		if _, ok := t.(*check.TypeParam); ok {
			return // its witness writes and reads it
		}
		key := l.wireKey(dir, t)
		if walked[key.kind+" "+key.obj.(string)] {
			return
		}
		walked[key.kind+" "+key.obj.(string)] = true
		if p := info.WirePairOf(t); p != nil {
			reserve(key, wireBase(dir, t), key.kind+" "+key.obj.(string))
			walk(dir, info, p.Base)
			return
		}
		if _, ok := t.(*check.Basic); ok {
			return
		}
		reserve(key, wireBase(dir, t), key.kind+" "+key.obj.(string))
		switch u := wireUnder(t).(type) {
		case *check.Slice:
			walk(dir, info, u.Elem)
		case *check.Map:
			walk(dir, info, u.Elem)
		case *check.Tuple:
			for _, e := range u.Types {
				walk(dir, info, e)
			}
		case *check.Struct:
			for _, f := range u.Fields {
				walk(dir, info, f.Type)
			}
		case *check.Union:
			for _, v := range u.Variants {
				walk(dir, info, v)
			}
		}
	}
	for _, p := range pkgs {
		if p.Info == nil {
			continue
		}
		for x, c := range p.Info.Calls {
			if !l.readsFact(p, x) {
				continue
			}
			switch c.Kind {
			case check.CallEncode:
				walk("encode", p.Info, c.Type)
				if wireFast() {
					walk("json", p.Info, c.Type)
				}
			case check.CallDecode:
				walk("decode", p.Info, c.Type)
			}
		}
		// A generic function's wire argument: its witness calls the
		// argument's codec functions (wireWitness).
		for x, inst := range p.Info.Instances {
			if inst == nil || inst.Func == nil || !l.readsFact(p, x) {
				continue
			}
			for i, tp := range inst.Func.Sig.TParams {
				if isWireParam(tp) && i < len(inst.TypeArgs) {
					walk("encode", p.Info, inst.TypeArgs[i])
					walk("decode", p.Info, inst.TypeArgs[i])
					if wireFast() {
						walk("json", p.Info, inst.TypeArgs[i])
					}
				}
			}
		}
	}
}

// wireCall lowers encode(v, w) and decode[T](r).
func (fc *funcCtx) wireCall(x *syntax.CallExpr, c *check.Call) operand {
	if fc.frame.polls {
		fc.poll(x)
	}
	vals := fc.callArgs(x.Args, x)
	env := fc.envExpr(x)
	tc := fc.wireCtx(x)
	if c.Kind == check.CallEncode {
		if wireFast() && isJSONText(fc.typeOf(x.Args[1])) {
			// The fast path (wirejson.go): the value's bytes, after the
			// comma the Text's state asks for.
			t := fc.temp("text")
			fc.line(t + " := " + vals[1])
			fc.line(t + ".JSONValue()")
			fc.line(tc.wireEncodeWith(jsonEvents{}, c.Type, env, t, vals[0]))
			fc.line(t + ".Comma = true")
			return operand{}
		}
		fc.line(tc.wireEncode(c.Type, env, vals[1], vals[0]))
		return operand{}
	}
	call := ""
	if tp, ok := c.Type.(*check.TypeParam); ok {
		call = tc.wireOf(tp) + ".Dec(" + env + ", " + vals[0] + ")"
	} else {
		call = fc.l.wireFunc("decode", c.Type, x) + "(" + env + tc.wireWits(c.Type) + ", " + vals[0] + ")"
	}
	v, e := fc.temp("v"), fc.temp("err")
	fc.line(v + ", " + e + " := " + call)
	if !failsHere(c) {
		fc.line("_ = " + e)
		return operand{val: v}
	}
	return operand{val: v, err: e}
}

// wireEncode is the Go statement that writes the events of v, of type t,
// on w.
func (tc *typeCtx) wireEncode(t check.Type, env, w, v string) string {
	return tc.wireEncodeWith(writerEvents{}, t, env, w, v)
}

// wireEncodeWith is the Go statement that writes v, of type t, on w by
// the path em (wirejson.go).
func (tc *typeCtx) wireEncodeWith(em wireEvents, t check.Type, env, w, v string) string {
	l := tc.l
	if l.wireInline(t) {
		return em.basic(tc, wireUnder(t).(*check.Basic), taskOf(env), w, v)
	}
	if tp, ok := t.(*check.TypeParam); ok {
		return em.param(tc, tp, env, w, v)
	}
	return l.wireFunc(em.dir(), t, tc.site) + "(" + env + tc.wireWits(t) + ", " + w + ", " + v + ")"
}

// wireDecode is the Go statements that read a value of type t from r into
// a new variable named into, returning zero and the failure from the
// enclosing function on a failure.
func (tc *typeCtx) wireDecode(t check.Type, env, r, into, zero string, scope *namer) string {
	l := tc.l
	task := taskOf(env)
	if l.wireInline(t) {
		b := wireUnder(t).(*check.Basic)
		gt := tc.goType(t)
		raw, err := scope.alloc("raw"), scope.alloc("err")
		read := "Int"
		switch {
		case b.Kind == check.Bool:
			read = "Bool"
		case b.Kind == check.String:
			read = "String"
		case b.Kind == check.Float32 || b.Kind == check.Float64:
			read = "Float"
		case b.Kind >= check.Uint && b.Kind <= check.Uintptr:
			read = "Uint"
		}
		var s strings.Builder
		s.WriteString(raw + ", " + err + " := " + r + "." + read + "(" + task + ")\n")
		s.WriteString("if " + err + " != nil {\nreturn " + zero + ", " + err + "\n}\n")
		if lo, hi, ok := intRange(b.Kind); ok {
			s.WriteString("if " + raw + " < " + lo + " || " + raw + " > " + hi + " {\nreturn " + zero + ", " + tc.wireKind(env, r, b.Name) + "\n}\n")
		} else if hi, ok := uintMax(b.Kind); ok {
			s.WriteString("if " + raw + " > " + hi + " {\nreturn " + zero + ", " + tc.wireKind(env, r, b.Name) + "\n}\n")
		}
		s.WriteString(into + " := " + gt + "(" + raw + ")\n")
		return s.String()
	}
	err := scope.alloc("err")
	call := ""
	if tp, ok := t.(*check.TypeParam); ok {
		call = tc.wireOf(tp) + ".Dec(" + env + ", " + r + ")"
	} else {
		call = l.wireFunc("decode", t, tc.site) + "(" + env + tc.wireWits(t) + ", " + r + ")"
	}
	return into + ", " + err + " := " + call + "\nif " + err + " != nil {\nreturn " + zero + ", " + err + "\n}\n"
}

// intRange is the bounds of a signed integer kind narrower than 64 bits.
func intRange(k check.BasicKind) (string, string, bool) {
	switch k {
	case check.Int8:
		return "-128", "127", true
	case check.Int16:
		return "-32768", "32767", true
	case check.Int32:
		return "-2147483648", "2147483647", true
	}
	return "", "", false
}

// uintMax is the maximum of an unsigned kind narrower than 64 bits.
func uintMax(k check.BasicKind) (string, bool) {
	switch k {
	case check.Uint8:
		return "255", true
	case check.Uint16:
		return "65535", true
	case check.Uint32:
		return "4294967295", true
	}
	return "", false
}

// wireFailure is the Go value of the prelude failure name with the given
// field values, in declaration order.
func (tc *typeCtx) wireFailure(name string, vals ...string) string {
	n := check.PreludeType(name)
	if n == nil {
		ice(tc.site, "the prelude has no %s", name)
	}
	td := tc.l.useType(tc.l.typeDecl(n.Obj, tc.site))
	s, ok := n.Underlying().(*check.Struct)
	if !ok || len(s.Fields) != len(vals) {
		ice(tc.site, "the prelude's %s is not a record of %d fields", name, len(vals))
	}
	var parts []string
	for i, f := range s.Fields {
		parts = append(parts, td.fieldName[f.Name]+": "+vals[i])
	}
	return tc.goType(n) + "{" + strings.Join(parts, ", ") + "}"
}

// wireKind is the WireWrongKind of r's place, wanting want.
func (tc *typeCtx) wireKind(env, r, want string) string {
	return tc.wireFailure(check.WireKindName, r+".Path("+taskOf(env)+")", strconv.Quote(want))
}

// wireFunc is the codec function of t in direction dir, emitted once, at
// its first use; its name is taken before the body is written, so a
// recursive type's function calls itself.
func (l *lowerer) wireFunc(dir string, t check.Type, site syntax.Node) string {
	key := l.wireKey(dir, t)
	name := l.pkgName(key, wireBase(dir, t))
	if l.wires == nil {
		l.wires = map[pkgKey]bool{}
	}
	if l.wires[key] {
		return name
	}
	l.wires[key] = true
	if n, ok := t.(*check.Named); ok {
		if fu := l.flatUnionSet()[n.Obj]; fu != nil && n.Orig == nil {
			flatBail(site, fu)
		}
	}
	scope := newNamer(l.names)
	// A type that holds type parameters has a generic function, which
	// takes each parameter's codec witness after the environment.
	free := freeTypeParams(t)
	tlist, tnames := l.typeParams(free, scope, site)
	tc := &typeCtx{l: l, params: tnames, site: site, wires: map[*check.TypeParam]string{}}
	goType := tc.goType(t)
	env := scope.alloc("env")
	envType := "*" + l.pkgName(pkgKey{"env", nil}, "Env")
	lead := env + " " + envType
	for _, tp := range free {
		tc.wires[tp] = scope.alloc("w" + tp.Name)
		lead += ", " + tc.wires[tp] + " " + tc.wireCodecType(tp)
	}
	task := taskOf(env)
	var body strings.Builder
	if dir == "encode" || dir == "json" {
		w, v := scope.alloc("w"), scope.alloc("v")
		var em wireEvents = writerEvents{}
		writer := tc.goType(check.PreludeType(check.WireWriterName))
		if dir == "json" {
			em, writer = jsonEvents{}, "*"+l.rt()+".Text"
		}
		l.wireEncodeBody(em, tc, &body, t, env, task, w, v, scope)
		l.gen.WriteString("func " + name + tlist + "(" + lead + ", " + w + " " + writer + ", " + v + " " + goType + ") {\n" + body.String() + "}\n\n")
		return name
	}
	r := scope.alloc("r")
	reader := tc.goType(check.PreludeType(check.WireReaderName))
	zero := "*new(" + goType + ")"
	l.wireDecodeBody(tc, &body, t, env, task, r, zero, scope)
	l.gen.WriteString("func " + name + tlist + "(" + lead + ", " + r + " " + reader + ") (" + goType + ", error) {\n" + body.String() + "}\n\n")
	return name
}

// wireCallFunc is the Go call of the declared function f on args, with
// the environment unless f is pure, as a method when f lowers to one.
func (l *lowerer) wireCallFunc(f *check.Func, env string, site syntax.Node, args ...string) string {
	fd := l.useFunc(f, site)
	lead := []string{}
	if !fd.pure {
		lead = []string{env}
	}
	if fd.method != nil {
		return "(" + args[0] + ")." + fd.name + "(" + strings.Join(append(lead, args[1:]...), ", ") + ")"
	}
	return fd.name + "(" + strings.Join(append(lead, args...), ", ") + ")"
}

// wireEncodeBody writes the statements of t's encode function on the
// path em: the Writer's events, or JSON appends (wirejson.go).
func (l *lowerer) wireEncodeBody(em wireEvents, tc *typeCtx, b *strings.Builder, t check.Type, env, task, w, v string, scope *namer) {
	if jsonWrapper(t, "Nullable") {
		l.wireNullableEncode(em, tc, b, t, env, task, w, v, scope)
		return
	}
	if jsonWrapper(t, "Value") {
		l.wireValueEncode(em, tc, b, t, env, task, w, v, scope)
		return
	}
	if jsonWrapper(t, "Num") {
		l.wireNumEncode(em, tc, b, t, task, w, v)
		return
	}
	if p := l.info.WirePairOf(t); p != nil {
		b.WriteString(tc.wireEncodeWith(em, p.Base, env, w, l.wireCallFunc(p.Wire, env, tc.site, v)) + "\n")
		return
	}
	if check.RefinementOf(t) != nil {
		if base, ok := wireUnder(t).(*check.Basic); ok {
			b.WriteString(tc.wireEncodeWith(em, base, env, w, v) + "\n")
			return
		}
	}
	fields := func(td *typeDecl, s *check.Struct, rec string, tagged bool) {
		l.wireFields(em, tc, b, td, s, rec, tagged, env, task, w, scope)
	}
	// index is a loop's index variable: "_" on a path that needs none.
	index := func() string {
		if em.indexed() {
			return scope.alloc("i")
		}
		return "_"
	}
	switch u := wireUnder(t).(type) {
	case *check.Slice:
		i, e := index(), scope.alloc("e")
		b.WriteString(em.open(true, "len("+v+")", task, w))
		b.WriteString("for " + i + ", " + e + " := range " + v + " {\n" + em.item(i, w) + tc.wireEncodeWith(em, u.Elem, env, w, e) + "\n}\n")
		b.WriteString(em.close(true, task, w))
	case *check.Map:
		i, k := index(), scope.alloc("k")
		b.WriteString(em.open(false, "len("+v+")", task, w))
		b.WriteString("for " + i + ", " + k + " := range " + l.pkgName(pkgKey{"import", runtimeImport}, "ergo") + ".WireKeys(" + v + ") {\n")
		b.WriteString(em.mapKey(tc, k, i, task, w) + tc.wireEncodeWith(em, u.Elem, env, w, v+"["+k+"]") + "\n}\n")
		b.WriteString(em.close(false, task, w))
	case *check.Tuple:
		b.WriteString(em.open(true, strconv.Itoa(len(u.Types)), task, w))
		for i, e := range u.Types {
			b.WriteString(em.itemAt(i, w) + tc.wireEncodeWith(em, e, env, w, v+"."+tupleField(i)) + "\n")
		}
		b.WriteString(em.close(true, task, w))
	case *check.Struct:
		n, ok := t.(*check.Named)
		if !ok {
			ice(tc.site, "the record type %s has no name", t)
		}
		td := l.typeDecl(n.Obj, tc.site)
		b.WriteString(em.open(false, l.wireFieldCount(em, tc, b, td, u, v, scope), task, w))
		fields(td, u, v, false)
		b.WriteString(em.close(false, task, w))
	case *check.Union:
		x := scope.alloc("x")
		b.WriteString("switch " + x + " := " + v + ".(type) {\n")
		for _, vt := range u.Variants {
			vn, ok := vt.(*check.Named)
			if !ok {
				ice(tc.site, "the variant %s of %s has no name", vt, t)
			}
			vs, _ := vn.Underlying().(*check.Struct)
			if vs == nil {
				ice(tc.site, "the variant %s of %s is not a record", vt, t)
			}
			td := l.typeDecl(vn.Obj, tc.site)
			b.WriteString("case " + tc.goType(vt) + ":\n")
			if len(vs.Fields) == 0 {
				b.WriteString("_ = " + x + "\n")
			}
			b.WriteString(em.open(false, l.wireFieldCount(em, tc, b, td, vs, x, scope), task, w))
			b.WriteString(em.tag(vn.Obj.Name, task, w))
			fields(td, vs, x, true)
			b.WriteString(em.close(false, task, w))
		}
		b.WriteString("}\n")
	default:
		ice(tc.site, "the type %s reached encode with no wire form", t)
	}
}

// wireDecodeBody writes the statements of t's decode function.
func (l *lowerer) wireDecodeBody(tc *typeCtx, b *strings.Builder, t check.Type, env, task, r, zero string, scope *namer) {
	if jsonWrapper(t, "Nullable") {
		l.wireNullableDecode(tc, b, t, env, task, r, zero, scope)
		return
	}
	if jsonWrapper(t, "Value") {
		l.wireValueDecode(tc, b, t, env, task, r, zero, scope)
		return
	}
	if jsonWrapper(t, "Num") {
		l.wireNumDecode(tc, b, t, task, r, zero, scope)
		return
	}
	parsed := func(base check.Type, parse *check.Func) {
		raw := scope.alloc("raw")
		b.WriteString(tc.wireDecode(base, env, r, raw, zero, scope))
		b.WriteString("return " + l.wireCallFunc(parse, env, tc.site, raw) + "\n")
	}
	if p := l.info.WirePairOf(t); p != nil {
		parsed(p.Base, p.Parse)
		return
	}
	if parse := check.RefinedParse(t); parse != nil {
		base := parse.Sig.Params[0].Type
		parsed(base, parse)
		return
	}
	goType := tc.goType(t)
	// record reads the fields of the record s into out, a variable of
	// Go type gt already declared, after BeginObject (and Tag).
	record := func(td *typeDecl, s *check.Struct, out string) {
		var seen []string
		for range s.Fields {
			seen = append(seen, scope.alloc("seen"))
		}
		if len(seen) > 0 {
			b.WriteString("var " + strings.Join(seen, ", ") + " bool\n")
		}
		b.WriteString("for " + r + ".More(" + task + ") {\nswitch " + r + ".Key(" + task + ") {\n")
		for i, f := range s.Fields {
			fv := scope.alloc("f")
			b.WriteString("case " + strconv.Quote(check.WireFieldName(f)) + ":\n")
			ft := f.Type
			if wit, ftc, ok := tc.paramField(ft); ok {
				x, y := scope.alloc("f"), scope.alloc("f")
				b.WriteString("var " + fv + " " + tc.goType(ft) + "\n")
				b.WriteString("if " + wit + ".Field != nil {\n" + ftc.wireDecode(ft, env, r, x, zero, scope) + fv + " = " + x + "\n} else {\n")
				b.WriteString(tc.wireDecode(ft, env, r, y, zero, scope) + fv + " = " + y + "\n}\n")
				b.WriteString(out + "." + td.fieldName[f.Name] + " = " + fv + "\n" + seen[i] + " = true\n")
				continue
			}
			optional := jsonWrapper(ft, "Optional")
			var present check.Type
			var field *check.Field
			if optional {
				_, present, field = wrapperParts(ft)
				ft = field.Type
			}
			b.WriteString(tc.wireDecode(ft, env, r, fv, zero, scope))
			if optional {
				fv = tc.wrapperValue(present, field, fv)
			}
			b.WriteString(out + "." + td.fieldName[f.Name] + " = " + fv + "\n" + seen[i] + " = true\n")
		}
		b.WriteString("default:\n" + r + ".Skip(" + task + ")\n}\n}\n" + r + ".End(" + task + ")\n")
		for i, f := range s.Fields {
			missing := "return " + zero + ", " + tc.wireFailure(check.WireMissingName, r+".Path("+task+")", strconv.Quote(check.WireFieldName(f))) + "\n"
			if wit, _, ok := tc.paramField(f.Type); ok {
				b.WriteString("if !" + seen[i] + " {\nif " + wit + ".Field == nil {\n" + missing + "}\n" + out + "." + td.fieldName[f.Name] + " = " + wit + ".Field.Empty\n}\n")
				continue
			}
			if jsonWrapper(f.Type, "Optional") {
				empty, _, _ := wrapperParts(f.Type)
				b.WriteString("if !" + seen[i] + " {\n" + out + "." + td.fieldName[f.Name] + " = " + tc.goType(empty) + "{}\n}\n")
				continue
			}
			b.WriteString("if !" + seen[i] + " {\n" + missing + "}\n")
		}
	}
	begin := func(what string) {
		err := scope.alloc("err")
		b.WriteString("if " + err + " := " + r + ".Begin" + what + "(" + task + "); " + err + " != nil {\nreturn " + zero + ", " + err + "\n}\n")
	}
	switch u := wireUnder(t).(type) {
	case *check.Slice:
		out := scope.alloc("out")
		begin("List")
		b.WriteString(out + " := " + goType + "{}\nfor " + r + ".More(" + task + ") {\n")
		e := scope.alloc("e")
		b.WriteString(tc.wireDecode(u.Elem, env, r, e, zero, scope))
		b.WriteString(out + " = append(" + out + ", " + e + ")\n}\n" + r + ".End(" + task + ")\nreturn " + out + ", nil\n")
	case *check.Map:
		out, k := scope.alloc("out"), scope.alloc("k")
		begin("Object")
		b.WriteString(out + " := " + goType + "{}\nfor " + r + ".More(" + task + ") {\n" + k + " := " + r + ".Key(" + task + ")\n")
		e := scope.alloc("e")
		b.WriteString(tc.wireDecode(u.Elem, env, r, e, zero, scope))
		b.WriteString(out + "[" + tc.goType(u.Key) + "(" + k + ")] = " + e + "\n}\n" + r + ".End(" + task + ")\nreturn " + out + ", nil\n")
	case *check.Tuple:
		begin("List")
		var parts []string
		for i, e := range u.Types {
			ev := scope.alloc("e")
			b.WriteString("if !" + r + ".More(" + task + ") {\nreturn " + zero + ", " + tc.wireKind(env, r, "a list of "+strconv.Itoa(len(u.Types))) + "\n}\n")
			b.WriteString(tc.wireDecode(e, env, r, ev, zero, scope))
			parts = append(parts, tupleField(i)+": "+ev)
		}
		b.WriteString("if " + r + ".More(" + task + ") {\nreturn " + zero + ", " + tc.wireKind(env, r, "a list of "+strconv.Itoa(len(u.Types))) + "\n}\n")
		b.WriteString(r + ".End(" + task + ")\nreturn " + goType + "{" + strings.Join(parts, ", ") + "}, nil\n")
	case *check.Struct:
		n, ok := t.(*check.Named)
		if !ok {
			ice(tc.site, "the record type %s has no name", t)
		}
		td := l.typeDecl(n.Obj, tc.site)
		out := scope.alloc("out")
		begin("Object")
		b.WriteString("var " + out + " " + goType + "\n")
		record(td, u, out)
		b.WriteString("return " + out + ", nil\n")
	case *check.Union:
		begin("Object")
		tag, err := scope.alloc("tag"), scope.alloc("err")
		b.WriteString(tag + ", " + err + " := " + r + ".Tag(" + task + ")\nif " + err + " != nil {\nreturn " + zero + ", " + err + "\n}\n")
		b.WriteString("switch " + tag + " {\n")
		for _, vt := range u.Variants {
			vn := vt.(*check.Named)
			vs := vn.Underlying().(*check.Struct)
			td := l.typeDecl(vn.Obj, tc.site)
			out := scope.alloc("out")
			b.WriteString("case " + strconv.Quote(vn.Obj.Name) + ":\nvar " + out + " " + tc.goType(vt) + "\n")
			record(td, vs, out)
			b.WriteString("return " + out + ", nil\n")
		}
		b.WriteString("}\nreturn " + zero + ", " + tc.wireFailure(check.WireTagName, r+".Path("+task+")", tag) + "\n")
	default:
		ice(tc.site, "the type %s reached decode with no wire form", t)
	}
}
