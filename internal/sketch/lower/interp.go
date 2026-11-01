package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// interpolated lowers `"a \(x) b"` to a Go string concatenation (grammar.md,
// Strings 5): each literal segment is its Go string, and each interpolated
// value of a basic type is its text as Go's %v writes it (fmt.Sprint of the
// one value), through runtime/v1's Text functions. A value of any other
// type is check-v1 stage E's (the stage D3b ruling on interpolation). The
// values are lowered left to right, each where the string reads it.
func (fc *funcCtx) interpolated(x *syntax.InterpolatedString) string {
	if len(x.Texts) != len(x.Exprs)+1 {
		ice(x, "an interpolated string with %d segments for %d values", len(x.Texts), len(x.Exprs))
	}
	var parts []string
	for i, seg := range x.Texts {
		if s := fc.segment(seg, i == 0, i == len(x.Texts)-1); s != "" {
			parts = append(parts, strconv.Quote(s))
		}
		if i < len(x.Exprs) {
			parts = append(parts, fc.text(x.Exprs[i]))
		}
	}
	if len(parts) == 0 {
		return `""`
	}
	return "(" + strings.Join(parts, " + ") + ")"
}

// segment is the value of one literal segment of an interpolated string:
// its raw source without the delimiters (the head's opening quote, a `)`
// before a middle or the tail, a `\(` after the head or a middle, the
// tail's closing quote), with its escapes read as a string literal's.
func (fc *funcCtx) segment(seg *syntax.BasicLit, head, tail bool) string {
	raw := seg.Value
	if len(raw) < 2 {
		ice(seg, "an interpolated string segment %q", raw)
	}
	raw = raw[1:] // the opening quote or the `)`
	if tail {
		raw = raw[:len(raw)-1] // the closing quote
	} else {
		raw = strings.TrimSuffix(raw, `\(`)
	}
	s, err := strconv.Unquote(`"` + raw + `"`)
	if err != nil {
		ice(seg, "an interpolated string segment %q that is not a string literal's text: %v", seg.Value, err)
	}
	return s
}

// text is the Go string of an interpolated value of a basic type.
func (fc *funcCtx) text(x syntax.Expr) string {
	t := fc.typeOf(x)
	b, ok := t.(*check.Basic)
	if !ok {
		refuse(x, "an interpolated value of type %s is lowered after check-v1 stage E; a value of a basic type is lowered now", t)
	}
	v := fc.value(x)
	rt := fc.l.rt()
	switch {
	case b.Kind == check.String:
		return v
	case b.Kind == check.Bool:
		return rt + ".TextBool(" + v + ")"
	case b.Kind >= check.Int && b.Kind <= check.Int64:
		return rt + ".TextInt(int64(" + v + "))"
	case b.Kind >= check.Uint && b.Kind <= check.Uintptr:
		return rt + ".TextUint(uint64(" + v + "))"
	case b.Kind == check.Float32:
		return rt + ".TextFloat(float64(" + v + "), 32)"
	case b.Kind == check.Float64:
		return rt + ".TextFloat(" + v + ", 64)"
	case b.Kind == check.Duration:
		return rt + ".TextDuration(" + v + ")"
	}
	refuse(x, "an interpolated value of type %s is lowered after check-v1 stage E; a value of a basic type is lowered now", t)
	return ""
}
