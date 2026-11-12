package eval

import (
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
	"testing"

	v1 "ergo.local/prototype/runtime/v1"
)

// TestJSONAgreesWithRuntime holds std json's natives (nativesjson.go) to
// the runtime/v1 bodies the built program calls, on generated texts and
// events: open's verdict (the offset and rule of a broken text, under
// random MaxBytes and MaxDepth), a Cursor's every answer along a random
// walk that keeps the read protocol decode keeps (a value a wrong read
// left is skipped), and a Text's bytes after a random well-formed run of
// Writer events, HTML or not. The texts hold every escape, surrogate
// pairs, lone surrogates, \udcXX bytes, invalid UTF-8, the float strings,
// "_tag" members, numbers past int64 and uint64, and one-byte damage. A
// failing seed prints the text, the step and both answers, and the
// replay command.
//
// Mutants only it kills (testdata/mutants.json): oracle-o18-json-html-ignored,
// oracle-o18-json-pair-split, oracle-o18-json-path-index,
// oracle-o18-json-depth-one-more, oracle-o18-json-tag-any-kind.
func TestJSONAgreesWithRuntime(t *testing.T) {
	n := 400
	if testing.Short() {
		n = 150
	}
	first := uint64(0)
	if s := os.Getenv("ERGO_EVAL_JSON_SEED"); s != "" {
		v, err := strconv.ParseUint(s, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		first, n = v, 1
	}
	pkg := checked(t, "test T {\n\tassert true\n}\n")
	in := &interp{budget: DefaultBudget()}
	fr := in.newFrame(pkg, pkg.Tests[0].Decl)
	replay := func(seed uint64) string {
		return fmt.Sprintf("replay: ERGO_EVAL_JSON_SEED=%d go test -run TestJSONAgreesWithRuntime ./internal/sketch/eval", seed)
	}
	for seed := first; seed < first+uint64(n); seed++ {
		g := jsonGen{rand.New(rand.NewPCG(seed, 0x15))}
		text := g.text()
		maxBytes, maxDepth := int64(0), int64(0)
		if g.r.IntN(4) == 0 {
			maxBytes = int64(g.r.IntN(len(text) + 4))
		}
		if g.r.IntN(3) == 0 {
			maxDepth = int64(g.r.IntN(5))
		}
		cur, err := v1.JSONOpen(text, int(maxBytes), int(maxDepth))
		root, off, why := jsonParse(text, maxBytes, maxDepth)
		want := "ok"
		if err != nil {
			m := err.(v1.JSONMalformed)
			want = fmt.Sprintf("Malformed{%d %q}", m.Offset, m.Reason)
		}
		got := "ok"
		if why != "" {
			got = fmt.Sprintf("Malformed{%d %q}", off, why)
		}
		if got != want {
			t.Fatalf("seed %d: open(%q, %d, %d): the evaluator gives %s, runtime/v1 %s\n%s", seed, text, maxBytes, maxDepth, got, want, replay(seed))
		}
		if err == nil {
			mine := &jsonCursor{root: root}
			ask := func(step int, op string) any {
				in.steps, in.bytes = 0, 0
				v, ab := mine.opCall(in, fr, fr.decl, op, nil)
				if ab != nil {
					t.Fatalf("seed %d: %s stopped: %s", seed, op, ab.msg)
				}
				w := cursorOp(cur, op)
				if fmt.Sprint(v) != fmt.Sprint(w) {
					t.Fatalf("seed %d: text %q, step %d %s: the evaluator gives %v, runtime/v1 %v\n%s", seed, text, step, op, v, w, replay(seed))
				}
				return v
			}
			jsonWalk(g, ask)
		}
		// A Text after a random run of events.
		html := g.r.IntN(2) == 0
		ref := v1.NewJSONText()
		if html {
			ref = v1.NewJSONHTMLText()
		}
		mine := &jsonText{html: html}
		var events []string
		emit := func(op string, arg any) {
			events = append(events, fmt.Sprintf("%s(%v)", op, arg))
			var args []Value
			if arg != nil {
				args = []Value{arg}
			}
			in.steps, in.bytes = 0, 0
			if _, ab := mine.opCall(in, fr, fr.decl, op, args); ab != nil {
				t.Fatalf("seed %d: %s stopped: %s", seed, op, ab.msg)
			}
			textOp(ref, op, arg)
		}
		g.events(emit, 0)
		if got, want := mine.b.String(), ref.Written(); got != want {
			t.Fatalf("seed %d: events %v (html %v): the evaluator writes %q, runtime/v1 %q\n%s", seed, events, html, got, want, replay(seed))
		}
	}
}

// cursorOp is op on the runtime's Cursor.
func cursorOp(c *v1.Cursor, op string) any {
	switch op {
	case "TakeNull":
		return c.TakeNull()
	case "Kind":
		return int64(c.Kind())
	case "Path":
		return c.Path()
	case "Wrong":
		return c.Wrong()
	case "Bool":
		return c.Bool()
	case "Number":
		return c.Number()
	case "Int":
		v := c.Int()
		if c.Wrong() {
			return int64(0) // the value of a wrong read is no answer
		}
		return v
	case "Uint":
		v := c.Uint()
		if c.Wrong() {
			return uint64(0)
		}
		return v
	case "Float":
		f := c.Float()
		if c.Wrong() {
			return float64(0) // the value of a wrong read is no answer
		}
		return f
	case "String":
		return c.String()
	case "BeginList":
		return c.BeginList()
	case "BeginObject":
		return c.BeginObject()
	case "Tag":
		return c.Tag()
	case "More":
		return c.More()
	case "Key":
		return c.Key()
	case "Skip":
		c.Skip()
	case "End":
		c.End()
	}
	return unit{}
}

// textOp is op on the runtime's Text.
func textOp(t *v1.Text, op string, arg any) {
	switch op {
	case "Null":
		t.Null(nil)
	case "Number":
		t.Number(nil, arg.(string))
	case "Bool":
		t.Bool(nil, arg.(bool))
	case "Int":
		t.Int(nil, arg.(int64))
	case "Uint":
		t.Uint(nil, arg.(uint64))
	case "Float":
		t.Float(nil, arg.(float64))
	case "String":
		t.String(nil, arg.(string))
	case "BeginList":
		t.BeginList(nil, int(arg.(int64)))
	case "EndList":
		t.EndList(nil)
	case "BeginObject":
		t.BeginObject(nil, int(arg.(int64)))
	case "EndObject":
		t.EndObject(nil)
	case "Tag":
		t.Tag(nil, arg.(string))
	case "Key":
		t.Key(nil, arg.(string))
	}
}

// jsonWalk drives a cursor as decode does: at each value it asks the
// kind, then reads it as a random kind, begins it, skips it or tests it
// for null; a value a wrong read left in place is skipped; a container's
// members are walked to its end or left early by End. Path, Kind and
// Wrong are asked along the way.
func jsonWalk(g jsonGen, ask func(step int, op string) any) {
	step := 0
	do := func(op string) any {
		step++
		return ask(step, op)
	}
	var value func(depth int)
	value = func(depth int) {
		kind := do("Kind").(int64)
		do("Path")
		switch g.r.IntN(5) {
		case 0:
			do("Skip")
			return
		case 1:
			if do("TakeNull").(bool) {
				return
			}
		}
		if kind == 4 || kind == 5 {
			begin := "BeginList"
			if g.r.IntN(4) == 0 {
				begin = "BeginObject" // perhaps the wrong one
			}
			if kind == 5 && g.r.IntN(4) != 0 {
				begin = "BeginObject"
			}
			if !do(begin).(bool) {
				do("Skip")
				return
			}
			if begin == "BeginObject" && g.r.IntN(2) == 0 {
				do("Tag")
				do("Wrong")
			}
			for do("More").(bool) {
				if begin == "BeginObject" {
					do("Key")
				}
				if g.r.IntN(8) == 0 {
					break // End passes the rest
				}
				value(depth + 1)
				do("Path")
			}
			do("End")
			do("Path")
			return
		}
		reads := []string{"Bool", "Number", "Int", "Uint", "Float", "String"}
		op := reads[g.r.IntN(len(reads))]
		do(op)
		wrong := do("Wrong").(bool)
		taken := !wrong || (kind == 2 && op != "Bool" && op != "String")
		if !taken {
			do("Skip")
		}
	}
	value(0)
	do("Kind")
	do("More")
}

type jsonGen struct{ r *rand.Rand }

// text is a JSON text, perhaps damaged by one byte.
func (g jsonGen) text() string {
	var b strings.Builder
	g.space(&b)
	g.value(&b, 0)
	g.space(&b)
	s := b.String()
	if g.r.IntN(3) == 0 && len(s) > 0 {
		i := g.r.IntN(len(s))
		junk := []byte("{}[],:\"\\ tfn0-.eE\x01x\xff")
		switch g.r.IntN(3) {
		case 0:
			s = s[:i] + s[i+1:]
		case 1:
			s = s[:i] + string(junk[g.r.IntN(len(junk))]) + s[i:]
		case 2:
			s = s[:i]
		}
	}
	return s
}

func (g jsonGen) space(b *strings.Builder) {
	for g.r.IntN(4) == 0 {
		b.WriteByte(" \t\n\r"[g.r.IntN(4)])
	}
}

func (g jsonGen) value(b *strings.Builder, depth int) {
	k := g.r.IntN(8)
	if depth > 3 {
		k %= 5
	}
	switch k {
	case 0:
		b.WriteString([]string{"null", "true", "false"}[g.r.IntN(3)])
	case 1, 2:
		b.WriteString(g.number())
	case 3, 4:
		b.WriteString(g.str())
	case 5, 6:
		b.WriteByte('{')
		n := g.r.IntN(4)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			g.space(b)
			b.WriteString(g.key())
			g.space(b)
			b.WriteByte(':')
			g.space(b)
			g.value(b, depth+1)
			g.space(b)
		}
		b.WriteByte('}')
	default:
		b.WriteByte('[')
		n := g.r.IntN(4)
		for i := 0; i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			g.space(b)
			g.value(b, depth+1)
			g.space(b)
		}
		b.WriteByte(']')
	}
}

func (g jsonGen) key() string {
	switch g.r.IntN(4) {
	case 0:
		return `"_tag"`
	case 1:
		return `"a"`
	}
	return g.str()
}

func (g jsonGen) number() string {
	switch g.r.IntN(6) {
	case 0:
		return []string{"0", "-0", "1e400", "-1e400", "18446744073709551615", "18446744073709551616", "9223372036854775807", "-9223372036854775808", "-9223372036854775809", "1.5", "0.1e-7"}[g.r.IntN(11)]
	}
	var b strings.Builder
	if g.r.IntN(3) == 0 {
		b.WriteByte('-')
	}
	b.WriteString(strconv.Itoa(g.r.IntN(1000)))
	if g.r.IntN(4) == 0 {
		b.WriteString("." + strconv.Itoa(g.r.IntN(100)))
	}
	if g.r.IntN(5) == 0 {
		b.WriteString([]string{"e", "E", "e+", "e-"}[g.r.IntN(4)] + strconv.Itoa(g.r.IntN(30)))
	}
	return b.String()
}

func (g jsonGen) str() string {
	if g.r.IntN(5) == 0 {
		return []string{`"NaN"`, `"Infinity"`, `"-Infinity"`, `"\u004eaN"`, `"inf"`}[g.r.IntN(5)]
	}
	var b strings.Builder
	b.WriteByte('"')
	n := g.r.IntN(6)
	for i := 0; i < n; i++ {
		switch g.r.IntN(9) {
		case 0:
			b.WriteString([]string{`\"`, `\\`, `\/`, `\b`, `\f`, `\n`, `\r`, `\t`}[g.r.IntN(8)])
		case 1:
			b.WriteString(fmt.Sprintf(`\u%04x`, g.r.IntN(0x10000)))
		case 2:
			b.WriteString(fmt.Sprintf(`\u%04X\u%04x`, 0xD800+g.r.IntN(0x400), 0xDC00+g.r.IntN(0x400)))
		case 3:
			b.WriteString(fmt.Sprintf(`\udc%02x`, 0x80+g.r.IntN(0x80)))
		case 4:
			b.WriteString(fmt.Sprintf(`\ud8%02x\u00%02x`, g.r.IntN(0x100), g.r.IntN(0x80)))
		case 5:
			b.WriteByte(byte(0x80 + g.r.IntN(0x80)))
		case 6:
			b.WriteString([]string{"é", "\u2028", "\u2029", "<&>", "日本"}[g.r.IntN(5)])
		default:
			b.WriteByte(byte('a' + g.r.IntN(26)))
		}
	}
	b.WriteByte('"')
	return b.String()
}

// raw is a string value for a Text: every byte kind a string may hold.
func (g jsonGen) raw() string {
	var b strings.Builder
	n := g.r.IntN(6)
	for i := 0; i < n; i++ {
		switch g.r.IntN(5) {
		case 0:
			b.WriteByte(byte(g.r.IntN(0x20)))
		case 1:
			b.WriteByte(byte(0x80 + g.r.IntN(0x80)))
		case 2:
			b.WriteString([]string{"\"", "\\", "<", ">", "&", "\u2028", "\u2029", "é", "\x7f", "\ufffd"}[g.r.IntN(10)])
		default:
			b.WriteByte(byte('a' + g.r.IntN(26)))
		}
	}
	return b.String()
}

// events emits one well-formed value's Writer events.
func (g jsonGen) events(emit func(op string, arg any), depth int) {
	k := g.r.IntN(10)
	if depth > 2 {
		k %= 7
	}
	switch k {
	case 0:
		emit("Null", nil)
	case 1:
		emit("Bool", g.r.IntN(2) == 0)
	case 2:
		emit("Int", int64(g.r.Uint64()>>g.r.IntN(64)))
	case 3:
		emit("Uint", g.r.Uint64()>>g.r.IntN(64))
	case 4:
		fs := []float64{0, math.Copysign(0, -1), math.NaN(), math.Inf(1), math.Inf(-1), 1e21, 1e20, 1e-6, 1e-7, 123.456, -2.5e-10, math.MaxFloat64, math.SmallestNonzeroFloat64}
		f := fs[g.r.IntN(len(fs))]
		if g.r.IntN(2) == 0 {
			f = math.Float64frombits(g.r.Uint64())
		}
		emit("Float", f)
	case 5:
		emit("String", g.raw())
	case 6:
		emit("Number", g.number())
	case 7, 8:
		n := g.r.IntN(4)
		emit("BeginObject", int64(n))
		if g.r.IntN(3) == 0 {
			emit("Tag", g.raw())
		}
		for i := 0; i < n; i++ {
			emit("Key", g.raw())
			g.events(emit, depth+1)
		}
		emit("EndObject", nil)
	default:
		n := g.r.IntN(4)
		emit("BeginList", int64(n))
		for i := 0; i < n; i++ {
			g.events(emit, depth+1)
		}
		emit("EndList", nil)
	}
}
