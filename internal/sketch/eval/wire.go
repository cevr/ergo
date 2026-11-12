package eval

import (
	"sort"
	"strconv"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// The predeclared encode(v, w) and decode[T](r) (types.md, Codecs; ORACLE
// O18), walked from the type at run time: the events of rule 2 on the
// Writer's operations, the declared forms of rule 8, a by-name pair or a
// refinement through its functions. The walk is the evaluator's own; it
// reads only the checker's facts (WireFormOf, WireFieldName, the pairs,
// RefinedParse, WireVariantKind).
//
//   - a record: BeginObject(n), Key and the field for each field in
//     declaration order (an omittable field's empty state has no key and
//     is not counted), EndObject; decode reads members in any order,
//     skips unknown keys, a later member of one key wins, then fails
//     WireMissingField for the first field (in declaration order) it did
//     not read;
//   - a union: its variant's record with Tag(name) first; decode reads the
//     tag after BeginObject and fails WireUnknownTag for another name;
//   - a slice: BeginList(n), the elements, EndList; a tuple too, whose
//     decode fails WireWrongKind "a list of N" on a short or long list;
//   - a Map: an object, its keys in ascending order;
//   - a narrow integer out of its range fails WireWrongKind with the
//     type's name.
type wireWalk struct {
	in *interp
	fr *frame
	at *syntax.CallExpr
}

// wireCall runs a CallEncode or CallDecode.
func (in *interp) wireCall(fr *frame, x *syntax.CallExpr, c *check.Call) (Value, *abrupt) {
	if ab := in.poll(fr, x); ab != nil {
		return nil, ab
	}
	args := make([]Value, 0, len(x.Args))
	for _, a := range x.Args {
		v, ab := in.expr(fr, a)
		if ab != nil {
			return nil, ab
		}
		args = append(args, v)
	}
	in.handParts(fr, x.Args...)
	w := wireWalk{in: in, fr: fr, at: x}
	if c.Kind == check.CallEncode {
		if ab := w.encode(fr.rt(c.Type), args[1], args[0]); ab != nil {
			return nil, ab
		}
		return unit{}, nil
	}
	return w.decode(fr.rt(c.Type), args[0])
}

// wirePair is the by-name pair of t, as some package's check recorded it
// (the package of the encode or decode, or of a generic codec's
// instance). A type has one pair (coherence), so the union of every
// package's record is exact.
func (in *interp) wirePair(t check.Type) *check.WirePair {
	n, ok := t.(*check.Named)
	if !ok {
		return nil
	}
	if in.pairs == nil {
		in.pairs = map[*check.TypeName]*check.WirePair{}
		seen := map[*check.Package]bool{}
		var visit func(p *check.Package)
		visit = func(p *check.Package) {
			if p == nil || seen[p] || p.Info == nil {
				return
			}
			seen[p] = true
			for k, v := range p.Info.WirePairs {
				in.pairs[k] = v
			}
			for _, dep := range imports(p) {
				visit(dep)
			}
		}
		visit(in.root)
	}
	return in.pairs[n.Obj]
}

// event calls the operation name of a Writer or Reader.
func (w wireWalk) event(on Value, name string, args ...Value) (Value, *abrupt) {
	return w.in.dispatch(w.fr, w.at, on, name, args)
}

func (w wireWalk) failure(name string, fields ...Value) *abrupt {
	n := check.PreludeType(name)
	return w.fr.abrupt(ctlFail, w.at, &record{t: n, fields: fields}, "")
}

// wrongKind is WireWrongKind at r's place.
func (w wireWalk) wrongKind(r Value, want string) *abrupt {
	path, ab := w.event(r, "Path")
	if ab != nil {
		return ab
	}
	return w.failure(check.WireKindName, path, want)
}

// variants are the variants of the union under r, read in its env.
func variants(r rtype) []rtype {
	u := r.under()
	var out []rtype
	for _, v := range u.t.(*check.Union).Variants {
		out = append(out, u.env.resolve(v))
	}
	return out
}

// fields are the fields of the record under r, each type read in its env.
func wireFields(r rtype) ([]*check.Field, []rtype) {
	u := r.under()
	s := u.t.(*check.Struct)
	var ts []rtype
	for _, f := range s.Fields {
		ts = append(ts, u.env.resolve(f.Type))
	}
	return s.Fields, ts
}

// newRecordOf is a record of the record type r.
func newRecordOf(r rtype, fields []Value) *record {
	n := r.t.(*check.Named)
	return &record{t: n, key: argsKey(n, r.env), fields: fields}
}

// wrapper is the empty and the one-payload variant of an omittable or
// nullable union r, and the payload's type.
type wrapper struct {
	empty, present rtype
	payload        rtype
}

func wrapperOf(r rtype) wrapper {
	var out wrapper
	for _, v := range variants(r) {
		_, ts := wireFields(v)
		if len(ts) == 0 {
			out.empty = v
		} else {
			out.present, out.payload = v, ts[0]
		}
	}
	return out
}

// isVariant reports whether v is a value of the variant t.
func ofVariant(v Value, t rtype) bool {
	rec, ok := v.(*record)
	return ok && rec.t.Obj == t.t.(*check.Named).Obj
}

func (w wireWalk) callFunc(f *check.Func, arg Value) (Value, *abrupt) {
	return w.in.callResolved(w.fr, w.at, f, nil, []Value{arg})
}

func (w wireWalk) encode(r rtype, wr Value, v Value) *abrupt {
	if ab := w.in.charge(w.fr, w.at, 1); ab != nil {
		return ab
	}
	r = r.env.resolve(r.t)
	switch check.WireFormOf(r.t) {
	case check.WireNullable:
		p := wrapperOf(r)
		if ofVariant(v, p.empty) {
			_, ab := w.event(wr, "Null")
			return ab
		}
		return w.encode(p.payload, wr, v.(*record).fields[0])
	case check.WireBare:
		for _, vt := range variants(r) {
			if !ofVariant(v, vt) {
				continue
			}
			if check.WireVariantKind(vt.t) == "WireNull" {
				_, ab := w.event(wr, "Null")
				return ab
			}
			if _, ts := wireFields(vt); check.WireFormOf(vt.t) == check.WireDefault && len(ts) == 1 {
				return w.encode(ts[0], wr, v.(*record).fields[0])
			}
			return w.encode(vt, wr, v)
		}
		ice("a bare union value of no variant")
	case check.WireNumberText:
		s, ab := w.callFunc(w.in.wirePair(r.t).Wire, v)
		if ab != nil {
			return ab
		}
		_, ab = w.event(wr, "Number", s)
		return ab
	case check.WireMembers:
		_, ts := wireFields(r)
		list := v.(*record).fields[0].(sliceV)
		entry := ts[0].under().env.resolve(ts[0].under().t.(*check.Slice).Elem)
		_, ets := wireFields(entry)
		if _, ab := w.event(wr, "BeginObject", int64(list.n)); ab != nil {
			return ab
		}
		for _, e := range list.elems() {
			m := e.(*record)
			if _, ab := w.event(wr, "Key", m.fields[0]); ab != nil {
				return ab
			}
			if ab := w.encode(ets[1], wr, m.fields[1]); ab != nil {
				return ab
			}
		}
		_, ab := w.event(wr, "EndObject")
		return ab
	}
	if p := w.in.wirePair(r.t); p != nil {
		b, ab := w.callFunc(p.Wire, v)
		if ab != nil {
			return ab
		}
		return w.encode(rtype{t: p.Base}, wr, b)
	}
	u := r.under()
	switch t := u.t.(type) {
	case *check.Basic:
		var name string
		var arg Value
		switch {
		case t.Kind == check.Bool:
			name, arg = "Bool", v
		case t.Kind == check.String:
			name, arg = "String", v
		case t.Kind == check.Float32 || t.Kind == check.Float64:
			name, arg = "Float", mustConvert(v, check.Float64)
		case t.Kind >= check.Uint && t.Kind <= check.Uintptr:
			name, arg = "Uint", mustConvert(v, check.Uint64)
		default:
			name, arg = "Int", mustConvert(v, check.Int64)
		}
		_, ab := w.event(wr, name, arg)
		return ab
	case *check.Slice:
		s := v.(sliceV)
		elem := u.env.resolve(t.Elem)
		if _, ab := w.event(wr, "BeginList", int64(s.n)); ab != nil {
			return ab
		}
		for _, e := range s.elems() {
			if ab := w.encode(elem, wr, e); ab != nil {
				return ab
			}
		}
		_, ab := w.event(wr, "EndList")
		return ab
	case *check.Tuple:
		tv := v.(tuple)
		if _, ab := w.event(wr, "BeginList", int64(len(t.Types))); ab != nil {
			return ab
		}
		for i, e := range t.Types {
			if ab := w.encode(u.env.resolve(e), wr, tv.elems[i]); ab != nil {
				return ab
			}
		}
		_, ab := w.event(wr, "EndList")
		return ab
	case *check.Map:
		m := v.(mapV)
		elem := u.env.resolve(t.Elem)
		keys := make([]Value, 0, len(m.b.m))
		for k := range m.b.m {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool { return keys[i].(string) < keys[j].(string) })
		if ab := w.in.charge(w.fr, w.at, int64(len(keys))); ab != nil {
			return ab
		}
		if _, ab := w.event(wr, "BeginObject", int64(len(keys))); ab != nil {
			return ab
		}
		for _, k := range keys {
			if _, ab := w.event(wr, "Key", k); ab != nil {
				return ab
			}
			if ab := w.encode(elem, wr, m.b.m[k]); ab != nil {
				return ab
			}
		}
		_, ab := w.event(wr, "EndObject")
		return ab
	case *check.Struct:
		return w.encodeRecord(r, wr, v.(*record), "")
	case *check.Union:
		for _, vt := range variants(r) {
			if ofVariant(v, vt) {
				return w.encodeRecord(vt, wr, v.(*record), vt.t.(*check.Named).Obj.Name)
			}
		}
		ice("a union value of no variant")
	}
	ice("encode of %s", r.t)
	return nil
}

// encodeRecord writes the record rec of type r: tag is the variant's name
// of a union value, "" for a record.
func (w wireWalk) encodeRecord(r rtype, wr Value, rec *record, tag string) *abrupt {
	fields, ts := wireFields(r)
	count := 0
	for i := range fields {
		if check.WireFormOf(ts[i].t) == check.WireOmittable && ofVariant(rec.fields[i], wrapperOf(ts[i]).empty) {
			continue
		}
		count++
	}
	if _, ab := w.event(wr, "BeginObject", int64(count)); ab != nil {
		return ab
	}
	if tag != "" {
		if _, ab := w.event(wr, "Tag", tag); ab != nil {
			return ab
		}
	}
	for i, f := range fields {
		ft, fv := ts[i], rec.fields[i]
		if check.WireFormOf(ft.t) == check.WireOmittable {
			p := wrapperOf(ft)
			if ofVariant(fv, p.empty) {
				continue
			}
			ft, fv = p.payload, fv.(*record).fields[0]
		}
		if _, ab := w.event(wr, "Key", check.WireFieldName(f)); ab != nil {
			return ab
		}
		if ab := w.encode(ft, wr, fv); ab != nil {
			return ab
		}
	}
	_, ab := w.event(wr, "EndObject")
	return ab
}

func mustConvert(v Value, k check.BasicKind) Value {
	out, ok := convertBasic(v, k)
	if !ok {
		ice("a wire value %T as %v", v, k)
	}
	return out
}

func (w wireWalk) more(rd Value) (bool, *abrupt) {
	v, ab := w.event(rd, "More")
	if ab != nil {
		return false, ab
	}
	return v.(bool), nil
}

func (w wireWalk) decode(r rtype, rd Value) (Value, *abrupt) {
	if ab := w.in.charge(w.fr, w.at, 1); ab != nil {
		return nil, ab
	}
	r = r.env.resolve(r.t)
	switch check.WireFormOf(r.t) {
	case check.WireNullable:
		p := wrapperOf(r)
		null, ab := w.event(rd, "TakeNull")
		if ab != nil {
			return nil, ab
		}
		if null.(bool) {
			return newRecordOf(p.empty, nil), nil
		}
		x, ab := w.decode(p.payload, rd)
		if ab != nil {
			return nil, ab
		}
		return newRecordOf(p.present, []Value{x}), nil
	case check.WireBare:
		kind, ab := w.event(rd, "Peek")
		if ab != nil {
			return nil, ab
		}
		next := kind.(*record).t.Obj.Name
		for _, vt := range variants(r) {
			if check.WireVariantKind(vt.t) != next {
				continue
			}
			if next == "WireNull" {
				if _, ab := w.event(rd, "TakeNull"); ab != nil {
					return nil, ab
				}
				return newRecordOf(vt, nil), nil
			}
			if _, ts := wireFields(vt); check.WireFormOf(vt.t) == check.WireDefault && len(ts) == 1 {
				x, ab := w.decode(ts[0], rd)
				if ab != nil {
					return nil, ab
				}
				return newRecordOf(vt, []Value{x}), nil
			}
			return w.decode(vt, rd)
		}
		return nil, w.wrongKind(rd, "a value of the bare union")
	case check.WireNumberText:
		s, ab := w.event(rd, "Number")
		if ab != nil {
			return nil, ab
		}
		return w.callFunc(w.in.wirePair(r.t).Parse, s)
	case check.WireMembers:
		_, ts := wireFields(r)
		su := ts[0].under()
		entry := su.env.resolve(su.t.(*check.Slice).Elem)
		_, ets := wireFields(entry)
		if _, ab := w.event(rd, "BeginObject"); ab != nil {
			return nil, ab
		}
		var out []Value
		for {
			more, ab := w.more(rd)
			if ab != nil {
				return nil, ab
			}
			if !more {
				break
			}
			key, ab := w.event(rd, "Key")
			if ab != nil {
				return nil, ab
			}
			x, ab := w.decode(ets[1], rd)
			if ab != nil {
				return nil, ab
			}
			out = append(out, newRecordOf(entry, []Value{key, x}))
		}
		if _, ab := w.event(rd, "End"); ab != nil {
			return nil, ab
		}
		return newRecordOf(r, []Value{newSlice(out)}), nil
	}
	if p := w.in.wirePair(r.t); p != nil {
		raw, ab := w.decode(rtype{t: p.Base}, rd)
		if ab != nil {
			return nil, ab
		}
		return w.callFunc(p.Parse, raw)
	}
	if parse := check.RefinedParse(r.t); parse != nil {
		raw, ab := w.decode(rtype{t: parse.Sig.Params[0].Type}, rd)
		if ab != nil {
			return nil, ab
		}
		return w.callFunc(parse, raw)
	}
	u := r.under()
	switch t := u.t.(type) {
	case *check.Basic:
		return w.decodeBasic(t, rd)
	case *check.Slice:
		if _, ab := w.event(rd, "BeginList"); ab != nil {
			return nil, ab
		}
		elem := u.env.resolve(t.Elem)
		var out []Value
		for {
			more, ab := w.more(rd)
			if ab != nil {
				return nil, ab
			}
			if !more {
				break
			}
			x, ab := w.decode(elem, rd)
			if ab != nil {
				return nil, ab
			}
			out = append(out, x)
		}
		if _, ab := w.event(rd, "End"); ab != nil {
			return nil, ab
		}
		return newSlice(out), nil
	case *check.Map:
		if _, ab := w.event(rd, "BeginObject"); ab != nil {
			return nil, ab
		}
		elem := u.env.resolve(t.Elem)
		out := newMap()
		for {
			more, ab := w.more(rd)
			if ab != nil {
				return nil, ab
			}
			if !more {
				break
			}
			key, ab := w.event(rd, "Key")
			if ab != nil {
				return nil, ab
			}
			x, ab := w.decode(elem, rd)
			if ab != nil {
				return nil, ab
			}
			out.b.m[key] = x
		}
		if _, ab := w.event(rd, "End"); ab != nil {
			return nil, ab
		}
		return out, nil
	case *check.Tuple:
		if _, ab := w.event(rd, "BeginList"); ab != nil {
			return nil, ab
		}
		want := "a list of " + strconv.Itoa(len(t.Types))
		out := tuple{elems: make([]Value, len(t.Types))}
		for i, e := range t.Types {
			more, ab := w.more(rd)
			if ab != nil {
				return nil, ab
			}
			if !more {
				return nil, w.wrongKind(rd, want)
			}
			if out.elems[i], ab = w.decode(u.env.resolve(e), rd); ab != nil {
				return nil, ab
			}
		}
		more, ab := w.more(rd)
		if ab != nil {
			return nil, ab
		}
		if more {
			return nil, w.wrongKind(rd, want)
		}
		if _, ab := w.event(rd, "End"); ab != nil {
			return nil, ab
		}
		return out, nil
	case *check.Struct:
		if _, ab := w.event(rd, "BeginObject"); ab != nil {
			return nil, ab
		}
		return w.decodeRecord(r, rd)
	case *check.Union:
		if _, ab := w.event(rd, "BeginObject"); ab != nil {
			return nil, ab
		}
		tag, ab := w.event(rd, "Tag")
		if ab != nil {
			return nil, ab
		}
		for _, vt := range variants(r) {
			if vt.t.(*check.Named).Obj.Name == tag.(string) {
				return w.decodeRecord(vt, rd)
			}
		}
		path, ab := w.event(rd, "Path")
		if ab != nil {
			return nil, ab
		}
		return nil, w.failure(check.WireTagName, path, tag)
	}
	ice("decode of %s", r.t)
	return nil, nil
}

// decodeRecord reads the members of the object just begun into a record
// of type r.
func (w wireWalk) decodeRecord(r rtype, rd Value) (Value, *abrupt) {
	fields, ts := wireFields(r)
	vals := make([]Value, len(fields))
	seen := make([]bool, len(fields))
	for {
		more, ab := w.more(rd)
		if ab != nil {
			return nil, ab
		}
		if !more {
			break
		}
		key, ab := w.event(rd, "Key")
		if ab != nil {
			return nil, ab
		}
		i := -1
		for k, f := range fields {
			if check.WireFieldName(f) == key.(string) {
				i = k
				break
			}
		}
		if i < 0 {
			if _, ab := w.event(rd, "Skip"); ab != nil {
				return nil, ab
			}
			continue
		}
		if check.WireFormOf(ts[i].t) == check.WireOmittable {
			p := wrapperOf(ts[i])
			x, ab := w.decode(p.payload, rd)
			if ab != nil {
				return nil, ab
			}
			vals[i] = newRecordOf(p.present, []Value{x})
		} else {
			x, ab := w.decode(ts[i], rd)
			if ab != nil {
				return nil, ab
			}
			vals[i] = x
		}
		seen[i] = true
	}
	if _, ab := w.event(rd, "End"); ab != nil {
		return nil, ab
	}
	for i, f := range fields {
		if seen[i] {
			continue
		}
		if check.WireFormOf(ts[i].t) == check.WireOmittable {
			vals[i] = newRecordOf(wrapperOf(ts[i]).empty, nil)
			continue
		}
		path, ab := w.event(rd, "Path")
		if ab != nil {
			return nil, ab
		}
		return nil, w.failure(check.WireMissingName, path, check.WireFieldName(f))
	}
	return newRecordOf(r, vals), nil
}

// decodeBasic reads one basic value of t, a narrow integer within its
// type's range.
func (w wireWalk) decodeBasic(t *check.Basic, rd Value) (Value, *abrupt) {
	switch {
	case t.Kind == check.Bool:
		return w.event(rd, "Bool")
	case t.Kind == check.String:
		return w.event(rd, "String")
	case t.Kind == check.Float32 || t.Kind == check.Float64:
		f, ab := w.event(rd, "Float")
		if ab != nil {
			return nil, ab
		}
		return mustConvert(f, t.Kind), nil
	case t.Kind >= check.Uint && t.Kind <= check.Uintptr:
		n, ab := w.event(rd, "Uint")
		if ab != nil {
			return nil, ab
		}
		u := n.(uint64)
		if hi, ok := map[check.BasicKind]uint64{check.Uint8: 255, check.Uint16: 65535, check.Uint32: 4294967295}[t.Kind]; ok && u > hi {
			return nil, w.wrongKind(rd, t.Name)
		}
		return mustConvert(u, t.Kind), nil
	}
	n, ab := w.event(rd, "Int")
	if ab != nil {
		return nil, ab
	}
	i := n.(int64)
	bounds := map[check.BasicKind][2]int64{check.Int8: {-128, 127}, check.Int16: {-32768, 32767}, check.Int32: {-2147483648, 2147483647}}
	if b, ok := bounds[t.Kind]; ok && (i < b[0] || i > b[1]) {
		return nil, w.wrongKind(rd, t.Name)
	}
	return mustConvert(i, t.Kind), nil
}
