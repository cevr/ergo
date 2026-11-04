package lower

import (
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/check"
	"ergo.local/prototype/internal/sketch/syntax"
)

// interpolated lowers `"a \(x) b"` to one Go string concatenation (grammar.md,
// Strings 5): each literal segment is its Go string, and each interpolated
// value is its text as Go's %v writes it (Go's Sprint of the one value).
// The checker admits a value only when its type is a basic type or a type
// defined over one (D-E6), so every value has a direct text; text writes it
// with direct strconv calls: no formatting package, no empty interface and
// no run-time type inspection (196s: no boxing). The
// values are operands, lowered left to right, each once (operands): a value
// before one that writes statements is bound to a temporary first.
//
// The concatenation allocates once, as the Go a person writes with
// strconv appends into a sized buffer does (owner, #242/#243: emitted Go
// matches hand-written Go). Go's compiler reads a string(b) operand of a
// concatenation that holds a non-empty literal in place, with no copy
// (cmd/compile, order.go, OBYTES2STRTMP), and a constant-size make whose
// slice does not escape stays on the stack. So a number written by
// strconv.Append* into make([]byte, 0, max) costs no allocation of its
// own, and the concatenation's one allocation holds the whole text. With
// no non-empty literal Go copies each string(b), so text uses the
// strconv.Format* functions there, which keep Go's small-integer strings.
//
// Prior art: Go concatenates a + b + c in one allocation of the summed
// length (runtime.concatstrings); ReScript lowers a template literal to a
// JavaScript template literal and Gleam a <> chain to the host's
// concatenation, each with no runtime of its own.
func (fc *funcCtx) interpolated(x *syntax.InterpolatedString) string {
	if len(x.Texts) != len(x.Exprs)+1 {
		ice(x, "an interpolated string with %d segments for %d values", len(x.Texts), len(x.Exprs))
	}
	later := make([]bool, len(x.Exprs)+1)
	for i := len(x.Exprs) - 1; i >= 0; i-- {
		later[i] = later[i+1] || fc.emits(x.Exprs[i])
	}
	segs := make([]string, len(x.Texts))
	inPlace := false
	for i, seg := range x.Texts {
		segs[i] = fc.segment(seg, i == 0, i == len(x.Texts)-1)
		inPlace = inPlace || segs[i] != ""
	}
	var parts []string
	for i, s := range segs {
		if s != "" {
			parts = append(parts, strconv.Quote(s))
		}
		if i < len(x.Exprs) {
			v := fc.value(x.Exprs[i])
			if later[i+1] && !fc.isConst(x.Exprs[i]) {
				v = fc.spill(v)
			}
			parts = append(parts, fc.text(x.Exprs[i], v, inPlace))
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

// The longest text of a value of each kind that strconv.Append* writes:
// -9223372036854775808 and 18446744073709551615 (20 bytes), a float64 such
// as -2.2250738585072014e-308 (24 bytes; a float32's is shorter), false.
const (
	intTextMax   = 20
	floatTextMax = 24
)

// text is the Go string of v, the lowered value of the interpolated part x.
// x's type is a basic type or a type defined over one, at any depth (the
// checker's textForm); a defined type is converted to its basic Go type
// first, which is free. With inPlace, the concatenation holds a non-empty
// literal, so a number is appended into a stack buffer that the
// concatenation reads in place (interpolated).
func (fc *funcCtx) text(x syntax.Expr, v string, inPlace bool) string {
	t := fc.typeOf(x)
	b, ok := t.(*check.Basic)
	if n, isNamed := t.(*check.Named); isNamed {
		b, ok = n.Underlying().(*check.Basic)
	}
	if !ok {
		ice(x, "an interpolated value of type %s, which has no text form and which the checker refuses (D-E6)", t)
	}
	// as is v as a value of the Go type goType, converted unless it has it.
	as := func(goType string) string {
		if bt, basic := t.(*check.Basic); basic && bt.Name == goType {
			return v
		}
		return goType + "(" + v + ")"
	}
	if b.Kind == check.String {
		return as("string")
	}
	if b.Kind == check.Duration {
		// Duration's String is its %v text (1h0m0s).
		return fc.l.timePkg() + ".Duration(" + v + ").String()"
	}
	sc := fc.l.strconvPkg()
	switch {
	case b.Kind == check.Bool:
		// FormatBool returns a constant string: no allocation.
		return sc + ".FormatBool(" + as("bool") + ")"
	case b.Kind >= check.Int && b.Kind <= check.Int64:
		if inPlace {
			return "string(" + sc + ".AppendInt(make([]byte, 0, " + strconv.Itoa(intTextMax) + "), " + as("int64") + ", 10))"
		}
		return sc + ".FormatInt(" + as("int64") + ", 10)"
	case b.Kind >= check.Uint && b.Kind <= check.Uintptr:
		if inPlace {
			return "string(" + sc + ".AppendUint(make([]byte, 0, " + strconv.Itoa(intTextMax) + "), " + as("uint64") + ", 10))"
		}
		return sc + ".FormatUint(" + as("uint64") + ", 10)"
	case b.Kind == check.Float32, b.Kind == check.Float64:
		// A float32 widens to float64 exactly, and strconv writes the
		// shortest text that reads back as the same float32.
		bits := "64"
		if b.Kind == check.Float32 {
			bits = "32"
		}
		if inPlace {
			return "string(" + sc + ".AppendFloat(make([]byte, 0, " + strconv.Itoa(floatTextMax) + "), " + as("float64") + ", 'g', -1, " + bits + "))"
		}
		return sc + ".FormatFloat(" + as("float64") + ", 'g', -1, " + bits + ")"
	case b.Kind == check.Complex64, b.Kind == check.Complex128:
		// strconv has no AppendComplex; FormatComplex writes %v's text,
		// (re+imi), each half at the complex's float width.
		bits := "128"
		if b.Kind == check.Complex64 {
			bits = "64"
		}
		return sc + ".FormatComplex(" + as("complex128") + ", 'g', -1, " + bits + ")"
	}
	ice(x, "an interpolated value of the basic type %s with no lowering", t)
	return ""
}

// strconvPkg is the name in main.go of Go's strconv package, which writes
// the interpolated values. The naming pass reserves it (nameAll), and
// main.go imports it with the Go packages that user externs target
// (module.go), so an extern of strconv shares the one import.
func (l *lowerer) strconvPkg() string {
	if l.hostImports == nil {
		l.hostImports = map[string]bool{}
	}
	l.hostImports["strconv"] = true
	return l.pkgName(pkgKey{"import", "strconv"}, "strconv")
}
