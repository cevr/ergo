package selfhostcheck

import (
	"math/rand/v2"
	"strconv"
	"strings"

	"ergo.local/prototype/internal/sketch/sketchtest"
)

// generate is one seeded source of the formatter's input domain: a
// recursive grammar over the declarations, statements and expressions of
// the language, with author line breaks and comments at the places a line
// may break, operators of every precedence, composite literals and records
// whose alignment cells are short, long and empty, Unicode and invalid
// UTF-8 in strings and comments, raw strings with line breaks, string
// interpolation, imports and externs. Most outputs parse; the rest are
// compared as failures.
func generate(r *rand.Rand) string {
	g := &gen{r: r}
	n := 1 + r.IntN(6)
	for i := range n {
		if i > 0 {
			g.b.WriteString(g.pick("\n", "\n\n", "\n\n\n", "\n// between\n"))
		}
		g.decl()
	}
	g.b.WriteString(g.pick("\n", "", "\n\n", " // end\n", "\n/* end */"))
	return g.b.String()
}

// alignInputs are aligned columns whose cell widths taper, as the Go
// formatter's TestAlignmentIsLinear builds them: each width passes the
// geometric-mean test against its section while the section's widest and
// narrowest cells part by the ratio, so the section ends early. Each taper
// is followed by a run of short cells, in every column the formatter aligns.
func alignInputs() []sketchtest.Input {
	kinds := []struct {
		name, open, close string
		row               func(i, size int) string
	}{
		{"record names", "type T {\n", "}\n", func(i, size int) string { return cell("f", i, size) + " int,\n" }},
		{"record types", "type T {\n", "}\n", func(i, size int) string { return "f" + strconv.Itoa(i) + " " + cell("T", i, size) + ",\n" }},
		{"record types before comments", "type T {\n", "}\n", func(i, size int) string { return "f" + strconv.Itoa(i) + " " + cell("T", i, size) + ", // c\n" }},
		{"let values before comments", "", "", func(i, size int) string { return "let v" + strconv.Itoa(i) + " = " + cell("x", i, size) + " // c\n" }},
		{"map keys", "let m = Map[string, int]{\n", "}\n", func(i, size int) string { return strconv.Quote(cell("k", i, max(1, size-2))) + ": 1,\n" }},
		{"context entry keys", "let W = context {\n", "}\n", func(i, size int) string { return cell("K", i, size) + ": T{},\n" }},
	}
	tapers := map[string][]int{
		"factor 0.7":  {96, 67, 47, 32, 23, 16, 11},
		"factor 0.45": {96, 43, 19, 8},
		"rising":      {11, 16, 23, 32, 47, 67, 96},
		"steps":       {20, 45, 50, 60, 12},
	}
	var out []sketchtest.Input
	for _, kind := range kinds {
		for name, widths := range tapers {
			var b strings.Builder
			b.WriteString(kind.open)
			for i, w := range widths {
				b.WriteString(kind.row(i, w))
			}
			for i := range 8 {
				b.WriteString(kind.row(len(widths)+i, 4))
			}
			b.WriteString(kind.close)
			out = append(out, sketchtest.Input{Name: "align/" + kind.name + "/" + name, Src: b.String()})
		}
	}
	return out
}

// cell is a unique name of at least size characters.
func cell(prefix string, i, size int) string {
	name := prefix + strconv.Itoa(i)
	return name + strings.Repeat("a", max(0, size-len(name)))
}

type gen struct {
	r     *rand.Rand
	b     strings.Builder
	depth int
}

func (g *gen) pick(xs ...string) string { return xs[g.r.IntN(len(xs))] }

func (g *gen) w(xs ...string) {
	for _, x := range xs {
		g.b.WriteString(x)
	}
}

// gap is the whitespace at a place where a line may break: after an
// opener, a comma or a binary operator.
func (g *gen) gap() {
	switch g.r.IntN(12) {
	case 0:
		g.w("\n")
	case 1:
		g.w("\n\n")
	case 2:
		g.w(" /* ", g.text(), " */ ")
	case 3:
		g.w(" // ", g.text(), "\n")
	case 4:
		g.w("  ")
	case 5:
		g.w("")
	default:
		g.w(" ")
	}
}

// inline is the whitespace between two tokens on one line.
func (g *gen) inline() {
	switch g.r.IntN(10) {
	case 0:
		g.w(" /* ", g.text(), " */ ")
	case 1:
		g.w("/*", g.text(), "*/")
	case 2:
		g.w("\t")
	default:
		g.w(" ")
	}
}

// eol ends a line, maybe with a trailing comment.
func (g *gen) eol() {
	switch g.r.IntN(8) {
	case 0:
		g.w(" // ", g.text(), "\n")
	case 1:
		g.w(" /* ", g.text(), " */\n")
	case 2:
		g.w("\n\n")
	case 3:
		g.w(";")
	default:
		g.w("\n")
	}
}

var words = []string{"a", "b", "x", "count", "value", "é", "größe", "日本", "a_long_descriptive_name_for_alignment_tests", "n2", "Item", "Point"}

func (g *gen) name() string { return words[g.r.IntN(len(words))] }

func (g *gen) typeName() string {
	return g.pick("int", "string", "bool", "float64", "Point", "Option[int]", "[]string", "Map[string, int]", "[3]int", "*Point", "go.Error", "a_very_long_type_name_that_breaks_alignment_sections_apart")
}

func (g *gen) text() string {
	return g.pick("x", "note", "日本語", "a longer comment with words", "\xff\xfe", "é", "tab\there", "")
}

func (g *gen) literal() {
	switch g.r.IntN(12) {
	case 0:
		g.w(strconv.Itoa(g.r.IntN(1000)))
	case 1:
		g.w(g.pick("2.5", "1e9", "0x1F", "0b101", "1_000", "07", "500ms", "2s"))
	case 2:
		g.w("\"", g.text(), "\"")
	case 3:
		g.w("`", g.pick("raw", "two\nlines", "a\n\tb\n", "\n"), "`")
	case 4:
		g.w(g.pick("'a'", "'\\n'", "'é'"))
	case 5:
		g.w("\"a\\(")
		g.expr()
		g.w(")b")
		if g.r.IntN(2) == 0 {
			g.w("\\(")
			g.expr()
			g.w(")")
		}
		g.w("c\"")
	default:
		g.w(g.name())
	}
}

var binaryOps = []string{"||", "&&", "==", "!=", "<", "<=", ">", ">=", "+", "-", "|", "^", "*", "/", "%", "<<", ">>", "&", "&^", ".."}

func (g *gen) expr() {
	if g.depth > 4 {
		g.literal()
		return
	}
	g.depth++
	defer func() { g.depth-- }()
	switch g.r.IntN(20) {
	case 0, 1, 2:
		g.expr()
		g.inline()
		g.w(binaryOps[g.r.IntN(len(binaryOps))])
		g.gap()
		g.expr()
	case 3:
		g.w(g.pick("-", "!", "^", "*", "&", "+"))
		g.expr()
	case 4:
		g.w("(")
		g.gap()
		g.expr()
		g.w(g.pick(")", "\n)"))
	case 5:
		g.w(g.name(), "(")
		g.list(g.expr)
		g.w(")")
	case 6:
		g.literal()
		g.w(g.pick(".", " ."), g.name())
	case 7:
		g.w(g.name(), "[")
		g.list(g.expr)
		g.w("]")
	case 8:
		g.w(g.name(), "[")
		if g.r.IntN(2) == 0 {
			g.expr()
		}
		g.w(":")
		if g.r.IntN(2) == 0 {
			g.expr()
		}
		if g.r.IntN(4) == 0 {
			g.w(":")
			g.expr()
		}
		g.w("]")
	case 9:
		g.w(g.pick("Point", "[]int", "Map[string, int]", ""), "{")
		g.list(func() {
			if g.r.IntN(3) > 0 {
				g.w(g.name(), ":")
				g.inline()
			}
			g.expr()
		})
		g.w("}")
	case 10:
		g.w("Point{..")
		g.expr()
		g.w(", ", g.name(), ": ")
		g.expr()
		g.w("}")
	case 11:
		g.w("try ")
		g.expr()
	case 12:
		g.w("func(", g.name(), " int) int { ")
		g.expr()
		g.w(" }")
	case 13:
		g.w("(")
		g.list(g.expr)
		g.w(")")
	case 14:
		g.w(g.name(), "(")
		g.expr()
		g.w(") catch {\n\tcase ", g.name(), " as e:\n\t\t")
		g.expr()
		g.w("\n\tcase _:\n\t\t")
		g.expr()
		g.w("\n\t}")
	case 15:
		g.w("with ", g.name(), " { ")
		g.expr()
		g.w(" }")
	case 16:
		g.w(g.name(), "(")
		g.expr()
		g.w(") { ")
		g.expr()
		g.w(" }")
	case 17:
		// A selector on a number literal keeps its blank: `0 .A`.
		g.w(g.pick("0", "7", "2.5", "1e9"), " .", g.name())
	case 18:
		// A slice whose one present index is binary has no blanks.
		g.w(g.name(), "[", g.pick("a+1:", ":b*2", "a+b:c", "a:b-1", "x:", "a+b:c:d"), "]")
	default:
		g.literal()
	}
}

// list is a comma-separated list whose breaks follow gap, with a trailing
// comma when the closer is on its own line.
func (g *gen) list(item func()) {
	n := g.r.IntN(4)
	broke := false
	for i := range n {
		if i > 0 {
			g.w(",")
		}
		if g.r.IntN(4) == 0 {
			g.w("\n")
			broke = true
		} else if i > 0 {
			g.w(" ")
		}
		item()
	}
	if broke && n > 0 {
		g.w(",\n")
	}
}

// simple is a statement that prints on one line.
func (g *gen) simple() string {
	return g.pick(g.name(), g.name()+"(y)", g.name()+" = 1", "return")
}

func (g *gen) stmt() {
	switch g.r.IntN(15) {
	case 0:
		g.w("let ", g.pick("", "mut "), g.name())
		if g.r.IntN(3) == 0 {
			g.w(" ", g.typeName())
		}
		g.w(" =")
		g.gap()
		g.expr()
	case 1:
		g.w(g.name(), " ", g.pick("=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", "&^="), " ")
		g.expr()
	case 2:
		g.w("if ")
		g.expr()
		g.w(" {")
		g.block()
		if g.r.IntN(2) == 0 {
			g.w(" else ", g.pick("{", "if x {"))
			g.block()
		}
	case 3:
		g.w("for ")
		if g.r.IntN(2) == 0 {
			g.expr()
			g.w(" ")
		}
		g.w("{")
		g.block()
	case 4:
		g.w("for ", g.pick("", "i, "), g.name(), " in ")
		g.expr()
		g.w(" {", g.pick("", " break ", " continue "), "}")
	case 5:
		g.w("match ")
		g.expr()
		g.w(" {\n")
		for range 1 + g.r.IntN(3) {
			g.w(g.pick("case ", "\tcase "))
			g.expr()
			g.w(":")
			g.eol()
			g.stmt()
			g.eol()
			if g.r.IntN(3) == 0 {
				g.w("// ", g.text(), "\n", g.pick("", "\n"))
			}
		}
		g.w("}")
	case 6:
		g.w("match (a, b) {\ncase (A, _):\n\treturn 1\ncase _:\n\treturn ")
		g.expr()
		g.w("\n}")
	case 7:
		g.w("return")
		if g.r.IntN(2) == 0 {
			g.w(" ")
			g.expr()
		}
	case 8:
		g.w("assert ")
		g.expr()
	case 9:
		// A one-line body with two statements breaks.
		g.w(g.pick("if x", "for", "for x in xs"), " { ", g.simple(), "; ", g.simple(), " }")
	case 10:
		// An if whose else spans lines is not on one line.
		g.w("if x { ", g.simple(), " } else ", g.pick("{\n", "if y { b } else {\n"), g.simple(), "\n}")
	case 11:
		// A catch in a one-line body breaks it.
		g.w("if x { ", g.name(), "(y) catch { case A: 1 } }")
	default:
		g.expr()
	}
}

func (g *gen) block() {
	n := g.r.IntN(4)
	if n == 0 {
		g.w(g.pick("}", " }", "\n}"))
		return
	}
	g.w(g.pick(" ", "\n"))
	for i := range n {
		if i > 0 {
			g.eol()
		}
		g.stmt()
	}
	g.w(g.pick(" }", "\n}", "\n// last\n}"))
}

func (g *gen) fields(sep string) {
	for i := range g.r.IntN(5) {
		if i > 0 {
			g.w(sep)
		}
		names := g.name()
		if g.r.IntN(4) == 0 {
			names += ", " + g.name()
		}
		g.w(names, " ", g.typeName())
		if g.r.IntN(6) == 0 {
			g.w(" `json:\"", g.name(), "\"`")
		}
		if sep != ", " && g.r.IntN(4) == 0 {
			g.w(" // ", g.text())
		}
	}
}

func (g *gen) decl() {
	switch g.r.IntN(15) {
	case 0:
		g.w("import ", g.pick("strings", "go strconv", "s strings", "go net/http", "\"a/b\"", "selfhost/tree"))
	case 1:
		g.w("extern go ", g.pick("strings.ToUpper", "net/http.Get", "math.Pi"), " ", g.pick("func f(s string) string", "let Pi float64", "type T"))
	case 2:
		g.w("type ", g.name(), g.pick("", "[T any]"), " {", g.pick(" ", "\n\t"))
		g.fields(g.pick(", ", ",\n\t"))
		g.w(g.pick(" }", ",\n}", "\n}"))
	case 3:
		g.w("type ", g.name(), " = ", g.name(), g.pick("", "{X int}"), " | ", g.name(), "{")
		g.fields(", ")
		g.w("}", g.pick("", " | Z", "\n\t| W{}"))
	case 4:
		g.w("type ", g.name(), " {\n\tGet(k string) int fails NotFound needs Store,\n\tPut(k string, v int)", g.pick("", " takes v"), ",\n\n\tlaw RoundTrip(k string, v int) { Put(k, v); try Get(k) == v },\n}")
	case 5:
		g.w("type M machine[State, Signal] {\n\tinitial: ", g.name(), "{},\n\tstep:", g.pick(" ", "    "), "step,\n\tlaw Reach reaches Done,\n\tlaw P never path [A, B],\n\tlaw I invariant ok,\n}")
	case 6:
		g.w("let ", g.name(), " = context ", g.pick("", "provides A | B "), g.pick("", "needs C "), "{\n\t", g.name(), ": ")
		g.expr()
		g.w(",\n\treplace B: ")
		g.expr()
		g.w(",\n\tOther,\n}")
	case 7:
		g.w("let ", g.name(), " ", g.typeName(), " = ")
		g.expr()
	case 8:
		g.w("test ", g.name(), " {")
		g.block()
	case 9:
		g.w("law ", g.name(), "(a int, b ", g.typeName(), ") {")
		g.block()
	case 10:
		g.w("type Port int where 1 <= it && it <= ", strconv.Itoa(g.r.IntN(70000)))
	case 11:
		// Variants on one line, one of whose fields span lines: the union
		// prints one variant per line.
		g.w("type ", g.name(), " = ", g.name(), " | ", g.name(), "{X int,\n\tY ", g.typeName(), "}", g.pick("", " | Z", " | W{}"))
	default:
		g.w("func ", g.pick("", "(p *Point) "), g.name(), g.pick("", "[T any, U comparable]"), "(")
		g.list(func() { g.w(g.name(), " ", g.typeName()) })
		g.w(")", g.pick("", " int", " (int, string)", " int fails A | B", " int fails A needs B | go.Foreign", " escaping func() int"), " {")
		g.block()
	}
}
