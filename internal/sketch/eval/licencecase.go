package eval

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/testplan"
)

// A licence run's corpus line (runtime.md, Tests 7; LAWFFI r2 §3.4: "the
// arguments are from then on the first case of the law's run in that
// module, as a Go fuzz corpus keeps a crasher"): the refuted case's
// values, `(v, …)` in parameter order, each in an exact text of its type,
// so the case is the same values whatever the generator or the class
// tables later draw:
//
//   - a string: Go's quoted form;
//   - an integer: its decimal value;
//   - a float: Go's hexadecimal form ('x'), so -0, the infinities and a
//     NaN keep their class;
//   - a listable type: `#k`, its k-th value in testplan.Domain's order;
//   - a slice: `[v, …]`.
//
// Any other type has no line: such a law has no class table, so it is
// never validated, and a corpus would not change its verdict.

// safeDecode is decodeCase, false also when the budget cannot pay for it.
func (in *interp) safeDecode(fr *frame, law *check.Law, line string) (vals []Value, ok bool) {
	defer func() {
		if r := recover(); r != nil {
			if _, is := r.(*abrupt); !is {
				panic(r)
			}
			vals, ok = nil, false
		}
	}()
	return in.decodeCase(fr, law, line)
}

// encodeCase is the corpus line of vals, the values of law's parameters,
// "" when one has no encoding or the budget cannot pay for it.
func (in *interp) encodeCase(fr *frame, law *check.Law, vals []Value) (line string) {
	defer func() {
		if r := recover(); r != nil {
			if _, is := r.(*abrupt); !is {
				panic(r)
			}
			line = ""
		}
	}()
	parts := make([]string, len(vals))
	for i, v := range law.Params {
		s, ok := in.encodeValue(fr, v.Type, vals[i])
		if !ok {
			return ""
		}
		parts[i] = s
	}
	return "(" + strings.Join(parts, ", ") + ")"
}

func (in *interp) encodeValue(fr *frame, t check.Type, v Value) (string, bool) {
	if vs, ok := testplan.Domain(t); ok && len(vs) > 0 {
		want := describe(v)
		for k, x := range vs {
			if describe(in.listValue(fr, x)) == want {
				return "#" + strconv.Itoa(k), true
			}
		}
		return "", false
	}
	switch u := underOf(t).(type) {
	case *check.Basic:
		switch x := v.(type) {
		case string:
			return strconv.Quote(x), true
		case float64:
			return strconv.FormatFloat(x, 'x', -1, 64), true
		case float32:
			return strconv.FormatFloat(float64(x), 'x', -1, 32), true
		}
		if _, _, ok := intWidth(u.Kind); ok {
			switch x := v.(type) {
			case int64, int32, int16, int8, uint64, uint32, uint16, uint8:
				return fmtInt(x), true
			}
		}
	case *check.Slice:
		s, ok := v.(sliceV)
		if !ok {
			return "", false
		}
		parts := make([]string, 0, s.n)
		for _, e := range s.elems() {
			p, ok := in.encodeValue(fr, u.Elem, e)
			if !ok {
				return "", false
			}
			parts = append(parts, p)
		}
		return "[" + strings.Join(parts, ", ") + "]", true
	}
	return "", false
}

// fmtInt is an integer Value's decimal text.
func fmtInt(v Value) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int8:
		return strconv.FormatInt(int64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case uint32:
		return strconv.FormatUint(uint64(x), 10)
	case uint16:
		return strconv.FormatUint(uint64(x), 10)
	case uint8:
		return strconv.FormatUint(uint64(x), 10)
	}
	return ""
}

// decodeCase reads a corpus line back into the values of law's
// parameters; false when it does not fit them (the law's parameters
// changed since it was written).
func (in *interp) decodeCase(fr *frame, law *check.Law, line string) ([]Value, bool) {
	s := strings.TrimSpace(line)
	if !strings.HasPrefix(s, "(") {
		return nil, false
	}
	s = s[1:]
	vals := make([]Value, len(law.Params))
	for i, p := range law.Params {
		v, rest, ok := in.decodeValue(fr, p.Type, s)
		if !ok {
			return nil, false
		}
		vals[i] = v
		rest = strings.TrimLeft(rest, " ")
		switch {
		case i < len(law.Params)-1 && strings.HasPrefix(rest, ","):
			rest = strings.TrimLeft(rest[1:], " ")
		case i == len(law.Params)-1 && rest == ")":
		default:
			return nil, false
		}
		s = rest
	}
	if len(law.Params) == 0 && s != ")" {
		return nil, false
	}
	return vals, true
}

// decodeValue reads one value of t at the start of s, and the rest.
func (in *interp) decodeValue(fr *frame, t check.Type, s string) (Value, string, bool) {
	if vs, ok := testplan.Domain(t); ok && len(vs) > 0 {
		tok, rest := caseWord(s)
		k, err := strconv.Atoi(strings.TrimPrefix(tok, "#"))
		if !strings.HasPrefix(tok, "#") || err != nil || k < 0 || k >= len(vs) {
			return nil, s, false
		}
		return in.listValue(fr, vs[k]), rest, true
	}
	switch u := underOf(t).(type) {
	case *check.Basic:
		switch u.Kind {
		case check.String:
			q, err := strconv.QuotedPrefix(s)
			if err != nil {
				return nil, s, false
			}
			v, err := strconv.Unquote(q)
			if err != nil {
				return nil, s, false
			}
			mustCharge(in, fr, int64(len(v)))
			return v, s[len(q):], true
		case check.Float32, check.Float64:
			tok, rest := caseWord(s)
			bits := 64
			if u.Kind == check.Float32 {
				bits = 32
			}
			f, err := strconv.ParseFloat(tok, bits)
			if err != nil {
				return nil, s, false
			}
			if bits == 32 {
				return float32(f), rest, true
			}
			return f, rest, true
		}
		width, signed, ok := intWidth(u.Kind)
		if !ok {
			return nil, s, false
		}
		tok, rest := caseWord(s)
		var v Value
		if signed {
			n, err := strconv.ParseInt(tok, 10, int(width))
			if err != nil {
				return nil, s, false
			}
			v, ok = fromInt64(u.Kind, n)
		} else {
			n, err := strconv.ParseUint(tok, 10, int(width))
			if err != nil {
				return nil, s, false
			}
			v, ok = fromUint64(u.Kind, n)
		}
		return v, rest, ok
	case *check.Slice:
		if !strings.HasPrefix(s, "[") {
			return nil, s, false
		}
		s = strings.TrimLeft(s[1:], " ")
		var xs []Value
		for !strings.HasPrefix(s, "]") {
			v, rest, ok := in.decodeValue(fr, u.Elem, s)
			if !ok {
				return nil, s, false
			}
			xs = append(xs, v)
			rest = strings.TrimLeft(rest, " ")
			if strings.HasPrefix(rest, ",") {
				rest = strings.TrimLeft(rest[1:], " ")
			} else if !strings.HasPrefix(rest, "]") {
				return nil, s, false
			}
			s = rest
		}
		mustCharge(in, fr, int64(len(xs)))
		return newSlice(xs), s[1:], true
	}
	return nil, s, false
}

// caseWord is the bare word at the start of s, up to a comma or a closing
// bracket, and the rest.
func caseWord(s string) (string, string) {
	i := strings.IndexAny(s, ",)]")
	if i < 0 {
		return strings.TrimSpace(s), ""
	}
	return strings.TrimSpace(s[:i]), s[i:]
}
