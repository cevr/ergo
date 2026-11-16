package check

import "fmt"

// WireForm is a representation declared by the public wire protocol.
// WireForm(x X)'s result type is metadata, like a constraint: its body is
// checked normally but is not called by a derived codec.
type WireForm uint8

const (
	WireDefault WireForm = iota
	WireOmittable
	WireNullable
	WireBare
	WireNumberText
	WireMembers
)

// WireFormOf reads a fully checked type's protocol declaration. The names
// here belong to the language's predeclared protocol, not a std package.
func WireFormOf(t Type) WireForm {
	n, ok := t.(*Named)
	if !ok {
		return WireDefault
	}
	f := n.method("WireForm")
	if f == nil || f.Sig == nil || len(f.Sig.Results) != 1 {
		return WireDefault
	}
	result, ok := f.Sig.Results[0].(*Named)
	if !ok {
		return WireDefault
	}
	for k, name := range []string{"", "WireOmittable", "WireNullable", "WireBare", "WireNumberText", "WireMembers"} {
		if k > 0 && result.Obj == preludeType(name).Obj {
			return WireForm(k)
		}
	}
	return WireDefault
}

// wireFormReason validates the declaration before any structural walk.
// Shape checks do not grant wire admission: the normal walk still checks
// every payload and construction authority.
func (c *checker) wireFormReason(t Type) string {
	n, ok := t.(*Named)
	if !ok {
		return ""
	}
	f := n.method("WireForm")
	if f == nil {
		return ""
	}
	sig := c.funcSig(f)
	if sig == nil || len(sig.Params) != 1 || len(sig.Results) != 1 ||
		!sig.Errors.Empty() || !sig.Uses.Empty() || len(sig.TParams) != len(n.origin().TParams) {
		return "has a WireForm declaration that is not a rowless function of its receiver alone"
	}
	form := WireFormOf(t)
	if form != WireDefault && form != WireNumberText && c.wirePair(t) != nil {
		return "declares both a Parse/Wire pair and a structural WireForm"
	}
	switch form {
	case WireDefault:
		return "has a WireForm result that is not a predeclared wire-form marker"
	case WireOmittable, WireNullable:
		u, ok := under(t).(*Union)
		if !ok || len(u.Variants) != 2 {
			return "requires two record variants for its wire form: one empty and one with one payload"
		}
		empty, payload := 0, 0
		for _, v := range u.Variants {
			if !c.wireStructuralPart(v) {
				return "requires ordinary record alternatives, without independent codec forms"
			}
			s, ok := under(v).(*Struct)
			if !ok {
				continue
			}
			switch len(s.Fields) {
			case 0:
				empty++
			case 1:
				payload++
			}
		}
		if empty != 1 || payload != 1 {
			return "requires one empty variant and one single-payload variant for its wire form"
		}
	case WireNumberText:
		s, ok := under(t).(*Struct)
		p := c.wirePair(t)
		if !ok || len(s.Fields) != 1 || !isString(s.Fields[0].Type) ||
			p == nil || !c.identical(p.Base, Typ[String]) || !p.Parse.Sig.Uses.Empty() || !p.Wire.Sig.Uses.Empty() {
			return "requires one string field and a string Parse/Wire pair for WireNumberText"
		}
	case WireMembers:
		s, ok := under(t).(*Struct)
		if !ok || len(s.Fields) != 1 {
			return "requires one slice of key/value records for WireMembers"
		}
		list, ok := under(s.Fields[0].Type).(*Slice)
		if !ok || !c.wireStructuralPart(s.Fields[0].Type) {
			return "requires a slice field for WireMembers"
		}
		entry, ok := under(list.Elem).(*Struct)
		if !ok || len(entry.Fields) != 2 || !isString(entry.Fields[0].Type) {
			return "requires two entry fields, a string key followed by a wire value, for WireMembers"
		}
		if !c.wireStructuralPart(list.Elem) || !c.wireStructuralPart(entry.Fields[0].Type) {
			return "requires ordinary entry records and unrefined string keys, without independent codec forms"
		}
	case WireBare:
		u, ok := under(t).(*Union)
		if !ok {
			return "requires a union for WireBare"
		}
		kinds := map[string]bool{}
		for _, v := range u.Variants {
			if why := c.wireFormReason(v); why != "" {
				return why
			}
			kind := WireVariantKind(v)
			if kind == "" {
				return fmt.Sprintf("has a variant %s without a single event kind for WireBare", v)
			}
			if kinds[kind] {
				return fmt.Sprintf("has two variants of event kind %s for WireBare", kind)
			}
			kinds[kind] = true
		}
	}
	return ""
}

// A form that consumes a record's fields or a slice's elements directly
// must not bypass a parser, refinement or another representation on that
// intermediate type. Payload values still use their own codecs normally.
func (c *checker) wireStructuralPart(t Type) bool {
	if RefinementOf(t) != nil || c.wirePair(t) != nil {
		return false
	}
	n, ok := t.(*Named)
	return !ok || n.method("WireForm") == nil
}

// WireVariantKind is the Reader.Peek kind of an admitted WireBare variant.
// A variant with its own form uses that form; otherwise an empty record
// is null and a single-field record transparently carries its payload.
func WireVariantKind(t Type) string {
	if n, ok := t.(*Named); ok && n.method(wireMethodName) != nil && WireFormOf(t) == WireDefault {
		return "" // a pair's base, not its record fields, selects its events
	}
	switch WireFormOf(t) {
	case WireNumberText:
		return "WireNumber"
	case WireMembers:
		return "WireObject"
	}
	s, ok := under(t).(*Struct)
	if !ok {
		return ""
	}
	if len(s.Fields) == 0 {
		return "WireNull"
	}
	if len(s.Fields) != 1 {
		return ""
	}
	return wireEventKind(s.Fields[0].Type)
}

func wireEventKind(t Type) string {
	// A by-name pair may change the event kind. Unless the type declares
	// its form explicitly, there is no kind derivable from its record
	// shape alone; reject it rather than dispatch on a guessed kind.
	if n, ok := t.(*Named); ok && n.method(wireMethodName) != nil && WireFormOf(t) == WireDefault {
		return ""
	}
	switch WireFormOf(t) {
	case WireNumberText:
		return "WireNumber"
	case WireMembers:
		return "WireObject"
	case WireNullable, WireBare:
		return "" // a payload of several kinds cannot discriminate a variant
	}
	switch u := under(t).(type) {
	case *Basic:
		switch {
		case isBoolean(u):
			return "WireBool"
		case isString(u):
			return "WireString"
		case isInteger(u), isFloat(u):
			return "WireNumber"
		}
	case *Slice, *Tuple:
		return "WireList"
	case *Struct, *Map, *Union:
		return "WireObject"
	}
	return ""
}
